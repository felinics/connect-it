package credentialvalidator

import (
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

func TestFieldsInputUsesNamesAndRequiresWholeSet(t *testing.T) {
	input := connector.CredentialValidationInput{
		ConnectorType:   "multi",
		AuthMethodKey:   "custom",
		AuthType:        connector.AuthCustomCredential,
		AuthorizationID: "fields-validator-test",
		Fields: map[string]string{
			"secret":  "s",
			"account": "a",
		},
	}
	fields, err := FieldsInput(
		input,
		"multi",
		"custom",
		connector.AuthCustomCredential,
		"account",
		"secret",
	)
	if err != nil {
		t.Fatalf("FieldsInput: %v", err)
	}
	if !reflect.DeepEqual(fields, input.Fields) {
		t.Fatalf("fields = %#v, want %#v", fields, input.Fields)
	}
	fields["account"] = "changed"
	if input.Fields["account"] != "a" {
		t.Fatal("FieldsInput returned caller-owned map")
	}

	for name, mutated := range map[string]connector.CredentialValidationInput{
		"missing": {
			ConnectorType:   "multi",
			AuthMethodKey:   "custom",
			AuthType:        connector.AuthCustomCredential,
			AuthorizationID: "fields-validator-test",
			Fields:          map[string]string{"account": "a"},
		},
		"extra": {
			ConnectorType:   "multi",
			AuthMethodKey:   "custom",
			AuthType:        connector.AuthCustomCredential,
			AuthorizationID: "fields-validator-test",
			Fields: map[string]string{
				"account": "a",
				"secret":  "s",
				"unknown": "x",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, gotErr := FieldsInput(
				mutated,
				"multi",
				"custom",
				connector.AuthCustomCredential,
				"secret",
				"account",
			)
			var validationErr *connector.CredentialValidationError
			if !errors.As(gotErr, &validationErr) {
				t.Fatalf("error = %T %v", gotErr, gotErr)
			}
		})
	}
}

func TestCredentialInputRequiresExactlyOneCorrelationID(t *testing.T) {
	fields := connector.CredentialValidationInput{
		ConnectorType:   "multi",
		AuthMethodKey:   "custom",
		AuthType:        connector.AuthCustomCredential,
		AuthorizationID: "authorization-id",
		Fields:          map[string]string{"secret": "s"},
	}
	oauth := connector.CredentialValidationInput{
		ConnectorType:   "gmail",
		AuthMethodKey:   "oauth",
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "authorization-id",
		AccessToken:     "token",
		TokenType:       "Bearer",
	}
	for _, mutate := range []func(*connector.CredentialValidationInput){
		func(input *connector.CredentialValidationInput) {
			input.AuthorizationID = ""
		},
		func(input *connector.CredentialValidationInput) {
			input.ConnectionID = "connection-id"
		},
	} {
		fieldsInput := fields
		mutate(&fieldsInput)
		if _, err := FieldsInput(
			fieldsInput,
			"multi",
			"custom",
			connector.AuthCustomCredential,
			"secret",
		); err == nil {
			t.Fatalf("fields input accepted invalid correlation: %+v", fieldsInput)
		}
		oauthInput := oauth
		mutate(&oauthInput)
		if _, err := OAuthInput(
			oauthInput,
			"gmail",
			"oauth",
		); err == nil {
			t.Fatalf("OAuth input accepted invalid correlation: %+v", oauthInput)
		}
	}
}

func TestRequestLabelsPreserveValidatedCorrelation(t *testing.T) {
	for name, input := range map[string]connector.CredentialValidationInput{
		"authorization": {
			ConnectorType:   "example",
			AuthorizationID: "authorization-id",
		},
		"connection": {
			ConnectorType: "example",
			ConnectionID:  "connection-id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			labels := RequestLabels(input, "credential_validate")
			if labels.ConnectorType != "example" ||
				labels.Operation != "credential_validate" ||
				labels.ToolID != "" ||
				labels.ConnectionID != input.ConnectionID ||
				labels.AuthorizationID != input.AuthorizationID {
				t.Fatalf("request labels = %+v", labels)
			}
		})
	}
}

func TestOAuthInputRequiresNormalizedBearerAndEmptyFields(t *testing.T) {
	valid := connector.CredentialValidationInput{
		ConnectorType:   "gmail",
		AuthMethodKey:   "oauth",
		AuthType:        connector.AuthOAuth2,
		AuthorizationID: "oauth-validator-test",
		AccessToken:     "token",
		TokenType:       "Bearer",
	}
	if token, err := OAuthInput(valid, "gmail", "oauth"); err != nil || token != "token" {
		t.Fatalf("OAuthInput token=%q err=%v", token, err)
	}

	for _, mutate := range []func(*connector.CredentialValidationInput){
		func(input *connector.CredentialValidationInput) { input.TokenType = "bearer" },
		func(input *connector.CredentialValidationInput) { input.TokenType = "" },
		func(input *connector.CredentialValidationInput) { input.AccessToken = "" },
		func(input *connector.CredentialValidationInput) {
			input.Fields = map[string]string{"token": "other"}
		},
	} {
		input := valid
		mutate(&input)
		if _, err := OAuthInput(input, "gmail", "oauth"); err == nil {
			t.Fatalf("invalid OAuth input accepted: %+v", input)
		}
	}
}

func TestProviderErrorIsStronglyTypedAndCauseFree(t *testing.T) {
	const secret = "provider-body-secret"
	providerErr, err := providerkit.NewError(
		connector.FailureUpstreamUnavailable,
		"provider is temporarily unavailable",
		providerkit.WithCause(errors.New(secret)),
		providerkit.WithUpstreamStatus(http.StatusBadGateway),
	)
	if err != nil {
		t.Fatal(err)
	}
	mapped := ProviderError(providerErr)
	var validationErr *connector.CredentialValidationError
	if !errors.As(mapped, &validationErr) {
		t.Fatalf("mapped error = %T", mapped)
	}
	if validationErr.Code != connector.FailureUpstreamUnavailable ||
		!validationErr.Temporary ||
		validationErr.UpstreamStatus != http.StatusBadGateway {
		t.Fatalf("mapped error = %+v", validationErr)
	}
	if errors.Unwrap(mapped) != nil || validationErr.Error() == secret {
		t.Fatal("validation error retained Provider cause")
	}
}

func TestProviderErrorClassificationMatrix(t *testing.T) {
	t.Parallel()

	const secret = "provider-body-secret"
	for _, test := range []struct {
		name       string
		code       connector.FailureCode
		status     int
		retryAfter int
		wantCode   connector.FailureCode
		temporary  bool
	}{
		{
			name:     "400 is not credential evidence",
			code:     connector.FailureProviderError,
			status:   http.StatusBadRequest,
			wantCode: connector.FailureProviderError,
		},
		{
			name:     "401 invalid credential",
			code:     connector.FailureAuthorizationFailed,
			status:   http.StatusUnauthorized,
			wantCode: connector.FailureAuthorizationFailed,
		},
		{
			name:     "403 permission denied",
			code:     connector.FailurePermissionDenied,
			status:   http.StatusForbidden,
			wantCode: connector.FailurePermissionDenied,
		},
		{
			name:       "429 rate limited",
			code:       connector.FailureRateLimited,
			status:     http.StatusTooManyRequests,
			retryAfter: 17,
			wantCode:   connector.FailureRateLimited,
			temporary:  true,
		},
		{
			name:      "503 upstream unavailable",
			code:      connector.FailureUpstreamUnavailable,
			status:    http.StatusServiceUnavailable,
			wantCode:  connector.FailureUpstreamUnavailable,
			temporary: true,
		},
		{
			name:      "provider timeout",
			code:      connector.FailureTimeout,
			wantCode:  connector.FailureTimeout,
			temporary: true,
		},
		{
			name:     "caller canceled",
			code:     connector.FailureCanceled,
			wantCode: connector.FailureCanceled,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			options := []providerkit.ErrorOption{
				providerkit.WithCause(errors.New(secret)),
			}
			if test.status != 0 {
				options = append(
					options,
					providerkit.WithUpstreamStatus(test.status),
				)
			}
			if test.retryAfter != 0 {
				options = append(
					options,
					providerkit.WithRetryAfterSeconds(test.retryAfter),
				)
			}
			providerErr, err := providerkit.NewError(
				test.code,
				"safe provider failure",
				options...,
			)
			if err != nil {
				t.Fatal(err)
			}
			mapped := ProviderError(providerErr)
			var validationErr *connector.CredentialValidationError
			if !errors.As(mapped, &validationErr) {
				t.Fatalf("mapped error = %T", mapped)
			}
			if validationErr.Code != test.wantCode ||
				validationErr.Temporary != test.temporary ||
				validationErr.UpstreamStatus != test.status ||
				validationErr.RetryAfterSeconds != test.retryAfter {
				t.Fatalf("mapped error = %+v", validationErr)
			}
			if errors.Unwrap(mapped) != nil ||
				validationErr.Error() == secret {
				t.Fatal("validation error retained Provider cause")
			}
		})
	}
}

func TestHeaderScopesDistinguishesUnknownAndNormalizes(t *testing.T) {
	if scopes, known := HeaderScopes(http.Header{}, "X-OAuth-Scopes", ","); known || scopes != nil {
		t.Fatalf("absent header scopes=%v known=%v", scopes, known)
	}
	header := http.Header{
		"X-Oauth-Scopes": {" repo, read:user ", "repo"},
	}
	scopes, known := HeaderScopes(header, "X-OAuth-Scopes", ",")
	if !known || !reflect.DeepEqual(scopes, []string{"read:user", "repo"}) {
		t.Fatalf("scopes=%v known=%v", scopes, known)
	}
}
