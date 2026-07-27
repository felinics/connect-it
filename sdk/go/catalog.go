package connectit

import (
	"context"
	"net/http"
	"net/url"
)

// AuthMethod describes a downstream-safe connector authentication option.
type AuthMethod struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

// Connector is one item from the Connect-It catalog.
type Connector struct {
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Categories  []string     `json:"categories"`
	HomepageURL string       `json:"homepage_url"`
	IconURL     string       `json:"icon_url"`
	Mode        string       `json:"mode"`
	Status      string       `json:"status"`
	AuthMethods []AuthMethod `json:"auth_methods"`
}

// ListConnectors returns the configured connector catalog.
func (c *Client) ListConnectors(ctx context.Context) ([]Connector, error) {
	var connectors []Connector
	if err := c.doJSON(ctx, http.MethodGet, "/v1/connectors", nil, &connectors); err != nil {
		return nil, err
	}
	if connectors == nil {
		connectors = []Connector{}
	}
	return connectors, nil
}

// GetConnector returns one catalog item by stable connector type.
func (c *Client) GetConnector(ctx context.Context, connectorType string) (Connector, error) {
	var connector Connector
	err := c.doJSON(ctx, http.MethodGet,
		"/v1/connectors/"+url.PathEscape(connectorType), nil, &connector)
	return connector, err
}
