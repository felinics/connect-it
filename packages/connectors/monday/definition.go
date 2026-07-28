// Package monday defines the official hosted monday.com MCP connector.
package monday

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "monday",
	Name:                "monday.com",
	Description:         "monday.com 项目、工作流与协作平台",
	Categories:          []string{"productivity", "project_management"},
	HomepageURL:         "https://monday.com",
	IconURL:             "https://cdn.monday.com/images/logos/monday_logo_icon.png",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "personal_api_token",
		Type:  connector.AuthAPIKey,
		Label: "Personal API Token",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "Personal API Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "monday.com Developer Center 中生成的 Personal API Token。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.monday.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
