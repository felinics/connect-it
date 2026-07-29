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
//	@Summary	Log in as administrator and receive a session cookie
//	@ID			login
//	@Tags		admin
//	@Accept		json
//	@Param		body	body	api.loginRequest	true	"User name and password"
//	@Success	204
//	@Failure	401	{object}	api.ErrorResponse
//	@Router		/admin/login [post]
func (h *handlers) login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
	}
	ok, err := h.deps.Auth.VerifyAdminPassword(c.Request().Context(), req.Username, req.Password)
	if err != nil {
		return mapServiceError(c, err)
	}
	if !ok {
		return writeError(c, http.StatusUnauthorized, "invalid_credentials", "wrong user name or password")
	}
	expires := time.Now().Add(sessionTTL)
	c.SetCookie(&http.Cookie{
		Name:     sessionCookieName,
		Value:    signSession(h.deps.CookieSecret, expires),
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return c.NoContent(http.StatusNoContent)
}

// changePassword godoc
//
//	@Summary	Change the administrator password
//	@ID			changePassword
//	@Tags		admin
//	@Accept		json
//	@Param		body	body	api.changePasswordRequest	true	"New password, at least 8 characters"
//	@Success	204
//	@Failure	422	{object}	api.ErrorResponse
//	@Router		/admin/account/password [put]
func (h *handlers) changePassword(c echo.Context) error {
	var req changePasswordRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
	}
	if len(req.Password) < 8 {
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", "password must be at least 8 characters")
	}
	if err := h.deps.Auth.ChangeAdminPassword(c.Request().Context(), req.Password); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
