package googleads

import (
	"errors"
	"net/http"
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

func TestCredentialValidatorUsesOfficialAccessibleCustomers(t *testing.T) {
	server := validatorServer(
		t,
		func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path !=
				"/v24/customers:listAccessibleCustomers" {
				t.Errorf("path = %q", request.URL.Path)
			}
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Bearer ads-access-token",
			)
			testkit.AssertHeader(
				t,
				request,
				"developer-token",
				"ads-developer-token",
			)
			testkit.AssertNoHeader(t, request, "Referer")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"resourceNames":["customers/1234567890","customers/9999999999"]}`,
			))
		},
	)
	result, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validGoogleAdsOAuthInput(),
	)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Profile.AccountID != "" ||
		result.Profile.DisplayName != "Google Ads connection" {
		t.Fatalf(
			"accessible resource was promoted to identity: %+v",
			result.Profile,
		)
	}
	if result.ScopesKnown || result.GrantedScopes != nil {
		t.Fatalf(
			"listAccessibleCustomers cannot introspect scopes: %+v",
			result,
		)
	}
}

func TestCredentialValidatorRequiresDeveloperTokenBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := validatorServer(t, func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	})
	input := validGoogleAdsOAuthInput()
	input.Config = map[string]any{
		"customer_id": "1234567890",
	}
	_, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		input,
	)
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != connector.FailureConfigurationError {
		t.Fatalf("validation error = %+v", validationErr)
	}
	if calls.Load() != 0 {
		t.Fatal("missing developer token reached Provider")
	}
}

func TestCredentialValidatorMapsAuthorizationFailureSafely(t *testing.T) {
	const secret = "google-ads-raw-secret"
	server := validatorServer(t, func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(secret))
	})
	_, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validGoogleAdsOAuthInput(),
	)
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != connector.FailureAuthorizationFailed ||
		validationErr.UpstreamStatus != http.StatusUnauthorized {
		t.Fatalf("validation error = %+v", validationErr)
	}
	if strings.Contains(err.Error(), secret) || errors.Unwrap(err) != nil {
		t.Fatal("validation error retained Provider body/cause")
	}
}

func TestCredentialValidatorRejectsMalformedCustomerList(t *testing.T) {
	for _, body := range []string{
		`{"resourceNames":`,
		`{}`,
		`{"resourceNames":["not-a-customer"]}`,
		`{"resourceNames":[1]}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := validatorServer(t, func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(body))
			})
			_, err := newCredentialValidators(server.Client)["oauth"](
				t.Context(),
				validGoogleAdsOAuthInput(),
			)
			var validationErr *connector.CredentialValidationError
			if !errors.As(err, &validationErr) ||
				validationErr.Code != connector.FailureInvalidResponse {
				t.Fatalf("validation error = %+v", validationErr)
			}
		})
	}
}

func validGoogleAdsOAuthInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   "oauth",
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "googleads-validator-test",
		Config: map[string]any{
			"developer_token": "ads-developer-token",
			"customer_id":     "1234567890",
		},
		AccessToken: "ads-access-token",
		TokenType:   "Bearer",
	}
}
