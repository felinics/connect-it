package api

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/service/authsvc"
)

const apiTokenIDContextKey = "connect_it_api_token_id"

// RequireAPIToken 校验 "Authorization: Bearer cit_…" 静态 token（内部应用）。
func RequireAPIToken(auth *authsvc.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			const prefix = "Bearer "
			header := c.Request().Header.Get(echo.HeaderAuthorization)
			if !strings.HasPrefix(header, prefix) {
				return writeError(c, http.StatusUnauthorized, "unauthorized", "缺少 Bearer token")
			}
			id, ok, err := auth.VerifyAPIToken(c.Request().Context(), strings.TrimPrefix(header, prefix))
			if err != nil {
				c.Logger().Error(err)
				return writeError(c, http.StatusInternalServerError, "internal", "internal error")
			}
			if !ok {
				return writeError(c, http.StatusUnauthorized, "unauthorized", "无效的 API token")
			}
			c.Set(apiTokenIDContextKey, id)
			return next(c)
		}
	}
}

func requestAPITokenID(c echo.Context) (uuid.UUID, bool) {
	id, ok := c.Get(apiTokenIDContextKey).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

// RequireAdminSession 校验管理界面的 HMAC 签名 cookie。
func RequireAdminSession(secret []byte) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			cookie, err := c.Cookie(sessionCookieName)
			if err != nil || !verifySession(secret, cookie.Value) {
				return writeError(c, http.StatusUnauthorized, "unauthorized", "请先登录")
			}
			return next(c)
		}
	}
}
