// Package linear defines the official hosted Linear MCP connector.
package linear

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "linear",
	Name:                "Linear",
	Description:         "Linear product development, issue and project management platform",
	Categories:          []string{"developer_tools", "project_management"},
	HomepageURL:         "https://linear.app",
	IconURL:             "https://cdn.simpleicons.org/linear",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Linear OAuth",
			OAuth: &connector.OAuthConfig{Mode: connector.OAuthModeMCP},
		},
		{
			Key:   "api_key",
			Type:  connector.AuthAPIKey,
			Label: "Linear API Key",
			CredentialFields: []connector.ConfigField{{
				Key:         "token",
				Label:       "API Key",
				InputType:   connector.InputText,
				Required:    true,
				Secret:      true,
				Description: "Least-privilege API key generated under Linear Security & Access.",
			}},
		},
	},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.linear.app/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
