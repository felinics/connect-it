// Package stripe defines the official hosted Stripe MCP connector.
package stripe

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "stripe",
	Name:                "Stripe",
	Description:         "Stripe 支付与账单平台",
	Categories:          []string{"payments"},
	HomepageURL:         "https://stripe.com",
	IconURL:             "https://cdn.simpleicons.org/stripe",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "restricted_api_key",
		Type:  connector.AuthAPIKey,
		Label: "Restricted API Key",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "Restricted API Key",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "建议使用仅开放所需资源的 Stripe Restricted API Key。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.stripe.com",
		RequestTimeout: 30 * time.Second,
	},
}
