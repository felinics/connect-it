// Package asana defines the official hosted Asana MCP connector.
package asana

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "asana",
	Name:                "Asana",
	Description:         "Asana 项目、任务与协作平台",
	Categories:          []string{"productivity", "project_management"},
	HomepageURL:         "https://asana.com",
	IconURL:             "https://cdn.simpleicons.org/asana",
	ConfigSchemaVersion: 1,
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Asana Developer Console 中 MCP App 的 Client ID。",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Asana Developer Console 中 MCP App 的 Client Secret。",
		},
	},
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "Asana OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://app.asana.com/-/oauth_authorize",
			TokenEndpoint:         "https://app.asana.com/-/oauth_token",
			UsePKCE:               true,
			TokenEndpointAuth:     connector.TokenAuthPost,
			ExtraAuthParams: map[string]string{
				"resource": "https://mcp.asana.com/v2",
			},
		},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.asana.com/v2/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
