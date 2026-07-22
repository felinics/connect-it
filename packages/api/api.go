// Package api 装配 HTTP 层：路由、门禁中间件与统一错误映射。
package api

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/memohai/connect-it/packages/api/docs"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

type Deps struct {
	Registry     *registry.Registry
	Store        *store.Queries
	Config       *configsvc.Service
	Catalog      *catalogsvc.Service
	Auth         *authsvc.Service
	OAuth        *oauthsvc.Service
	Conns        *connsvc.Service
	CookieSecret []byte
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

	admin := e.Group("/admin", RequireAdminSession(deps.CookieSecret))
	admin.GET("/connections", h.listConnections)
	admin.POST("/connections/oauth", h.startOAuthConnection)
	admin.POST("/connections/api-key", h.createAPIKeyConnection)
	admin.POST("/connections/:id/reauth", h.reauthConnection)
	admin.DELETE("/connections/:id", h.deleteConnection)
	admin.GET("/connectors", h.adminListConnectors)
	admin.GET("/connectors/:type/config-schema", h.getConfigSchema)
	admin.GET("/connectors/:type/config", h.getConfig)
	admin.PUT("/connectors/:type/config", h.putConfig)
	admin.DELETE("/connectors/:type/config", h.deleteConfig)
	admin.POST("/connectors/:type/config\\:validate", h.validateConfig)
	admin.GET("/api-tokens", h.listAPITokens)
	admin.POST("/api-tokens", h.createAPIToken)
	admin.DELETE("/api-tokens/:id", h.deleteAPIToken)
	admin.PUT("/account/password", h.changePassword)

	return e
}

type handlers struct {
	deps Deps
}
