// Package mcpclient 用官方 go-sdk 以 Streamable HTTP 调用上游 MCP server。
// 每次调用新建并关闭 session；暂不维护进程内缓存。
package mcpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client 是无状态适配器，把包级函数暴露给执行引擎。
type Client struct{}

// UpstreamError marks a failed MCP operation and retains only its HTTP status,
// when one was observed. It never stores an upstream response body.
type UpstreamError struct {
	err        error
	statusCode int
}

func (e *UpstreamError) Error() string           { return e.err.Error() }
func (e *UpstreamError) Unwrap() error           { return e.err }
func (e *UpstreamError) UpstreamStatusCode() int { return e.statusCode }

func (Client) ListTools(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration) ([]*mcp.Tool, error) {
	return ListTools(ctx, endpoint, token, authorizationScheme, timeout)
}

func (Client) CallTool(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration, params *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	return CallTool(ctx, endpoint, token, authorizationScheme, timeout, params)
}

// ListTools 完成一次 MCP 握手并读取上游全部分页。
func ListTools(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration) ([]*mcp.Tool, error) {
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	session, tracker, err := connect(ctx, endpoint, token, authorizationScheme)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	var tools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, upstreamError(fmt.Errorf("mcpclient: tools/list 失败: %w", err), tracker)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// CallTool 完成一次 MCP 握手并把原生 MCP 参数和结果透传给上游。
func CallTool(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration, params *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	if params == nil {
		return nil, fmt.Errorf("mcpclient: call params 不能为空")
	}
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	session, tracker, err := connect(ctx, endpoint, token, authorizationScheme)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Meta:      params.Meta,
		Name:      params.Name,
		Arguments: params.Arguments,
	})
	if err != nil {
		return nil, upstreamError(fmt.Errorf("mcpclient: 调用 tool %q 失败: %w", params.Name, err), tracker)
	}
	return res, nil
}

// CheckEndpoint is kept as the shared URL-shape validator used by service
// packages. Remote MCP endpoints are code-defined and must always use HTTPS.
func CheckEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" ||
		u.User != nil || u.Fragment != "" {
		return fmt.Errorf("mcpclient: endpoint %q 必须是合法的 https URL", endpoint)
	}
	return nil
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func connect(ctx context.Context, endpoint, token, authorizationScheme string) (*mcp.ClientSession, *responseTracker, error) {
	if err := CheckEndpoint(endpoint); err != nil {
		return nil, nil, err
	}
	tracker := &responseTracker{}
	client := mcp.NewClient(&mcp.Implementation{Name: "connect-it", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{
			Transport: authorizationRoundTripper{
				token:        token,
				scheme:       authorizationScheme,
				base:         http.DefaultTransport,
				operationCtx: ctx,
				tracker:      tracker,
			},
			// Credentials must never follow an upstream redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, tracker, upstreamError(
			fmt.Errorf("mcpclient: 连接 %s 失败: %w", endpoint, err), tracker)
	}
	return session, tracker, nil
}

func upstreamError(err error, tracker *responseTracker) error {
	if err == nil {
		return nil
	}
	statusCode := 0
	if tracker != nil {
		statusCode = tracker.statusCode()
	}
	return &UpstreamError{err: err, statusCode: statusCode}
}

type responseTracker struct {
	lastErrorStatus atomic.Int64
}

func (t *responseTracker) observe(statusCode int) {
	if t != nil && (statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices) {
		t.lastErrorStatus.Store(int64(statusCode))
	}
}

func (t *responseTracker) statusCode() int {
	if t == nil {
		return 0
	}
	return int(t.lastErrorStatus.Load())
}

type authorizationRoundTripper struct {
	token        string
	scheme       string
	base         http.RoundTripper
	operationCtx context.Context
	tracker      *responseTracker
}

func (rt authorizationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := rt.operationCtx.Err(); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(rt.operationCtx, cancel)
	cleanup := func() {
		stop()
		cancel()
	}

	req = req.Clone(requestCtx)
	if rt.token != "" {
		scheme := rt.scheme
		if scheme == "" {
			scheme = "Bearer"
		}
		req.Header.Set("Authorization", scheme+" "+rt.token)
	}
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		cleanup()
		return nil, err
	}
	rt.tracker.observe(resp.StatusCode)
	if req.Method == http.MethodDelete {
		_ = resp.Body.Close()
		resp.Body = http.NoBody
		cleanup()
		return resp, nil
	}
	resp.Body = &cleanupReadCloser{ReadCloser: resp.Body, cleanup: cleanup}
	return resp, nil
}

type cleanupReadCloser struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
}

func (r *cleanupReadCloser) Close() error {
	r.once.Do(r.cleanup)
	return r.ReadCloser.Close()
}
