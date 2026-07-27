package connectit

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// BeginOAuthRequest starts an end-user authorization flow.
type BeginOAuthRequest struct {
	ConnectorType string `json:"connector_type"`
	AuthMethod    string `json:"auth_method"`
	Alias         string `json:"alias,omitempty"`
	RedirectURL   string `json:"redirect_url,omitempty"`
}

// OAuthAuthorization contains the persistent connection ID and the URL the
// end user must open.
type OAuthAuthorization struct {
	ConnectionID     string `json:"connection_id"`
	AuthorizationURL string `json:"authorization_url"`
}

// Connection is the credential-free connection view returned to a downstream.
type Connection struct {
	ID            string    `json:"id"`
	ConnectorType string    `json:"connector_type"`
	Alias         string    `json:"alias"`
	AuthMethod    string    `json:"auth_method"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateAPIKeyConnectionRequest creates an API-key or custom-credential
// connection using fields declared by its auth method.
type CreateAPIKeyConnectionRequest struct {
	ConnectorType string            `json:"connector_type"`
	AuthMethod    string            `json:"auth_method"`
	Alias         string            `json:"alias,omitempty"`
	Fields        map[string]string `json:"fields"`
}

// CreateConnectionResult contains the persistent Connect-It connection ID.
type CreateConnectionResult struct {
	ConnectionID string `json:"connection_id"`
}

// BeginOAuth creates a pending connection and returns its authorization URL.
func (c *Client) BeginOAuth(ctx context.Context, request BeginOAuthRequest) (OAuthAuthorization, error) {
	var result OAuthAuthorization
	err := c.doJSON(ctx, http.MethodPost, "/v1/connections/oauth", request, &result)
	return result, err
}

// CreateAPIKeyConnection creates an active non-OAuth connection.
func (c *Client) CreateAPIKeyConnection(ctx context.Context, request CreateAPIKeyConnectionRequest) (CreateConnectionResult, error) {
	var result CreateConnectionResult
	err := c.doJSON(ctx, http.MethodPost, "/v1/connections/api-key", request, &result)
	return result, err
}

// GetConnection returns the current connection status without credentials.
func (c *Client) GetConnection(ctx context.Context, connectionID string) (Connection, error) {
	var connection Connection
	err := c.doJSON(ctx, http.MethodGet, connectionPath(connectionID), nil, &connection)
	return connection, err
}

// ReauthorizeConnection starts OAuth again while preserving the connection ID.
func (c *Client) ReauthorizeConnection(ctx context.Context, connectionID, redirectURL string) (OAuthAuthorization, error) {
	var result OAuthAuthorization
	err := c.doJSON(ctx, http.MethodPost, connectionPath(connectionID)+"/reauth",
		struct {
			RedirectURL string `json:"redirect_url,omitempty"`
		}{RedirectURL: redirectURL}, &result)
	return result, err
}

// DeleteConnection removes a connection and its stored third-party credential.
func (c *Client) DeleteConnection(ctx context.Context, connectionID string) error {
	return c.doJSON(ctx, http.MethodDelete, connectionPath(connectionID), nil, nil)
}

func connectionPath(connectionID string) string {
	return "/v1/connections/" + url.PathEscape(connectionID)
}
