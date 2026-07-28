// Package github 是 GitHub Connector 的固定 Definition。
// GitHub 只支持 OAuth，凭证由下游发起授权获得。
package github

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "github",
	Name:                "GitHub",
	Description:         "GitHub 代码托管与协作平台",
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
			Description: "GitHub OAuth App 的 Client ID。",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "GitHub OAuth App 的 Client Secret。",
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
