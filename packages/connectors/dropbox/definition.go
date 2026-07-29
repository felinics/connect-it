// Package dropbox defines the official hosted Dropbox MCP connector.
package dropbox

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "dropbox",
	Name:                "Dropbox",
	Description:         "Dropbox cloud storage and file collaboration platform",
	Categories:          []string{"storage", "productivity"},
	HomepageURL:         "https://www.dropbox.com",
	IconURL:             "https://cdn.simpleicons.org/dropbox",
	ConfigSchemaVersion: 1,
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth App Key",
			InputType:   connector.InputText,
			Required:    true,
			Description: "App key from the Dropbox app console.",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth App Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "App secret from the Dropbox app console.",
		},
	},
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "Dropbox OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://www.dropbox.com/oauth2/authorize",
			TokenEndpoint:         "https://api.dropboxapi.com/oauth2/token",
			Scopes: []string{
				"files.metadata.read",
				"files.content.read",
				"files.content.write",
				"sharing.write",
				"account_info.read",
				"sharing.read",
				"file_requests.read",
				"file_requests.write",
			},
			UsePKCE:           true,
			TokenEndpointAuth: connector.TokenAuthPost,
			ExtraAuthParams: map[string]string{
				"token_access_type": "offline",
			},
		},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.dropbox.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
