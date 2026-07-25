package gitlab

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// GitLab 的 endpoint 由运营方按实例填写，凭据会被发到这个地址上，因此配置形状
// 本身就是安全契约：非 Secret 的 InputURL、必填、且刻意没有默认值。
func TestGitLabEndpointAndCredentialShape(t *testing.T) {
	t.Parallel()

	if Definition.Type != "gitlab" || Definition.Name != "GitLab" ||
		Definition.IconURL != "https://cdn.simpleicons.org/gitlab" ||
		!slices.Equal(Definition.Categories, []string{"developer_tools"}) {
		t.Fatalf("GitLab 元信息被改动: %+v", Definition)
	}

	fields := make(map[string]connector.ConfigField, len(Definition.ConfigFields))
	for _, field := range Definition.ConfigFields {
		fields[field.Key] = field
	}
	mcpURL, exists := fields[gitLabMCPURLField]
	if !exists || mcpURL.InputType != connector.InputURL ||
		!mcpURL.Required || mcpURL.Secret || mcpURL.DefaultValue != nil {
		t.Fatalf("mcp_url ConfigField 不正确: %+v", mcpURL)
	}

	if len(Definition.AuthMethods) != 1 {
		t.Fatalf("AuthMethods = %+v", Definition.AuthMethods)
	}
	method := Definition.AuthMethods[0]
	if method.Key != gitLabAccessTokenAuthMethod ||
		method.Type != connector.AuthAPIKey ||
		len(method.CredentialFields) != 1 {
		t.Fatalf("GitLab auth method = %+v", method)
	}
	token := method.CredentialFields[0]
	if token.Key != gitLabTokenField || token.InputType != connector.InputText ||
		!token.Required || !token.Secret {
		t.Fatalf("token field 不正确: %+v", token)
	}
	tokenPattern := regexp.MustCompile(token.Validation.Pattern)
	if !tokenPattern.MatchString("gloas-example") ||
		tokenPattern.MatchString("") ||
		tokenPattern.MatchString("has whitespace") ||
		tokenPattern.MatchString(strings.Repeat("x", 4097)) {
		t.Fatalf(
			"token Pattern 未限制为 1..4096 non-whitespace: %q",
			token.Validation.Pattern,
		)
	}

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatalf("RemoteMCPServers = %+v", Definition.RemoteMCPServers)
	}
	server := Definition.RemoteMCPServers[0]
	if server.Endpoint.Source != connector.EndpointConfigField ||
		server.Endpoint.ConfigFieldKey != gitLabMCPURLField ||
		server.Endpoint.URL != "" ||
		server.Provenance.Kind != connector.ProvenanceSelfHosted ||
		server.AuthBinding.Scheme != "bearer" ||
		server.AuthBinding.CredentialFieldByAuthMethod[gitLabAccessTokenAuthMethod] !=
			gitLabTokenField {
		t.Fatalf("remote MCP server 契约被改动: %+v", server)
	}
}

// 每个 Tool 都必须是 remote backend：参数直通（additionalProperties: true）、
// 不声明 OutputSchema（没有真实响应样本，猜错会让 tool 永久返回 invalid response）。
func TestGitLabToolsArePassThroughRemoteBackends(t *testing.T) {
	t.Parallel()

	if len(Definition.Tools) != 14 {
		t.Fatalf("Tool 数量 = %d, want 14", len(Definition.Tools))
	}
	for _, tool := range Definition.Tools {
		backend, remote := tool.Backend.(connector.RemoteMCPBackend)
		if !remote || backend.ServerKey != "instance" ||
			backend.RemoteToolName != tool.ID {
			t.Fatalf("Tool %q backend = %+v", tool.ID, tool.Backend)
		}
		if tool.OutputSchema != nil {
			t.Fatalf("Tool %q 不得声明 OutputSchema", tool.ID)
		}
		if !slices.Equal(tool.RequiredScopes, []string{"mcp"}) {
			t.Fatalf(
				"Tool %q RequiredScopes = %v, want [mcp]",
				tool.ID,
				tool.RequiredScopes,
			)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("Tool %q InputSchema 不是合法 JSON: %v", tool.ID, err)
		}
		if schema["additionalProperties"] != true {
			t.Fatalf("Tool %q InputSchema 必须允许未知参数直通", tool.ID)
		}
	}
}

// 上游 tool 名的大小写不一致是真实存在的，别在重构里"顺手纠正"。
func TestGitLabUpstreamToolNamesKeepInconsistentSpelling(t *testing.T) {
	t.Parallel()

	names := make(map[string]bool, len(Definition.Tools))
	for _, tool := range Definition.Tools {
		names[tool.Backend.(connector.RemoteMCPBackend).RemoteToolName] = true
	}
	for _, name := range []string{
		"create_workitem_note",
		"get_workitem_notes",
		"link_work_items",
	} {
		if !names[name] {
			t.Fatalf("上游 tool 名 %q 丢失: %v", name, names)
		}
	}
	for _, name := range []string{
		"create_work_item_note",
		"get_work_item_notes",
		"link_workitems",
		// 运维 / 破坏性 / EE 专属的 tool 刻意不暴露。
		"get_mcp_server_version",
		"manage_pipeline",
		"semantic_code_search",
		"attach_scan_profile",
	} {
		if names[name] {
			t.Fatalf("不该出现的 tool 名 %q", name)
		}
	}
}

func TestGitLabDefinitionRegisters(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	if err := reg.Register(Definition); err != nil {
		t.Fatalf("GitLab Definition 注册失败: %v", err)
	}
	for _, tool := range Definition.Tools {
		schemas, exists := reg.ToolSchemas(Definition.Type, tool.ID)
		if !exists || schemas.Input == nil {
			t.Fatalf("Tool %q 缺 compiled input schema", tool.ID)
		}
		if schemas.Output != nil {
			t.Fatalf("Tool %q 编出了 output schema", tool.ID)
		}
	}
}
