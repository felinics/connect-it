package gmail

import (
	"context"
	"net/http"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	credentialProfileBaseURL = "https://gmail.googleapis.com/"
	credentialProfilePath    = "/gmail/v1/users/me/profile"
)

// NewCredentialValidators constructs Gmail validators from the injected,
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
		"oauth": func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			token, err := credentialvalidator.OAuthInput(
				input,
				Definition.Type,
				"oauth",
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				token,
				credentialvalidator.RequestLabels(
					input,
					"credential_validate",
				),
			)
		},
	}
}

func validateCredential(
	ctx context.Context,
	client *providerkit.Client,
	token string,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	authorizer, err := credentialvalidator.Bearer(token)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	response, err := client.Do(ctx, providerkit.Request{
		Method:     http.MethodGet,
		URL:        credentialProfilePath,
		Authorizer: authorizer,
		Labels:     labels,
	})
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	var profile struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := response.DecodeJSON(&profile); err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	emailAddress, err := credentialvalidator.RequiredProviderString(
		profile.EmailAddress,
	)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}

	// Gmail getProfile exposes the mailbox address but no immutable Google
	// account ID. Keep AccountID empty rather than treating a renameable email
	// address as a stable identity. This endpoint also proves only that one of
	// its accepted Gmail scopes is present, not the complete grant set.
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			DisplayName: emailAddress,
		},
		ScopesKnown: false,
	}, nil
}
