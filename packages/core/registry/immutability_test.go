package registry_test

import (
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func immutableDefinition() connector.Definition {
	def := makeValid()
	def.Categories = []string{"developer", "productivity"}
	def.ConfigSchemaVersion = 2
	def.ConfigFields = append(def.ConfigFields, connector.ConfigField{
		Key:          "region",
		Label:        "Region",
		InputType:    connector.InputSelect,
		DefaultValue: strPtr("us"),
		Validation: connector.FieldValidation{
			Options: []string{"us", "eu"},
		},
	})
	def.AuthMethods[0].OAuth.Scopes = []string{"read", "write"}
	def.AuthMethods[0].OAuth.ExtraAuthParams =
		map[string]string{"audience": "catalog"}
	def.AuthMethods[0].OAuth.ExtraTokenParams =
		map[string]string{"resource": "catalog"}
	def.AuthMethods = append(def.AuthMethods, connector.AuthMethod{
		Key:   "api_token",
		Type:  connector.AuthAPIKey,
		Label: "API token",
		CredentialFields: []connector.ConfigField{
			{
				Key:       "token",
				Label:     "Token",
				InputType: connector.InputText,
				Required:  true,
				Secret:    true,
				Validation: connector.FieldValidation{
					Pattern: `^tok_[a-z]+$`,
				},
			},
			{
				Key:          "credential_region",
				Label:        "Credential region",
				InputType:    connector.InputSelect,
				DefaultValue: strPtr("us"),
				Validation: connector.FieldValidation{
					Options: []string{"us", "eu"},
				},
			},
		},
	})
	for index := range def.RemoteMCPServers {
		def.RemoteMCPServers[index].AuthBinding =
			connector.MCPAuthBinding{
				Scheme: "bearer",
				CredentialFieldByAuthMethod: map[string]string{
					"api_token": "token",
				},
			}
	}
	def.RemoteMCPServers[0].Provenance.AllowedHostnames =
		[]string{"mcp.example.com"}
	def.Tools[0].RequiredScopes = []string{"read"}
	def.Tools[1].RequiredScopes = []string{"write"}
	def.ConfigUpgraders = []connector.ConfigUpgrader{
		{
			FromVersion: 1,
			Upgrade: func(
				public map[string]any,
				secret map[string]any,
			) (map[string]any, map[string]any, error) {
				public["upgrader"] = "original"
				return public, secret, nil
			},
		},
	}
	return def
}

func mutateDefinitionSnapshot(def *connector.Definition) {
	def.Categories[0] = "attacker"
	def.ConfigFields[3].Validation.Options[0] = "attacker"
	*def.ConfigFields[3].DefaultValue = "attacker"
	def.AuthMethods[0].OAuth.Scopes[0] = "attacker"
	def.AuthMethods[0].OAuth.Egress.TokenOrigins[0] =
		"https://attacker.invalid:443"
	def.AuthMethods[0].OAuth.ExtraAuthParams["audience"] = "attacker"
	def.AuthMethods[0].OAuth.ExtraTokenParams["resource"] = "attacker"
	def.AuthMethods[1].CredentialFields[0].Validation.Pattern = "attacker"
	def.AuthMethods[1].CredentialFields[1].Validation.Options[0] = "attacker"
	*def.AuthMethods[1].CredentialFields[1].DefaultValue = "attacker"
	def.RemoteMCPServers[0].AuthBinding.
		CredentialFieldByAuthMethod["api_token"] = "attacker"
	def.RemoteMCPServers[0].Provenance.AllowedHostnames[0] =
		"attacker.invalid"
	def.Tools[0].InputSchema[0] = 'X'
	def.Tools[0].RequiredScopes[0] = "attacker"
	def.Tools[0].Risk = connector.RiskDestructive
	def.Tools[0].Backend = connector.ManagedBackend{HandlerKey: "attacker"}
	def.Tools[1].OutputSchema[0] = 'X'
	def.ConfigUpgraders[0].FromVersion = 99
}

func assertDefinitionUnchanged(t *testing.T, def connector.Definition) {
	t.Helper()
	if def.Categories[0] != "developer" {
		t.Fatalf("catalog data was mutated: %+v", def.Categories)
	}
	if def.ConfigFields[3].Validation.Options[0] != "us" ||
		*def.ConfigFields[3].DefaultValue != "us" {
		t.Fatalf("config fields were mutated: %+v", def.ConfigFields[3])
	}
	oauth := def.AuthMethods[0].OAuth
	if oauth.Scopes[0] != "read" ||
		oauth.Egress.TokenOrigins[0] != "https://example.com:443" ||
		oauth.ExtraAuthParams["audience"] != "catalog" ||
		oauth.ExtraTokenParams["resource"] != "catalog" {
		t.Fatalf("OAuth contract was mutated: %+v", oauth)
	}
	credentials := def.AuthMethods[1].CredentialFields
	if credentials[0].Validation.Pattern != `^tok_[a-z]+$` ||
		credentials[1].Validation.Options[0] != "us" ||
		*credentials[1].DefaultValue != "us" {
		t.Fatalf("credential fields were mutated: %+v", credentials)
	}
	server := def.RemoteMCPServers[0]
	if server.AuthBinding.CredentialFieldByAuthMethod["api_token"] != "token" ||
		server.Provenance.AllowedHostnames[0] != "mcp.example.com" {
		t.Fatalf("remote MCP contract was mutated: %+v", server)
	}
	remote, ok := def.Tools[0].Backend.(connector.RemoteMCPBackend)
	if def.Tools[0].InputSchema[0] != '{' ||
		def.Tools[0].RequiredScopes[0] != "read" ||
		def.Tools[0].Risk != connector.RiskRead ||
		!ok ||
		remote.ServerKey != "main" ||
		def.Tools[1].OutputSchema[0] != '{' {
		t.Fatalf("tool contract was mutated: %+v", def.Tools)
	}
	if def.ConfigUpgraders[0].FromVersion != 1 {
		t.Fatalf("config upgrader was mutated: %+v", def.ConfigUpgraders[0])
	}
}

func TestDefinitionSnapshotsAreDeeplyImmutable(t *testing.T) {
	def := immutableDefinition()
	r := registry.New()
	if err := r.Register(def, "create_item"); err != nil {
		t.Fatal(err)
	}

	// Register must not retain any caller-owned mutable backing storage.
	mutateDefinitionSnapshot(&def)
	registered, ok := r.Get("example_app")
	if !ok {
		t.Fatal("registered definition missing")
	}
	assertDefinitionUnchanged(t, registered)

	// Get must return a complete defensive copy, not just clone OAuth/install.
	mutateDefinitionSnapshot(&registered)
	afterGetMutation, _ := r.Get("example_app")
	assertDefinitionUnchanged(t, afterGetMutation)

	// All has the same immutability contract as Get.
	all := r.All()
	if len(all) != 1 {
		t.Fatalf("All length = %d, want 1", len(all))
	}
	mutateDefinitionSnapshot(&all[0])
	afterAllMutation, _ := r.Get("example_app")
	assertDefinitionUnchanged(t, afterAllMutation)

	// Compiled schemas are registration-time objects and remain independent of
	// both the caller's RawMessage and returned Definition snapshots.
	schemas, ok := r.ToolSchemas("example_app", "list_items")
	if !ok {
		t.Fatal("compiled schemas missing")
	}
	if err := schemas.Input.ApplyDefaultsAndValidate(map[string]any{}); err != nil {
		t.Fatalf("compiled input schema changed after RawMessage mutation: %v", err)
	}
}
