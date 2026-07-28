// Package sentry defines the official hosted Sentry MCP connector.
package sentry

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "sentry",
	Name:                "Sentry",
	Description:         "Sentry 错误追踪与应用性能平台",
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
			Description: "仅授予所需 Sentry organization、project、team 与 event scopes。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:            "https://mcp.sentry.dev/mcp",
		AuthorizationScheme: "Sentry-Bearer",
		RequestTimeout:      30 * time.Second,
	},
}
