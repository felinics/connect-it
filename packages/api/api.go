// Package api 装配 HTTP 层：路由、门禁中间件与统一错误映射。
package api

import (
	"context"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/memohai/connect-it/packages/api/docs"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	execsvc "github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/sessions"
)

// ToolExecutor 抽象 exec.Engine，便于 /mcp 测试注入假执行器；
// *exec.Engine 的方法集恰好满足本接口。
type ToolExecutor interface {
	Execute(ctx context.Context, request execsvc.ExecuteRequest) (connector.ToolResultData, error)
}

type Deps struct {
	Registry     *registry.Registry
	Config       *configsvc.Service
	Catalog      *catalogsvc.Service
	Auth         *authsvc.Service
	OAuth        *oauthsvc.Service
	Conns        *connsvc.Service
	Exec         ToolExecutor
	MCPTools     catalogsvc.MCPToolLister
	Sessions     *sessions.Service
	CookieSecret []byte
	// CookieSecure is derived once from the trusted CONNECT_IT_BASE_URL at
	// the composition root. Request proxy headers are not authoritative:
	// an inner reverse proxy can overwrite an outer TLS terminator's scheme.
	CookieSecure bool
}

func New(deps Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	h := &handlers{deps: deps}

	e.GET("/healthz", h.healthz)
	e.GET("/swagger/*", echoSwagger.WrapHandler)
	e.POST("/admin/login", h.login)
	// 回调不走 Bearer 门禁，安全性由一次性 state 保证。
	e.GET("/v1/oauth/callback", h.oauthCallback)

	v1 := e.Group("/v1", RequireAPIToken(deps.Auth))
	v1.GET("/connectors", h.listConnectors)
	v1.GET("/connectors/:type", h.getConnector)
	v1.POST("/connections/oauth", h.beginOAuthConnection)
	v1.POST("/connections/api-key", h.createAPIKeyConnection)
	v1.GET("/connections/:id", h.getConnection)
	v1.PUT("/connections/:id/credential", h.recredentialConnection)
	v1.POST("/connections/:id/reauth", h.reauthConnection)
	v1.DELETE("/connections/:id", h.deleteConnection)
	v1.POST("/mcp-sessions", h.createMCPSession)
	registerMCP(e, deps)

	admin := e.Group(
		"/admin",
		RequireAdminSession(deps.CookieSecret),
		RequireAdminCSRF,
	)
	admin.GET("/connections", h.listConnections)
	admin.POST("/connections/oauth", h.adminBeginOAuthConnection)
	admin.POST("/connections/api-key", h.adminCreateAPIKeyConnection)
	admin.PUT("/connections/:id/credential", h.adminRecredentialConnection)
	admin.POST("/connections/:id/reauth", h.adminReauthConnection)
	admin.DELETE("/connections/:id", h.adminDeleteConnection)
	admin.GET("/connectors", h.adminListConnectors)
	admin.GET("/connectors/:type/config-schema", h.getConfigSchema)
	admin.GET("/connectors/:type/auth-methods", h.listAuthMethods)
	admin.GET("/connectors/:type/config", h.getConfig)
	admin.PUT("/connectors/:type/config", h.putConfig)
	admin.DELETE("/connectors/:type/config", h.deleteConfig)
	admin.POST("/connectors/:type/config\\:validate", h.validateConfig)
	admin.POST("/connectors/:type/mcp\\:verify", h.verifyMCP)
	admin.GET("/api-tokens", h.listAPITokens)
	admin.POST("/api-tokens", h.createAPIToken)
	admin.DELETE("/api-tokens/:id", h.deleteAPIToken)
	admin.PUT("/account/password", h.changePassword)

	return e
}

type handlers struct {
	deps Deps
}
