// Package box defines the official hosted Box MCP connector.
package box

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "box",
	Name:                "Box",
	Description:         "Box cloud content management and collaboration platform",
	Categories:          []string{"storage", "productivity"},
	HomepageURL:         "https://www.box.com",
	IconURL:             "https://cdn.simpleicons.org/box/_/e5e5e5",
	ConfigSchemaVersion: 1,
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Client ID of the Box platform app.",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Client secret of the Box platform app.",
		},
	},
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "Box OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://account.box.com/api/oauth2/authorize",
			TokenEndpoint:         "https://api.box.com/oauth2/token",
			TokenEndpointAuth:     connector.TokenAuthPost,
		},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.box.com",
		RequestTimeout: 30 * time.Second,
	},
}
