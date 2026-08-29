// Package posthog defines the official hosted PostHog MCP connector.
package posthog

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "posthog",
	Name:                "PostHog",
	Description:         "PostHog product analytics, feature flag and experimentation platform",
	Categories:          []string{"analytics"},
	HomepageURL:         "https://posthog.com",
	IconURL:             "https://cdn.simpleicons.org/posthog/_/e5e5e5",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "personal_api_key",
		Type:  connector.AuthAPIKey,
		Label: "Personal API Key",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "Personal API Key",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Personal API key created with PostHog's MCP server preset.",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.posthog.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
