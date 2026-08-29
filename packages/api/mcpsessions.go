package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/felinics/connect-it/packages/service/sessions"
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

func registerMCPSessions(g *echo.Group, deps Deps) {
	g.POST("/mcp-sessions", func(c echo.Context) error {
		return createMCPSession(c, deps)
	})
}

// createMCPSession godoc
//
//	@Summary	Issue a short-lived MCP session token aggregating several connections
//	@ID			createMcpSession
//	@Tags		mcp
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createMCPSessionRequest	true	"connections maps namespace to connection_id; allowlist entries use namespace__tool names"
//	@Success	201		{object}	api.createMCPSessionResponse
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	502		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/mcp-sessions [post]
func createMCPSession(c echo.Context, deps Deps) error {
	var req createMCPSessionRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "invalid_body", "request body must be valid JSON")
	}
	bindings := make(map[string]uuid.UUID, len(req.Connections))
	for alias, raw := range req.Connections {
		connectionID, err := uuid.Parse(raw)
		if err != nil {
			return writeError(c, http.StatusBadRequest, "invalid_connection_id",
				"connection id "+raw+" is not a valid UUID")
		}
		bindings[alias] = connectionID
	}
	apiTokenID, ok := requestAPITokenID(c)
	if !ok {
		return writeError(c, http.StatusInternalServerError, "internal", "missing API token identity")
	}
	ctx := c.Request().Context()
	result, err := deps.Sessions.Create(
		ctx,
		apiTokenID,
		bindings,
		req.ToolAllowlist,
		time.Duration(req.TTLSeconds)*time.Second,
	)
	var validationErr *sessions.ValidationError
	if errors.As(err, &validationErr) {
		return writeError(c, http.StatusBadRequest, validationErr.Code, validationErr.Message)
	}
	if errors.Is(err, sessions.ErrToolDiscovery) {
		c.Logger().Error(err)
		return writeError(c, http.StatusBadGateway, "tool_discovery_failed",
			"failed to discover tools for the requested connections")
	}
	if err != nil {
		c.Logger().Error(err)
		return writeError(c, http.StatusInternalServerError, "internal", "failed to create mcp session")
	}
	return c.JSON(http.StatusCreated, createMCPSessionResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt.UTC().Format(time.RFC3339),
	})
}
