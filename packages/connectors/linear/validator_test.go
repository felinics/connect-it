package linear

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

// validatorServer starts a guarded test client bound to this Provider identity.
func validatorServer(
	t *testing.T,
	handler http.HandlerFunc,
	maxResponseBytes int64,
) *testkit.Server {
	t.Helper()
	return testkit.NewServerWithOptions(t, handler, testkit.ServerOptions{
		Provider:         string(Definition.Type),
		MaxResponseBytes: maxResponseBytes,
	})
}

func fixedResponse(
	status int,
	body string,
	retryAfter string,
) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if retryAfter != "" {
			writer.Header().Set("Retry-After", retryAfter)
		}
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}
}

func apiKeyInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   linearAuthMethod,
		AuthType:        connector.AuthAPIKey,
		AuthorizationID: "linear-validator-test",
		Fields: map[string]string{
			linearAPIKeyField: "lin_api_validator-secret",
		},
	}
}

func oauthInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   linearOAuthMethod,
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "linear-validator-test",
		AccessToken:     "lin_oauth_validator-secret",
		TokenType:       "Bearer",
	}
}

const viewerResponse = `{
  "data": {
    "viewer": {
      "id": "user-42",
      "displayName": "Ada",
      "name": "Ada Lovelace"
    }
  }
}`

// The personal API key is the complete Authorization value for the GraphQL API;
// the Bearer form belongs to the Remote MCP binding, not to validation.
func TestLinearAPIKeyValidatorUsesMinimalViewerQueryAndRawAPIKey(t *testing.T) {
	var calls int
	server := validatorServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		calls++
		if request.Method != http.MethodPost ||
			request.URL.EscapedPath() != "/graphql" ||
			request.URL.RawQuery != "" {
			t.Errorf(
				"request = %s %s?%s",
				request.Method,
				request.URL.EscapedPath(),
				request.URL.RawQuery,
			)
		}
		testkit.AssertHeader(
			t,
			request,
			"Authorization",
			"lin_api_validator-secret",
		)
		testkit.AssertNoHeader(t, request, "Referer")
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}

		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode GraphQL request: %v", err)
		}
		if !slices.Equal(
			strings.Fields(payload.Query),
			strings.Fields(credentialProfileQuery),
		) {
			t.Errorf("validator query = %q", payload.Query)
		}
		if payload.Variables == nil || len(payload.Variables) != 0 {
			t.Errorf("variables = %#v, want empty object", payload.Variables)
		}
		for _, forbidden := range []string{"email", "avatarUrl", "admin"} {
			if strings.Contains(payload.Query, forbidden) {
				t.Errorf("validator query requested unnecessary %q", forbidden)
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(viewerResponse))
	}, 64<<10)

	result, err := newCredentialValidators(server.Client)[linearAuthMethod](
		t.Context(),
		apiKeyInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.AccountID != "user-42" ||
		result.Profile.DisplayName != "Ada" ||
		result.ScopesKnown ||
		result.GrantedScopes != nil {
		t.Fatalf("validation result = %+v", result)
	}
	if calls != 1 {
		t.Fatalf("requests = %d, want 1", calls)
	}
}

func TestLinearOAuthValidatorPresentsBearerToken(t *testing.T) {
	server := validatorServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		testkit.AssertHeader(
			t,
			request,
			"Authorization",
			"Bearer lin_oauth_validator-secret",
		)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(viewerResponse))
	}, 64<<10)

	result, err := newCredentialValidators(server.Client)[linearOAuthMethod](
		t.Context(),
		oauthInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.AccountID != "user-42" ||
		result.Profile.DisplayName != "Ada" ||
		result.ScopesKnown {
		t.Fatalf("validation result = %+v", result)
	}
}

func TestLinearCredentialDisplayNameFallback(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		displayName string
		fullName    string
		want        string
	}{
		{name: "display name", displayName: "Ada", fullName: "Ada Lovelace", want: "Ada"},
		{name: "name fallback", displayName: "", fullName: "Ada Lovelace", want: "Ada Lovelace"},
		{name: "invalid display name", displayName: "bad\nname", fullName: "Ada Lovelace", want: "Ada Lovelace"},
		{name: "stable provider fallback", want: "Linear user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := linearCredentialDisplayName(test.displayName, test.fullName)
			if got != test.want {
				t.Fatalf("display name = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLinearValidatorsRejectInvalidInputWithoutRequest(t *testing.T) {
	var calls int
	server := validatorServer(t, func(http.ResponseWriter, *http.Request) {
		calls++
		t.Error("invalid validator input reached Linear")
	}, 1024)

	tests := []struct {
		name   string
		method string
		mutate func(*connector.CredentialValidationInput)
		code   connector.FailureCode
	}{
		{
			name:   "missing API key",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Fields, linearAPIKeyField)
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "extra field",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields["base_url"] = "https://evil.example"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name:   "wrong connector",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.ConnectorType = "other"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name:   "wrong auth type",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.AuthType = connector.AuthOAuth2
			},
			code: connector.FailureConfigurationError,
		},
		{
			name:   "OAuth material on the API key path",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.AccessToken = "unexpected"
				input.TokenType = "Bearer"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name:   "leading whitespace",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[linearAPIKeyField] = " secret"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "unicode whitespace",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[linearAPIKeyField] = "secret value"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "control byte",
			method: linearAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[linearAPIKeyField] = "secret\nvalue"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "missing access token",
			method: linearOAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.AccessToken = ""
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "unsupported token type",
			method: linearOAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.TokenType = "mac"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "credential fields on the OAuth path",
			method: linearOAuthMethod,
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields = map[string]string{
					linearAPIKeyField: "unexpected",
				}
			},
			code: connector.FailureConfigurationError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := apiKeyInput()
			if test.method == linearOAuthMethod {
				input = oauthInput()
			}
			test.mutate(&input)
			_, err := newCredentialValidators(server.Client)[test.method](
				t.Context(),
				input,
			)
			assertCredentialError(t, err, test.code, 0)
		})
	}
	if calls != 0 {
		t.Fatalf("invalid validator inputs made %d requests", calls)
	}
}

func TestLinearValidatorRejectsMalformedOrMissingProfile(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		maxBytes int64
		code     connector.FailureCode
	}{
		{
			name:     "null data",
			body:     `{"data":null}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name:     "null viewer",
			body:     `{"data":{"viewer":null}}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name: "missing id",
			body: `{"data":{"viewer":{` +
				`"displayName":"Ada","name":"Ada Lovelace"}}}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name: "missing selected profile field",
			body: `{"data":{"viewer":{` +
				`"id":"user-42","displayName":"Ada"}}}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name: "wrong field type",
			body: `{"data":{"viewer":{` +
				`"id":42,"displayName":"Ada","name":"Ada Lovelace"}}}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name: "oversize",
			body: `{"data":{"viewer":{"id":"user-42",` +
				`"displayName":"` + strings.Repeat("x", 256) +
				`","name":"Ada"}}}`,
			maxBytes: 64,
			code:     connector.FailureResponseTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := validatorServer(
				t,
				fixedResponse(http.StatusOK, test.body, ""),
				test.maxBytes,
			)
			_, err := newCredentialValidators(server.Client)[linearAuthMethod](
				t.Context(),
				apiKeyInput(),
			)
			assertCredentialError(t, err, test.code, 0)
			assertErrorRedacted(t, err)
		})
	}
}

// Definitive HTTP statuses win over the GraphQL extensions; Linear's own
// authentication and rate-limit signals arrive as HTTP 400 with an errors array.
func TestLinearValidatorFailureMatrixAndRedaction(t *testing.T) {
	const (
		providerSecret = "provider-response-secret"
		apiKey         = "lin_api_validator-secret"
	)
	tests := []struct {
		name       string
		status     int
		body       string
		code       connector.FailureCode
		retryAfter string
		wantRetry  int
	}{
		{
			name:   "HTTP 401",
			status: http.StatusUnauthorized,
			body:   providerSecret + ":" + apiKey,
			code:   connector.FailureAuthorizationFailed,
		},
		{
			name:   "HTTP 403 outranks a conflicting extension",
			status: http.StatusForbidden,
			body: `{"errors":[{"message":"` + providerSecret +
				`","extensions":{"code":"RATELIMITED"}}]}`,
			code: connector.FailurePermissionDenied,
		},
		{
			name:       "HTTP 429",
			status:     http.StatusTooManyRequests,
			body:       providerSecret + ":" + apiKey,
			code:       connector.FailureRateLimited,
			retryAfter: "17",
			wantRetry:  17,
		},
		{
			name:   "HTTP 503",
			status: http.StatusServiceUnavailable,
			body:   providerSecret + ":" + apiKey,
			code:   connector.FailureUpstreamUnavailable,
		},
		{
			name:   "HTTP 200 GraphQL authentication",
			status: http.StatusOK,
			body: `{"data":{"viewer":{"id":"partial"}},` +
				`"errors":[{"message":"` + providerSecret + ":" + apiKey +
				`","path":["viewer"],"extensions":{` +
				`"code":"UNAUTHENTICATED","secret":"ignored"}}]}`,
			code: connector.FailureAuthorizationFailed,
		},
		{
			name:   "HTTP 200 GraphQL permission",
			status: http.StatusOK,
			body: `{"data":{"viewer":{"id":"partial"}},` +
				`"errors":[{"message":"` + providerSecret + ":" + apiKey +
				`","extensions":{"code":"FORBIDDEN"}}]}`,
			code: connector.FailurePermissionDenied,
		},
		{
			name:   "HTTP 200 generic GraphQL error",
			status: http.StatusOK,
			body: `{"errors":[{"message":"` + providerSecret + ":" +
				apiKey + `"}]}`,
			code: connector.FailureProviderError,
		},
		{
			name:   "HTTP 400 Linear rate extension code",
			status: http.StatusBadRequest,
			body: `{"errors":[{"message":"` + providerSecret + ":" +
				apiKey + `","extensions":{"code":"RATELIMITED"}}]}`,
			code: connector.FailureRateLimited,
			// Only the delay providerkit itself parsed from an audited
			// rate-limit response is ever republished; a Retry-After on a plain
			// 400 is not.
			retryAfter: "23",
			wantRetry:  0,
		},
		{
			name:   "HTTP 400 Linear authentication extension type",
			status: http.StatusBadRequest,
			body: `{"errors":[{"message":"` + providerSecret + ":" +
				apiKey + `","extensions":{"type":"authentication error"}}]}`,
			code: connector.FailureAuthorizationFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := validatorServer(
				t,
				fixedResponse(test.status, test.body, test.retryAfter),
				64<<10,
			)
			_, err := newCredentialValidators(server.Client)[linearAuthMethod](
				t.Context(),
				apiKeyInput(),
			)
			assertCredentialError(t, err, test.code, test.status)
			var validationErr *connector.CredentialValidationError
			if !errors.As(err, &validationErr) ||
				validationErr.RetryAfterSeconds != test.wantRetry {
				t.Fatalf(
					"RetryAfterSeconds = %+v, want %d",
					validationErr,
					test.wantRetry,
				)
			}
			assertErrorRedacted(t, err)
		})
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
	if !errors.As(err, &validationErr) ||
		validationErr.Code != code ||
		(status != 0 && validationErr.UpstreamStatus != status) {
		t.Fatalf("validation error = %#v (%v)", validationErr, err)
	}
}

func assertErrorRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected validator error")
	}
	for _, secret := range []string{
		"provider-response-secret",
		"lin_api_validator-secret",
		"UNAUTHENTICATED",
		"RATELIMITED",
	} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validator error leaked %q: %q", secret, err.Error())
		}
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("validator error exposed internal cause: %v", err)
	}
}
