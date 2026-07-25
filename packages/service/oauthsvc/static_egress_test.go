package oauthsvc_test

import (
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
)

func staticOAuthDefinition() connector.Definition {
	return connector.Definition{
		Type: "example",
		AuthMethods: []connector.AuthMethod{
			{
				Key:  "oauth",
				Type: connector.AuthOAuth2,
				OAuth: &connector.OAuthConfig{
					AuthorizationEndpoint: "https://login.example.test/{tenant}/authorize",
					TokenEndpoint:         "https://login.example.test/{tenant}/token",
					Egress: connector.OAuthEgressConfig{
						AuthorizationOrigins: []string{
							"https://login.example.test:443",
						},
						TokenOrigins: []string{
							"https://login.example.test:443",
						},
					},
				},
			},
		},
	}
}

func TestPreflightDefinitionsValidatesStaticOAuthPolicies(t *testing.T) {
	factory := providerkit.NewFactory()
	valid := staticOAuthDefinition()
	if err := oauthsvc.PreflightDefinitions(
		factory,
		[]connector.Definition{valid},
	); err != nil {
		t.Fatalf("valid policies failed preflight: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*connector.Definition)
		label  string
	}{
		{
			name: "authorization private literal",
			mutate: func(definition *connector.Definition) {
				definition.AuthMethods[0].OAuth.Egress.
					AuthorizationOrigins = []string{
					"https://127.0.0.1:443",
				}
			},
			label: "authorization policy",
		},
		{
			name: "token private literal",
			mutate: func(definition *connector.Definition) {
				definition.AuthMethods[0].OAuth.Egress.TokenOrigins =
					[]string{"https://127.0.0.1:443"}
			},
			label: "token policy",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := staticOAuthDefinition()
			test.mutate(&definition)

			err := oauthsvc.PreflightDefinitions(
				factory,
				[]connector.Definition{definition},
			)
			if err == nil {
				t.Fatal("unsafe static OAuth policy passed preflight")
			}
			if !strings.Contains(err.Error(), `connector "example"`) ||
				!strings.Contains(err.Error(), `auth method`) ||
				!strings.Contains(err.Error(), test.label) {
				t.Fatalf("preflight error lacks safe identity: %v", err)
			}
		})
	}
}

func TestPreflightDefinitionsRejectsNilFactory(t *testing.T) {
	if err := oauthsvc.PreflightDefinitions(nil, nil); err == nil {
		t.Fatal("nil factory passed preflight")
	}
}
