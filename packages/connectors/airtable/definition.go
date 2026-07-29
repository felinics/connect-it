// Package airtable defines the official hosted Airtable MCP connector.
package airtable

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "airtable",
	Name:                "Airtable",
	Description:         "Airtable database and collaboration platform",
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
			Description: "Airtable personal access token, scoped to only the bases and scopes you need.",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.airtable.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
