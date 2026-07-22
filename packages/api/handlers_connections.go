package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
)

type startOAuthRequest struct {
	ConnectorType string `json:"connector_type"`
	AuthMethod    string `json:"auth_method"`
	Alias         string `json:"alias"`
}

type authorizationURLResponse struct {
	AuthorizationURL string `json:"authorization_url"`
}

type createAPIKeyRequest struct {
	ConnectorType string            `json:"connector_type"`
	AuthMethod    string            `json:"auth_method"`
	Alias         string            `json:"alias"`
	Fields        map[string]string `json:"fields"`
}

type createConnectionResponse struct {
	ID uuid.UUID `json:"id"`
}

// listConnections godoc
//
//	@Summary	列出全部 connection（不含 credential）
//	@ID			listConnections
//	@Tags		connections
//	@Produce	json
//	@Success	200	{array}	connsvc.ConnectionView
//	@Router		/admin/connections [get]
func (h *handlers) listConnections(c echo.Context) error {
	list, err := h.deps.Conns.List(c.Request().Context())
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, list)
}

// startOAuthConnection godoc
//
//	@Summary	发起 OAuth 授权，返回跳转地址
//	@ID			startOAuth
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.startOAuthRequest	true	"目标 connector、auth method 与 alias"
//	@Success	200		{object}	api.authorizationURLResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	409		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Router		/admin/connections/oauth [post]
func (h *handlers) startOAuthConnection(c echo.Context) error {
	var req startOAuthRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
	}
	authURL, err := h.deps.OAuth.Begin(c.Request().Context(),
		connector.Type(req.ConnectorType), req.AuthMethod, req.Alias)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, authorizationURLResponse{AuthorizationURL: authURL})
}

// createAPIKeyConnection godoc
//
//	@Summary	用 API key / 自定义凭证创建 connection
//	@ID			createApiKeyConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createAPIKeyRequest	true	"凭证字段按 auth method 的 CredentialFields 填写"
//	@Success	201		{object}	api.createConnectionResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	409		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Router		/admin/connections/api-key [post]
func (h *handlers) createAPIKeyConnection(c echo.Context) error {
	var req createAPIKeyRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
	}
	id, err := h.deps.Conns.CreateAPIKey(c.Request().Context(),
		connector.Type(req.ConnectorType), req.AuthMethod, req.Alias, req.Fields)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusCreated, createConnectionResponse{ID: id})
}

// reauthConnection godoc
//
//	@Summary	对既有 connection 重新发起授权
//	@ID			reauthConnection
//	@Tags		connections
//	@Produce	json
//	@Param		id	path		string	true	"connection id（uuid）"
//	@Success	200	{object}	api.authorizationURLResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id}/reauth [post]
func (h *handlers) reauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	authURL, err := h.deps.OAuth.BeginReauth(c.Request().Context(), id)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, authorizationURLResponse{AuthorizationURL: authURL})
}

// deleteConnection godoc
//
//	@Summary	删除 connection
//	@ID			deleteConnection
//	@Tags		connections
//	@Param		id	path	string	true	"connection id（uuid）"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id} [delete]
func (h *handlers) deleteConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	if err := h.deps.Conns.Delete(c.Request().Context(), id); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
