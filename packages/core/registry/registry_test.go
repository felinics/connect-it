package registry_test

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func handler(context.Context, connector.ManagedCall) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

func definition(implementation connector.Implementation) connector.Definition {
	return connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 1,
		Implementation:      implementation,
	}
}

func TestRegisterImplementations(t *testing.T) {
	for _, implementation := range []connector.Implementation{
		connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
		connector.Managed{Tools: []connector.ManagedTool{{
			Tool: mcp.Tool{
				Name:        "list-items.v1",
				InputSchema: map[string]any{"type": "object"},
			},
			Handler: handler,
		}}},
	} {
		if err := registry.New().Register(definition(implementation)); err != nil {
			t.Fatalf("%s should register: %v", implementation.Mode(), err)
		}
	}
}

func TestRegisterRejectsInvalidImplementation(t *testing.T) {
	cases := []struct {
		name string
		impl connector.Implementation
		want string
	}{
		{"nil", nil, "Implementation"},
		{"insecure remote", connector.RemoteMCP{
			Endpoint: "http://mcp.example.com",
		}, "https"},
		{"missing endpoint", connector.RemoteMCP{}, "https"},
		{"managed without tools", connector.Managed{}, "at least"},
		{"managed without handler", connector.Managed{Tools: []connector.ManagedTool{{
			Tool: mcp.Tool{Name: "tool", InputSchema: map[string]any{"type": "object"}},
		}}}, "Handler"},
		{"invalid tool name", connector.Managed{Tools: []connector.ManagedTool{{
			Tool:    mcp.Tool{Name: "bad/tool", InputSchema: map[string]any{"type": "object"}},
			Handler: handler,
		}}}, "tool name"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := registry.New().Register(definition(test.impl))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRegistryRejectsDuplicateTypeAndSorts(t *testing.T) {
	impl := connector.RemoteMCP{Endpoint: "https://mcp.example.com"}
	r := registry.New()
	b := definition(impl)
	b.Type = "bbb"
	a := definition(impl)
	a.Type = "aaa"
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(a); err == nil {
		t.Fatal("duplicate type should fail")
	}
	all := r.All()
	if len(all) != 2 || all[0].Type != "aaa" || all[1].Type != "bbb" {
		t.Fatalf("unexpected order: %+v", all)
	}
}

func TestRegisterValidatesCredentialFields(t *testing.T) {
	tests := []struct {
		name   string
		fields []connector.ConfigField
		want   string
	}{
		{name: "missing", want: "CredentialFields"},
		{
			name: "first optional",
			fields: []connector.ConfigField{{
				Key: "token", InputType: connector.InputText,
			}},
			want: "Required",
		},
		{
			name: "duplicate",
			fields: []connector.ConfigField{
				{Key: "token", InputType: connector.InputText, Required: true},
				{Key: "token", InputType: connector.InputText},
			},
			want: "duplicate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			def := definition(connector.RemoteMCP{Endpoint: "https://mcp.example.com"})
			def.AuthMethods = []connector.AuthMethod{{
				Key: "token", Type: connector.AuthAPIKey,
				CredentialFields: test.fields,
			}}
			err := registry.New().Register(def)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want substring %q", err, test.want)
			}
		})
	}
}
