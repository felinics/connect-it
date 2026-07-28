// Package supabase defines the official hosted Supabase MCP connector.
package supabase

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "supabase",
	Name:                "Supabase",
	Description:         "Supabase 数据库与后端开发平台",
	Categories:          []string{"developer_tools", "database"},
	HomepageURL:         "https://supabase.com",
	IconURL:             "https://cdn.simpleicons.org/supabase",
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
			Description: "Supabase Personal Access Token；仅建议连接开发或测试项目。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.supabase.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
