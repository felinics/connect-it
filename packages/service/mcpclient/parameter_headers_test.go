package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/service/exec"
	"github.com/felinics/connect-it/packages/service/mcpclient"
)

func TestCallToolLoadsParameterHeadersInItsOwnSession(t *testing.T) {
	for _, pageSize := range []int{1, 100} {
		t.Run(map[int]string{1: "paginated", 100: "single_page"}[pageSize], func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "headers", Version: "1"}, &mcp.ServerOptions{PageSize: pageSize})
			var calls atomic.Int32
			server.AddTool(&mcp.Tool{Name: "a_control", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					t.Error("called an unrelated tool")
					return &mcp.CallToolResult{}, nil
				})
			server.AddTool(&mcp.Tool{Name: "z_lookup", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{
					"owner": map[string]any{"type": "string", "x-mcp-header": "Owner"},
					"repo":  map[string]any{"type": "string", "x-mcp-header": "Repo"},
				},
			}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				if req.Params.Meta["test-origin"] != "caller" {
					t.Error("lost caller metadata")
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
			})
			upstream := serveParameterUpstream(t, server)
			if _, err := mcpclient.ListTools(t.Context(), upstream.URL, "", "Bearer", time.Second); err != nil {
				t.Fatal(err)
			}
			for _, repo := range []string{"Memoh", "另一个仓库"} {
				want := map[string]any{"owner": "felinics", "repo": repo}
				args, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				result, err := mcpclient.CallTool(t.Context(), upstream.URL, "", "Bearer", time.Second,
					&mcp.CallToolParamsRaw{Name: "z_lookup", Arguments: args, Meta: mcp.Meta{"test-origin": "caller"}})
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				if result.IsError || len(result.Content) != 1 {
					t.Fatalf("unexpected result: %+v", result)
				}
				if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &got); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("arguments changed: %v, err=%v", got, err)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("tool executed %d times, want 2", calls.Load())
			}
		})
	}
}

func TestCallToolDiscoveryCancellation(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "headers", Version: "1"}, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				close(started)
				select {
				case <-ctx.Done():
				case <-release:
				}
				return nil, context.Canceled
			}
			if method == "tools/call" {
				calls.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	upstream := serveParameterUpstream(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := mcpclient.CallTool(ctx, upstream.URL, "", "Bearer", time.Second, &mcp.CallToolParamsRaw{Name: "lookup"})
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("call completed before discovery: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}
	cancel()
	select {
	case err := <-done:
		var upstreamErr *mcpclient.UpstreamError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &upstreamErr) || upstreamErr.UpstreamStage() != "tools/list" {
			t.Fatalf("lost discovery cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled discovery did not stop")
	}
	if calls.Load() != 0 {
		t.Fatal("tool dispatched after discovery cancellation")
	}
}

func TestCallToolRejectsRepeatedDiscoveryCursor(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "headers", Version: "1"}, nil)
	var calls, pages atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				pages.Add(1)
				result := &mcp.ListToolsResult{Tools: []*mcp.Tool{}, NextCursor: "repeat"}
				result.TTLMs = 60_000
				return result, nil
			}
			if method == "tools/call" {
				calls.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	upstream := serveParameterUpstream(t, server)
	_, err := mcpclient.CallTool(t.Context(), upstream.URL, "", "Bearer", time.Second, &mcp.CallToolParamsRaw{Name: "lookup"})
	var upstreamErr *mcpclient.UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.UpstreamStage() != "tools/list" || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected discovery failure, got %v", err)
	}
	if pages.Load() != 2 {
		t.Fatalf("discovery requested %d pages, want 2", pages.Load())
	}
	if calls.Load() != 0 {
		t.Fatal("tool dispatched after invalid pagination")
	}
}

func TestCallToolDiscoveryTimeout(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "headers", Version: "1"}, nil)
	release := make(chan struct{})
	defer close(release)
	var listing atomic.Bool
	var calls atomic.Int32
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				listing.Store(true)
				<-release
				return nil, context.DeadlineExceeded
			}
			if method == "tools/call" {
				calls.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	upstream := serveParameterUpstream(t, server)
	_, err := mcpclient.CallTool(t.Context(), upstream.URL, "", "Bearer", time.Second, &mcp.CallToolParamsRaw{Name: "lookup"})
	failure := exec.DescribeError(err)
	if !errors.Is(err, context.DeadlineExceeded) || failure.Kind != "timeout" || failure.Stage != "tools/list" {
		t.Fatalf("lost discovery timeout: %+v, err=%v", failure, err)
	}
	if !listing.Load() || calls.Load() != 0 {
		t.Fatalf("listing=%v calls=%d", listing.Load(), calls.Load())
	}
}

func TestCallToolDiscoveryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, stage, kind string
		rpcCode           int64
		listed            bool
		listFails         bool
		wantCalls         int32
	}{
		{name: "first_page_match", listed: true, wantCalls: 1},
		{name: "missing_tool", stage: "tools/call", kind: "invalid_args", rpcCode: -32602, wantCalls: 1},
		{name: "list_failure", listFails: true, stage: "tools/list", kind: "upstream_rpc", rpcCode: -32603},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "headers", Version: "1"}, nil)
			var calls, pages atomic.Int32
			server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					switch method {
					case "tools/list":
						pages.Add(1)
						if tc.listFails || req.GetParams().(*mcp.ListToolsParams).Cursor != "" {
							return nil, &jsonrpc.Error{Code: -32603, Message: "discovery failed"}
						}
						result := &mcp.ListToolsResult{Tools: []*mcp.Tool{}}
						if tc.listed {
							result.Tools = []*mcp.Tool{{Name: "lookup", InputSchema: map[string]any{"type": "object"}}}
							result.NextCursor = "unneeded-page"
						}
						return result, nil
					case "tools/call":
						calls.Add(1)
						if !tc.listed {
							return nil, &jsonrpc.Error{Code: -32602, Message: "unknown tool"}
						}
						return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
					}
					return next(ctx, method, req)
				}
			})
			upstream := serveParameterUpstream(t, server)
			result, err := mcpclient.CallTool(t.Context(), upstream.URL, "", "Bearer", time.Second, &mcp.CallToolParamsRaw{Name: "lookup"})
			if tc.stage == "" {
				if err != nil || result.IsError {
					t.Fatalf("valid call failed: %v", err)
				}
			} else {
				var upstreamErr *mcpclient.UpstreamError
				var rpcErr *jsonrpc.Error
				if !errors.As(err, &upstreamErr) || upstreamErr.UpstreamStage() != tc.stage || !errors.As(err, &rpcErr) || rpcErr.Code != tc.rpcCode || exec.DescribeError(err).Kind != tc.kind {
					t.Fatalf("lost upstream failure: %v", err)
				}
			}
			if calls.Load() != tc.wantCalls || pages.Load() != 1 {
				t.Fatalf("calls=%d pages=%d, want %d and 1", calls.Load(), pages.Load(), tc.wantCalls)
			}
		})
	}
}

func TestCallToolSkipsDiscoveryForLegacyProtocol(t *testing.T) {
	var calls, pages atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method == "server/discover" {
			http.Error(w, "legacy", http.StatusBadRequest)
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "legacy", "version": "1"}}
		case "tools/list":
			pages.Add(1)
			http.Error(w, "discovery unavailable", http.StatusServiceUnavailable)
			return
		case "tools/call":
			calls.Add(1)
			result = &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { upstream.Close(); http.DefaultTransport = previous })
	result, err := mcpclient.CallTool(t.Context(), upstream.URL, "", "Bearer", time.Second, &mcp.CallToolParamsRaw{Name: "lookup"})
	if err != nil || result.IsError || calls.Load() != 1 || pages.Load() != 0 {
		t.Fatalf("legacy call changed: calls=%d pages=%d err=%v", calls.Load(), pages.Load(), err)
	}
}

func TestCallToolKeepsAccountSchemasIsolated(t *testing.T) {
	gate, release := context.WithCancel(t.Context())
	defer release()
	started := make(chan struct{}, 2)
	servers := map[string]*mcp.Server{}
	for _, account := range []string{"A", "B"} {
		server := mcp.NewServer(&mcp.Implementation{Name: "account", Version: "1"}, nil)
		server.AddTool(&mcp.Tool{Name: "lookup", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{
				"owner": map[string]any{"type": "string", "x-mcp-header": "Account-" + account},
			},
		}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "tools/list" {
					started <- struct{}{}
					select {
					case <-gate.Done():
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return next(ctx, method, req)
			}
		})
		servers["Bearer "+account] = server
	}
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return servers[r.Header.Get("Authorization")]
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") == "tools/call" {
			for _, account := range []string{"A", "B"} {
				want := ""
				if r.Header.Get("Authorization") == "Bearer "+account {
					want = account
				}
				if got := r.Header.Get("Mcp-Param-Account-" + account); got != want {
					t.Errorf("account %s header = %q, want %q", account, got, want)
				}
			}
		}
		handler.ServeHTTP(w, r)
	}))
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { upstream.Close(); http.DefaultTransport = previous })
	done := make(chan error, 2)
	for _, account := range []string{"A", "B"} {
		go func() {
			result, err := mcpclient.CallTool(t.Context(), upstream.URL, account, "Bearer", 5*time.Second,
				&mcp.CallToolParamsRaw{Name: "lookup", Arguments: json.RawMessage(`{"owner":"` + account + `"}`)})
			if err == nil && result.IsError {
				err = errors.New("account call failed")
			}
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-started:
		case err := <-done:
			t.Fatalf("call completed before discovery: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent discovery did not start")
		}
	}
	release()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func serveParameterUpstream(t *testing.T, server *mcp.Server) *httptest.Server {
	t.Helper()
	upstream := httptest.NewTLSServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { upstream.Close(); http.DefaultTransport = previous })
	return upstream
}
