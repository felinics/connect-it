package notion

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

// validatorServer starts a guarded test client bound to this Provider identity.
func validatorServer(t *testing.T, handler http.HandlerFunc) *testkit.Server {
	t.Helper()
	options := testkit.ServerOptions{Provider: string(Definition.Type)}
	return testkit.NewServerWithOptions(t, handler, options)
}

func notionValidationInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   notionAuthMethod,
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "notion-validator-test",
		AccessToken:     "notion-access-token-secret",
		TokenType:       "Bearer",
		Config: map[string]any{
			"client_id":     "notion-client-id",
			"client_secret": "notion-client-secret",
		},
	}
}

func TestNotionCredentialValidatorIntrospectsAccessToken(t *testing.T) {
	var calls atomic.Int32
	server := validatorServer(
		t,
		func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			if request.Method != http.MethodPost ||
				request.URL.Path != notionIntrospectionPath {
				t.Errorf(
					"request = %s %s",
					request.Method,
					request.URL.Path,
				)
			}
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Basic "+base64.StdEncoding.EncodeToString(
					[]byte("notion-client-id:notion-client-secret"),
				),
			)
			testkit.AssertNoHeader(t, request, "Referer")
			// RFC 7662：被验证的 token 只能出现在 form body。
			testkit.AssertFormBody(t, request, url.Values{
				"token":           {"notion-access-token-secret"},
				"token_type_hint": {"access_token"},
			})
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"active":true,"sub":"user-1234","username":"Ada"}`,
			))
		},
	)

	result, err := newCredentialValidators(server.Client)[notionAuthMethod](
		t.Context(),
		notionValidationInput(),
	)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Profile.AccountID != "user-1234" ||
		result.Profile.DisplayName != "Ada" ||
		result.ScopesKnown ||
		result.GrantedScopes != nil {
		t.Fatalf("validation result = %+v", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1", calls.Load())
	}
}

// introspection 的可选字段缺失不能让连接永远建不起来：active 才是判据。
func TestNotionCredentialValidatorToleratesMissingOptionalIdentity(t *testing.T) {
	server := validatorServer(
		t,
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"active":true}`))
		},
	)
	result, err := newCredentialValidators(server.Client)[notionAuthMethod](
		t.Context(),
		notionValidationInput(),
	)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Profile.AccountID != "" ||
		result.Profile.DisplayName != "Notion workspace" {
		t.Fatalf("profile = %+v", result.Profile)
	}
}

func TestNotionCredentialValidatorRejectsInactiveAndUnusableResponses(
	t *testing.T,
) {
	tests := []struct {
		name string
		body string
		code connector.FailureCode
	}{
		{
			name: "inactive token",
			body: `{"active":false}`,
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "missing active",
			body: `{"sub":"user-1234"}`,
			code: connector.FailureInvalidResponse,
		},
		{
			name: "malformed",
			body: `{"active":`,
			code: connector.FailureInvalidResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := validatorServer(
				t,
				func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					_, _ = writer.Write([]byte(test.body))
				},
			)
			_, err := newCredentialValidators(
				server.Client,
			)[notionAuthMethod](t.Context(), notionValidationInput())
			assertNotionCredentialError(t, err, test.code)
			assertNotionCredentialErrorRedacted(t, err)
		})
	}
}

func TestNotionCredentialValidatorRequiresExactInputWithoutRequest(
	t *testing.T,
) {
	server := validatorServer(
		t,
		func(http.ResponseWriter, *http.Request) {
			t.Error("invalid validator input reached Notion")
		},
	)
	tests := []struct {
		name   string
		mutate func(*connector.CredentialValidationInput)
		code   connector.FailureCode
	}{
		{
			name: "missing access token",
			mutate: func(input *connector.CredentialValidationInput) {
				input.AccessToken = ""
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "unnormalized token type",
			mutate: func(input *connector.CredentialValidationInput) {
				input.TokenType = "bearer"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "api key material",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields = map[string]string{"token": "ntn_secret"}
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "wrong connector",
			mutate: func(input *connector.CredentialValidationInput) {
				input.ConnectorType = "other"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "wrong auth method",
			mutate: func(input *connector.CredentialValidationInput) {
				input.AuthMethodKey = "internal_integration_token"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "missing client id",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Config, "client_id")
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "missing client secret",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Config, "client_secret")
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "client id with basic delimiter",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Config["client_id"] = "notion:client"
			},
			code: connector.FailureConfigurationError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := notionValidationInput()
			test.mutate(&input)
			_, err := newCredentialValidators(
				server.Client,
			)[notionAuthMethod](t.Context(), input)
			assertNotionCredentialError(t, err, test.code)
		})
	}
}

func TestNotionCredentialValidatorFailureMatrix(t *testing.T) {
	tests := []struct {
		status int
		code   connector.FailureCode
	}{
		{http.StatusUnauthorized, connector.FailureAuthorizationFailed},
		{http.StatusForbidden, connector.FailurePermissionDenied},
		{http.StatusTooManyRequests, connector.FailureRateLimited},
		{http.StatusServiceUnavailable, connector.FailureUpstreamUnavailable},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := validatorServer(
				t,
				func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					writer.WriteHeader(test.status)
					_, _ = writer.Write([]byte(
						`{"error":"provider-response-secret ` +
							`notion-access-token-secret"}`,
					))
				},
			)
			_, err := newCredentialValidators(
				server.Client,
			)[notionAuthMethod](t.Context(), notionValidationInput())
			assertNotionCredentialError(t, err, test.code)
			assertNotionCredentialErrorRedacted(t, err)
		})
	}
}

func assertNotionCredentialError(
	t *testing.T,
	err error,
	code connector.FailureCode,
) {
	t.Helper()
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) || validationErr.Code != code {
		t.Fatalf("validation error = %#v (%v)", validationErr, err)
	}
}

func assertNotionCredentialErrorRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected validator error")
	}
	for _, secret := range []string{
		"provider-response-secret",
		"notion-access-token-secret",
		"notion-client-secret",
	} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validator error leaked %q: %q", secret, err.Error())
		}
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("validator error exposed internal cause: %v", err)
	}
}
