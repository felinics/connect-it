package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/service/mcpclient"
)

type echoInput struct {
	Message string `json:"message"`
}

type echoOutput struct {
	Echoed string `json:"echoed"`
}

func newUpstream(t *testing.T, wantAuth string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo message"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Message}},
			}, echoOutput{Echoed: in.Message}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "always_fail"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
			}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	previousTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	t.Cleanup(upstream.Close)
	return upstream
}

func TestListAndCallTool(t *testing.T) {
	upstream := newUpstream(t, "Bearer secret-token")
	tools, err := mcpclient.ListTools(t.Context(), upstream.URL, "secret-token", "Bearer", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name == "" || tools[0].InputSchema == nil {
		t.Fatalf("tools=%+v", tools)
	}

	result, err := mcpclient.CallTool(t.Context(), upstream.URL, "secret-token", "Bearer", 5*time.Second,
		&mcp.CallToolParamsRaw{Name: "echo", Arguments: json.RawMessage(`{"message":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "echo:hi" {
		t.Fatalf("content=%+v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["echoed"] != "hi" {
		t.Fatalf("structured=%+v", result.StructuredContent)
	}
}

func TestCallToolPreservesIsError(t *testing.T) {
	upstream := newUpstream(t, "")
	result, err := mcpclient.CallTool(context.Background(), upstream.URL, "", "Bearer", 5*time.Second,
		&mcp.CallToolParamsRaw{Name: "always_fail", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("result=%+v", result)
	}
}

func TestBearerTokenDoesNotFollowRedirect(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
		http.Error(w, "unexpected redirect target", http.StatusInternalServerError)
	}))
	defer target.Close()

	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = redirect.Client().Transport
	defer func() { http.DefaultTransport = previousTransport }()

	_, err := mcpclient.ListTools(t.Context(), redirect.URL, "secret-token", "Bearer", time.Second)
	if err == nil {
		t.Fatal("a redirect must not count as a successful MCP handshake")
	}
	var upstreamErr *mcpclient.UpstreamError
	if !errors.As(err, &upstreamErr) ||
		upstreamErr.UpstreamStatusCode() != http.StatusTemporaryRedirect {
		t.Fatalf("upstream error=%T %v", err, err)
	}
	if targetHit.Load() {
		t.Fatal("a Bearer token request followed a redirect")
	}
}

func TestCustomAuthorizationScheme(t *testing.T) {
	upstream := newUpstream(t, "Sentry-Bearer secret-token")
	if _, err := mcpclient.ListTools(
		t.Context(), upstream.URL, "secret-token", "Sentry-Bearer", 5*time.Second,
	); err != nil {
		t.Fatal(err)
	}
}
