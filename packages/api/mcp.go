package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/service/sessions"
)

type sessionCtxKey struct{}

// mcpHost exposes the tools available through the Connection bound to a
// short-lived Connect-It session.
type mcpHost struct {
	sessions *sessions.Service
	exec     ToolExecutor
	logger   echo.Logger
}

func registerMCP(e *echo.Echo, deps Deps) {
	h := &mcpHost{sessions: deps.Sessions, exec: deps.Exec, logger: e.Logger}
	handler := mcp.NewStreamableHTTPHandler(h.serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless: true,
		// Connect-It is a bearer-authenticated network service and may sit behind
		// a reverse proxy. The SDK's localhost-only Host check is intended for
		// local MCP servers and rejects legitimate proxy/Docker hostnames.
		DisableLocalhostProtection: true,
	})
	e.Any("/mcp", echo.WrapHandler(handler), h.requireSession)
}

// requireSession validates Authorization: Bearer <session token> on every request.
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
			h.logger.Errorf("解析 MCP session 失败: %v", err)
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
		&mcp.Implementation{Name: "connect-it", Version: "0.1.0"},
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

func (h *mcpHost) callTool(ctx context.Context, view sessions.SessionView, params *mcp.CallToolParamsRaw) *mcp.CallToolResult {
	if params == nil {
		return errorResult("tool_unavailable", "missing tool call parameters")
	}
	if !view.AllowedTools[params.Name] {
		return errorResult("tool_unavailable", "tool "+params.Name+" is not allowed")
	}

	result, err := h.exec.CallTool(ctx, view.ID, view.APITokenID, view.ConnectionID, params)
	if err != nil {
		h.logger.Errorf("MCP tool 调用失败 connection_id=%s tool_name=%q error=%v",
			view.ConnectionID, params.Name, err)
		return errorResult("execution_failed", "tool execution failed")
	}
	if result == nil {
		return errorResult("execution_failed", "tool returned no result")
	}
	return result
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
