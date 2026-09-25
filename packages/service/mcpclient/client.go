// Package mcpclient calls upstream MCP servers over Streamable HTTP using the
// official go-sdk. Each call opens and closes its own session; no in-process
// cache is kept for now.
package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/core/buildinfo"
)

// Client is a stateless adapter that exposes the package-level functions to
// the execution engine.
type Client struct{}

// UpstreamError marks a failed MCP operation and retains only its HTTP status,
// when one was observed. It never stores an upstream response body.
type UpstreamError struct {
	err              error
	statusCode       int
	stage            string
	transportFailure bool
}

func (e *UpstreamError) Error() string                  { return e.err.Error() }
func (e *UpstreamError) Unwrap() error                  { return e.err }
func (e *UpstreamError) UpstreamStatusCode() int        { return e.statusCode }
func (e *UpstreamError) UpstreamStage() string          { return e.stage }
func (e *UpstreamError) UpstreamTransportFailure() bool { return e.transportFailure }

func (Client) ListTools(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration) ([]*mcp.Tool, error) {
	return ListTools(ctx, endpoint, token, authorizationScheme, timeout)
}

func (Client) CallTool(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration, params *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	return CallTool(ctx, endpoint, token, authorizationScheme, timeout, params)
}

// ListTools performs one MCP handshake and reads every upstream page.
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
			return nil, upstreamError(ctx, "tools/list", fmt.Errorf("mcpclient: tools/list failed: %w", err), tracker)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// CallTool performs one MCP handshake and passes native MCP params and
// results straight through to the upstream server.
func CallTool(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration, params *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	if params == nil {
		return nil, fmt.Errorf("mcpclient: call params must not be nil")
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
		return nil, upstreamError(ctx, "tools/call", fmt.Errorf("mcpclient: calling tool %q failed: %w", params.Name, err), tracker)
	}
	return res, nil
}

// CheckEndpoint is kept as the shared URL-shape validator used by service
// packages. Remote MCP endpoints are code-defined and must always use HTTPS.
func CheckEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" ||
		u.User != nil || u.Fragment != "" {
		return fmt.Errorf("mcpclient: endpoint %q must be a valid https URL", endpoint)
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
	client := mcp.NewClient(&mcp.Implementation{Name: "connect-it", Version: buildinfo.Version}, nil)
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
		return nil, tracker, upstreamError(ctx, "connect",
			fmt.Errorf("mcpclient: connecting to %s failed: %w", endpoint, err), tracker)
	}
	return session, tracker, nil
}

func upstreamError(ctx context.Context, stage string, err error, tracker *responseTracker) error {
	if err == nil {
		return nil
	}
	statusCode := 0
	var transportErr error
	if tracker != nil {
		statusCode, transportErr = tracker.failure()
		if transportErr != nil {
			err = errors.Join(err, transportErr)
		}
	}
	if ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		err = errors.Join(err, ctx.Err())
	}
	return &UpstreamError{err: err, statusCode: statusCode, stage: stage, transportFailure: transportErr != nil}
}

type responseTracker struct {
	mu              sync.Mutex
	lastErrorStatus int
	transportErr    error
}

func (t *responseTracker) observe(statusCode int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastErrorStatus, t.transportErr = 0, nil
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		t.lastErrorStatus = statusCode
	}
}

func (t *responseTracker) observeTransportError(err error) {
	if t == nil || err == nil || err == io.EOF {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.transportErr = err
}

func (t *responseTracker) failure() (int, error) {
	if t == nil {
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastErrorStatus, t.transportErr
}

type authorizationRoundTripper struct {
	token        string
	scheme       string
	base         http.RoundTripper
	operationCtx context.Context
	tracker      *responseTracker
}

func (rt authorizationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tracker := rt.tracker
	if req.Method == http.MethodDelete {
		tracker = nil
	} else {
		tracker.observe(http.StatusOK)
	}
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
		tracker.observeTransportError(err)
		cleanup()
		return nil, err
	}
	tracker.observe(resp.StatusCode)
	if req.Method == http.MethodDelete {
		_ = resp.Body.Close()
		resp.Body = http.NoBody
		cleanup()
		return resp, nil
	}
	resp.Body = &cleanupReadCloser{ReadCloser: resp.Body, cleanup: cleanup, tracker: tracker}
	return resp, nil
}

type cleanupReadCloser struct {
	io.ReadCloser
	once    sync.Once
	cleanup func()
	tracker *responseTracker
}

func (r *cleanupReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.tracker.observeTransportError(err)
	return n, err
}

func (r *cleanupReadCloser) Close() error {
	r.once.Do(r.cleanup)
	return r.ReadCloser.Close()
}
