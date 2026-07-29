// Package intercom defines the official hosted Intercom MCP connector.
package intercom

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "intercom",
	Name:                "Intercom",
	Description:         "Intercom customer messaging and support platform",
	Categories:          []string{"customer_support"},
	HomepageURL:         "https://www.intercom.com",
	IconURL:             "https://cdn.simpleicons.org/intercom",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "access_token",
		Type:  connector.AuthAPIKey,
		Label: "Access Token",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "Access Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Intercom access token with the permissions MCP requires. The official MCP server currently supports US workspaces only.",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.intercom.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
