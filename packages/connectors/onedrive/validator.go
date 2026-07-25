package onedrive

import (
	"context"
	"net/http"
	"net/url"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	credentialProfileBaseURL = "https://graph.microsoft.com/"
	credentialProfilePath    = "/v1.0/me"
)

// NewCredentialValidators constructs OneDrive validators from the injected,
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
		Method: http.MethodGet,
		URL:    credentialProfilePath,
		Query: url.Values{
			"$select": {"id,displayName,mail,userPrincipalName"},
		},
		Authorizer: authorizer,
		Labels:     labels,
	})
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	var profile struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := response.DecodeJSON(&profile); err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	accountID, err := credentialvalidator.RequiredProviderString(profile.ID)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	displayName := credentialvalidator.OptionalProviderString(
		profile.DisplayName,
	)
	if displayName == "" {
		displayName = credentialvalidator.OptionalProviderString(profile.Mail)
	}
	if displayName == "" {
		displayName = credentialvalidator.OptionalProviderString(
			profile.UserPrincipalName,
		)
	}
	if displayName == "" {
		displayName = "Microsoft account"
	}

	// Microsoft Graph /me returns a stable user ID, but opaque Graph access
	// tokens have no supported scope-introspection endpoint.
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   accountID,
			DisplayName: displayName,
		},
		ScopesKnown: false,
	}, nil
}
