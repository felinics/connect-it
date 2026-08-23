package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/buildinfo"
	"github.com/memohai/connect-it/packages/service/configsvc"
	execsvc "github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/tokens"
)

type sessionCtxKey struct{}

// mcpHost exposes one immutable aggregate session through the shared /mcp URL.
type mcpHost struct {
	sessions *sessions.Service
	exec     ToolExecutor
	logger   echo.Logger
}

func registerMCP(e *echo.Echo, deps Deps) {
	h := &mcpHost{sessions: deps.Sessions, exec: deps.Exec, logger: e.Logger}
	handler := mcp.NewStreamableHTTPHandler(h.serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless:                  true,
		DisableLocalhostProtection: true,
	})
	e.Any("/mcp", echo.WrapHandler(handler), h.requireSession)
}

func (h *mcpHost) requireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			return writeError(c, http.StatusUnauthorized, "unauthorized", "missing bearer session token")
		}
		view, err := h.sessions.Resolve(c.Request().Context(), token)
		if errors.Is(err, sessions.ErrInvalidSession) {
			return writeError(c, http.StatusUnauthorized, "invalid_session",
				"session token is invalid, expired or revoked")
		}
		if err != nil {
			h.logger.Errorf("parsing the MCP session failed: %v", err)
			return writeError(c, http.StatusInternalServerError, "internal",
				"failed to resolve mcp session")
		}
		request := c.Request()
		c.SetRequest(request.WithContext(context.WithValue(request.Context(), sessionCtxKey{}, view)))
		return next(c)
	}
}

func (h *mcpHost) serverForRequest(request *http.Request) *mcp.Server {
	view, ok := request.Context().Value(sessionCtxKey{}).(sessions.SessionView)
	if !ok {
		return nil
	}
	server := mcp.NewServer(
		&mcp.Implementation{Name: "connect-it", Version: buildinfo.Version},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{},
			},
		},
	)
	server.AddReceivingMiddleware(h.proxyMiddleware(view))
	return server
}

func (h *mcpHost) proxyMiddleware(view sessions.SessionView) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcp.ListToolsResult{Tools: cloneTools(view.Tools)}, nil
			case "tools/call":
				params, ok := request.GetParams().(*mcp.CallToolParamsRaw)
				if !ok {
					return next(ctx, method, request)
				}
				return h.callTool(ctx, view, params), nil
			default:
				return next(ctx, method, request)
			}
		}
	}
}

func cloneTools(tools []*mcp.Tool) []*mcp.Tool {
	cloned := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		copy := *tool
		cloned = append(cloned, &copy)
	}
	return cloned
}

func (h *mcpHost) callTool(
	ctx context.Context,
	view sessions.SessionView,
	params *mcp.CallToolParamsRaw,
) *mcp.CallToolResult {
	if params == nil {
		return errorResult("tool_unavailable", "missing tool call parameters")
	}
	route, allowed := view.Routes[params.Name]
	if !allowed {
		return errorResult("tool_unavailable", "tool "+params.Name+" is not allowed")
	}
	routed := *params
	routed.Name = route.ToolName
	result, err := h.exec.CallTool(
		ctx, view.ID, view.APITokenID, route.ConnectionID, &routed,
	)
	if err != nil {
		h.logger.Errorf(
			"MCP tool call failed connection_id=%s tool_name=%q upstream_tool=%q error=%v",
			route.ConnectionID, params.Name, route.ToolName, err,
		)
		code, message := publicToolError(err)
		return errorResult(code, message)
	}
	if result == nil {
		return errorResult("execution_failed", "tool returned no result")
	}
	return result
}

type upstreamStatusError interface {
	UpstreamStatusCode() int
}

func publicToolError(err error) (string, string) {
	switch {
	case errors.Is(err, tokens.ErrReauthRequired):
		return "reauth_required", "connection needs reauthorization"
	case errors.Is(err, execsvc.ErrToolUnavailable),
		errors.Is(err, execsvc.ErrConnectionNotFound),
		errors.Is(err, execsvc.ErrConnectionInactive),
		errors.Is(err, configsvc.ErrConnectorDisabled),
		errors.Is(err, tokens.ErrNotFound):
		return "tool_unavailable", "tool is unavailable"
	}

	var upstream upstreamStatusError
	if errors.As(err, &upstream) {
		switch status := upstream.UpstreamStatusCode(); {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return "upstream_auth_error", "upstream rejected the connection credentials"
		case status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
			return "temporarily_unavailable", "upstream is temporarily unavailable"
		}
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.As(err, &networkError) {
		return "temporarily_unavailable", "tool is temporarily unavailable"
	}
	return "execution_failed", "tool execution failed"
}

func errorResult(code, message string) *mcp.CallToolResult {
	body := map[string]string{"error": code, "message": message}
	encoded, _ := json.Marshal(body)
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: body,
	}
}
