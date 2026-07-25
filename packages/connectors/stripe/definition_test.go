package stripe

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestStripeDefinitionAuthenticationAndRemoteServer(t *testing.T) {
	t.Parallel()

	if Definition.Type != "stripe" ||
		Definition.Name != "Stripe" ||
		Definition.ConfigSchemaVersion != 1 {
		t.Fatalf("Stripe definition identity = %+v", Definition)
	}

	if len(Definition.AuthMethods) != 1 {
		t.Fatalf("auth methods = %d", len(Definition.AuthMethods))
	}
	auth := Definition.AuthMethods[0]
	if auth.Key != stripeAuthMethod ||
		auth.Type != connector.AuthAPIKey ||
		auth.OAuth != nil ||
		len(auth.CredentialFields) != 1 {
		t.Fatalf("auth method = %+v", auth)
	}
	field := auth.CredentialFields[0]
	if field.Key != stripeAPIKeyField ||
		field.InputType != connector.InputText ||
		!field.Required || !field.Secret ||
		field.Validation.Pattern == "" ||
		!strings.Contains(field.Description, "Opaque") ||
		strings.Contains(field.Validation.Pattern, "sk_") ||
		strings.Contains(field.Validation.Pattern, "rk_") {
		t.Fatalf("API-key field = %+v", field)
	}
	// MCP does not negotiate tool-level permission, so the operator must be
	// told to narrow the key itself.
	if !strings.Contains(field.Description, "rk_") {
		t.Fatal("API-key field must recommend a Restricted API Key")
	}

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatalf("remote MCP servers = %d", len(Definition.RemoteMCPServers))
	}
	server := Definition.RemoteMCPServers[0]
	if server.Key != "official" ||
		server.Endpoint.Source != connector.EndpointFixed ||
		server.Endpoint.URL != "https://mcp.stripe.com" ||
		server.Provenance.Kind != connector.ProvenanceOfficial ||
		!slices.Equal(
			server.Provenance.AllowedHostnames,
			[]string{"mcp.stripe.com"},
		) ||
		server.AuthBinding.Scheme != "bearer" ||
		server.AuthBinding.CredentialFieldByAuthMethod[stripeAuthMethod] !=
			stripeAPIKeyField ||
		server.RequestTimeout != 30*time.Second {
		t.Fatalf("remote MCP server = %+v", server)
	}
}

func TestStripeExposesOnlyReviewedRemoteTools(t *testing.T) {
	t.Parallel()

	expected := []struct {
		id   string
		risk connector.ToolRisk
	}{
		{"get_stripe_account_info", connector.RiskRead},
		{"get_balance_summary", connector.RiskRead},
		{"search_stripe_resources", connector.RiskRead},
		{"fetch_stripe_resources", connector.RiskRead},
		{"create_refund", connector.RiskDestructive},
	}
	if len(Definition.Tools) != len(expected) {
		t.Fatalf("tools = %d, want %d", len(Definition.Tools), len(expected))
	}
	for index, want := range expected {
		tool := Definition.Tools[index]
		backend, ok := tool.Backend.(connector.RemoteMCPBackend)
		if !ok ||
			backend.ServerKey != "official" ||
			backend.RemoteToolName != want.id {
			t.Fatalf("tool %q backend = %#v", tool.ID, tool.Backend)
		}
		if tool.ID != want.id || tool.Risk != want.risk {
			t.Fatalf("tool[%d] = %+v", index, tool)
		}
		// Remote responses are the upstream server's contract; a guessed
		// OutputSchema would fail every call at runtime.
		if tool.OutputSchema != nil {
			t.Fatalf("tool %q declares an OutputSchema", tool.ID)
		}
	}

	// The blueprint rules out generic proxy Tools: stripe_api_write alone
	// would reach every write endpoint the credential permits. Developer aids
	// are not account capabilities either. Both stay unexposed.
	for _, forbidden := range []string{
		"stripe_api_read",
		"stripe_api_write",
		"stripe_api_search",
		"stripe_api_details",
		"search_stripe_documentation",
		"stripe_implementation_planner",
		"send_stripe_mcp_feedback",
	} {
		for _, tool := range Definition.Tools {
			backend, _ := tool.Backend.(connector.RemoteMCPBackend)
			if tool.ID == forbidden || backend.RemoteToolName == forbidden {
				t.Fatalf("Stripe exposes withheld upstream tool %q", forbidden)
			}
		}
	}
}

// Remote MCP arguments pass through to Stripe, so the published schemas stay
// open: they describe what is known without rejecting anything upstream may
// accept.
func TestStripeSchemasRegisterAndPassArgumentsThrough(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	if err := reg.Register(Definition); err != nil {
		t.Fatalf("register Stripe definition: %v", err)
	}
	minimal := map[string]map[string]any{
		"create_refund": {"payment_intent": "pi_123"},
	}
	for _, tool := range Definition.Tools {
		schemas, exists := reg.ToolSchemas(Definition.Type, tool.ID)
		if !exists || schemas.Input == nil || schemas.Output != nil {
			t.Fatalf("tool %q schemas = %+v (exists %t)", tool.ID, schemas, exists)
		}
		input := map[string]any{"upstream_only_parameter": "passes through"}
		for key, value := range minimal[tool.ID] {
			input[key] = value
		}
		if err := schemas.Input.Validate(input); err != nil {
			t.Fatalf("tool %q rejected an upstream parameter: %v", tool.ID, err)
		}
	}

	refund, _ := reg.ToolSchemas(Definition.Type, "create_refund")
	if err := refund.Input.Validate(map[string]any{
		"payment_intent": "pi_123",
		"amount":         float64(500),
	}); err != nil {
		t.Fatalf("create_refund rejected a valid refund: %v", err)
	}
	if err := refund.Input.Validate(map[string]any{}); err == nil {
		t.Fatal("create_refund accepted a missing payment_intent")
	}
	if err := refund.Input.Validate(map[string]any{
		"payment_intent": "pi_123",
		"amount":         1.5,
	}); err == nil {
		t.Fatal("create_refund accepted a fractional amount")
	}
}
