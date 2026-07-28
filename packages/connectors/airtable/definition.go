// Package airtable defines the official hosted Airtable MCP connector.
package airtable

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "airtable",
	Name:                "Airtable",
	Description:         "Airtable 数据库与协作平台",
	Categories:          []string{"database", "productivity"},
	HomepageURL:         "https://www.airtable.com",
	IconURL:             "https://cdn.simpleicons.org/airtable",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "personal_access_token",
		Type:  connector.AuthAPIKey,
		Label: "Personal Access Token",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "Personal Access Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "仅授权所需 base 和 scopes 的 Airtable Personal Access Token。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.airtable.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
