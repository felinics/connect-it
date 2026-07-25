package mcpclient

import (
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

func TestPreflightDefinitionsValidatesFixedRemoteMCPPolicies(t *testing.T) {
	client, err := New(providerkit.NewFactory())
	if err != nil {
		t.Fatal(err)
	}

	valid := connector.Definition{
		Type: "github",
		RemoteMCPServers: []connector.RemoteMCPServer{
			fixedServer(
				"https://api.example.test/mcp",
				"api.example.test",
			),
			selfHostedServer(time.Second),
		},
	}
	if err := client.PreflightDefinitions(
		[]connector.Definition{valid},
	); err != nil {
		t.Fatalf("valid policies failed preflight: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*connector.RemoteMCPServer)
	}{
		{
			name: "private literal",
			mutate: func(server *connector.RemoteMCPServer) {
				server.Endpoint.URL = "https://127.0.0.1/mcp"
				server.Provenance.AllowedHostnames = []string{"127.0.0.1"}
			},
		},
		{
			name: "hostname not reviewed",
			mutate: func(server *connector.RemoteMCPServer) {
				server.Provenance.AllowedHostnames =
					[]string{"other.example.test"}
			},
		},
		{
			name: "fixed provenance is self hosted",
			mutate: func(server *connector.RemoteMCPServer) {
				server.Provenance.Kind = connector.ProvenanceSelfHosted
			},
		},
		{
			name: "negative timeout",
			mutate: func(server *connector.RemoteMCPServer) {
				server.RequestTimeout = -time.Second
			},
		},
		{
			name: "query in policy base URL",
			mutate: func(server *connector.RemoteMCPServer) {
				server.Endpoint.URL =
					"https://api.example.test/mcp?secret=unexpected"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := valid
			definition.RemoteMCPServers = append(
				[]connector.RemoteMCPServer(nil),
				valid.RemoteMCPServers[:1]...,
			)
			test.mutate(&definition.RemoteMCPServers[0])

			err := client.PreflightDefinitions(
				[]connector.Definition{definition},
			)
			if err == nil {
				t.Fatal("unsafe fixed policy passed preflight")
			}
			if !strings.Contains(err.Error(), `connector "github"`) ||
				!strings.Contains(err.Error(), `server "official"`) {
				t.Fatalf("preflight error lacks safe identity: %v", err)
			}
		})
	}
}
