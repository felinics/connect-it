package api

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
)

// verifyMCP godoc
//
//	@Summary	实测 Connector 的全部 Remote MCP server 并比对 tool 映射
//	@ID			verifyMcp
//	@Tags		admin
//	@Param		X-Connect-It-CSRF	header	string	true	"管理台同源写请求固定值 1"
//	@Param		type	path	string	true	"connector_type"
//	@Success	204
//	@Failure	401	{object}	api.ErrorResponse
//	@Failure	403	{object}	api.ErrorResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Failure	422	{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/mcp:verify [post]
func (h *handlers) verifyMCP(c echo.Context) error {
	if err := h.deps.Catalog.VerifyMCP(
		c.Request().Context(),
		connector.Type(c.Param("type")),
		h.deps.MCPTools,
	); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
