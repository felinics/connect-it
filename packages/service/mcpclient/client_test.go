package mcpclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// newUpstream 用官方 SDK 起一个真实的 Streamable HTTP MCP server（httptest），
// 注册 echo 与 always_fail 两个工具；wantAuth 非空时校验 Authorization 头。
func newUpstream(t *testing.T, wantAuth string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "回显 message"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Message}},
			}, echoOutput{Echoed: in.Message}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "always_fail", Description: "恒返回 IsError"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
			}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCallToolEndToEnd(t *testing.T) {
	ts := newUpstream(t, "Bearer secret-token")
	res, err := mcpclient.CallTool(context.Background(), ts.URL, "secret-token",
		5*time.Second, "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("echo 不应返回 IsError")
	}
	if res.Text != "echo:hi" {
		t.Fatalf("Text 不符: %q", res.Text)
	}
	var structured map[string]any
	if err := json.Unmarshal(res.Structured, &structured); err != nil {
		t.Fatalf("Structured 应为 JSON: %v; raw=%s", err, res.Structured)
	}
	if structured["echoed"] != "hi" {
		t.Fatalf("structured: %s", res.Structured)
	}
}

func TestCallToolIsError(t *testing.T) {
	ts := newUpstream(t, "")
	res, err := mcpclient.CallTool(context.Background(), ts.URL, "",
		5*time.Second, "always_fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("always_fail 应返回 IsError")
	}
	if !strings.Contains(res.Text, "boom") {
		t.Fatalf("text: %s", res.Text)
	}
}

func TestCallToolWrongTokenFails(t *testing.T) {
	ts := newUpstream(t, "Bearer right")
	_, err := mcpclient.CallTool(context.Background(), ts.URL, "wrong",
		5*time.Second, "echo", json.RawMessage(`{"message":"x"}`))
	if err == nil {
		t.Fatal("错误 token 应导致握手失败")
	}
}

func TestCallToolUnknownToolErrors(t *testing.T) {
	ts := newUpstream(t, "")
	res, err := mcpclient.CallTool(context.Background(), ts.URL, "",
		5*time.Second, "nope", json.RawMessage(`{}`))
	if err == nil && !res.IsError {
		t.Fatalf("调用不存在的 tool 应报错: res=%+v", res)
	}
}

func TestListToolsEndToEnd(t *testing.T) {
	ts := newUpstream(t, "Bearer secret-token")
	names, err := mcpclient.ListTools(context.Background(), ts.URL, "secret-token", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"always_fail", "echo"}) {
		t.Fatalf("names: %v", names)
	}
}

func TestCheckEndpoint(t *testing.T) {
	cases := []struct {
		endpoint      string
		allowInsecure bool
		wantErr       bool
	}{
		{"https://mcp.internal/mcp", false, false},
		{"http://mcp.internal/mcp", false, true},
		{"http://mcp.internal/mcp", true, false},
		{"ftp://mcp.internal/mcp", true, true},
		{"not a url", false, true},
	}
	for _, tc := range cases {
		err := mcpclient.CheckEndpoint(tc.endpoint, tc.allowInsecure)
		if (err != nil) != tc.wantErr {
			t.Errorf("CheckEndpoint(%q, %v) = %v, wantErr=%v", tc.endpoint, tc.allowInsecure, err, tc.wantErr)
		}
	}
}
