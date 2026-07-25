package notion

import (
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestNotionDefinitionAuthentication(t *testing.T) {
	t.Parallel()

	if Definition.Type != "notion" ||
		Definition.Name != "Notion" ||
		Definition.ConfigSchemaVersion != 1 {
		t.Fatalf("Notion definition identity = %+v", Definition)
	}

	if len(Definition.AuthMethods) != 1 {
		t.Fatalf("auth methods = %d", len(Definition.AuthMethods))
	}
	auth := Definition.AuthMethods[0]
	// 远程 server 只认 OAuth 签发的 access token，长期 internal integration
	// token 只在已废弃的本地版上可用，因此这里不允许再出现 api_key 方式。
	if auth.Key != notionAuthMethod ||
		auth.Type != connector.AuthOAuth2 ||
		auth.OAuth == nil ||
		len(auth.CredentialFields) != 0 {
		t.Fatalf("auth method = %+v", auth)
	}
	if auth.OAuth.AuthorizationEndpoint != "https://mcp.notion.com/authorize" ||
		auth.OAuth.TokenEndpoint != "https://mcp.notion.com/token" ||
		!auth.OAuth.UsePKCE {
		t.Fatalf("OAuth config = %+v", *auth.OAuth)
	}

	fields := map[string]connector.ConfigField{}
	for _, field := range Definition.ConfigFields {
		fields[field.Key] = field
	}
	if len(fields) != 2 ||
		!fields["client_id"].Required ||
		fields["client_id"].Secret ||
		!fields["client_secret"].Required ||
		!fields["client_secret"].Secret {
		t.Fatalf("config fields = %+v", Definition.ConfigFields)
	}
}

func TestNotionRemoteMCPServerIsTheOfficialHostedEndpoint(t *testing.T) {
	t.Parallel()

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatalf("MCP servers = %d", len(Definition.RemoteMCPServers))
	}
	server := Definition.RemoteMCPServers[0]
	if server.Key != "official" ||
		server.Endpoint.Source != connector.EndpointFixed ||
		server.Endpoint.URL != "https://mcp.notion.com/mcp" ||
		server.Provenance.Kind != connector.ProvenanceOfficial ||
		len(server.Provenance.AllowedHostnames) != 1 ||
		server.Provenance.AllowedHostnames[0] != "mcp.notion.com" ||
		server.AuthBinding.Scheme != "bearer" ||
		len(server.AuthBinding.CredentialFieldByAuthMethod) != 0 {
		t.Fatalf("MCP server = %+v", server)
	}
}

// 三代 Notion MCP 的 tool namespace 互不兼容：当前托管远程版一律是 notion- 前缀
// 的连字符名，已废弃本地版是 API-xxx（v1）或无前缀连字符名（v2）。这条断言是防止
// 后人照着开源仓库改错的唯一护栏。
func TestNotionToolsUseHostedRemoteNamespace(t *testing.T) {
	t.Parallel()

	expected := map[string]struct {
		remote string
		risk   connector.ToolRisk
	}{
		"search":             {"notion-search", connector.RiskRead},
		"fetch":              {"notion-fetch", connector.RiskRead},
		"query_data_sources": {"notion-query-data-sources", connector.RiskRead},
		"get_users":          {"notion-get-users", connector.RiskRead},
		"get_teams":          {"notion-get-teams", connector.RiskRead},
		"get_comments":       {"notion-get-comments", connector.RiskRead},
		"create_pages":       {"notion-create-pages", connector.RiskWrite},
		"update_page":        {"notion-update-page", connector.RiskWrite},
		"create_database":    {"notion-create-database", connector.RiskWrite},
		"create_comment":     {"notion-create-comment", connector.RiskWrite},
	}
	if len(Definition.Tools) != len(expected) {
		t.Fatalf("tools = %d, want %d", len(Definition.Tools), len(expected))
	}
	for _, tool := range Definition.Tools {
		want, known := expected[tool.ID]
		if !known {
			t.Fatalf("unexpected tool %q", tool.ID)
		}
		backend, ok := tool.Backend.(connector.RemoteMCPBackend)
		if !ok ||
			backend.ServerKey != "official" ||
			backend.RemoteToolName != want.remote {
			t.Fatalf("tool %q backend = %#v", tool.ID, tool.Backend)
		}
		if !strings.HasPrefix(backend.RemoteToolName, "notion-") ||
			strings.Contains(backend.RemoteToolName, "_") {
			t.Fatalf(
				"tool %q remote name %q is not the hosted namespace",
				tool.ID,
				backend.RemoteToolName,
			)
		}
		if tool.Risk != want.risk {
			t.Fatalf("tool %q risk = %q", tool.ID, tool.Risk)
		}
		// 没有真实响应样本，声明 OutputSchema 会让 tool 永久返回
		// invalid_response；上游响应契约由 mcp:verify 现场核对。
		if len(tool.OutputSchema) != 0 {
			t.Fatalf("tool %q must not declare an OutputSchema", tool.ID)
		}
	}
}

// remote tool 的 schema 以上游 server 为准：参数直通，未知参数不得在本地被拒。
func TestNotionToolSchemasPassArgumentsThrough(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	if err := reg.Register(Definition); err != nil {
		t.Fatalf("register Notion definition: %v", err)
	}
	for _, tool := range Definition.Tools {
		schemas, exists := reg.ToolSchemas(Definition.Type, tool.ID)
		if !exists || schemas.Input == nil || schemas.Output != nil {
			t.Fatalf("tool %q schemas = %+v exists=%v", tool.ID, schemas, exists)
		}
		if err := schemas.Input.Validate(map[string]any{}); err != nil {
			t.Fatalf("tool %q empty input: %v", tool.ID, err)
		}
		if err := schemas.Input.Validate(map[string]any{
			"parent":                map[string]any{"page_id": "abc"},
			"undocumented_upstream": true,
		}); err != nil {
			t.Fatalf("tool %q rejected pass-through arguments: %v", tool.ID, err)
		}
	}
}
