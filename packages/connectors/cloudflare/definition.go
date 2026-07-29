// Package cloudflare defines the official hosted Cloudflare MCP connector.
package cloudflare

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "cloudflare",
	Name:                "Cloudflare",
	Description:         "Cloudflare network, security and developer platform",
	Categories:          []string{"developer_tools", "infrastructure"},
	HomepageURL:         "https://www.cloudflare.com",
	IconURL:             "https://cdn.simpleicons.org/cloudflare",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "api_token",
		Type:  connector.AuthAPIKey,
		Label: "API Token",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "API Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Cloudflare API token, granted only the resources and permissions you need.",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.cloudflare.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
