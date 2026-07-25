package gmail

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

func TestCredentialValidatorUsesGmailProfile(t *testing.T) {
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/gmail/v1/users/me/profile" {
				t.Errorf("path = %q", request.URL.Path)
			}
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Bearer gmail-token",
			)
			testkit.AssertNoHeader(t, request, "Referer")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"emailAddress":"person@example.com","messagesTotal":1}`,
			))
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	result, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validGmailOAuthInput(),
	)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Profile.AccountID != "" ||
		result.Profile.DisplayName != "person@example.com" {
		t.Fatalf("profile = %+v", result.Profile)
	}
	if result.ScopesKnown || result.GrantedScopes != nil {
		t.Fatalf(
			"Gmail endpoint cannot introspect all scopes: %+v",
			result,
		)
	}
}

func TestCredentialValidatorRejectsWrongSchemeBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	input := validGmailOAuthInput()
	input.TokenType = "bearer"
	_, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		input,
	)
	assertGmailCredentialError(
		t,
		err,
		connector.FailureAuthorizationFailed,
		0,
		false,
	)
	if calls.Load() != 0 {
		t.Fatal("non-normalized OAuth scheme reached Provider")
	}
}

func TestCredentialValidatorRejectsMalformedOrMissingProfile(t *testing.T) {
	for _, body := range []string{
		`{"emailAddress":`,
		`{"messagesTotal":1}`,
		`{"emailAddress":"\u0000"}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := testkit.NewServerWithOptions(
				t,
				http.HandlerFunc(func(
					writer http.ResponseWriter,
					_ *http.Request,
				) {
					writer.Header().Set(
						"Content-Type",
						"application/json",
					)
					_, _ = writer.Write([]byte(body))
				}),
				testkit.ServerOptions{Provider: string(Definition.Type)},
			)
			_, err := newCredentialValidators(server.Client)["oauth"](
				t.Context(),
				validGmailOAuthInput(),
			)
			var validationErr *connector.CredentialValidationError
			if !errors.As(err, &validationErr) ||
				validationErr.Code != connector.FailureInvalidResponse ||
				validationErr.Temporary {
				t.Fatalf("validation error = %+v", validationErr)
			}
		})
	}
}

func TestCredentialValidatorTimeoutIsTemporaryAndTyped(t *testing.T) {
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(
			_ http.ResponseWriter,
			_ *http.Request,
		) {
			t.Error("expired credential request reached handler")
		}),
		testkit.ServerOptions{
			Provider:       string(Definition.Type),
			RequestTimeout: time.Nanosecond,
		},
	)
	_, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validGmailOAuthInput(),
	)
	assertGmailCredentialError(
		t,
		err,
		connector.FailureTimeout,
		0,
		true,
	)
}

func validGmailOAuthInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   "oauth",
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "gmail-validator-test",
		AccessToken:     "gmail-token",
		TokenType:       "Bearer",
	}
}

func assertGmailCredentialError(
	t *testing.T,
	err error,
	code connector.FailureCode,
	status int,
	temporary bool,
) {
	t.Helper()
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if validationErr.Code != code ||
		validationErr.UpstreamStatus != status ||
		validationErr.Temporary != temporary {
		t.Fatalf("validation error = %+v", validationErr)
	}
}
