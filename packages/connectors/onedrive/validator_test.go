package onedrive

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

func TestCredentialValidatorUsesMicrosoftGraphMe(t *testing.T) {
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/v1.0/me" {
				t.Errorf("path = %q", request.URL.Path)
			}
			testkit.AssertQuery(
				t,
				request,
				"$select",
				"id,displayName,mail,userPrincipalName",
			)
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Bearer graph-token",
			)
			testkit.AssertNoHeader(t, request, "Referer")
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{
				"id":"87d349ed-44d7-43e1-9a83-5f2406dee5bd",
				"displayName":"Adele Vance",
				"mail":"adele@example.com"
			}`))
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	result, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validOneDriveOAuthInput(),
	)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if result.Profile.AccountID !=
		"87d349ed-44d7-43e1-9a83-5f2406dee5bd" ||
		result.Profile.DisplayName != "Adele Vance" {
		t.Fatalf("profile = %+v", result.Profile)
	}
	if result.ScopesKnown || result.GrantedScopes != nil {
		t.Fatalf("Graph /me cannot introspect scopes: %+v", result)
	}
}

func TestCredentialValidatorMapsForbiddenWithoutBodyLeak(t *testing.T) {
	const secret = "graph-raw-error-secret"
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusForbidden)
			_, _ = writer.Write([]byte(secret))
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	_, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validOneDriveOAuthInput(),
	)
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != connector.FailurePermissionDenied ||
		validationErr.UpstreamStatus != http.StatusForbidden {
		t.Fatalf("validation error = %+v", validationErr)
	}
	if strings.Contains(err.Error(), secret) || errors.Unwrap(err) != nil {
		t.Fatal("validation error retained Provider body/cause")
	}
}

func TestCredentialValidatorRequiresStableUserID(t *testing.T) {
	for _, body := range []string{
		`{"id":`,
		`{"displayName":"Missing ID"}`,
		`{"id":"\u0000","displayName":"Bad ID"}`,
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
				validOneDriveOAuthInput(),
			)
			var validationErr *connector.CredentialValidationError
			if !errors.As(err, &validationErr) ||
				validationErr.Code != connector.FailureInvalidResponse {
				t.Fatalf("validation error = %+v", validationErr)
			}
		})
	}
}

func TestCredentialValidatorRejectsInvalidUTF8ProfileID(t *testing.T) {
	body := append([]byte(`{"id":"`), byte(0xff))
	body = append(body, []byte(`","displayName":"Invalid UTF-8"}`)...)
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(body)
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)

	result, err := newCredentialValidators(server.Client)["oauth"](
		t.Context(),
		validOneDriveOAuthInput(),
	)
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != connector.FailureInvalidResponse {
		t.Fatalf("validation error = %+v", validationErr)
	}
	if result.Profile.AccountID != "" {
		t.Fatalf("非法 UTF-8 不得产出 AccountID: %+v", result.Profile)
	}
}

func validOneDriveOAuthInput() connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   "oauth",
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "onedrive-validator-test",
		AccessToken:     "graph-token",
		TokenType:       "Bearer",
	}
}
