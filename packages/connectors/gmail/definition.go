// Package gmail 是 Gmail Connector 的固定 Definition。
package gmail

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "gmail",
	Name:                "Gmail",
	Description:         "Google Gmail 邮件服务",
	Categories:          []string{"communication"},
	HomepageURL:         "https://mail.google.com",
	IconURL:             "https://cdn.simpleicons.org/gmail",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client ID。",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client Secret。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Google OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
				TokenEndpoint:         "https://oauth2.googleapis.com/token",
				Scopes: []string{
					"https://www.googleapis.com/auth/gmail.readonly",
					"https://www.googleapis.com/auth/gmail.compose",
				},
				UsePKCE:           false,
				TokenEndpointAuth: connector.TokenAuthPost,
				ExtraAuthParams: map[string]string{
					"access_type": "offline",
					"prompt":      "consent",
				},
			},
		},
	},

	Implementation: connector.RemoteMCP{
		Endpoint:       "https://gmailmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
