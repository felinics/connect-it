package credentialcheck

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestValidateSafeScopeIntersectionAndProfile(t *testing.T) {
	def, method := testDefinition()
	validators := connector.CredentialValidatorMap{
		def.Type: {
			method.Key: func(
				context.Context,
				connector.CredentialValidationInput,
			) (connector.CredentialValidationResult, error) {
				return connector.CredentialValidationResult{
					Profile: connector.CredentialProfile{
						AccountID:   "acct_1",
						DisplayName: "Example",
					},
					GrantedScopes: []string{"write", "read", "read"},
					ScopesKnown:   true,
				}, nil
			},
		},
	}

	got, err := Validate(
		context.Background(),
		validators,
		nil,
		def,
		method,
		oauthInput(def, method),
		[]string{"admin", "read", "write"},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"read", "write"}; !reflect.DeepEqual(got.Scopes, want) {
		t.Fatalf("scopes = %v, want %v", got.Scopes, want)
	}
	if !got.ScopesKnown {
		t.Fatal("scope intersection must remain known")
	}
	if string(got.Profile) != `{"account_id":"acct_1","display_name":"Example"}` {
		t.Fatalf("profile = %s", got.Profile)
	}
}

func TestValidateScopeSourcesAndProviderMatcher(t *testing.T) {
	def, method := testDefinition()
	def.Tools = []connector.Tool{
		{ID: "read", RequiredScopes: []string{"read:account"}},
	}
	validator := func(
		context.Context,
		connector.CredentialValidationInput,
	) (connector.CredentialValidationResult, error) {
		return connector.CredentialValidationResult{
			GrantedScopes: []string{"account"},
			ScopesKnown:   true,
		}, nil
	}
	matchers := connector.ScopeMatcherMap{
		def.Type: {
			method.Key: func(granted []string, required string) bool {
				for _, scope := range granted {
					if scope == required ||
						(scope == "account" && required == "read:account") {
						return true
					}
				}
				return false
			},
		},
	}

	got, err := Validate(
		context.Background(),
		connector.CredentialValidatorMap{
			def.Type: {method.Key: validator},
		},
		matchers,
		def,
		method,
		oauthInput(def, method),
		[]string{"read:account"},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"read:account"}; !reflect.DeepEqual(got.Scopes, want) {
		t.Fatalf("scopes = %v, want %v", got.Scopes, want)
	}
}

func TestValidateRejectsKnownMissingPermission(t *testing.T) {
	def, method := testDefinition()
	_, err := Validate(
		context.Background(),
		connector.CredentialValidatorMap{
			def.Type: {
				method.Key: func(
					context.Context,
					connector.CredentialValidationInput,
				) (connector.CredentialValidationResult, error) {
					return connector.CredentialValidationResult{
						GrantedScopes: []string{"read"},
						ScopesKnown:   true,
					}, nil
				},
			},
		},
		nil,
		def,
		method,
		oauthInput(def, method),
		[]string{"read", "write"},
		true,
	)
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != connector.FailurePermissionDenied {
		t.Fatalf("error = %#v, want permission_denied", err)
	}
}

func TestValidateUnknownScopesDoNotInventFacts(t *testing.T) {
	def, method := testDefinition()
	got, err := Validate(
		context.Background(),
		connector.CredentialValidatorMap{
			def.Type: {
				method.Key: func(
					context.Context,
					connector.CredentialValidationInput,
				) (connector.CredentialValidationResult, error) {
					return connector.CredentialValidationResult{}, nil
				},
			},
		},
		nil,
		def,
		method,
		oauthInput(def, method),
		nil,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.ScopesKnown || got.Scopes == nil || len(got.Scopes) != 0 {
		t.Fatalf("unknown scopes changed to %#v, known=%v", got.Scopes, got.ScopesKnown)
	}
}

func TestValidateKnownEmptyScopesRemainNonNilForPersistence(t *testing.T) {
	def, method := testDefinition()
	def.Tools = nil
	got, err := Validate(
		context.Background(),
		connector.CredentialValidatorMap{
			def.Type: {
				method.Key: func(
					context.Context,
					connector.CredentialValidationInput,
				) (connector.CredentialValidationResult, error) {
					return connector.CredentialValidationResult{}, nil
				},
			},
		},
		nil,
		def,
		method,
		oauthInput(def, method),
		[]string{},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ScopesKnown || got.Scopes == nil || len(got.Scopes) != 0 {
		t.Fatalf("known-empty scopes = %#v, known=%v", got.Scopes, got.ScopesKnown)
	}
}

func TestValidateFailsClosedForMissingValidatorAndBadOutput(t *testing.T) {
	def, method := testDefinition()
	_, err := Validate(
		context.Background(),
		nil,
		nil,
		def,
		method,
		oauthInput(def, method),
		nil,
		false,
	)
	if !errors.Is(err, ErrValidatorMissing) {
		t.Fatalf("missing validator error = %v", err)
	}

	_, err = Validate(
		context.Background(),
		connector.CredentialValidatorMap{
			def.Type: {
				method.Key: func(
					context.Context,
					connector.CredentialValidationInput,
				) (connector.CredentialValidationResult, error) {
					return connector.CredentialValidationResult{
						Profile: connector.CredentialProfile{
							DisplayName: string([]byte{0xff}),
						},
					}, nil
				},
			},
		},
		nil,
		def,
		method,
		oauthInput(def, method),
		nil,
		false,
	)
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != connector.FailureInvalidResponse {
		t.Fatalf("bad output error = %#v", err)
	}
}

func TestValidateInputShapeRequiresExactlyOneCorrelationID(t *testing.T) {
	def, method := testDefinition()
	valid := oauthInput(def, method)
	if err := validateInputShape(valid); err != nil {
		t.Fatalf("valid authorization correlation: %v", err)
	}

	connection := valid
	connection.AuthorizationID = ""
	connection.ConnectionID = "connection-id"
	if err := validateInputShape(connection); err != nil {
		t.Fatalf("valid connection correlation: %v", err)
	}

	for _, input := range []connector.CredentialValidationInput{
		func() connector.CredentialValidationInput {
			value := valid
			value.AuthorizationID = ""
			return value
		}(),
		func() connector.CredentialValidationInput {
			value := valid
			value.ConnectionID = "connection-id"
			return value
		}(),
	} {
		if err := validateInputShape(input); err == nil {
			t.Fatalf("invalid correlation accepted: %+v", input)
		}
	}
}

func testDefinition() (connector.Definition, connector.AuthMethod) {
	method := connector.AuthMethod{
		Key:  "oauth",
		Type: connector.AuthOAuth2,
	}
	return connector.Definition{
		Type:        "example",
		AuthMethods: []connector.AuthMethod{method},
		Tools: []connector.Tool{
			{ID: "read", RequiredScopes: []string{"read"}},
			{ID: "write", RequiredScopes: []string{"write"}},
		},
	}, method
}

func oauthInput(
	def connector.Definition,
	method connector.AuthMethod,
) connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   def.Type,
		AuthMethodKey:   method.Key,
		AuthType:        method.Type,
		AuthorizationID: "credentialcheck-test",
		AccessToken:     "token",
		TokenType:       "Bearer",
	}
}
