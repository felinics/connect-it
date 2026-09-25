package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/service/exec"
	"github.com/felinics/connect-it/packages/service/mcpclient"
)

func TestOperationTimeout(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
	}))
	defer upstream.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	_, err := mcpclient.CallTool(t.Context(), upstream.URL, "", "", 30*time.Millisecond, &mcp.CallToolParamsRaw{Name: "echo"})
	if !errors.Is(err, context.DeadlineExceeded) || exec.DescribeError(err).Kind != "timeout" {
		t.Fatalf("timeout misclassified: %+v, error types %T", exec.DescribeError(err), err)
	}
}

func TestHandshakeFailureTracking(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		abort      bool
		status     int
	}{
		{"disconnect_after_discovery", "transport", true, 0},
		{"notification_auth_failure", "auth", false, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusOK)
					return
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Method {
				case "server/discover":
					http.Error(w, "legacy", 400)
				case "initialize":
					if tc.abort {
						panic(http.ErrAbortHandler)
					}
					w.Header().Set("Mcp-Session-Id", "test-session")
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1"}}})
				case "notifications/initialized":
					http.Error(w, "private-auth-error", 401)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			}))
			defer upstream.Close()
			previous := http.DefaultTransport
			http.DefaultTransport = upstream.Client().Transport
			defer func() { http.DefaultTransport = previous }()
			_, err := mcpclient.ListTools(t.Context(), upstream.URL, "", "", time.Second)
			f := exec.DescribeError(err)
			if err == nil || f.Kind != tc.kind || f.UpstreamStatus != tc.status {
				t.Fatalf("handshake failure=%+v", f)
			}
		})
	}
}

func TestFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, method, stage string
		status, rpcCode     int
		malformed           bool
		truncated           bool
	}{
		{name: "handshake", method: "initialize", stage: "connect", status: 401},
		{name: "http400", method: "tools/call", stage: "tools/call", status: 400},
		{name: "http403", method: "tools/call", stage: "tools/call", status: 403},
		{name: "http429", method: "tools/call", stage: "tools/call", status: 429},
		{name: "http500", method: "tools/call", stage: "tools/call", status: 500},
		{name: "invalid_params", method: "tools/call", stage: "tools/call", rpcCode: -32602},
		{name: "internal_rpc", method: "tools/call", stage: "tools/call", rpcCode: -32603},
		{name: "invalid_content", method: "tools/call", stage: "tools/call", malformed: true},
		{name: "truncated_response", method: "tools/call", stage: "tools/call", truncated: true},
		{name: "list_failure", method: "tools/list", stage: "tools/list", rpcCode: -32603},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if len(request.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if request.Method == "server/discover" {
					http.Error(w, "legacy endpoint", http.StatusBadRequest)
					return
				}
				response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
				if request.Method == tc.method {
					if tc.truncated {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Content-Length", "10000")
						_, _ = w.Write([]byte(`{"jsonrpc":"2.0"`))
						return
					}
					if tc.status != 0 {
						http.Error(w, "private-response-body", tc.status)
						return
					}
					if tc.malformed {
						response["result"] = map[string]any{"content": []any{map[string]any{"type": "private-invalid-type"}}}
					} else {
						response["error"] = map[string]any{"code": tc.rpcCode, "message": "private-rpc-message", "data": "private-rpc-data"}
					}
				} else {
					response["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer upstream.Close()
			previous := http.DefaultTransport
			http.DefaultTransport = upstream.Client().Transport
			defer func() { http.DefaultTransport = previous }()
			var err error
			if tc.method == "tools/list" {
				_, err = mcpclient.ListTools(t.Context(), upstream.URL, "test-token", "Bearer", time.Second)
			} else {
				_, err = mcpclient.CallTool(t.Context(), upstream.URL, "test-token", "Bearer", time.Second, &mcp.CallToolParamsRaw{Name: "echo"})
			}
			var detail interface {
				UpstreamStatusCode() int
				UpstreamStage() string
			}
			if !errors.As(err, &detail) {
				t.Fatalf("missing typed failure diagnostics: %T", err)
			}
			if detail.UpstreamStage() != tc.stage || detail.UpstreamStatusCode() != tc.status {
				t.Fatalf("stage=%s status=%d", detail.UpstreamStage(), detail.UpstreamStatusCode())
			}
			failure := exec.DescribeError(err)
			if tc.rpcCode != 0 && (failure.RPCCode == nil || *failure.RPCCode != int64(tc.rpcCode)) {
				t.Fatalf("lost RPC code: %+v", failure)
			}
			if tc.malformed && failure.Kind != "upstream_protocol" {
				t.Fatalf("malformed response classified as %+v", failure)
			}
			if tc.truncated && failure.Kind != "transport" {
				t.Fatalf("truncated response classified as %+v", failure)
			}
			encoded, _ := json.Marshal(failure)
			if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "test-token") {
				t.Fatalf("private data in diagnostics: %s", encoded)
			}
		})
	}
}
