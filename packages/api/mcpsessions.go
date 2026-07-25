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
	// Connections 将 Session-local alias 映射到持久 Connection UUID。
	Connections map[string]string `json:"connections"`
	// ToolAllowlist 区分三态：省略时固化当前 read Tool，[] 授权零 Tool，null
	// 非法；write/destructive Tool 必须显式列出。
	ToolAllowlist sessions.AllowlistInput `json:"tool_allowlist" swaggertype:"array,string"`
	// TTLSeconds 是 Session 有效期；0 使用默认 3600 秒，上限 86400 秒。
	TTLSeconds int64 `json:"ttl_seconds" minimum:"0" maximum:"86400"`
}

type createMCPSessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// createMCPSession godoc
//
//	@Summary	签发短期 MCP session token（固化 alias、Tool grant 与授权代际）
//	@ID			createMcpSession
//	@Tags		mcp
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createMCPSessionRequest	true	"连接绑定与不可变 grant 快照；省略 allowlist 默认当前 read，[] 为零 Tool，null 拒绝；write/destructive 必须显式列出；ttl_seconds 默认 3600、上限 86400"
//	@Success	201		{object}	api.createMCPSessionResponse
//	@Failure	400		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/mcp-sessions [post]
func (h *handlers) createMCPSession(c echo.Context) error {
	var req createMCPSessionRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
	}
	maxTTLSeconds := int64(sessions.MaxTTL / time.Second)
	if req.TTLSeconds < 0 || req.TTLSeconds > maxTTLSeconds {
		return writeError(
			c,
			http.StatusBadRequest,
			"invalid_ttl",
			"ttl_seconds must be between 0 and 86400",
		)
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
	created, err := h.deps.Sessions.CreateWithExpiry(
		ctx,
		bindings,
		req.ToolAllowlist,
		time.Duration(req.TTLSeconds)*time.Second,
	)
	var verr *sessions.ValidationError
	if errors.As(err, &verr) {
		return writeError(c, http.StatusBadRequest, verr.Code, verr.Message)
	}
	if err != nil {
		c.Logger().Error(err)
		return writeError(c, http.StatusInternalServerError, "internal", "failed to create mcp session")
	}
	return c.JSON(http.StatusCreated, createMCPSessionResponse{
		Token:     created.Token,
		ExpiresAt: created.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}
