// Package posthog defines the official hosted PostHog MCP connector.
package posthog

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "posthog",
	Name:                "PostHog",
	Description:         "PostHog 产品分析、功能开关与实验平台",
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
			Description: "使用 PostHog 的 MCP Server preset 创建 Personal API Key。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.posthog.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
