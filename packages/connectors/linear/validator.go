package linear

import (
	"context"
	"net/http"
	"strings"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	credentialProfileBaseURL = "https://api.linear.app/"
	credentialProfilePath    = "/graphql"
	// The smallest query that proves the credential and yields a stable
	// identity. Nothing beyond id/displayName/name is ever requested.
	credentialProfileQuery = `query ValidateCredential ` +
		`{ viewer { id displayName name } }`
	// Bounded defence against a hostile errors array.
	maxCredentialGraphQLErrors = 64
	maxGraphQLExtensionBytes   = 128
)

// NewCredentialValidators constructs Linear's OAuth and personal API-key
// validators from the injected, policy-bound Provider client factory. Tool
// execution never comes through here: it goes to the Remote MCP server.
func NewCredentialValidators(
	factory *providerkit.Factory,
) (map[string]connector.CredentialValidator, error) {
	client, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          credentialProfileBaseURL,
		AllowedOrigins:   []string{credentialProfileBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: 64 << 10,
		// Validation is a POST; never replay it automatically.
		Retry: providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		return nil, err
	}
	return newCredentialValidators(client), nil
}

func newCredentialValidators(
	client *providerkit.Client,
) map[string]connector.CredentialValidator {
	return map[string]connector.CredentialValidator{
		linearOAuthMethod: func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			token, err := credentialvalidator.OAuthInput(
				input,
				Definition.Type,
				linearOAuthMethod,
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			authorizer, err := credentialvalidator.Bearer(token)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				authorizer,
				credentialvalidator.RequestLabels(input, "credential_validate"),
			)
		},
		linearAuthMethod: func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			fields, err := credentialvalidator.FieldsInput(
				input,
				Definition.Type,
				linearAuthMethod,
				connector.AuthAPIKey,
				linearAPIKeyField,
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			authorizer, err := personalAPIKeyAuthorizer(fields[linearAPIKeyField])
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				authorizer,
				credentialvalidator.RequestLabels(input, "credential_validate"),
			)
		},
	}
}

// personalAPIKeyAuthorizer presents the key the way the Linear GraphQL API
// documents it: the key is the complete Authorization value and is never given
// a Bearer prefix, which would change the credential. The Remote MCP server
// takes the same key as a Bearer token, which is the binding declared on the
// MCP server, not here.
func personalAPIKeyAuthorizer(apiKey string) (providerkit.Authorizer, error) {
	if !credentialvalidator.OpaqueSecret(apiKey) {
		return nil,
			credentialvalidator.Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	authorizer, err := providerkit.HeaderAPIKey("Authorization", apiKey)
	if err != nil {
		return nil,
			credentialvalidator.Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	return authorizer, nil
}

type linearGraphQLError struct {
	Extensions struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"extensions"`
}

type linearViewerEnvelope struct {
	Data struct {
		Viewer *struct {
			ID          string  `json:"id"`
			DisplayName *string `json:"displayName"`
			Name        *string `json:"name"`
		} `json:"viewer"`
	} `json:"data"`
	Errors []linearGraphQLError `json:"errors"`
}

// validateCredential runs the fixed-origin viewer probe. A non-empty GraphQL
// errors array fails the whole operation, even on HTTP 200 with partial data,
// and Provider messages, paths and arbitrary extensions are never decoded.
//
// This request is written by hand rather than through credentialvalidator.Run:
// Linear reports authentication and rate limiting as HTTP 400 plus a GraphQL
// errors array, so the failure classification needs the body of a response that
// providerkit already rejected as an error status. Run projects only successful
// bodies, which is the first case its doc comment sends back here. Widening it
// to carry Linear's classification would move a Provider difference into the
// shared layer for a single caller.
func validateCredential(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	response, requestErr := client.Do(ctx, providerkit.Request{
		Method:  http.MethodPost,
		URL:     credentialProfilePath,
		Headers: http.Header{"Accept": {"application/json"}},
		JSON: map[string]any{
			"query":     credentialProfileQuery,
			"variables": map[string]any{},
		},
		Authorizer: authorizer,
		Labels:     labels,
	})
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	// Only HTTP 400 yields to the GraphQL errors array; every other error status
	// has a definitive meaning and must win over whatever the extensions claim.
	if requestErr != nil && status != http.StatusBadRequest {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(requestErr)
	}

	var envelope linearViewerEnvelope
	decoded := response != nil &&
		response.DecodeJSON(&envelope) == nil &&
		len(envelope.Errors) <= maxCredentialGraphQLErrors
	if decoded && len(envelope.Errors) > 0 {
		if code := graphQLFailureCode(envelope.Errors); code != "" {
			return connector.CredentialValidationResult{},
				credentialvalidator.Error(code, status, retryAfterSeconds(requestErr))
		}
	}
	if requestErr != nil {
		// A 400 carrying no recognizable GraphQL signal keeps providerkit's verdict.
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(requestErr)
	}
	if !decoded {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureInvalidResponse, status, 0)
	}
	if len(envelope.Errors) > 0 {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureProviderError, status, 0)
	}

	viewer := envelope.Data.Viewer
	if viewer == nil || viewer.DisplayName == nil || viewer.Name == nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureInvalidResponse, status, 0)
	}
	accountID, err := credentialvalidator.RequiredProviderString(viewer.ID)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   accountID,
			DisplayName: linearCredentialDisplayName(*viewer.DisplayName, *viewer.Name),
		},
		// Neither a personal API key nor a Linear access token exposes a
		// reliable scope snapshot on this endpoint.
		GrantedScopes: nil,
		ScopesKnown:   false,
	}, nil
}

// graphQLFailureCode maps the only extension values Linear documents for a
// failed credential. An empty code means "nothing recognized": the caller then
// keeps the HTTP-level classification.
func graphQLFailureCode(errors []linearGraphQLError) connector.FailureCode {
	code := connector.FailureCode("")
	for _, graphQLError := range errors {
		for _, marker := range []string{
			normalizeGraphQLMarker(graphQLError.Extensions.Code),
			normalizeGraphQLMarker(graphQLError.Extensions.Type),
		} {
			switch marker {
			case "RATELIMITED", "RATE_LIMITED":
				return connector.FailureRateLimited
			case "AUTHENTICATION_ERROR", "AUTHENTICATION ERROR",
				"UNAUTHENTICATED", "INVALID_API_KEY":
				code = connector.FailureAuthorizationFailed
			case "FORBIDDEN", "FORBIDDEN_ERROR", "PERMISSION_DENIED",
				"FEATURE NOT ACCESSIBLE":
				if code != connector.FailureAuthorizationFailed {
					code = connector.FailurePermissionDenied
				}
			}
		}
	}
	return code
}

// retryAfterSeconds republishes only the delay providerkit already parsed from
// the audited response.
func retryAfterSeconds(err error) int {
	failure := providerkit.AsToolFailure(err)
	if failure == nil {
		return 0
	}
	return failure.RetryAfterSeconds
}

func normalizeGraphQLMarker(value string) string {
	if len(value) > maxGraphQLExtensionBytes {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(value))
}

func linearCredentialDisplayName(displayName string, name string) string {
	for _, candidate := range []string{displayName, name} {
		value := credentialvalidator.OptionalProviderString(candidate)
		if value != "" {
			return value
		}
	}
	return "Linear user"
}
