// Package api wires the HTTP layer: routes, gate middleware and a single
// error mapping.
package api

import (
	"context"
	"io/fs"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/memohai/connect-it/packages/api/docs"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
)

// ToolExecutor abstracts exec.Engine so /mcp tests can inject a fake
// executor. The method set of *exec.Engine satisfies this interface exactly.
type ToolExecutor interface {
	CallTool(
		ctx context.Context,
		sessionID, apiTokenID, connectionID uuid.UUID,
		params *mcp.CallToolParamsRaw,
	) (*mcp.CallToolResult, error)
}

type Deps struct {
	Registry     *registry.Registry
	Store        *store.Queries
	Config       *configsvc.Service
	Catalog      *catalogsvc.Service
	Auth         *authsvc.Service
	OAuth        *oauthsvc.Service
	Conns        *connsvc.Service
	Exec         ToolExecutor
	Sessions     *sessions.Service
	CookieSecret []byte
	Web          fs.FS
}

func New(deps Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	h := &handlers{deps: deps}

	e.GET("/healthz", h.healthz)
	e.GET("/version", h.getVersion)
	e.GET("/swagger/*", echoSwagger.WrapHandler)
	e.POST("/admin/login", h.login)
	// The callback skips the Bearer gate; a single-use state secures it.
	e.GET("/v1/oauth/callback", h.oauthCallback)

	v1 := e.Group("/v1", RequireAPIToken(deps.Auth))
	v1.GET("/connectors", h.listConnectors)
	v1.GET("/connectors/:type", h.getConnector)
	v1.POST("/connections/oauth", h.beginOAuthConnection)
	v1.POST("/connections/api-key", h.createAPIKeyConnection)
	v1.GET("/connections/:id", h.getConnection)
	v1.POST("/connections/:id/reauth", h.reauthConnection)
	v1.DELETE("/connections/:id", h.deleteConnection)
	registerMCPSessions(v1, deps)
	registerMCP(e, deps)

	admin := e.Group("/admin", RequireAdminSession(deps.CookieSecret))
	admin.GET("/connections", h.listConnections)
	admin.POST("/connections/:id/reauth", h.adminReauthConnection)
	admin.DELETE("/connections/:id", h.adminDeleteConnection)
	admin.GET("/connectors", h.adminListConnectors)
	admin.GET("/connectors/:type/config-schema", h.getConfigSchema)
	admin.GET("/connectors/:type/auth-methods", h.listAuthMethods)
	admin.GET("/connectors/:type/config", h.getConfig)
	admin.PUT("/connectors/:type/config", h.putConfig)
	admin.DELETE("/connectors/:type/config", h.deleteConfig)
	admin.GET("/api-tokens", h.listAPITokens)
	admin.POST("/api-tokens", h.createAPIToken)
	admin.DELETE("/api-tokens/:id", h.deleteAPIToken)
	admin.PUT("/account/password", h.changePassword)

	registerWeb(e, deps.Web)

	return e
}

type handlers struct {
	deps Deps
}
