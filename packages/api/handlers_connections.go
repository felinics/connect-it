package api

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/felinics/connect-it/packages/core/connector"
)

type beginOAuthRequest struct {
	ConnectorType string `json:"connector_type"`
	AuthMethod    string `json:"auth_method"`
	Alias         string `json:"alias"`
}

type beginOAuthResponse struct {
	ConnectionID     uuid.UUID `json:"connection_id"`
	AuthorizationURL string    `json:"authorization_url"`
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
//	@Summary	Start an OAuth authorization: create a pending connection and return its durable ID and authorization URL
//	@ID			beginOAuthConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.beginOAuthRequest	true	"alias is an optional display label"
//	@Success	201		{object}	api.beginOAuthResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse	"validation_failed, or oauth_client_not_configured when the connector has no OAuth client config"
//	@Security	BearerAuth
//	@Router		/v1/connections/oauth [post]
func (h *handlers) beginOAuthConnection(c echo.Context) error {
	var req beginOAuthRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
	}
	result, err := h.deps.OAuth.Begin(c.Request().Context(),
		connector.Type(req.ConnectorType), req.AuthMethod, req.Alias)
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
//	@Summary	Create a connection from an API key or custom credential and return its durable ID
//	@ID			createApiKeyConnection
//	@Tags		connections
//	@Accept		json
//	@Produce	json
//	@Param		body	body		api.createAPIKeyRequest	true	"Fill credential fields per the CredentialFields of the auth method; alias is optional"
//	@Success	201		{object}	api.createConnectionResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Security	BearerAuth
//	@Router		/v1/connections/api-key [post]
func (h *handlers) createAPIKeyConnection(c echo.Context) error {
	var req createAPIKeyRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "request body is not valid JSON")
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
//	@Summary	Get connection status (pending / active / reauth_required and so on)
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
		return writeError(c, http.StatusNotFound, "not_found", "resource not found")
	}
	view, err := h.deps.Conns.Get(c.Request().Context(), id)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, view)
}

// reauthConnection godoc
//
//	@Summary	Re-authorize an existing connection, keeping the same ID
//	@ID			reauthConnection
//	@Tags		connections
//	@Produce	json
//	@Param		id		path		string				true	"connection id（uuid）"
//	@Success	200		{object}	api.beginOAuthResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse	"validation_failed, or oauth_client_not_configured when the connector has no OAuth client config"
//	@Security	BearerAuth
//	@Router		/v1/connections/{id}/reauth [post]
func (h *handlers) reauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "resource not found")
	}
	result, err := h.deps.OAuth.BeginReauth(c.Request().Context(), id)
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
//	@Summary	Delete a connection
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
		return writeError(c, http.StatusNotFound, "not_found", "resource not found")
	}
	if err := h.deps.Conns.Delete(c.Request().Context(), id); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Operator view, cookie authenticated: list every connection, delete one, and
// mint a re-authorization link.

// listConnections godoc
//
//	@Summary	List every connection for operators, without credentials
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
//	@Summary	Mint a re-authorization link for an operator to hand to the right end user
//	@ID			adminReauthConnection
//	@Tags		admin
//	@Produce	json
//	@Param		id	path		string	true	"connection id（uuid）"
//	@Success	200	{object}	api.beginOAuthResponse
//	@Failure	404	{object}	api.ErrorResponse
//	@Failure	422	{object}	api.ErrorResponse	"validation_failed, or oauth_client_not_configured when the connector has no OAuth client config"
//	@Router		/admin/connections/{id}/reauth [post]
func (h *handlers) adminReauthConnection(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return writeError(c, http.StatusNotFound, "not_found", "resource not found")
	}
	result, err := h.deps.OAuth.BeginReauth(c.Request().Context(), id)
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
//	@Summary	Delete a connection, operator view
//	@ID			adminDeleteConnection
//	@Tags		admin
//	@Param		id	path	string	true	"connection id（uuid）"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connections/{id} [delete]
func (h *handlers) adminDeleteConnection(c echo.Context) error {
	return h.deleteConnection(c)
}
