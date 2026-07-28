// Package postman defines the official hosted Postman MCP connector.
package postman

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "postman",
	Name:                "Postman",
	Description:         "Postman API 开发与协作平台",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://www.postman.com",
	IconURL:             "https://cdn.simpleicons.org/postman",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "api_key",
		Type:  connector.AuthAPIKey,
		Label: "Postman API Key",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "API Key",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Postman 账户生成的 API Key。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.postman.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
