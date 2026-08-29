// Package slack defines the official hosted Slack MCP connector.
package slack

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "slack",
	Name:                "Slack",
	Description:         "Slack messaging, file and collaboration platform",
	Categories:          []string{"communication", "productivity"},
	HomepageURL:         "https://slack.com",
	IconURL:             "https://api.iconify.design/streamline-color:slack.svg",
	ConfigSchemaVersion: 1,
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Client ID of an MCP-enabled Slack internal app or directory-published app.",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Slack app Client Secret。",
		},
	},
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "Slack OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://slack.com/oauth/v2_user/authorize",
			TokenEndpoint:         "https://slack.com/api/oauth.v2.user.access",
			Scopes: []string{
				"search:read.public",
				"search:read.private",
				"search:read.mpim",
				"search:read.im",
				"search:read.files",
				"search:read.users",
				"chat:write",
				"channels:history",
				"groups:history",
				"mpim:history",
				"im:history",
				"canvases:read",
				"canvases:write",
				"users:read",
				"users:read.email",
				"reactions:write",
				"reactions:read",
				"emoji:read",
				"files:read",
				"channels:write",
				"groups:write",
				"im:write",
				"mpim:write",
				"channels:read",
				"groups:read",
				"mpim:read",
			},
			ScopeSeparator:    ",",
			UsePKCE:           true,
			TokenEndpointAuth: connector.TokenAuthPost,
		},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.slack.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
