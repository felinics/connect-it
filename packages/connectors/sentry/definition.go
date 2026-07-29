// Package sentry defines the official hosted Sentry MCP connector.
package sentry

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "sentry",
	Name:                "Sentry",
	Description:         "Sentry error tracking and application performance platform",
	Categories:          []string{"monitoring", "developer_tools"},
	HomepageURL:         "https://sentry.io",
	IconURL:             "https://cdn.simpleicons.org/sentry/_/e5e5e5",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "user_auth_token",
		Type:  connector.AuthAPIKey,
		Label: "User Auth Token",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "User Auth Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Grant only the Sentry organization, project, team and event scopes you need.",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:            "https://mcp.sentry.dev/mcp",
		AuthorizationScheme: "Sentry-Bearer",
		RequestTimeout:      30 * time.Second,
	},
}
