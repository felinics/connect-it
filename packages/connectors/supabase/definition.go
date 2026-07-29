// Package supabase defines the official hosted Supabase MCP connector.
package supabase

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "supabase",
	Name:                "Supabase",
	Description:         "Supabase database and backend development platform",
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
			Description: "Supabase personal access token. Connect development or test projects only.",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.supabase.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
