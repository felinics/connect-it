package api

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/buildinfo"
)

type VersionResponse struct {
	Version string `json:"version"`
}

// getVersion godoc
//
//	@Summary	Get the running service version
//	@ID			getVersion
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	VersionResponse
//	@Router		/version [get]
func (h *handlers) getVersion(c echo.Context) error {
	return c.JSON(http.StatusOK, VersionResponse{Version: buildinfo.Version})
}
