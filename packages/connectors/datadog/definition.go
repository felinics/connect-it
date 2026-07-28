// Package datadog defines the official hosted Datadog MCP connector.
package datadog

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var defaultSite = "us1"

var Definition = connector.Definition{
	Type:                "datadog",
	Name:                "Datadog",
	Description:         "Datadog 可观测性与安全平台",
	Categories:          []string{"monitoring", "developer_tools"},
	HomepageURL:         "https://www.datadoghq.com",
	IconURL:             "https://cdn.simpleicons.org/datadog/_/e5e5e5",
	ConfigSchemaVersion: 1,
	ConfigFields: []connector.ConfigField{{
		Key:          "site",
		Label:        "Datadog Site",
		InputType:    connector.InputSelect,
		Required:     true,
		DefaultValue: &defaultSite,
		Validation: connector.FieldValidation{Options: []string{
			"us1", "us3", "us5", "eu1", "ap1", "ap2", "uk1",
		}},
	}},
	AuthMethods: []connector.AuthMethod{{
		Key:   "access_token",
		Type:  connector.AuthAPIKey,
		Label: "Personal or Service Access Token",
		CredentialFields: []connector.ConfigField{{
			Key:         "token",
			Label:       "PAT or SAT",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "具备所需 Datadog scopes 的 Personal Access Token 或 Service Access Token。",
		}},
	}},
	Implementation: connector.RemoteMCP{
		EndpointSelector: &connector.RemoteMCPEndpointSelector{
			ConfigField: "site",
			Endpoints: map[string]string{
				"us1": "https://mcp.datadoghq.com/v1/mcp",
				"us3": "https://mcp.us3.datadoghq.com/v1/mcp",
				"us5": "https://mcp.us5.datadoghq.com/v1/mcp",
				"eu1": "https://mcp.datadoghq.eu/v1/mcp",
				"ap1": "https://mcp.ap1.datadoghq.com/v1/mcp",
				"ap2": "https://mcp.ap2.datadoghq.com/v1/mcp",
				"uk1": "https://mcp.uk1.datadoghq.com/v1/mcp",
			},
		},
		RequestTimeout: 30 * time.Second,
	},
}
