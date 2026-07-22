package status_test

import (
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/status"
)

var now = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

func strPtr(s string) *string { return &s }

// makeDef 返回一个有 Tool、有必填字段（含 secret）、含 self_hosted MCP 的 definition。
func makeDef() connector.Definition {
	return connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 2,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Required: true, InputType: connector.InputText},
			{Key: "client_secret", Required: true, Secret: true, InputType: connector.InputText},
			{Key: "tenant", Required: true, InputType: connector.InputText, DefaultValue: strPtr("common")},
			{Key: "mcp_url", InputType: connector.InputURL},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{
			{Key: "self",
				Endpoint:   connector.Endpoint{Source: connector.EndpointConfigField, ConfigFieldKey: "mcp_url"},
				Provenance: connector.Provenance{Kind: connector.ProvenanceSelfHosted}},
		},
		Tools: []connector.Tool{
			{ID: "t", Backend: connector.ManagedBackend{HandlerKey: "t"}},
		},
	}
}

// fullConfig 返回满足 makeDef 全部要求的配置状态。
func fullConfig() status.ConfigState {
	return status.ConfigState{
		Exists:              true,
		SchemaVersion:       2,
		PublicValues:        map[string]any{"client_id": "abc", "mcp_url": "https://mcp.internal/mcp"},
		SecretKeysSet:       map[string]bool{"client_secret": true},
		MCPVerified:         true,
		MCPVerifiedEndpoint: "https://mcp.internal/mcp",
	}
}

func TestCompute(t *testing.T) {
	cases := []struct {
		name string
		def  func() *connector.Definition
		cfg  func() status.ConfigState
		h    status.Health
		want status.Status
	}{
		{"definition_missing", func() *connector.Definition { return nil },
			fullConfig, status.Health{}, status.DefinitionMissing},
		{"deprecated 优先于其他", func() *connector.Definition {
			d := makeDef()
			d.Deprecated = true
			return &d
		}, fullConfig, status.Health{}, status.Deprecated},
		{"catalog_only", func() *connector.Definition {
			d := makeDef()
			d.Tools = nil
			return &d
		}, fullConfig, status.Health{}, status.CatalogOnly},
		{"config 比代码新", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.SchemaVersion = 3
			return c
		}, status.Health{}, status.ConfigIncompatible},
		{"缺必填公开字段", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			delete(c.PublicValues, "client_id")
			return c
		}, status.Health{}, status.NeedsConfig},
		{"缺必填 secret 字段", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.SecretKeysSet = nil
			return c
		}, status.Health{}, status.NeedsConfig},
		{"必填但有默认值不算缺", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig, status.Health{}, status.Ready},
		{"self_hosted endpoint 未验证", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.MCPVerified = false
			return c
		}, status.Health{}, status.NeedsConfig},
		{"验证后 endpoint 又被改动", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.PublicValues["mcp_url"] = "https://other.internal/mcp"
			return c
		}, status.Health{}, status.NeedsConfig},
		{"连续失败进入 degraded", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig,
			status.Health{ConsecutiveFailures: 3, LastErrorAt: now.Add(-5 * time.Minute)},
			status.Degraded},
		{"失败已过期回到 ready", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig,
			status.Health{ConsecutiveFailures: 5, LastErrorAt: now.Add(-16 * time.Minute)},
			status.Ready},
		{"ready", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig, status.Health{}, status.Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := status.Compute(tc.def(), tc.cfg(), tc.h, now)
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
