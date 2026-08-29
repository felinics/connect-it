// Package github holds the code-fixed Definition of the GitHub connector.
// GitHub supports OAuth only; credentials come from an authorization flow
// started by the downstream service.
package github

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "github",
	Name:                "GitHub",
	Description:         "GitHub code hosting and collaboration platform",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://github.com",
	IconURL:             "https://cdn.simpleicons.org/github/_/e5e5e5",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Client ID of the GitHub OAuth app.",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Client secret of the GitHub OAuth app.",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "GitHub OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
				TokenEndpoint:         "https://github.com/login/oauth/access_token",
				Scopes:                []string{"repo", "read:user"},
				UsePKCE:               false,
				TokenEndpointAuth:     connector.TokenAuthPost,
			},
		},
	},

	Implementation: connector.RemoteMCP{
		Endpoint:       "https://api.githubcopilot.com/mcp/",
		RequestTimeout: 30 * time.Second,
	},
}
