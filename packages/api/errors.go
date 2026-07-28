package api

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
)

// ErrorResponse 是统一错误响应体。
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(c echo.Context, httpStatus int, code, message string) error {
	return c.JSON(httpStatus, ErrorResponse{Error: code, Message: message})
}

// mapServiceError 把业务错误映射为统一 HTTP 响应；未识别的错误一律 500，
// 详情只进日志不出响应。
func mapServiceError(c echo.Context, err error) error {
	var ve *configsvc.ValidationError
	switch {
	case errors.Is(err, configsvc.ErrUnknownConnector),
		errors.Is(err, configsvc.ErrNotFound),
		errors.Is(err, authsvc.ErrNotFound),
		errors.Is(err, connsvc.ErrNotFound),
		errors.Is(err, connsvc.ErrUnknownConnector),
		errors.Is(err, oauthsvc.ErrUnknownConnector),
		errors.Is(err, oauthsvc.ErrConnectionGone):
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, configsvc.ErrConflict):
		return writeError(c, http.StatusConflict, "conflict", "配置已被修改，请刷新后重试")
	case errors.Is(err, configsvc.ErrIncompatible):
		return writeError(c, http.StatusConflict, "config_incompatible", "数据库配置版本比当前代码新")
	case errors.Is(err, connsvc.ErrInvalidAlias),
		errors.Is(err, connsvc.ErrUnknownAuthMethod),
		errors.Is(err, connsvc.ErrWrongAuthType),
		errors.Is(err, connsvc.ErrInvalidFields),
		errors.Is(err, oauthsvc.ErrUnknownAuthMethod),
		errors.Is(err, oauthsvc.ErrNotOAuth),
		errors.Is(err, oauthsvc.ErrMissingClient):
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, oauthsvc.ErrMCPDiscovery):
		c.Logger().Error(err)
		return writeError(c, http.StatusBadGateway, "mcp_oauth_discovery_failed",
			"上游 MCP OAuth discovery 失败")
	case errors.Is(err, oauthsvc.ErrMCPRegistration):
		c.Logger().Error(err)
		return writeError(c, http.StatusBadGateway, "mcp_oauth_registration_failed",
			"上游 MCP OAuth client registration 失败")
	case errors.As(err, &ve):
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", ve.Error())
	default:
		c.Logger().Error(err)
		return writeError(c, http.StatusInternalServerError, "internal", "internal error")
	}
}
