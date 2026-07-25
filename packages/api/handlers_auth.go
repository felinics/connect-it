package api

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type changePasswordRequest struct {
	Password string `json:"password"`
}

// login godoc
//
//	@Summary	管理员登录，成功后下发会话 cookie
//	@ID			login
//	@Tags		admin
//	@Accept		json
//	@Param		body	body	api.loginRequest	true	"用户名与密码"
//	@Success	204
//	@Failure	401	{object}	api.ErrorResponse
//	@Router		/admin/login [post]
func (h *handlers) login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
	}
	ok, err := h.deps.Auth.VerifyAdminPassword(c.Request().Context(), req.Username, req.Password)
	if err != nil {
		return mapServiceError(c, err)
	}
	if !ok {
		return writeError(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
	}
	expires := time.Now().Add(sessionTTL)
	c.SetCookie(&http.Cookie{
		Name:     sessionCookieName,
		Value:    signSession(h.deps.CookieSecret, expires),
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   h.deps.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	return c.NoContent(http.StatusNoContent)
}

// changePassword godoc
//
//	@Summary	修改管理员密码
//	@ID			changePassword
//	@Tags		admin
//	@Accept		json
//	@Param		X-Connect-It-CSRF	header	string						true	"管理台同源写请求固定值 1"
//	@Param		body	body	api.changePasswordRequest	true	"新密码（至少 8 个字符）"
//	@Success	204
//	@Failure	400	{object}	api.ErrorResponse
//	@Failure	401	{object}	api.ErrorResponse
//	@Failure	403	{object}	api.ErrorResponse
//	@Failure	422	{object}	api.ErrorResponse
//	@Router		/admin/account/password [put]
func (h *handlers) changePassword(c echo.Context) error {
	var req changePasswordRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
	}
	if len(req.Password) < 8 {
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", "密码至少 8 个字符")
	}
	if err := h.deps.Auth.ChangeAdminPassword(c.Request().Context(), req.Password); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
