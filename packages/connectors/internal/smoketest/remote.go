package smoketest

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// Connect opens one Remote MCP session over an already providerkit-guarded HTTP
// client and registers its close. Retries are disabled so a lost response is
// visible to the harness instead of being silently replayed against a real
// account.
func Connect(
	t *testing.T,
	ctx context.Context,
	name string,
	endpoint string,
	httpClient *http.Client,
) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(
		&mcp.Implementation{Name: name, Version: "0.1.0"},
		nil,
	)
	session, err := client.Connect(
		ctx,
		&mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           httpClient,
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("connect to %s MCP server", name)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close %s MCP smoke session", name)
		}
	})
	return session
}

// RunRemote calls one Tool through its reviewed Remote MCP mapping, after
// validating the arguments against the Tool's compiled input schema. The
// Definition's own mapping is the only remote name ever sent, so a smoke run
// cannot reach a Tool the review did not cover.
//
// The MCP payload is never logged, in either direction.
func RunRemote(
	t *testing.T,
	ctx context.Context,
	definitionRegistry *registry.Registry,
	definition connector.Definition,
	session *mcp.ClientSession,
	toolID string,
	arguments map[string]any,
) {
	t.Helper()
	schemas, exists := definitionRegistry.ToolSchemas(definition.Type, toolID)
	if !exists {
		t.Fatalf("%s has no registered schemas", toolID)
	}
	if err := schemas.Input.Validate(arguments); err != nil {
		t.Fatalf("%s smoke input violates its compiled schema", toolID)
	}
	remoteToolName := ""
	for _, tool := range definition.Tools {
		if tool.ID != toolID {
			continue
		}
		backend, ok := tool.Backend.(connector.RemoteMCPBackend)
		if !ok {
			t.Fatalf("%s does not have a Remote MCP backend", toolID)
		}
		remoteToolName = backend.RemoteToolName
		break
	}
	if remoteToolName == "" {
		t.Fatalf("%s has no reviewed remote Tool mapping", toolID)
	}
	response, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      remoteToolName,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("%s returned an MCP protocol or transport error", toolID)
	}
	if response == nil || response.IsError ||
		(len(response.Content) == 0 && response.StructuredContent == nil) {
		t.Fatalf("%s returned an unsuccessful or empty MCP result", toolID)
	}
}
