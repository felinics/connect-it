package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
)

type beginOAuthRequest struct {
	ConnectorType string `json:"connector_type"`
	AuthMethod    string `json:"auth_method"`
	Alias         string `json:"alias"`
	RedirectURL   string `json:"redirect_url"`
}

type beginOAuthResponse struct {
	ConnectionID     uuid.UUID `json:"connection_id"`
	AuthorizationURL string    `json:"authorization_url"`
}

type reauthRequest struct {
	RedirectURL string `json:"redirect_url"`
}

type createAPIKeyRequest struct {
	ConnectorType string            `json:"connector_type"`
	AuthMethod    string            `json:"auth_method"`
	Alias         string            `json:"alias"`
	Fields        map[string]string `json:"fields"`
}

type createConnectionResponse struct {
	ConnectionID uuid.UUID `json:"connection_id"`
}

// beginOAuthConnection godoc
//
//	@Summary	发起 OAuth 授权：立即创建 pending 连接并返回其持久 ID 与授权 URL
//	@ID			beginOAuthConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.beginOAuthRequest	true	"alias 为可选展示标签；redirect_url 为授权完成后回跳调用方的地址（可选）"
//	@Success	201		{object}	api.beginOAuthResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/oauth [post]
func (h *handlers) beginOAuthConnection(c echo.Context) error {
	var req beginOAuthRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
	}
	result, err := h.deps.OAuth.Begin(c.Request().Context(),
		connector.Type(req.ConnectorType), req.AuthMethod, req.Alias, req.RedirectURL)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusCreated, beginOAuthResponse{
		ConnectionID:     result.ConnectionID,
		AuthorizationURL: result.AuthorizationURL,
	})
}

// createApiKeyConnection godoc
//
//	@Summary	用 API key / 自定义凭证创建连接，返回其持久 ID
//	@ID			createApiKeyConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createAPIKeyRequest	true	"凭证字段按 auth method 的 CredentialFields 填写；alias 可选"
//	@Success	201		{object}	api.createConnectionResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/api-key [post]
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
	return c.JSON(http.StatusCreated, createConnectionResponse{ConnectionID: id})
}

// getConnection godoc
//
//	@Summary	查询连接状态（pending / active / reauth_required …）
//	@ID			getConnection
//	@Tags		connections
//	@Produce	json
//	@Param		id	path		string	true	"connection id（uuid）"
//	@Success	200	{object}	connsvc.ConnectionView
//	@Failure	404	{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/{id} [get]
func (h *handlers) getConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	view, err := h.deps.Conns.Get(c.Request().Context(), id)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, view)
}

// reauthConnection godoc
//
//	@Summary	对既有连接重新发起授权（ID 不变）
//	@ID			reauthConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		id		path		string				true	"connection id（uuid）"
//	@Param		body	body		api.reauthRequest	false	"redirect_url 可选"
//	@Success	200		{object}	api.beginOAuthResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/{id}/reauth [post]
func (h *handlers) reauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	var req reauthRequest
	_ = c.Bind(&req) // body 可省略
	result, err := h.deps.OAuth.BeginReauth(c.Request().Context(), id, req.RedirectURL)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, beginOAuthResponse{
		ConnectionID:     result.ConnectionID,
		AuthorizationURL: result.AuthorizationURL,
	})
}

// deleteConnection godoc
//
//	@Summary	删除连接
//	@ID			deleteConnection
//	@Tags		connections
//	@Param		id	path	string	true	"connection id（uuid）"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/{id} [delete]
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

// —— 管理台运维视角（cookie 鉴权）：查看全部连接、删除、生成重授权链接 ——

// listConnections godoc
//
//	@Summary	列出全部连接（运维视角，不含 credential）
//	@ID			listConnections
//	@Tags		admin
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

// adminReauthConnection godoc
//
//	@Summary	生成重授权链接（运维转交给对应用户打开）
//	@ID			adminReauthConnection
//	@Tags		admin
//	@Produce	json
//	@Param		id	path		string	true	"connection id（uuid）"
//	@Success	200	{object}	api.beginOAuthResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id}/reauth [post]
func (h *handlers) adminReauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	result, err := h.deps.OAuth.BeginReauth(c.Request().Context(), id, "")
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, beginOAuthResponse{
		ConnectionID:     result.ConnectionID,
		AuthorizationURL: result.AuthorizationURL,
	})
}

// adminDeleteConnection godoc
//
//	@Summary	删除连接（运维）
//	@ID			adminDeleteConnection
//	@Tags		admin
//	@Param		id	path	string	true	"connection id（uuid）"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id} [delete]
func (h *handlers) adminDeleteConnection(c echo.Context) error {
	return h.deleteConnection(c)
}
