// Package credentialvalidator contains the fail-closed input and error
// normalization shared by concrete Provider credential validators.
package credentialvalidator

import (
	"net/http"
	"sort"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const normalizedBearerTokenType = "Bearer"

// OAuthInput checks the connector/auth identity and the OAuth-only input
// shape. TokenType must already have been normalized by the OAuth boundary.
func OAuthInput(
	input connector.CredentialValidationInput,
	connectorType connector.Type,
	authMethodKey string,
) (string, error) {
	if input.ConnectorType != connectorType ||
		input.AuthMethodKey != authMethodKey ||
		input.AuthType != connector.AuthOAuth2 ||
		len(input.Fields) != 0 ||
		(input.ConnectionID == "") == (input.AuthorizationID == "") {
		return "", Error(connector.FailureConfigurationError, 0, 0)
	}
	if input.AccessToken == "" ||
		input.TokenType != normalizedBearerTokenType {
		return "", Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	return input.AccessToken, nil
}

// FieldsInput checks the API-key/custom-credential-only input shape and
// returns a defensive map containing the exact required field set. Field
// lookup is by key, never declaration or map iteration order.
func FieldsInput(
	input connector.CredentialValidationInput,
	connectorType connector.Type,
	authMethodKey string,
	authType connector.AuthMethodType,
	requiredFields ...string,
) (map[string]string, error) {
	if input.ConnectorType != connectorType ||
		input.AuthMethodKey != authMethodKey ||
		input.AuthType != authType ||
		(authType != connector.AuthAPIKey &&
			authType != connector.AuthCustomCredential) ||
		input.AccessToken != "" ||
		input.TokenType != "" ||
		(input.ConnectionID == "") == (input.AuthorizationID == "") {
		return nil, Error(connector.FailureConfigurationError, 0, 0)
	}

	expected := make(map[string]struct{}, len(requiredFields))
	for _, key := range requiredFields {
		if key == "" {
			return nil, Error(connector.FailureConfigurationError, 0, 0)
		}
		if _, duplicate := expected[key]; duplicate {
			return nil, Error(connector.FailureConfigurationError, 0, 0)
		}
		expected[key] = struct{}{}
	}
	if len(input.Fields) != len(expected) {
		for key := range input.Fields {
			if _, known := expected[key]; !known {
				return nil, Error(connector.FailureConfigurationError, 0, 0)
			}
		}
		return nil, Error(connector.FailureAuthorizationFailed, 0, 0)
	}

	fields := make(map[string]string, len(expected))
	for key := range expected {
		value, ok := input.Fields[key]
		if !ok || value == "" {
			return nil, Error(connector.FailureAuthorizationFailed, 0, 0)
		}
		fields[key] = value
	}
	for key := range input.Fields {
		if _, known := expected[key]; !known {
			return nil, Error(connector.FailureConfigurationError, 0, 0)
		}
	}
	return fields, nil
}

// RequestLabels threads the already-validated credential correlation into a
// Provider request without exposing credential material.
func RequestLabels(
	input connector.CredentialValidationInput,
	operation string,
) providerkit.RequestLabels {
	return providerkit.RequestLabels{
		ConnectorType:   string(input.ConnectorType),
		Operation:       operation,
		ConnectionID:    input.ConnectionID,
		AuthorizationID: input.AuthorizationID,
	}
}

// Bearer constructs the only OAuth scheme accepted in the first phase.
func Bearer(token string) (providerkit.Authorizer, error) {
	if strings.TrimSpace(token) != token ||
		strings.ContainsAny(token, " \t\r\n") {
		return nil, Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	authorizer, err := providerkit.Bearer(token)
	if err != nil {
		return nil, Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	return authorizer, nil
}

// RequiredConfigString reads a Provider configuration value by its explicit
// key. It never falls back to map order or a Tool argument.
func RequiredConfigString(config map[string]any, key string) (string, error) {
	value, ok := config[key].(string)
	if !ok || value == "" {
		return "", Error(connector.FailureConfigurationError, 0, 0)
	}
	return value, nil
}

// ProviderError discards Provider bodies and internal causes while preserving
// only the stable failure classification and bounded metadata.
func ProviderError(err error) error {
	if err == nil {
		return nil
	}
	failure := providerkit.AsToolFailure(err)
	code := failure.Code
	switch failure.UpstreamStatus {
	case http.StatusUnauthorized:
		code = connector.FailureAuthorizationFailed
	case http.StatusForbidden:
		code = connector.FailurePermissionDenied
	}
	return Error(
		code,
		failure.UpstreamStatus,
		failure.RetryAfterSeconds,
	)
}

// Error creates the public, cause-free validator error representation.
func Error(
	code connector.FailureCode,
	upstreamStatus int,
	retryAfterSeconds int,
) *connector.CredentialValidationError {
	if !code.Valid() {
		code = connector.FailureInternalError
	}
	if upstreamStatus < 100 || upstreamStatus > 599 {
		upstreamStatus = 0
	}
	if retryAfterSeconds < 0 {
		retryAfterSeconds = 0
	}
	return &connector.CredentialValidationError{
		Code:              code,
		SafeMessage:       safeMessage(code),
		Temporary:         temporary(code),
		RetryAfterSeconds: retryAfterSeconds,
		UpstreamStatus:    upstreamStatus,
	}
}

// HeaderScopes distinguishes an absent scope header (unknown) from a present
// but empty header (known empty), then trims, de-duplicates, and sorts scopes
// without changing their case.
func HeaderScopes(
	header http.Header,
	name string,
	separator string,
) ([]string, bool) {
	var (
		values  []string
		present bool
	)
	for key, candidates := range header {
		if strings.EqualFold(key, name) {
			present = true
			values = append(values, candidates...)
		}
	}
	if !present {
		return nil, false
	}

	seen := make(map[string]struct{})
	for _, value := range values {
		for _, item := range strings.Split(value, separator) {
			item = strings.TrimSpace(item)
			if item != "" {
				seen[item] = struct{}{}
			}
		}
	}
	scopes := make([]string, 0, len(seen))
	for scope := range seen {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	return scopes, true
}

// RequiredProviderString validates a required string in a successful Provider
// response without ever echoing its value through an error.
func RequiredProviderString(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || containsControl(value) {
		return "", Error(connector.FailureInvalidResponse, 0, 0)
	}
	return value, nil
}

// OptionalProviderString returns a bounded, trimmed display value or empty.
func OptionalProviderString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 || containsControl(value) {
		return ""
	}
	return value
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func temporary(code connector.FailureCode) bool {
	switch code {
	case connector.FailureRateLimited,
		connector.FailureUpstreamUnavailable,
		connector.FailureTimeout:
		return true
	default:
		return false
	}
}

func safeMessage(code connector.FailureCode) string {
	switch code {
	case connector.FailureAuthorizationFailed:
		return "provider credential is invalid"
	case connector.FailurePermissionDenied:
		return "provider credential lacks required permission"
	case connector.FailureRateLimited:
		return "provider credential validation was rate limited"
	case connector.FailureUpstreamUnavailable:
		return "provider is temporarily unavailable"
	case connector.FailureTimeout:
		return "provider credential validation timed out"
	case connector.FailureCanceled:
		return "credential validation was canceled"
	case connector.FailureInvalidResponse:
		return "provider returned an invalid credential profile"
	case connector.FailureResponseTooLarge:
		return "provider credential response is too large"
	case connector.FailureConfigurationError:
		return "credential validator input is invalid"
	case connector.FailurePolicyDenied:
		return "provider credential validation was denied by policy"
	default:
		return "credential validation failed"
	}
}
