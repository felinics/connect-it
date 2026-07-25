package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
)

type beginOAuthRequest struct {
	ConnectorType string `json:"connector_type" binding:"required"`
	AuthMethod    string `json:"auth_method" binding:"required"`
	Alias         string `json:"alias"`
	RedirectURL   string `json:"redirect_url"`
}

// adminBeginOAuthRequest intentionally has no caller-controlled redirect URL.
// The management console completes OAuth on connect-it's own confirmation
// page, so a compromised admin browser cannot turn this endpoint into an open
// redirect handoff.
type adminBeginOAuthRequest struct {
	ConnectorType string `json:"connector_type" binding:"required"`
	AuthMethod    string `json:"auth_method" binding:"required"`
	Alias         string `json:"alias"`
}

type beginOAuthResponse struct {
	ConnectionID     uuid.UUID `json:"connection_id"`
	AuthorizationURL string    `json:"authorization_url"`
}

type reauthRequest struct {
	RedirectURL string `json:"redirect_url"`
}

type createAPIKeyRequest struct {
	ConnectorType string            `json:"connector_type" binding:"required"`
	AuthMethod    string            `json:"auth_method" binding:"required"`
	Alias         string            `json:"alias"`
	Fields        map[string]string `json:"fields" binding:"required"`
}

type createConnectionResponse struct {
	ConnectionID uuid.UUID `json:"connection_id"`
}

type recredentialRequest struct {
	// Fields 是完整 API-key/custom-credential 字段组；缺少 required 字段会被
	// 拒绝，本端点不是局部 PATCH。
	Fields map[string]string `json:"fields" binding:"required"`
}

func bindOptionalBody(c echo.Context, target any) error {
	if c.Request().Body == nil || c.Request().ContentLength == 0 {
		return nil
	}
	err := c.Bind(target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
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
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/oauth [post]
func (h *handlers) beginOAuthConnection(c echo.Context) error {
	var req beginOAuthRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
	}
	result, err := h.deps.OAuth.BeginWithInput(
		c.Request().Context(),
		oauthsvc.BeginInput{
			ConnectorType: connector.Type(req.ConnectorType),
			AuthMethodKey: req.AuthMethod,
			Alias:         req.Alias,
			RedirectURL:   req.RedirectURL,
		},
	)
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
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	403		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	408		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Failure	429		{object}	api.ErrorResponse
//	@Failure	502		{object}	api.ErrorResponse
//	@Failure	503		{object}	api.ErrorResponse
//	@Failure	504		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/api-key [post]
func (h *handlers) createAPIKeyConnection(c echo.Context) error {
	var req createAPIKeyRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
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
		return notFound(c)
	}
	view, err := h.deps.Conns.Get(c.Request().Context(), id)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, view)
}

// recredentialConnection godoc
//
//	@Summary	验证并整组替换 API key / 自定义凭证；并发旧请求按版本冲突拒绝
//	@ID			recredentialConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		id		path		string						true	"connection id（uuid）"
//	@Param		body	body		api.recredentialRequest	true	"完整 credential 字段组；不是局部 PATCH"
//	@Success	200		{object}	connsvc.ConnectionView
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	403		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	408		{object}	api.ErrorResponse
//	@Failure	409		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Failure	429		{object}	api.ErrorResponse
//	@Failure	502		{object}	api.ErrorResponse
//	@Failure	503		{object}	api.ErrorResponse
//	@Failure	504		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/{id}/credential [put]
func (h *handlers) recredentialConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return notFound(c)
	}
	var req recredentialRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
	}
	view, err := h.deps.Conns.RecredentialAPIKey(
		c.Request().Context(),
		id,
		req.Fields,
	)
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
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	409		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/{id}/reauth [post]
func (h *handlers) reauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return notFound(c)
	}
	var req reauthRequest
	if err := bindOptionalBody(c, &req); err != nil {
		return badRequest(c)
	}
	result, err := h.deps.OAuth.BeginReauthWithInput(
		c.Request().Context(),
		oauthsvc.ReauthInput{
			ConnectionID: id,
			RedirectURL:  req.RedirectURL,
		},
	)
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
		return notFound(c)
	}
	if err := h.deps.Conns.Delete(c.Request().Context(), id); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// —— 管理台运维视角（cookie 鉴权）：创建、查看、换密、重授权与删除 ——

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

// adminBeginOAuthConnection godoc
//
//	@Summary	从管理台发起 OAuth 授权，返回持久连接 ID 与授权 URL
//	@ID			adminBeginOAuthConnection
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		X-Connect-It-CSRF	header	string							true	"管理台同源写请求固定值 1"
//	@Param		body	body		api.adminBeginOAuthRequest	true	"alias 为可选展示标签；管理台授权固定回到 connect-it 完成页"
//	@Success	201		{object}	api.beginOAuthResponse
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	403		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Router		/admin/connections/oauth [post]
func (h *handlers) adminBeginOAuthConnection(c echo.Context) error {
	var req adminBeginOAuthRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c)
	}
	result, err := h.deps.OAuth.BeginWithInput(
		c.Request().Context(),
		oauthsvc.BeginInput{
			ConnectorType: connector.Type(req.ConnectorType),
			AuthMethodKey: req.AuthMethod,
			Alias:         req.Alias,
		},
	)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusCreated, beginOAuthResponse{
		ConnectionID:     result.ConnectionID,
		AuthorizationURL: result.AuthorizationURL,
	})
}

// adminCreateApiKeyConnection godoc
//
//	@Summary	从管理台用 API key / 自定义凭证创建连接
//	@ID			adminCreateApiKeyConnection
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		X-Connect-It-CSRF	header	string						true	"管理台同源写请求固定值 1"
//	@Param		body	body		api.createAPIKeyRequest	true	"完整凭证字段组按 auth method 的 CredentialFields 填写；alias 可选"
//	@Success	201		{object}	api.createConnectionResponse
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	403		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	408		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Failure	429		{object}	api.ErrorResponse
//	@Failure	502		{object}	api.ErrorResponse
//	@Failure	503		{object}	api.ErrorResponse
//	@Failure	504		{object}	api.ErrorResponse
//	@Router		/admin/connections/api-key [post]
func (h *handlers) adminCreateAPIKeyConnection(c echo.Context) error {
	return h.createAPIKeyConnection(c)
}

// adminRecredentialConnection godoc
//
//	@Summary	从管理台验证并整组替换 API key / 自定义凭证
//	@ID			adminRecredentialConnection
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		X-Connect-It-CSRF	header	string						true	"管理台同源写请求固定值 1"
//	@Param		id		path		string						true	"connection id（uuid）"
//	@Param		body	body		api.recredentialRequest	true	"完整 credential 字段组；不是局部 PATCH"
//	@Success	200		{object}	connsvc.ConnectionView
//	@Failure	400		{object}	api.ErrorResponse
//	@Failure	401		{object}	api.ErrorResponse
//	@Failure	403		{object}	api.ErrorResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	408		{object}	api.ErrorResponse
//	@Failure	409		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Failure	429		{object}	api.ErrorResponse
//	@Failure	502		{object}	api.ErrorResponse
//	@Failure	503		{object}	api.ErrorResponse
//	@Failure	504		{object}	api.ErrorResponse
//	@Router		/admin/connections/{id}/credential [put]
func (h *handlers) adminRecredentialConnection(c echo.Context) error {
	return h.recredentialConnection(c)
}

// adminReauthConnection godoc
//
//	@Summary	生成重授权链接（运维转交给对应用户打开）
//	@ID			adminReauthConnection
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		X-Connect-It-CSRF	header	string					true	"管理台同源写请求固定值 1"
//	@Param		id					path	string					true	"connection id（uuid）"
//	@Success	200	{object}	api.beginOAuthResponse
//	@Failure	400	{object}	api.ErrorResponse
//	@Failure	401	{object}	api.ErrorResponse
//	@Failure	403	{object}	api.ErrorResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Failure	409	{object}	api.ErrorResponse
//	@Failure	422	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id}/reauth [post]
func (h *handlers) adminReauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return notFound(c)
	}
	result, err := h.deps.OAuth.BeginReauthWithInput(
		c.Request().Context(),
		oauthsvc.ReauthInput{
			ConnectionID: id,
		},
	)
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
//	@Param		X-Connect-It-CSRF	header	string	true	"管理台同源写请求固定值 1"
//	@Param		id	path	string	true	"connection id（uuid）"
//	@Success	204
//	@Failure	401	{object}	api.ErrorResponse
//	@Failure	403	{object}	api.ErrorResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id} [delete]
func (h *handlers) adminDeleteConnection(c echo.Context) error {
	return h.deleteConnection(c)
}
