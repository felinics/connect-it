package linear

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestLinearDefinitionIdentity(t *testing.T) {
	t.Parallel()

	if Definition.Type != "linear" ||
		Definition.Name != "Linear" ||
		Definition.ConfigSchemaVersion != 1 {
		t.Fatalf(
			"Definition identity = type=%q name=%q version=%d",
			Definition.Type,
			Definition.Name,
			Definition.ConfigSchemaVersion,
		)
	}
}

// Both credentials are load-bearing: OAuth and a personal API key reach the
// same official Remote MCP server over the same Bearer binding.
func TestLinearDefinitionKeepsBothAuthenticationMethods(t *testing.T) {
	t.Parallel()

	if len(Definition.AuthMethods) != 2 {
		t.Fatalf("AuthMethods = %d, want 2", len(Definition.AuthMethods))
	}
	oauth := Definition.AuthMethods[0]
	if oauth.Key != linearOAuthMethod ||
		oauth.Type != connector.AuthOAuth2 ||
		oauth.OAuth == nil ||
		len(oauth.CredentialFields) != 0 ||
		oauth.OAuth.AuthorizationEndpoint != "https://linear.app/oauth/authorize" ||
		oauth.OAuth.TokenEndpoint != "https://api.linear.app/oauth/token" ||
		!slices.Equal(oauth.OAuth.Scopes, []string{
			"read",
			"write",
			"issues:create",
			"comments:create",
		}) {
		t.Fatalf("OAuth method = %+v", oauth)
	}

	apiKey := Definition.AuthMethods[1]
	if apiKey.Key != linearAuthMethod ||
		apiKey.Type != connector.AuthAPIKey ||
		apiKey.OAuth != nil ||
		len(apiKey.CredentialFields) != 1 {
		t.Fatalf("API key method = %+v", apiKey)
	}
	field := apiKey.CredentialFields[0]
	if field.Key != linearAPIKeyField ||
		field.InputType != connector.InputText ||
		!field.Required ||
		!field.Secret ||
		field.DefaultValue != nil ||
		field.Validation.Pattern == "" {
		t.Fatalf("API key field = %+v", field)
	}
}

func TestLinearDefinitionUsesOfficialRemoteMCPServer(t *testing.T) {
	t.Parallel()

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatalf(
			"RemoteMCPServers = %d, want 1",
			len(Definition.RemoteMCPServers),
		)
	}
	server := Definition.RemoteMCPServers[0]
	if server.Key != linearMCPServerKey ||
		server.Endpoint.Source != connector.EndpointFixed ||
		server.Endpoint.URL != "https://mcp.linear.app/mcp" ||
		server.AuthBinding.Scheme != "bearer" ||
		server.AuthBinding.CredentialFieldByAuthMethod[linearAuthMethod] !=
			linearAPIKeyField ||
		server.Provenance.Kind != connector.ProvenanceOfficial ||
		!slices.Equal(
			server.Provenance.AllowedHostnames,
			[]string{"mcp.linear.app"},
		) ||
		server.RequestTimeout <= 0 {
		t.Fatalf("Remote MCP server = %+v", server)
	}
	// The deprecated SSE transport must never come back.
	if strings.HasSuffix(server.Endpoint.URL, "/sse") {
		t.Fatal("Linear must use the Streamable HTTP endpoint")
	}
}

func TestLinearDefinitionHasExactRemoteToolsScopesAndRisk(t *testing.T) {
	t.Parallel()

	expected := []struct {
		id    string
		scope string
		risk  connector.ToolRisk
	}{
		{id: "list_issues", scope: "read", risk: connector.RiskRead},
		{id: "get_issue", scope: "read", risk: connector.RiskRead},
		{
			id:    "create_issue",
			scope: "issues:create",
			risk:  connector.RiskWrite,
		},
		{id: "update_issue", scope: "write", risk: connector.RiskWrite},
		{id: "list_comments", scope: "read", risk: connector.RiskRead},
		{
			id:    "create_comment",
			scope: "comments:create",
			risk:  connector.RiskWrite,
		},
		{id: "list_projects", scope: "read", risk: connector.RiskRead},
		{id: "list_teams", scope: "read", risk: connector.RiskRead},
		{id: "list_users", scope: "read", risk: connector.RiskRead},
		{id: "list_issue_labels", scope: "read", risk: connector.RiskRead},
		{id: "list_issue_statuses", scope: "read", risk: connector.RiskRead},
		{id: "list_cycles", scope: "read", risk: connector.RiskRead},
	}
	if len(Definition.Tools) != len(expected) {
		t.Fatalf(
			"Tools = %d, want exactly %d",
			len(Definition.Tools),
			len(expected),
		)
	}
	for index, want := range expected {
		tool := Definition.Tools[index]
		if tool.ID != want.id ||
			tool.Risk != want.risk ||
			!slices.Equal(tool.RequiredScopes, []string{want.scope}) {
			t.Fatalf(
				"Tools[%d] = id=%q risk=%q scopes=%v",
				index,
				tool.ID,
				tool.Risk,
				tool.RequiredScopes,
			)
		}
		backend, ok := tool.Backend.(connector.RemoteMCPBackend)
		if !ok ||
			backend.ServerKey != linearMCPServerKey ||
			backend.RemoteToolName != tool.ID {
			t.Fatalf(
				"Tool %q backend = %#v, want the same-name official remote Tool",
				tool.ID,
				tool.Backend,
			)
		}
		// The upstream response shape is unknown, so declaring an OutputSchema
		// would make the Tool fail validation forever.
		if len(tool.OutputSchema) != 0 {
			t.Fatalf("Tool %q must not declare an OutputSchema", tool.ID)
		}
	}
	// Linear's own documentation search is not a workspace capability.
	for _, tool := range Definition.Tools {
		if tool.ID == "search_documentation" {
			t.Fatal("search_documentation must not be exposed")
		}
	}
}

// Remote Tool arguments pass through to the upstream server, whose tools/list is
// the authority; this Definition's schemas are a permissive approximation and
// must never reject an argument the upstream server would accept.
func TestLinearRemoteInputSchemasArePassThrough(t *testing.T) {
	t.Parallel()

	reg := registeredLinearDefinition(t)
	minimalInputs := map[string]map[string]any{
		"list_issues":         {},
		"get_issue":           {"id": "ENG-42"},
		"create_issue":        {"team": "Engineering", "title": "Ship it"},
		"update_issue":        {"id": "ENG-42", "title": "Updated"},
		"list_comments":       {"issueId": "issue-1"},
		"create_comment":      {"issueId": "issue-1", "body": "Done"},
		"list_projects":       {},
		"list_teams":          {},
		"list_users":          {},
		"list_issue_labels":   {},
		"list_issue_statuses": {"team": "Engineering"},
		"list_cycles":         {"teamId": "team-1"},
	}
	for _, tool := range Definition.Tools {
		var schema struct {
			AdditionalProperties *bool `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("Tool %q input schema is not valid JSON: %v", tool.ID, err)
		}
		if schema.AdditionalProperties == nil || !*schema.AdditionalProperties {
			t.Fatalf("Tool %q input schema is not pass-through", tool.ID)
		}

		schemas, exists := reg.ToolSchemas(Definition.Type, tool.ID)
		if !exists || schemas.Input == nil {
			t.Fatalf("Tool %q is missing a compiled input schema", tool.ID)
		}
		input, declared := minimalInputs[tool.ID]
		if !declared {
			t.Fatalf("Tool %q has no minimal input in this test", tool.ID)
		}
		if err := schemas.Input.Validate(input); err != nil {
			t.Fatalf("Tool %q minimal input was rejected: %v", tool.ID, err)
		}
		withUnknown := make(map[string]any, len(input)+1)
		for key, value := range input {
			withUnknown[key] = value
		}
		withUnknown["orderBy"] = "updatedAt"
		if err := schemas.Input.Validate(withUnknown); err != nil {
			t.Fatalf(
				"Tool %q rejected an argument the upstream server may accept: %v",
				tool.ID,
				err,
			)
		}
	}
}

func TestLinearRequiredArgumentsAreEnforced(t *testing.T) {
	t.Parallel()

	reg := registeredLinearDefinition(t)
	for toolID, input := range map[string]map[string]any{
		"get_issue":           {},
		"create_issue":        {"title": "Missing team"},
		"update_issue":        {"title": "Missing id"},
		"list_comments":       {},
		"create_comment":      {"issueId": "issue-1"},
		"list_issue_statuses": {},
		"list_cycles":         {},
	} {
		schemas, _ := reg.ToolSchemas(Definition.Type, toolID)
		if err := schemas.Input.Validate(input); err == nil {
			t.Errorf("%s accepted input without its required arguments", toolID)
		}
	}
}

func registeredLinearDefinition(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := reg.Register(Definition); err != nil {
		t.Fatalf("register Linear Definition: %v", err)
	}
	return reg
}
