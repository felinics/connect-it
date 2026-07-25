package stripe

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	// Credential validation is the only egress this package performs itself;
	// Tool traffic goes to the Remote MCP server through the engine.
	credentialProfileBaseURL = "https://api.stripe.com/"
	credentialProfilePath    = "/v1/account"
	// Pinned so the validated response shape cannot drift under us.
	stripeAPIVersion = "2026-06-24.dahlia"
)

// NewCredentialValidators constructs the Stripe validator from the injected,
// policy-bound Provider client factory.
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
		Retry:            providerkit.RetryPolicy{Disabled: true},
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
		stripeAuthMethod: func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			fields, err := credentialvalidator.FieldsInput(
				input,
				Definition.Type,
				stripeAuthMethod,
				connector.AuthAPIKey,
				stripeAPIKeyField,
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				fields[stripeAPIKeyField],
				credentialvalidator.RequestLabels(input, "credential_validate"),
			)
		},
	}
}

type stripeAccount struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Email  string `json:"email"`
}

// errStripeUnusableAccount is projected to FailureInvalidResponse carrying the
// upstream status; it never reaches a caller.
var errStripeUnusableAccount = errors.New("stripe: unusable account response")

// validateCredential presents the key exactly the way the Remote MCP server
// will — as a bearer token — so a key that passes here is a key that can
// actually authenticate a Tool call.
func validateCredential(
	ctx context.Context,
	client *providerkit.Client,
	apiKey string,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	if !validStripeAPIKey(apiKey) {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	authorizer, err := credentialvalidator.Bearer(apiKey)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	return credentialvalidator.Run(
		ctx,
		client,
		credentialvalidator.Probe[stripeAccount]{
			Method: http.MethodGet,
			Path:   credentialProfilePath,
			Headers: http.Header{
				"Accept":         {"application/json"},
				"Stripe-Version": {stripeAPIVersion},
			},
			Authorizer: authorizer,
			Project:    projectStripeAccount,
		},
		labels,
	)
}

func projectStripeAccount(
	account stripeAccount,
) (connector.CredentialValidationResult, error) {
	accountID := credentialvalidator.OptionalProviderString(account.ID)
	if account.Object != "account" || accountID == "" {
		return connector.CredentialValidationResult{}, errStripeUnusableAccount
	}
	displayName := credentialvalidator.OptionalProviderString(account.Email)
	if displayName == "" {
		displayName = accountID
	}
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   accountID,
			DisplayName: displayName,
		},
		GrantedScopes: nil,
		// A Stripe key exposes no reliable, complete permission introspection
		// through /v1/account, so the granted set is never claimed as known.
		ScopesKnown: false,
	}, nil
}

// validStripeAPIKey keeps the key opaque: no prefix or alphabet is assumed,
// only that it is a bounded, single-line secret usable in a header. The colon
// rejection is inherited from Stripe's Basic-auth form of the same key, where
// a colon would be a field delimiter.
func validStripeAPIKey(value string) bool {
	return credentialvalidator.OpaqueSecret(value) &&
		!strings.Contains(value, ":")
}
