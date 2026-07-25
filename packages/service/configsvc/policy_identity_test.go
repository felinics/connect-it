package configsvc

import (
	"bytes"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/store"
)

func mustPolicySpec(t *testing.T, def connector.Definition) registry.ConnectorPolicyIdentitySpec {
	t.Helper()
	r := registry.New()
	if err := r.Register(def); err != nil {
		t.Fatal(err)
	}
	spec, ok := r.PolicyIdentitySpec(def.Type)
	if !ok {
		t.Fatal("missing policy spec")
	}
	return spec
}

func TestPolicyIdentityCanonicalizesEndpointAndTracksSecurityInputs(t *testing.T) {
	def := Fixture("policy_app", "https://login.example/{tenant}/token")
	spec := mustPolicySpec(t, def)
	first, err := projectPolicyIdentity(def, spec, map[string]string{
		"client_id":           "client-a",
		"tenant":              "common",
		"mcp_url":             "HTTPS://MCP.EXAMPLE:443/mcp?b=2&a=1",
		"allow_insecure_http": "false",
		"private_partition":   "secret-partition",
	})
	if err != nil {
		t.Fatal(err)
	}
	equivalent, err := projectPolicyIdentity(def, spec, map[string]string{
		"client_id":           "client-a",
		"tenant":              "common",
		"mcp_url":             "https://mcp.example/mcp?a=1&b=2",
		"allow_insecure_http": "false",
		"private_partition":   "secret-partition",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.identity != equivalent.identity {
		t.Fatal("equivalent canonical endpoints produced different identities")
	}

	for name, mutate := range map[string]func(map[string]string){
		"oauth client": func(values map[string]string) { values["client_id"] = "client-b" },
		"oauth tenant": func(values map[string]string) { values["tenant"] = "organizations" },
		"mcp path":     func(values map[string]string) { values["mcp_url"] = "https://mcp.example/v2" },
		"network flag": func(values map[string]string) { values["allow_insecure_http"] = "true" },
		"secret extra": func(values map[string]string) { values["private_partition"] = "other-secret" },
	} {
		t.Run(name, func(t *testing.T) {
			values := map[string]string{
				"client_id":           "client-a",
				"tenant":              "common",
				"mcp_url":             "https://mcp.example/mcp?a=1&b=2",
				"allow_insecure_http": "false",
				"private_partition":   "secret-partition",
			}
			mutate(values)
			got, err := projectPolicyIdentity(def, spec, values)
			if err != nil {
				t.Fatal(err)
			}
			if got.identity == first.identity {
				t.Fatal("security input change did not alter identity")
			}
		})
	}
	if bytes.Contains(first.identity[:], []byte("secret-partition")) {
		t.Fatal("persistable identity digest contains secret plaintext")
	}
}

func TestPolicyIdentityTracksStaticDefinitionDrift(t *testing.T) {
	firstDefinition := Fixture("policy_app", "https://login.example/{tenant}/token")
	first, err := projectPolicyIdentity(
		firstDefinition,
		mustPolicySpec(t, firstDefinition),
		map[string]string{
			"client_id":           "client-a",
			"tenant":              "common",
			"mcp_url":             "https://mcp.example/mcp",
			"allow_insecure_http": "false",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondDefinition := Fixture("policy_app", "https://login.example/{tenant}/token")
	secondDefinition.AuthMethods[0].OAuth.TokenEndpoint =
		"https://tokens.example/{tenant}/token"
	secondDefinition.AuthMethods[0].OAuth.Egress.TokenOrigins =
		[]string{"https://tokens.example:443"}
	second, err := projectPolicyIdentity(
		secondDefinition,
		mustPolicySpec(t, secondDefinition),
		map[string]string{
			"client_id":           "client-a",
			"tenant":              "common",
			"mcp_url":             "https://mcp.example/mcp",
			"allow_insecure_http": "false",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.definition == second.definition || first.identity == second.identity {
		t.Fatal("static OAuth endpoint drift did not alter policy digests")
	}
}

func TestMatchesPolicyIdentityRequiresExactPersistedState(t *testing.T) {
	digests := policyDigests{version: 1}
	for i := range digests.identity {
		digests.identity[i] = byte(i)
		digests.definition[i] = byte(i + 1)
	}
	validState := func() store.ConnectorPolicyIdentity {
		return store.ConnectorPolicyIdentity{
			IdentityVersion:  int32(digests.version),
			IdentityDigest:   append([]byte(nil), digests.identity[:]...),
			DefinitionDigest: append([]byte(nil), digests.definition[:]...),
			Initialized:      true,
		}
	}
	tests := []struct {
		name   string
		mutate func(*store.ConnectorPolicyIdentity)
		want   bool
	}{
		{name: "exact", mutate: func(*store.ConnectorPolicyIdentity) {}, want: true},
		{
			name: "uninitialized",
			mutate: func(state *store.ConnectorPolicyIdentity) {
				state.Initialized = false
			},
		},
		{
			name: "version",
			mutate: func(state *store.ConnectorPolicyIdentity) {
				state.IdentityVersion++
			},
		},
		{
			name: "identity digest",
			mutate: func(state *store.ConnectorPolicyIdentity) {
				state.IdentityDigest[0] ^= 0xff
			},
		},
		{
			name: "definition digest",
			mutate: func(state *store.ConnectorPolicyIdentity) {
				state.DefinitionDigest[0] ^= 0xff
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := validState()
			test.mutate(&state)
			if got := matchesPolicyIdentity(state, digests); got != test.want {
				t.Fatalf("matchesPolicyIdentity() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPolicyIdentityURLFailureDoesNotEchoValue(t *testing.T) {
	def := Fixture("policy_app", "https://login.example/{tenant}/token")
	secretLikeValue := "https://user:credential@example.com/mcp"
	_, err := projectPolicyIdentity(def, mustPolicySpec(t, def), map[string]string{
		"mcp_url": secretLikeValue,
	})
	if err == nil {
		t.Fatal("userinfo URL was accepted")
	}
	if strings.Contains(err.Error(), secretLikeValue) ||
		strings.Contains(err.Error(), "credential") {
		t.Fatalf("safe validation error leaked URL: %v", err)
	}
}
