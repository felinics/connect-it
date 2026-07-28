// Package cloudflare defines the official hosted Cloudflare MCP connector.
package cloudflare

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "cloudflare",
	Name:                "Cloudflare",
	Description:         "Cloudflare 网络、安全与开发平台",
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
			Description: "仅授予所需 Cloudflare 资源和权限的 API Token。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.cloudflare.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
