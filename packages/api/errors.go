package api

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/felinics/connect-it/packages/service/authsvc"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/connsvc"
	"github.com/felinics/connect-it/packages/service/oauthsvc"
)

// ErrorResponse is the single error response body.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(c echo.Context, httpStatus int, code, message string) error {
	return c.JSON(httpStatus, ErrorResponse{Error: code, Message: message})
}

// mapServiceError maps service errors onto uniform HTTP responses. Anything
// unrecognised becomes a 500, and the details go to the log, never into the
// response.
func mapServiceError(c echo.Context, err error) error {
	var ve *configsvc.ValidationError
	switch {
	case errors.Is(err, configsvc.ErrUnknownConnector),
		errors.Is(err, configsvc.ErrNotFound),
		errors.Is(err, configsvc.ErrConnectorDisabled),
		errors.Is(err, authsvc.ErrNotFound),
		errors.Is(err, connsvc.ErrNotFound),
		errors.Is(err, connsvc.ErrUnknownConnector),
		errors.Is(err, oauthsvc.ErrUnknownConnector),
		errors.Is(err, oauthsvc.ErrConnectionGone):
		return writeError(c, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, configsvc.ErrConflict):
		return writeError(c, http.StatusConflict, "conflict", "the config was modified, reload and retry")
	case errors.Is(err, configsvc.ErrIncompatible):
		return writeError(c, http.StatusConflict, "config_incompatible", "the stored config version is newer than the running code")
	case errors.Is(err, connsvc.ErrInvalidAlias),
		errors.Is(err, connsvc.ErrUnknownAuthMethod),
		errors.Is(err, connsvc.ErrWrongAuthType),
		errors.Is(err, connsvc.ErrInvalidFields),
		errors.Is(err, oauthsvc.ErrUnknownAuthMethod),
		errors.Is(err, oauthsvc.ErrNotOAuth):
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, oauthsvc.ErrMissingClient):
		return writeError(c, http.StatusUnprocessableEntity, "oauth_client_not_configured",
			"the connector's OAuth client is not configured; an administrator must set its client ID and client secret")
	case errors.Is(err, oauthsvc.ErrMCPDiscovery):
		c.Logger().Error(err)
		return writeError(c, http.StatusBadGateway, "mcp_oauth_discovery_failed",
			"upstream MCP OAuth discovery failed")
	case errors.Is(err, oauthsvc.ErrMCPRegistration):
		c.Logger().Error(err)
		return writeError(c, http.StatusBadGateway, "mcp_oauth_registration_failed",
			"upstream MCP OAuth client registration failed")
	case errors.As(err, &ve):
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", ve.Error())
	default:
		c.Logger().Error(err)
		return writeError(c, http.StatusInternalServerError, "internal", "internal error")
	}
}
