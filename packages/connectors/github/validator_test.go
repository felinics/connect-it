package github

import (
	"errors"
	"net/http"
	"reflect"
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

// jsonBodyServer replies with one fixed JSON body.
func jsonBodyServer(t *testing.T, body string) *testkit.Server {
	t.Helper()
	return validatorServer(t, func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(body))
	})
}

func TestCredentialValidatorsSuccessAndScopes(t *testing.T) {
	var calls atomic.Int32
	server := validatorServer(
		t,
		func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			if request.URL.Path != "/user" {
				t.Errorf("path = %q", request.URL.Path)
			}
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Bearer secret-token",
			)
			testkit.AssertHeader(
				t,
				request,
				"Accept",
				"application/vnd.github+json",
			)
			testkit.AssertHeader(
				t,
				request,
				"X-GitHub-Api-Version",
				restAPIVersion,
			)
			testkit.AssertNoHeader(t, request, "Referer")
			writer.Header().Set(
				"X-OAuth-Scopes",
				" repo, user, repo ",
			)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"id":12345678901234567890,"login":"octocat","name":"Octo Cat"}`,
			))
		},
	)
	validators := newCredentialValidators(server.Client)

	for _, test := range []struct {
		name  string
		key   string
		input connector.CredentialValidationInput
	}{
		{
			name: "oauth",
			key:  "oauth",
			input: connector.CredentialValidationInput{
				ConnectorType:   Definition.Type,
				AuthMethodKey:   "oauth",
				AuthType:        connector.AuthOAuth2,
				AuthorizationID: "github-validator-test",
				AccessToken:     "secret-token",
				TokenType:       "Bearer",
			},
		},
		{
			name: "pat by field name",
			key:  "pat",
			input: connector.CredentialValidationInput{
				ConnectorType:   Definition.Type,
				AuthMethodKey:   "pat",
				AuthType:        connector.AuthAPIKey,
				AuthorizationID: "github-validator-test",
				Fields:          map[string]string{"token": "secret-token"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := validators[test.key](t.Context(), test.input)
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if result.Profile.AccountID != "12345678901234567890" ||
				result.Profile.DisplayName != "Octo Cat" {
				t.Fatalf("profile = %+v", result.Profile)
			}
			if !result.ScopesKnown ||
				!reflect.DeepEqual(
					result.GrantedScopes,
					[]string{"repo", "user"},
				) {
				t.Fatalf(
					"scopes=%v known=%v",
					result.GrantedScopes,
					result.ScopesKnown,
				)
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if !ScopeMatcher([]string{"user"}, "read:user") ||
		ScopeMatcher([]string{"read:user"}, "repo") {
		t.Fatal("GitHub scope implication changed")
	}
}

func TestCredentialValidatorRejectsMissingPATFieldBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := validatorServer(t, func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	})
	_, err := newCredentialValidators(server.Client)["pat"](
		t.Context(),
		connector.CredentialValidationInput{
			ConnectorType:   Definition.Type,
			AuthMethodKey:   "pat",
			AuthType:        connector.AuthAPIKey,
			AuthorizationID: "github-validator-test",
			Fields:          map[string]string{},
		},
	)
	assertCredentialError(
		t,
		err,
		connector.FailureAuthorizationFailed,
		0,
	)
	if calls.Load() != 0 {
		t.Fatal("missing credential reached Provider")
	}
}

func TestCredentialValidatorMapsProviderFailuresSafely(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		code   connector.FailureCode
	}{
		{"unauthorized", http.StatusUnauthorized, connector.FailureAuthorizationFailed},
		{"forbidden", http.StatusForbidden, connector.FailurePermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			const secret = "raw-provider-secret"
			server := validatorServer(t, func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(secret))
			})
			_, err := newCredentialValidators(server.Client)["oauth"](
				t.Context(),
				validOAuthInput(),
			)
			assertCredentialError(t, err, test.code, test.status)
			if strings.Contains(err.Error(), secret) ||
				errors.Unwrap(err) != nil {
				t.Fatal("credential error retained Provider body/cause")
			}
		})
	}
}

func TestCredentialValidatorRejectsMalformedProfile(t *testing.T) {
	for _, body := range []string{
		`{"id":`,
		`{"login":"missing-id"}`,
		`{"id":"not-a-number","login":"octocat"}`,
		`{"id":"123","login":"quoted-number"}`,
		`{"id":-1,"login":"negative"}`,
		`{"id":1.5,"login":"fractional"}`,
		`{"id":1e3,"login":"exponent"}`,
		`{"id":18446744073709551616,"login":"too-large"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := jsonBodyServer(t, body)
			_, err := newCredentialValidators(server.Client)["oauth"](
				t.Context(),
				validOAuthInput(),
			)
			var validationErr *connector.CredentialValidationError
			if !errors.As(err, &validationErr) ||
				validationErr.Code != connector.FailureInvalidResponse {
				t.Fatalf("validation error = %+v", validationErr)
			}
		})
	}
}

func validOAuthInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   "oauth",
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "github-validator-test",
		AccessToken:     "secret-token",
		TokenType:       "Bearer",
	}
}

func assertCredentialError(
	t *testing.T,
	err error,
	code connector.FailureCode,
	status int,
) {
	t.Helper()
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if validationErr.Code != code ||
		validationErr.UpstreamStatus != status {
		t.Fatalf("validation error = %+v", validationErr)
	}
}
