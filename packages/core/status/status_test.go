package status_test

import (
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/status"
)

func strPtr(s string) *string { return &s }

// makeDef 返回一个有必填字段（含 secret）的 definition。
func makeDef() connector.Definition {
	return connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 2,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Required: true, InputType: connector.InputText},
			{Key: "client_secret", Required: true, Secret: true, InputType: connector.InputText},
			{Key: "tenant", Required: true, InputType: connector.InputText, DefaultValue: strPtr("common")},
		},
		Implementation: connector.RemoteMCP{},
	}
}

// fullConfig 返回满足 makeDef 全部要求的配置状态。
func fullConfig() status.ConfigState {
	return status.ConfigState{
		Exists:        true,
		SchemaVersion: 2,
		PublicValues:  map[string]any{"client_id": "abc"},
		SecretKeysSet: map[string]bool{"client_secret": true},
	}
}

func TestCompute(t *testing.T) {
	cases := []struct {
		name string
		def  func() *connector.Definition
		cfg  func() status.ConfigState
		want status.Status
	}{
		{"definition_missing", func() *connector.Definition { return nil },
			fullConfig, status.DefinitionMissing},
		{"deprecated 优先于其他", func() *connector.Definition {
			d := makeDef()
			d.Deprecated = true
			return &d
		}, fullConfig, status.Deprecated},
		{"config 比代码新", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.SchemaVersion = 3
			return c
		}, status.ConfigIncompatible},
		{"缺必填公开字段", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			delete(c.PublicValues, "client_id")
			return c
		}, status.NeedsConfig},
		{"缺必填 secret 字段", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.SecretKeysSet = nil
			return c
		}, status.NeedsConfig},
		{"必填但有默认值不算缺", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig, status.Ready},
		{"ready", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig, status.Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := status.Compute(tc.def(), tc.cfg())
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
