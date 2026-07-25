package registry_test

import (
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func policyDefinition() connector.Definition {
	falseValue := "false"
	return connector.Definition{
		Type:                "policy_app",
		Name:                "Policy App",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{
				Key:       "client_id",
				Label:     "Client ID",
				InputType: connector.InputText,
			},
			{
				Key:       "tenant",
				Label:     "Tenant",
				InputType: connector.InputText,
			},
			{
				Key:       "mcp_url",
				Label:     "MCP URL",
				InputType: connector.InputURL,
			},
			{
				Key:          "allow_insecure_http",
				Label:        "Allow HTTP",
				InputType:    connector.InputSelect,
				DefaultValue: &falseValue,
				Validation: connector.FieldValidation{
					Options: []string{"false", "true"},
				},
			},
			{
				Key:            "regional_partition",
				Label:          "Partition",
				InputType:      connector.InputText,
				PolicyIdentity: true,
			},
			{
				Key:            "private_policy",
				Label:          "Private policy",
				InputType:      connector.InputText,
				Secret:         true,
				PolicyIdentity: true,
			},
			{
				Key:       "display_only",
				Label:     "Display",
				InputType: connector.InputText,
			},
		},
		AuthMethods: []connector.AuthMethod{{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://login.example/{tenant}/authorize",
				TokenEndpoint:         "https://login.example/{tenant}/token",
				RefreshTokenEndpoint:  "https://login.example/{tenant}/refresh",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://login.example:443"},
					TokenOrigins:         []string{"https://login.example:443"},
				},
			},
		}},
		RemoteMCPServers: []connector.RemoteMCPServer{{
			Key: "self",
			Endpoint: connector.Endpoint{
				Source:         connector.EndpointConfigField,
				ConfigFieldKey: "mcp_url",
			},
			Provenance: connector.Provenance{Kind: connector.ProvenanceSelfHosted},
		}},
	}
}

func TestPolicyIdentitySpecAutomaticallyIncludesSecurityFields(t *testing.T) {
	r := registry.New()
	if err := r.Register(policyDefinition()); err != nil {
		t.Fatal(err)
	}
	spec, ok := r.PolicyIdentitySpec("policy_app")
	if !ok || spec.Version != 2 {
		t.Fatalf("spec = %+v, ok=%v", spec, ok)
	}
	got := make(map[string]bool, len(spec.Fields))
	for _, field := range spec.Fields {
		got[field.Key] = field.Secret
	}
	for _, key := range []string{
		"client_id",
		"tenant",
		"mcp_url",
		"allow_insecure_http",
		"regional_partition",
		"private_policy",
	} {
		if _, included := got[key]; !included {
			t.Fatalf("field %q is missing from policy identity", key)
		}
	}
	if _, included := got["display_only"]; included {
		t.Fatal("ordinary display field unexpectedly entered policy identity")
	}
	if !got["private_policy"] {
		t.Fatal("secret classification was not retained")
	}

	// Caller mutation must not alter the Registry's immutable Definition.
	spec.Fields[0].Key = "tampered"
	again, _ := r.PolicyIdentitySpec("policy_app")
	if again.Fields[0].Key == "tampered" {
		t.Fatal("PolicyIdentitySpec returned aliased storage")
	}
}

func TestPolicyIdentityDefinitionLint(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*connector.Definition)
		wantErr string
	}{
		{
			name: "remote endpoint must be non-secret URL",
			mutate: func(def *connector.Definition) {
				def.ConfigFields[2].Secret = true
			},
			wantErr: "InputURL",
		},
		{
			name: "unknown oauth placeholder",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].OAuth.TokenEndpoint =
					"https://login.example/{unknown}/token"
			},
			wantErr: "不存在",
		},
		{
			name: "malformed oauth placeholder",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].OAuth.TokenEndpoint =
					"https://login.example/{Tenant}/token"
			},
			wantErr: "placeholder",
		},
		{
			name: "unknown refresh placeholder",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].OAuth.RefreshTokenEndpoint =
					"https://login.example/{unknown}/refresh"
			},
			wantErr: "不存在",
		},
		{
			name: "secret oauth placeholder",
			mutate: func(def *connector.Definition) {
				def.ConfigFields[1].Secret = true
			},
			wantErr: "Secret",
		},
		{
			name: "network flag exact declaration",
			mutate: func(def *connector.Definition) {
				def.ConfigFields[3].Validation.Options = []string{"false", "yes"}
			},
			wantErr: "allow_insecure_http",
		},
		{
			name: "oauth client id public",
			mutate: func(def *connector.Definition) {
				def.ConfigFields[0].Secret = true
			},
			wantErr: "client_id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			def := policyDefinition()
			test.mutate(&def)
			err := registry.New().Register(def)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestCredentialScopedInsecureHTTPFlagRequiresExactDeclaration(t *testing.T) {
	falseValue := "false"
	withCredentialFlag := func() connector.Definition {
		def := policyDefinition()
		def.AuthMethods = append(def.AuthMethods, connector.AuthMethod{
			Key:   "pat",
			Type:  connector.AuthAPIKey,
			Label: "PAT",
			CredentialFields: []connector.ConfigField{
				{
					Key:       "token",
					Label:     "Token",
					InputType: connector.InputText,
					Required:  true,
					Secret:    true,
				},
				{
					Key:          "allow_insecure_http",
					Label:        "Allow insecure HTTP",
					InputType:    connector.InputSelect,
					DefaultValue: &falseValue,
					Validation: connector.FieldValidation{
						Options: []string{"false", "true"},
					},
				},
			},
		})
		def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod =
			map[string]string{"pat": "token"}
		return def
	}

	if err := registry.New().Register(withCredentialFlag()); err != nil {
		t.Fatalf("valid credential-scoped network flag: %v", err)
	}

	for _, mutate := range []func(*connector.ConfigField){
		func(field *connector.ConfigField) { field.Secret = true },
		func(field *connector.ConfigField) {
			field.InputType = connector.InputText
		},
		func(field *connector.ConfigField) { field.DefaultValue = nil },
		func(field *connector.ConfigField) {
			field.Validation.Options = []string{"false", "yes"}
		},
	} {
		def := withCredentialFlag()
		mutate(&def.AuthMethods[1].CredentialFields[1])
		err := registry.New().Register(def)
		if err == nil || !strings.Contains(err.Error(), "allow_insecure_http") {
			t.Fatalf("invalid credential flag error = %v", err)
		}
	}
}

func TestRemoteMCPCredentialBindingIsExplicitAndComplete(t *testing.T) {
	withPAT := func() connector.Definition {
		def := policyDefinition()
		def.AuthMethods = append(def.AuthMethods, connector.AuthMethod{
			Key:  "pat",
			Type: connector.AuthAPIKey,
			CredentialFields: []connector.ConfigField{{
				Key:       "token",
				InputType: connector.InputText,
				Required:  true,
				Secret:    true,
			}},
		})
		def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod =
			map[string]string{"pat": "token"}
		return def
	}
	if err := registry.New().Register(withPAT()); err != nil {
		t.Fatalf("valid explicit credential binding: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*connector.Definition)
		wantErr string
	}{
		{
			name: "missing binding",
			mutate: func(def *connector.Definition) {
				def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod = nil
			},
			wantErr: "缺少显式",
		},
		{
			name: "unknown method",
			mutate: func(def *connector.Definition) {
				def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod =
					map[string]string{"missing": "token", "pat": "token"}
			},
			wantErr: "不存在 auth method",
		},
		{
			name: "oauth method cannot bind credential field",
			mutate: func(def *connector.Definition) {
				def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod =
					map[string]string{"oauth": "token", "pat": "token"}
			},
			wantErr: "api_key/custom_credential",
		},
		{
			name: "unknown field",
			mutate: func(def *connector.Definition) {
				def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod["pat"] =
					"missing"
			},
			wantErr: "不存在字段",
		},
		{
			name: "field must be secret",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[1].CredentialFields[0].Secret = false
			},
			wantErr: "Secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			def := withPAT()
			test.mutate(&def)
			err := registry.New().Register(def)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}
