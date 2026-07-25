package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	execsvc "github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/sessions"
)

type sessionCtxKey struct{}

// mcpHost 把一次 HTTP 请求携带的 session token 变成一个只含该 session
// 可见工具的一次性 mcp.Server 实例。
type mcpHost struct {
	registry *registry.Registry
	sessions *sessions.Service
	exec     ToolExecutor
}

// registerMCP 在 Echo 上挂载聚合 /mcp 端点（Streamable HTTP，Stateless 模式）。
// Stateless 下 SDK 对每个 POST 调用 serverForRequest：每请求按 token 动态构建
// server，无进程内长驻状态；协议 session 生命周期由 SDK handler 管理。
func registerMCP(e *echo.Echo, deps Deps) {
	h := &mcpHost{registry: deps.Registry, sessions: deps.Sessions, exec: deps.Exec}
	handler := mcp.NewStreamableHTTPHandler(h.serverForRequest,
		&mcp.StreamableHTTPOptions{Stateless: true})
	// Authentication intentionally runs before the body admission/read. An
	// unauthenticated caller cannot reserve one of the bounded decode slots.
	e.Any("/mcp", h.requireSession(
		newMCPIngress().limitBody(echo.WrapHandler(handler)),
	))
}

// requireSession 校验 Authorization: Bearer <session token>，
// 并把解析出的 SessionView 放进请求 context。每个请求都会重新校验。
func (h *mcpHost) requireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			return writeError(c, http.StatusUnauthorized, "unauthorized", "missing bearer session token")
		}
		if h.sessions == nil {
			c.Logger().Error("mcp session service is not configured")
			return writeError(
				c,
				http.StatusInternalServerError,
				"internal",
				"failed to validate mcp session",
			)
		}
		view, err := h.sessions.Resolve(c.Request().Context(), token)
		if err != nil {
			if errors.Is(err, sessions.ErrInvalidSession) {
				return writeError(c, http.StatusUnauthorized, "invalid_session",
					"session token is invalid, expired or revoked")
			}
			c.Logger().Errorf("mcp session resolution failed: %v", err)
			return writeError(
				c,
				http.StatusInternalServerError,
				"internal",
				"failed to validate mcp session",
			)
		}
		req := c.Request()
		c.SetRequest(req.WithContext(context.WithValue(req.Context(), sessionCtxKey{}, view)))
		return next(c)
	}
}

// serverForRequest is the infallible factory required by the MCP SDK.
// requireSession has already resolved the session, and buildServer only reads
// Definition data the Registry validated at registration time.
func (h *mcpHost) serverForRequest(r *http.Request) *mcp.Server {
	view, _ := r.Context().Value(sessionCtxKey{}).(sessions.SessionView)
	return h.buildServer(view)
}

// buildServer 按 session 绑定与 allowlist 构建一次性 MCP server。
func (h *mcpHost) buildServer(view sessions.SessionView) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "connect-it", Version: "0.1.0"}, nil)

	aliases := make([]string, 0, len(view.Bindings))
	if h.registry != nil { // 未装配 registry：不暴露工具，调用走 tool_unavailable
		for alias := range view.Bindings {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases) // 工具注册顺序稳定，便于测试与日志

	registered := map[string]bool{}
	for _, alias := range aliases {
		binding := view.Bindings[alias]
		connID := binding.ConnectionID
		ct := binding.ActiveConnectorType
		if ct == "" {
			continue // 连接已被删除：不暴露工具，调用走 tool_unavailable
		}
		def, ok := h.registry.Get(connector.Type(ct))
		if !ok {
			continue // definition_missing：同上
		}
		for _, tool := range def.Tools {
			exposed := alias + "__" + tool.ID
			grant, granted := view.Grants[exposed]
			if !granted || grant.Risk != tool.Risk {
				continue
			}
			// Registry 注册时已解析过两个 schema，这里的失败分支不可达；
			// 真出现时不注册该 Tool，调用走 tool_unavailable。
			schema := &jsonschema.Schema{Type: "object"}
			if len(tool.InputSchema) > 0 {
				schema = new(jsonschema.Schema)
				if err := json.Unmarshal(tool.InputSchema, schema); err != nil {
					continue
				}
			}
			var outputSchema *jsonschema.Schema
			if len(tool.OutputSchema) > 0 {
				outputSchema = new(jsonschema.Schema)
				if err := json.Unmarshal(tool.OutputSchema, outputSchema); err != nil {
					continue
				}
			}
			srv.AddTool(&mcp.Tool{
				Name:         exposed,
				Description:  tool.Description,
				InputSchema:  schema,
				OutputSchema: outputSchema,
			}, h.callHandler(view.ID, exposed, connID, tool.ID))
			registered[exposed] = true
		}
	}

	srv.AddReceivingMiddleware(unavailableToolMiddleware(view, registered))
	return srv
}

// callHandler 返回一个绑定了（connection, tool）的 tools/call 处理函数。
// go-sdk 低阶 ToolHandler 返回 error 会成为协议错误，因此业务失败一律
// 以 CallToolResult{IsError: true} 返回。
func (h *mcpHost) callHandler(
	sessionID uuid.UUID,
	exposedToolName string,
	connID uuid.UUID,
	toolID string,
) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		data, err := h.exec.Execute(ctx, execsvc.ExecuteRequest{
			ConnectionID: connID,
			ToolID:       toolID,
			Arguments:    args,
			Authorization: execsvc.ExecutionGrant{
				SessionID:       sessionID,
				ExposedToolName: exposedToolName,
				ConnectionID:    connID,
				ToolID:          toolID,
			},
		})
		if err != nil {
			return errorResult(
				string(connector.FailureInternalError),
				"tool execution failed",
			), nil
		}
		return toolResultToMCP(data), nil
	}
}

// unavailableToolMiddleware 拦截 tools/call：目标名在 session 语义下本应合法
// （alias 已绑定且通过 allowlist），但当前 Definition 已无此 Tool——返回
// tool_unavailable（spec §9「删除 Tool」规则）。其余未注册名字放行给 SDK，
// 得到标准的协议级 unknown tool 错误。
func unavailableToolMiddleware(view sessions.SessionView, registered map[string]bool) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if !ok {
				return next(ctx, method, req)
			}
			name := params.Name
			if registered[name] {
				return next(ctx, method, req)
			}
			if _, granted := view.Grants[name]; !granted {
				return next(ctx, method, req)
			}
			return errorResult("tool_unavailable",
				"tool "+name+" is not available in the current definition"), nil
		}
	}
}

// toolResultToMCP 把统一执行结果转成 MCP CallToolResult，IsError 透传。
func toolResultToMCP(data connector.ToolResultData) *mcp.CallToolResult {
	if data.Failure != nil {
		failure := connector.NormalizeToolFailure(data.Failure)
		return failureResultToMCP(failure)
	}
	res := &mcp.CallToolResult{
		IsError: data.Failed(),
		Content: []mcp.Content{&mcp.TextContent{Text: data.Text}},
	}
	if len(data.Structured) > 0 {
		res.StructuredContent = json.RawMessage(data.Structured)
	}
	return res
}

// errorResult 构造统一错误形态的工具级错误结果，文本体沿用项目错误 JSON 约定。
func errorResult(code, message string) *mcp.CallToolResult {
	failure, err := connector.NewToolFailure(
		connector.FailureCode(code),
		message,
		0,
		0,
	)
	if err != nil {
		normalized := connector.NormalizeToolFailure(nil)
		failure = &normalized
	}
	return failureResultToMCP(*failure)
}

func failureResultToMCP(
	failure connector.ToolFailure,
) *mcp.CallToolResult {
	bodyValue := map[string]any{
		"error":   string(failure.Code),
		"message": failure.Message,
	}
	if failure.RetryAfterSeconds > 0 {
		bodyValue["retry_after_seconds"] = failure.RetryAfterSeconds
	}
	body, _ := json.Marshal(bodyValue)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}
