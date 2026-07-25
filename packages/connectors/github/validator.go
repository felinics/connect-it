package github

import (
	"context"
	"net/http"
	"strconv"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	credentialProfileBaseURL = "https://api.github.com/"
	credentialProfilePath    = "/user"
)

// NewCredentialValidators constructs GitHub validators from the injected,
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
				true,
				credentialvalidator.RequestLabels(
					input,
					"credential_validate",
				),
			)
		},
		"pat": func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			fields, err := credentialvalidator.FieldsInput(
				input,
				Definition.Type,
				"pat",
				connector.AuthAPIKey,
				"token",
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				fields["token"],
				false,
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
	knownEmptyScopes bool,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	authorizer, err := credentialvalidator.Bearer(token)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	response, err := client.Do(ctx, providerkit.Request{
		Method: http.MethodGet,
		URL:    credentialProfilePath,
		Headers: http.Header{
			"Accept":               {"application/vnd.github+json"},
			"X-GitHub-Api-Version": {restAPIVersion},
		},
		Authorizer: authorizer,
		Labels:     labels,
	})
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}

	var profile struct {
		ID    uint64 `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := response.DecodeJSON(&profile); err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	accountID, err := githubAccountID(profile.ID)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	login := credentialvalidator.OptionalProviderString(profile.Login)
	displayName := credentialvalidator.OptionalProviderString(profile.Name)
	if displayName == "" {
		displayName = login
	}
	if displayName == "" {
		displayName = "GitHub user"
	}
	scopes, scopesKnown := credentialvalidator.HeaderScopes(
		response.Header,
		"X-OAuth-Scopes",
		",",
	)
	if scopesKnown && len(scopes) == 0 && !knownEmptyScopes {
		// Fine-grained PATs use a permission model rather than OAuth scopes.
		// GitHub may expose an empty scope header for such tokens, which cannot
		// prove a known-empty permission set.
		scopes = nil
		scopesKnown = false
	}
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   accountID,
			DisplayName: displayName,
		},
		GrantedScopes: scopes,
		ScopesKnown:   scopesKnown,
	}, nil
}

func githubAccountID(value uint64) (string, error) {
	if value == 0 {
		return "", credentialvalidator.Error(
			connector.FailureInvalidResponse,
			0,
			0,
		)
	}
	return strconv.FormatUint(value, 10), nil
}

// ScopeMatcher implements the only current GitHub scope implication needed by
// this Definition: the broader "user" scope includes "read:user".
func ScopeMatcher(granted []string, required string) bool {
	for _, scope := range granted {
		if scope == required ||
			(required == "read:user" && scope == "user") {
			return true
		}
	}
	return false
}
