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
//	@Summary	List API tokens without plaintext
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
//	@Summary	Create an API token; the plaintext appears in this response only
//	@ID			createApiToken
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createTokenRequest	true	"Token name"
//	@Success	201		{object}	api.createTokenResponse
//	@Router		/admin/api-tokens [post]
func (h *handlers) createAPIToken(c echo.Context) error {
	var req createTokenRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
	}
	if req.Name == "" {
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", "name must not be empty")
	}
	plaintext, id, err := h.deps.Auth.CreateAPIToken(c.Request().Context(), req.Name)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusCreated, createTokenResponse{ID: id, Token: plaintext})
}

// deleteAPIToken godoc
//
//	@Summary	Revoke an API token
//	@ID			deleteApiToken
//	@Tags		admin
//	@Param		id	path	string	true	"token id（uuid）"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/api-tokens/{id} [delete]
func (h *handlers) deleteAPIToken(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "resource not found")
	}
	if err := h.deps.Auth.RevokeAPIToken(c.Request().Context(), id); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
