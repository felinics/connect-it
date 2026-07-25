package gmail

import (
	"reflect"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

// The published Tool set, each Tool's reviewed risk, and the reviewed server
// identity are pinned in CI, not in the opt-in smoke harness.
func TestDefinitionPublishesExactlyTheReviewedTools(t *testing.T) {
	type published struct {
		id   string
		risk connector.ToolRisk
	}
	got := make([]published, 0, len(Definition.Tools))
	for _, tool := range Definition.Tools {
		got = append(got, published{id: tool.ID, risk: tool.Risk})
	}
	if want := []published{
		{"search_threads", connector.RiskRead},
		{"create_draft", connector.RiskWrite},
		{"list_messages", connector.RiskRead},
		{"send_message", connector.RiskDestructive},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tools = %#v, want %#v", got, want)
	}
}

func TestRemoteMCPServerIdentity(t *testing.T) {
	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatalf("Remote MCP servers = %d, want 1", len(Definition.RemoteMCPServers))
	}
	server := Definition.RemoteMCPServers[0]
	if server.Key != "official" ||
		server.Endpoint.Source != connector.EndpointFixed ||
		server.Endpoint.URL != "https://gmailmcp.googleapis.com/mcp/v1" ||
		server.Provenance.Kind != connector.ProvenanceOfficial ||
		!reflect.DeepEqual(
			server.Provenance.AllowedHostnames,
			[]string{"gmailmcp.googleapis.com"},
		) ||
		server.AuthBinding.Scheme != "bearer" ||
		server.RequestTimeout != 30*time.Second {
		t.Fatalf("Gmail MCP server = %+v", server)
	}
}

// Gmail mixes Remote MCP and Managed Tools: the Remote ones must resolve to
// the official server with a verbatim upstream tool name and no mapper key.
func TestRemoteMCPToolMappings(t *testing.T) {
	want := map[string]string{
		"search_threads": "search_threads",
		"create_draft":   "create_draft",
	}
	got := make(map[string]string, len(want))
	for _, tool := range Definition.Tools {
		backend, ok := tool.Backend.(connector.RemoteMCPBackend)
		if !ok {
			continue
		}
		if backend.ServerKey != "official" ||
			backend.InputMapperKey != "" ||
			backend.OutputMapperKey != "" {
			t.Fatalf("Tool %q backend = %+v", tool.ID, backend)
		}
		got[tool.ID] = backend.RemoteToolName
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Remote MCP Tool mapping = %#v, want %#v", got, want)
	}
}
