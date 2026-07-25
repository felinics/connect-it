package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/svcerr"
)

// ErrorResponse 是统一错误响应体。
type ErrorResponse struct {
	// Error 是稳定、机器可读的错误码。
	Error string `json:"error"`
	// Message 可安全返回调用方，不含 Provider 原始响应或 credential 值。
	Message string `json:"message"`
	// Temporary 表示稍后重试是否可能成功。
	Temporary bool `json:"temporary,omitempty"`
	// RetryAfterSeconds 仅在已知正数重试延迟时出现。
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

func writeError(c echo.Context, httpStatus int, code, message string) error {
	return c.JSON(httpStatus, ErrorResponse{Error: code, Message: message})
}

// notFound 让「资源不存在」与「路径 ID 形态非法」返回同一个响应，
// 调用方无法区分这两种情况。
func notFound(c echo.Context) error {
	return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
}

// badRequest 是请求体解码失败的统一响应。
func badRequest(c echo.Context) error {
	return writeError(c, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
}

// serviceErrorResponses 是 service 层错误词汇表到 HTTP 的完整映射。message 为
// 空表示回给调用方的是错误自身的安全文案（含业务包附加的字段名等上下文），
// 否则用这里的固定文案，避免把 Provider 细节带出去。
var serviceErrorResponses = map[svcerr.Kind]struct {
	status  int
	code    string
	message string
}{
	svcerr.NotFound:           {http.StatusNotFound, "not_found", "资源不存在"},
	svcerr.ConfigConflict:     {http.StatusConflict, "conflict", "配置已被修改，请刷新后重试"},
	svcerr.ConnectionConflict: {http.StatusConflict, "conflict", "连接已被修改，请重新读取后重试"},
	svcerr.ConfigIncompatible: {
		http.StatusConflict, "config_incompatible", "数据库配置版本比当前代码新",
	},
	svcerr.Invalid: {http.StatusUnprocessableEntity, "validation_failed", ""},
	svcerr.EgressRejected: {
		http.StatusUnprocessableEntity,
		"egress_policy_rejected",
		"OAuth endpoint configuration was rejected",
	},
	svcerr.VerifyFailed: {http.StatusUnprocessableEntity, "verify_failed", ""},
}

// mapServiceError 把业务错误映射为统一 HTTP 响应；未识别的错误一律 500，
// 详情只进日志不出响应。
func mapServiceError(c echo.Context, err error) error {
	if serviceErr := svcerr.From(err); serviceErr != nil {
		if response, known := serviceErrorResponses[serviceErr.Kind]; known {
			message := response.message
			if message == "" {
				message = err.Error()
			}
			return writeError(c, response.status, response.code, message)
		}
	}
	var credentialErr *connector.CredentialValidationError
	if errors.As(err, &credentialErr) {
		return writeCredentialValidationError(c, credentialErr)
	}
	c.Logger().Error(err)
	return writeError(c, http.StatusInternalServerError, "internal", "internal error")
}

func writeCredentialValidationError(
	c echo.Context,
	validationErr *connector.CredentialValidationError,
) error {
	if validationErr == nil {
		return writeError(
			c,
			http.StatusInternalServerError,
			"internal",
			"internal error",
		)
	}
	// Reuse the public ToolFailure shape validator so a Provider-local
	// validator cannot smuggle raw response text or malformed metadata into
	// the Connection API.
	if _, err := connector.NewToolFailure(
		validationErr.Code,
		validationErr.SafeMessage,
		validationErr.UpstreamStatus,
		validationErr.RetryAfterSeconds,
	); err != nil {
		c.Logger().Error("invalid credential validation error shape")
		return writeError(
			c,
			http.StatusInternalServerError,
			"internal",
			"internal error",
		)
	}
	status := http.StatusUnprocessableEntity
	switch validationErr.Code {
	case connector.FailureAuthorizationFailed:
		status = http.StatusUnauthorized
	case connector.FailurePermissionDenied:
		status = http.StatusForbidden
	case connector.FailureRateLimited:
		status = http.StatusTooManyRequests
	case connector.FailureUpstreamUnavailable:
		status = http.StatusServiceUnavailable
	case connector.FailureTimeout:
		status = http.StatusGatewayTimeout
	case connector.FailureCanceled:
		status = http.StatusRequestTimeout
	case connector.FailureInvalidResponse,
		connector.FailureResponseTooLarge,
		connector.FailureProviderError:
		status = http.StatusBadGateway
	}
	if validationErr.RetryAfterSeconds > 0 {
		c.Response().Header().Set(
			"Retry-After",
			strconv.Itoa(validationErr.RetryAfterSeconds),
		)
	}
	return c.JSON(status, ErrorResponse{
		Error:             string(validationErr.Code),
		Message:           validationErr.SafeMessage,
		Temporary:         validationErr.Temporary,
		RetryAfterSeconds: validationErr.RetryAfterSeconds,
	})
}
