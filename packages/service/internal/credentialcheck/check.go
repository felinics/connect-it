// Package credentialcheck owns the service-side credential validation
// boundary. Provider validators prove identity and permissions; this package
// normalizes the proof into the exact Connection snapshot that may become
// active.
package credentialcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/memohai/connect-it/packages/core/connector"
)

const (
	maxAccountIDBytes   = 512
	maxDisplayNameBytes = 2048
)

var ErrValidatorMissing = errors.New("credential validator is not configured")

// Snapshot is the non-secret part of a validated Connection credential.
type Snapshot struct {
	Profile     json.RawMessage
	Scopes      []string
	ScopesKnown bool
}

// Validate invokes the exact Provider/auth-method validator, safely combines
// its scope proof with protocol-side OAuth scopes, and rejects a credential
// that is known not to satisfy every Tool exposed by the Definition.
func Validate(
	ctx context.Context,
	validators connector.CredentialValidatorMap,
	matchers connector.ScopeMatcherMap,
	def connector.Definition,
	method connector.AuthMethod,
	input connector.CredentialValidationInput,
	protocolScopes []string,
	protocolScopesKnown bool,
) (Snapshot, error) {
	validator := validators[def.Type][method.Key]
	if validator == nil {
		return Snapshot{}, fmt.Errorf(
			"%w for connector %q auth method %q",
			ErrValidatorMissing,
			def.Type,
			method.Key,
		)
	}
	if input.ConnectorType != def.Type ||
		input.AuthMethodKey != method.Key ||
		input.AuthType != method.Type {
		return Snapshot{}, errors.New("credential validator input identity mismatch")
	}
	if err := validateInputShape(input); err != nil {
		return Snapshot{}, err
	}

	result, err := validator(ctx, input)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateProfile(result.Profile); err != nil {
		return Snapshot{}, credentialValidationFailure(
			connector.FailureInvalidResponse,
			"provider returned an invalid account profile",
			false,
		)
	}
	validatorScopes, err := connector.NormalizeScopes(result.GrantedScopes)
	if err != nil {
		return Snapshot{}, credentialValidationFailure(
			connector.FailureInvalidResponse,
			"provider returned invalid permission metadata",
			false,
		)
	}
	protocolScopes, err = connector.NormalizeScopes(protocolScopes)
	if err != nil {
		return Snapshot{}, errors.New("credentialcheck: invalid protocol scope")
	}

	matcher := connector.ExactScopeMatcher
	if byMethod := matchers[def.Type]; byMethod != nil && byMethod[method.Key] != nil {
		matcher = byMethod[method.Key]
	}
	scopes, scopesKnown := combineScopes(
		protocolScopes,
		protocolScopesKnown,
		validatorScopes,
		result.ScopesKnown,
		matcher,
	)
	if scopesKnown {
		for _, required := range requiredScopes(def) {
			if !matcher(scopes, required) {
				return Snapshot{}, credentialValidationFailure(
					connector.FailurePermissionDenied,
					"credential does not grant all permissions required by this connector",
					false,
				)
			}
		}
	}

	profile, err := marshalProfile(result.Profile)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Profile:     profile,
		Scopes:      scopes,
		ScopesKnown: scopesKnown,
	}, nil
}

func marshalProfile(profile connector.CredentialProfile) ([]byte, error) {
	value, err := json.Marshal(struct {
		AccountID   string `json:"account_id,omitempty"`
		DisplayName string `json:"display_name,omitempty"`
	}{
		AccountID:   profile.AccountID,
		DisplayName: profile.DisplayName,
	})
	if err != nil {
		return nil, fmt.Errorf("credentialcheck: marshal profile: %w", err)
	}
	return value, nil
}

func validateInputShape(input connector.CredentialValidationInput) error {
	if (input.ConnectionID == "") == (input.AuthorizationID == "") {
		return errors.New("credential validator input correlation mismatch")
	}
	switch input.AuthType {
	case connector.AuthAPIKey, connector.AuthCustomCredential:
		if len(input.Fields) == 0 ||
			input.AccessToken != "" ||
			input.TokenType != "" {
			return errors.New("credential validator input does not match fields auth")
		}
	case connector.AuthOAuth2:
		if len(input.Fields) != 0 ||
			input.AccessToken == "" ||
			input.TokenType == "" {
			return errors.New("credential validator input does not match oauth auth")
		}
	default:
		return errors.New("credential validator input has unsupported auth type")
	}
	return nil
}

func validateProfile(profile connector.CredentialProfile) error {
	if !validBoundedString(profile.AccountID, maxAccountIDBytes) ||
		!validBoundedString(profile.DisplayName, maxDisplayNameBytes) {
		return errors.New("invalid profile")
	}
	return nil
}

func validBoundedString(value string, max int) bool {
	return utf8.ValidString(value) &&
		len(value) <= max &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func combineScopes(
	protocol []string,
	protocolKnown bool,
	validator []string,
	validatorKnown bool,
	matcher connector.ScopeMatcher,
) ([]string, bool) {
	switch {
	case protocolKnown && validatorKnown:
		// Keep only protocol-side facts that the independent Provider
		// validation also proves. This can narrow but never expand the token
		// response.
		out := make([]string, 0, len(protocol))
		for _, scope := range protocol {
			if matcher(validator, scope) {
				out = append(out, scope)
			}
		}
		return out, true
	case protocolKnown:
		return append(make([]string, 0, len(protocol)), protocol...), true
	case validatorKnown:
		return append(make([]string, 0, len(validator)), validator...), true
	default:
		// PostgreSQL scopes is NOT NULL; scopes_known is the sole semantic
		// discriminator between an unknown set and a known-empty set.
		return []string{}, false
	}
}

func requiredScopes(def connector.Definition) []string {
	var values []string
	for _, tool := range def.Tools {
		values = append(values, tool.RequiredScopes...)
	}
	normalized, err := connector.NormalizeScopes(values)
	if err != nil {
		// Registry already validates Definition strings. Keep this helper pure
		// and fail closed if it is ever called with an unregistered value.
		return []string{"\x00invalid-definition-scope"}
	}
	return normalized
}

func credentialValidationFailure(
	code connector.FailureCode,
	message string,
	temporary bool,
) error {
	return &connector.CredentialValidationError{
		Code:        code,
		SafeMessage: message,
		Temporary:   temporary,
	}
}
