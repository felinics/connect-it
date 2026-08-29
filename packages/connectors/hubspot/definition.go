// Package hubspot defines the official hosted HubSpot MCP connector.
package hubspot

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "hubspot",
	Name:                "HubSpot",
	Description:         "HubSpot CRM, marketing and customer platform",
	Categories:          []string{"crm", "marketing"},
	HomepageURL:         "https://www.hubspot.com",
	IconURL:             "https://cdn.simpleicons.org/hubspot",
	ConfigSchemaVersion: 1,
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Client ID of the HubSpot MCP auth app.",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Client secret of the HubSpot MCP auth app.",
		},
	},
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "HubSpot OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://mcp.hubspot.com/oauth/authorize/user",
			TokenEndpoint:         "https://mcp.hubspot.com/oauth/v3/token",
			UsePKCE:               true,
			TokenEndpointAuth:     connector.TokenAuthPost,
		},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.hubspot.com/",
		RequestTimeout: 30 * time.Second,
	},
}
