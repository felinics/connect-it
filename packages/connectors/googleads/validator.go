package googleads

import (
	"context"
	"net/http"
	"strings"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	credentialValidationBaseURL = "https://googleads.googleapis.com/"
	credentialValidationPath    = "/v24/customers:listAccessibleCustomers"
)

// NewCredentialValidators constructs Google Ads validators from the injected,
// policy-bound Provider client factory.
func NewCredentialValidators(
	factory *providerkit.Factory,
) (map[string]connector.CredentialValidator, error) {
	client, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          credentialValidationBaseURL,
		AllowedOrigins:   []string{credentialValidationBaseURL},
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
			developerToken, err := credentialvalidator.RequiredConfigString(
				input.Config,
				"developer_token",
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				token,
				developerToken,
				credentialvalidator.RequestLabels(
					input,
					"credential_validate",
				),
			)
		},
	}
}

type googleAdsAccessibleCustomers struct {
	ResourceNames *[]string `json:"resourceNames"`
}

func validateCredential(
	ctx context.Context,
	client *providerkit.Client,
	token string,
	developerToken string,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	bearer, err := credentialvalidator.Bearer(token)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	developerTokenHeader, err := providerkit.HeaderAPIKey(
		"developer-token",
		developerToken,
	)
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(
				connector.FailureConfigurationError,
				0,
				0,
			)
	}
	return credentialvalidator.Run(
		ctx,
		client,
		credentialvalidator.Probe[googleAdsAccessibleCustomers]{
			Method: http.MethodGet,
			Path:   credentialValidationPath,
			// Google Ads authenticates caller and API client separately.
			Authorizer: combinedAuthorizer{
				bearer,
				developerTokenHeader,
			},
			Project: projectGoogleAdsAccessibleCustomers,
		},
		labels,
	)
}

func projectGoogleAdsAccessibleCustomers(
	payload googleAdsAccessibleCustomers,
) (connector.CredentialValidationResult, error) {
	if payload.ResourceNames == nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(
				connector.FailureInvalidResponse,
				0,
				0,
			)
	}
	for _, resourceName := range *payload.ResourceNames {
		if !validCustomerResourceName(resourceName) {
			return connector.CredentialValidationResult{},
				credentialvalidator.Error(
					connector.FailureInvalidResponse,
					0,
					0,
				)
		}
	}

	// listAccessibleCustomers is the official light-weight read that validates
	// both the OAuth token and developer token. It returns resources, not the
	// authenticated user's stable identity or the token grant set. In
	// particular, never promote the first resource name to Profile.AccountID.
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			DisplayName: "Google Ads connection",
		},
		ScopesKnown: false,
	}, nil
}

func validCustomerResourceName(value string) bool {
	const prefix = "customers/"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return false
	}
	for _, r := range value[len(prefix):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type combinedAuthorizer []providerkit.Authorizer

func (authorizers combinedAuthorizer) Apply(request *http.Request) error {
	for _, authorizer := range authorizers {
		if authorizer == nil {
			return providerkit.ErrInvalidAuthorizerConfig
		}
		if err := authorizer.Apply(request); err != nil {
			return err
		}
	}
	return nil
}

func (authorizers combinedAuthorizer) Footprint() providerkit.CredentialFootprint {
	var footprint providerkit.CredentialFootprint
	for _, authorizer := range authorizers {
		if authorizer == nil {
			continue
		}
		current := authorizer.Footprint()
		footprint.HeaderNames = append(
			footprint.HeaderNames,
			current.HeaderNames...,
		)
		footprint.QueryParamNames = append(
			footprint.QueryParamNames,
			current.QueryParamNames...,
		)
		footprint.Body = footprint.Body || current.Body
	}
	return footprint
}
