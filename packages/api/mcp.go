package api

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/gommon/log"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/core/buildinfo"
	execsvc "github.com/felinics/connect-it/packages/service/exec"
	"github.com/felinics/connect-it/packages/service/sessions"
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
) (result *mcp.CallToolResult) {
	ctx, runID := execsvc.WithRunID(ctx)
	started := time.Now()
	var failure *execsvc.Failure
	var route sessions.ToolRoute
	var toolName string
	defer func() {
		diagnostics := map[string]any{"run_id": runID.String()}
		if failure != nil {
			diagnostics["kind"] = failure.Kind
			record := log.JSON{
				"event": "mcp_tool_call_failed", "run_id": runID.String(),
				"session_id": view.ID.String(), "connection_id": route.ConnectionID.String(),
				"tool_name": toolName, "upstream_tool": route.ToolName,
				"error": failure.Code, "kind": failure.Kind,
				"duration_ms": time.Since(started).Milliseconds(),
			}
			if failure.Stage != "" {
				record["stage"] = failure.Stage
			}
			if failure.UpstreamStatus != 0 {
				record["upstream_status"] = failure.UpstreamStatus
			}
			if failure.RPCCode != nil {
				record["rpc_code"] = *failure.RPCCode
			}
			h.logger.Errorj(record)
		}
		copy := *result
		copy.Meta = maps.Clone(result.Meta)
		if copy.Meta == nil {
			copy.Meta = mcp.Meta{}
		}
		copy.Meta["connect-it.dev/diagnostics"] = diagnostics
		result = &copy
	}()
	fail := func(f execsvc.Failure) *mcp.CallToolResult {
		failure = &f
		return errorResult(f, runID.String())
	}
	if params == nil {
		return fail(execsvc.Failure{Code: "tool_unavailable", Message: "missing tool call parameters", Kind: "tool_unavailable", Stage: "route"})
	}
	var allowed bool
	route, allowed = view.Routes[params.Name]
	if !allowed {
		return fail(execsvc.Failure{Code: "tool_unavailable", Message: "tool is not allowed", Kind: "tool_unavailable", Stage: "route"})
	}
	toolName = params.Name
	routed := *params
	routed.Name = route.ToolName
	result, err := h.exec.CallTool(ctx, view.ID, view.APITokenID, route.ConnectionID, &routed)
	if err != nil {
		return fail(execsvc.DescribeError(err))
	}
	if result == nil {
		return fail(execsvc.DescribeError(errors.New("tool returned no result")))
	}
	if result.IsError {
		f := execsvc.DescribeToolError(result)
		failure = &f
	}
	return result
}

func errorResult(f execsvc.Failure, runID string) *mcp.CallToolResult {
	body := map[string]any{"error": f.Code, "message": f.Message, "kind": f.Kind, "run_id": runID}
	if f.Stage != "" {
		body["stage"] = f.Stage
	}
	if f.UpstreamStatus != 0 {
		body["upstream_status"] = f.UpstreamStatus
	}
	if f.RPCCode != nil {
		body["rpc_code"] = *f.RPCCode
	}
	encoded, _ := json.Marshal(body)
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: body,
	}
}
