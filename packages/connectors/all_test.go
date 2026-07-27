package connectors_test

import (
	"encoding/json"
	"testing"

	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func definitions(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	connectors.RegisterAll(r)
	return r
}

func TestRegisterAllAndModes(t *testing.T) {
	r := definitions(t)
	want := map[connector.Type]connector.Mode{
		"github":     connector.ModeRemoteMCP,
		"gmail":      connector.ModeRemoteMCP,
		"google_ads": connector.ModeManaged,
		"one_drive":  connector.ModeManaged,
		"youtube":    connector.ModeManaged,
	}
	if len(r.All()) != len(want) {
		t.Fatalf("got %d definitions", len(r.All()))
	}
	for connectorType, mode := range want {
		def, ok := r.Get(connectorType)
		if !ok || def.Mode() != mode {
			t.Fatalf("%s: found=%v mode=%q", connectorType, ok, def.Mode())
		}
	}
}

func TestRemoteEndpoints(t *testing.T) {
	r := definitions(t)
	cases := map[connector.Type]string{
		"github": "https://api.githubcopilot.com/mcp/",
		"gmail":  "https://gmailmcp.googleapis.com/mcp/v1",
	}
	for connectorType, endpoint := range cases {
		def, _ := r.Get(connectorType)
		remote := def.Implementation.(connector.RemoteMCP)
		if remote.Endpoint != endpoint {
			t.Fatalf("%s endpoint=%q", connectorType, remote.Endpoint)
		}
	}
}

func TestManagedTools(t *testing.T) {
	r := definitions(t)
	want := map[connector.Type]int{
		"one_drive":  2,
		"google_ads": 2,
		"youtube":    4,
	}
	for connectorType, toolCount := range want {
		def, _ := r.Get(connectorType)
		managed := def.Implementation.(connector.Managed)
		if len(managed.Tools) != toolCount {
			t.Fatalf("%s: got %d tools", connectorType, len(managed.Tools))
		}
		for _, managedTool := range managed.Tools {
			if managedTool.Handler == nil {
				t.Fatalf("%s: %s handler is nil", connectorType, managedTool.Tool.Name)
			}
			data, err := json.Marshal(managedTool.Tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(data, &schema); err != nil || schema["type"] != "object" {
				t.Fatalf("%s: %s schema=%s err=%v",
					connectorType, managedTool.Tool.Name, data, err)
			}
		}
	}
}
