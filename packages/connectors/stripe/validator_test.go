package stripe

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

const stripeAccountFixture = `{
  "id":"acct_123",
  "object":"account",
  "email":"owner@example.com"
}`

func stripeValidatorServer(
	t *testing.T,
	handler http.HandlerFunc,
) *testkit.Server {
	t.Helper()
	return testkit.NewServerWithOptions(
		t,
		handler,
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
}

func stripeValidationInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   stripeAuthMethod,
		AuthType:        connector.AuthAPIKey,
		AuthorizationID: "stripe-validator-test",
		Fields: map[string]string{
			stripeAPIKeyField: "opaque_stripe_validator_key",
		},
	}
}

// The key is presented exactly as the Remote MCP server will receive it: a
// bearer token, never Basic auth or a query parameter.
func TestStripeCredentialValidatorPresentsBearerKeyAndStrictProfile(t *testing.T) {
	var calls atomic.Int32
	server := stripeValidatorServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		calls.Add(1)
		if request.Method != http.MethodGet ||
			request.URL.EscapedPath() != "/v1/account" ||
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
			"Bearer opaque_stripe_validator_key",
		)
		testkit.AssertHeader(t, request, "Accept", "application/json")
		testkit.AssertHeader(t, request, "Stripe-Version", stripeAPIVersion)
		testkit.AssertNoHeader(t, request, "Referer")
		if _, _, ok := request.BasicAuth(); ok {
			t.Error("Stripe key was sent as Basic auth")
		}
		_, _ = io.WriteString(writer, stripeAccountFixture)
	})

	result, err := newCredentialValidators(server.Client)[stripeAuthMethod](
		t.Context(),
		stripeValidationInput(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.AccountID != "acct_123" ||
		result.Profile.DisplayName != "owner@example.com" ||
		result.ScopesKnown ||
		result.GrantedScopes != nil {
		t.Fatalf("validation result = %+v", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("requests = %d, want 1", calls.Load())
	}
}

func TestStripeCredentialDisplayNameFallsBackToAccountID(t *testing.T) {
	for name, body := range map[string]string{
		"null email":    `{"id":"acct_123","object":"account","email":null}`,
		"blank email":   `{"id":"acct_123","object":"account","email":"  "}`,
		"control email": `{"id":"acct_123","object":"account","email":"bad\u0000email"}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := stripeValidatorServer(t, func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				_, _ = io.WriteString(writer, body)
			})
			result, err := newCredentialValidators(
				server.Client,
			)[stripeAuthMethod](t.Context(), stripeValidationInput())
			if err != nil {
				t.Fatal(err)
			}
			if result.Profile.DisplayName != "acct_123" {
				t.Fatalf("display name = %q", result.Profile.DisplayName)
			}
		})
	}
}

func TestStripeCredentialValidatorRequiresExactInputWithoutRequest(
	t *testing.T,
) {
	var calls atomic.Int32
	server := stripeValidatorServer(t, func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
		t.Error("invalid validator input reached Stripe")
	})
	tests := []struct {
		name   string
		mutate func(*connector.CredentialValidationInput)
		code   connector.FailureCode
	}{
		{
			name: "missing API key",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Fields, stripeAPIKeyField)
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "extra field",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields["base_url"] = "https://evil.example"
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
				input.AuthMethodKey = "oauth"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "wrong auth type",
			mutate: func(input *connector.CredentialValidationInput) {
				input.AuthType = connector.AuthOAuth2
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "OAuth material",
			mutate: func(input *connector.CredentialValidationInput) {
				input.AccessToken = "unexpected"
				input.TokenType = "Bearer"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "whitespace",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[stripeAPIKeyField] = "key value"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "colon",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[stripeAPIKeyField] = "key:value"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "control",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[stripeAPIKeyField] = "key\nvalue"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "oversize",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[stripeAPIKeyField] = strings.Repeat("x", 4097)
			},
			code: connector.FailureAuthorizationFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := stripeValidationInput()
			test.mutate(&input)
			_, err := newCredentialValidators(
				server.Client,
			)[stripeAuthMethod](t.Context(), input)
			assertStripeCredentialError(t, err, test.code, 0, 0)
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid validator inputs made %d requests", calls.Load())
	}
}

func TestStripeCredentialValidatorRejectsMalformedAccounts(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":         `{"object":`,
		"wrong object":      `{"id":"acct_123","object":"customer"}`,
		"missing object":    `{"id":"acct_123"}`,
		"missing ID":        `{"object":"account"}`,
		"null ID":           `{"id":null,"object":"account"}`,
		"empty ID":          `{"id":"","object":"account"}`,
		"control ID":        `{"id":"acct\u0000123","object":"account"}`,
		"wrong email type":  `{"id":"acct_123","object":"account","email":123}`,
		"wrong object type": `{"id":"acct_123","object":["account"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := stripeValidatorServer(t, func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				_, _ = io.WriteString(writer, body)
			})
			_, err := newCredentialValidators(
				server.Client,
			)[stripeAuthMethod](t.Context(), stripeValidationInput())
			assertStripeCredentialError(
				t,
				err,
				connector.FailureInvalidResponse,
				http.StatusOK,
				0,
			)
			assertStripeCredentialErrorRedacted(t, err)
		})
	}
}

func TestStripeCredentialValidatorFailureMatrix(t *testing.T) {
	tests := []struct {
		status     int
		code       connector.FailureCode
		retryAfter string
		wantRetry  int
	}{
		{401, connector.FailureAuthorizationFailed, "", 0},
		{403, connector.FailurePermissionDenied, "", 0},
		{429, connector.FailureRateLimited, "17", 17},
		{503, connector.FailureUpstreamUnavailable, "", 0},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := stripeValidatorServer(t, func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				if test.retryAfter != "" {
					writer.Header().Set("Retry-After", test.retryAfter)
				}
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(
					writer,
					`{"error":{"message":"provider-response-secret"}}`,
				)
			})
			_, err := newCredentialValidators(
				server.Client,
			)[stripeAuthMethod](t.Context(), stripeValidationInput())
			assertStripeCredentialError(
				t,
				err,
				test.code,
				test.status,
				test.wantRetry,
			)
			assertStripeCredentialErrorRedacted(t, err)
		})
	}
}

func assertStripeCredentialError(
	t *testing.T,
	err error,
	code connector.FailureCode,
	status int,
	retryAfter int,
) {
	t.Helper()
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != code ||
		validationErr.UpstreamStatus != status ||
		validationErr.RetryAfterSeconds != retryAfter {
		t.Fatalf("validation error = %#v (%v)", validationErr, err)
	}
}

func assertStripeCredentialErrorRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected validator error")
	}
	for _, secret := range []string{
		"provider-response-secret",
		"opaque_stripe_validator_key",
	} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("validator error leaked %q: %q", secret, err.Error())
		}
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("validator error exposed internal cause: %v", err)
	}
}
