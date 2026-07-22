package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/service/sessions"
)

type createMCPSessionRequest struct {
	Connections   map[string]string `json:"connections"`
	ToolAllowlist []string          `json:"tool_allowlist"`
	TTLSeconds    int               `json:"ttl_seconds"`
}

type createMCPSessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// registerMCPSessions 在 /v1 组（已挂 RequireAPIToken）上注册 POST /mcp-sessions。
func registerMCPSessions(g *echo.Group, deps Deps) {
	g.POST("/mcp-sessions", func(c echo.Context) error {
		return createMCPSession(c, deps)
	})
}

// createMCPSession godoc
//
//	@Summary	签发短期 MCP session token（绑定 alias→connection 与 tool allowlist）
//	@ID			createMcpSession
//	@Tags		mcp
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createMCPSessionRequest	true	"绑定与 allowlist；ttl_seconds 默认 3600、上限 86400"
//	@Success	201		{object}	api.createMCPSessionResponse
//	@Failure	400		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/mcp-sessions [post]
func createMCPSession(c echo.Context, deps Deps) error {
	var req createMCPSessionRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
	}
	bindings := make(map[string]uuid.UUID, len(req.Connections))
	for alias, raw := range req.Connections {
		id, err := uuid.Parse(raw)
		if err != nil {
			return writeError(c, http.StatusBadRequest, "invalid_connection_id",
				"connection id "+raw+" is not a valid UUID")
		}
		bindings[alias] = id
	}
	ctx := c.Request().Context()
	token, err := deps.Sessions.Create(ctx, bindings, req.ToolAllowlist,
		time.Duration(req.TTLSeconds)*time.Second)
	var verr *sessions.ValidationError
	if errors.As(err, &verr) {
		return writeError(c, http.StatusBadRequest, verr.Code, verr.Message)
	}
	if err != nil {
		c.Logger().Error(err)
		return writeError(c, http.StatusInternalServerError, "internal", "failed to create mcp session")
	}
	// 回读 expires_at：Create 只返回 token，过期时间以库中落定值为准。
	view, err := deps.Sessions.Resolve(ctx, token)
	if err != nil {
		c.Logger().Error(err)
		return writeError(c, http.StatusInternalServerError, "internal", "failed to load created session")
	}
	return c.JSON(http.StatusCreated, createMCPSessionResponse{
		Token:     token,
		ExpiresAt: view.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
