package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type createTokenRequest struct {
	Name string `json:"name"`
}

type createTokenResponse struct {
	ID    uuid.UUID `json:"id"`
	Token string    `json:"token"`
}

// listAPITokens godoc
//
//	@Summary	列出 API token（不含明文）
//	@ID			listApiTokens
//	@Tags		admin
//	@Produce	json
//	@Success	200	{array}	authsvc.APITokenView
//	@Router		/admin/api-tokens [get]
func (h *handlers) listAPITokens(c echo.Context) error {
	list, err := h.deps.Auth.ListAPITokens(c.Request().Context())
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, list)
}

// createAPIToken godoc
//
//	@Summary	创建 API token（明文仅在响应中出现一次）
//	@ID			createApiToken
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		X-Connect-It-CSRF	header	string					true	"管理台同源写请求固定值 1"
//	@Param		body	body		api.createTokenRequest	true	"token 名称"
//	@Success	201		{object}	api.createTokenResponse
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	403		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Router		/admin/api-tokens [post]
func (h *handlers) createAPIToken(c echo.Context) error {
	var req createTokenRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
	}
	if req.Name == "" {
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", "name 不能为空")
	}
	plaintext, id, err := h.deps.Auth.CreateAPIToken(c.Request().Context(), req.Name)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusCreated, createTokenResponse{ID: id, Token: plaintext})
}

// deleteAPIToken godoc
//
//	@Summary	撤销 API token
//	@ID			deleteApiToken
//	@Tags		admin
//	@Param		X-Connect-It-CSRF	header	string	true	"管理台同源写请求固定值 1"
//	@Param		id	path	string	true	"token id（uuid）"
//	@Success	204
//	@Failure	401	{object}	api.ErrorResponse
//	@Failure	403	{object}	api.ErrorResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/api-tokens/{id} [delete]
func (h *handlers) deleteAPIToken(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return notFound(c)
	}
	if err := h.deps.Auth.RevokeAPIToken(c.Request().Context(), id); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
