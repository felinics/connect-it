package api

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
)

// healthz godoc
//
//	@Summary	Health check
//	@ID			healthz
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	map[string]string
//	@Router		/healthz [get]
func (h *handlers) healthz(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// listConnectors godoc
//
//	@Summary	List every connector with its runtime status
//	@ID			listConnectors
//	@Tags		catalog
//	@Produce	json
//	@Success	200	{array}		catalogsvc.Item
//	@Failure	401	{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connectors [get]
func (h *handlers) listConnectors(c echo.Context) error {
	items, err := h.deps.Catalog.List(c.Request().Context())
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, items)
}

// getConnector godoc
//
//	@Summary	Get a single connector
//	@ID			getConnector
//	@Tags		catalog
//	@Produce	json
//	@Param		type	path		string	true	"connector_type"
//	@Success	200		{object}	catalogsvc.Item
//	@Failure	404		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connectors/{type} [get]
func (h *handlers) getConnector(c echo.Context) error {
	item, err := h.deps.Catalog.Get(c.Request().Context(), connector.Type(c.Param("type")))
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, item)
}

// adminListConnectors godoc
//
//	@Summary	List every connector for the admin UI, cookie authenticated
//	@ID			adminListConnectors
//	@Tags		admin
//	@Produce	json
//	@Success	200	{array}		catalogsvc.Item
//	@Failure	401	{object}	api.ErrorResponse
//	@Router		/admin/connectors [get]
func (h *handlers) adminListConnectors(c echo.Context) error {
	items, err := h.deps.Catalog.List(c.Request().Context())
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, items)
}
