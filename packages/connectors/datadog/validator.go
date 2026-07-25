package datadog

import (
	"context"
	"net/http"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

func NewCredentialValidators(
	factory *providerkit.Factory,
) (map[string]connector.CredentialValidator, error) {
	source, err := newDatadogClientSource(factory)
	if err != nil {
		return nil, err
	}
	return newCredentialValidators(source), nil
}

func newCredentialValidators(
	source *datadogClientSource,
) map[string]connector.CredentialValidator {
	return map[string]connector.CredentialValidator{
		datadogAuthMethod: func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			fields, err := credentialvalidator.FieldsInput(
				input,
				Definition.Type,
				datadogAuthMethod,
				connector.AuthCustomCredential,
				datadogAPIKeyField,
				datadogApplicationKeyField,
				datadogSiteField,
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			credentials, code := normalizeDatadogCredentials(fields)
			if code != "" {
				return connector.CredentialValidationResult{},
					credentialvalidator.Error(code, 0, 0)
			}
			return validateCredential(
				ctx,
				source,
				credentials,
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
	source *datadogClientSource,
	credentials datadogCredentials,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	authorizer, code := credentials.authorizer()
	if code != "" {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(code, 0, 0)
	}
	lease, failure := source.clientFor(credentials)
	if failure != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(failure.Code, 0, 0)
	}
	defer lease.Close()
	response, err := lease.Client.Do(ctx, providerkit.Request{
		Method:     http.MethodGet,
		URL:        "api/v2/validate_keys",
		Authorizer: authorizer,
		Headers: http.Header{
			"Accept": {"application/json"},
		},
		Labels: labels,
	})
	if err != nil {
		failure := providerkit.AsToolFailure(err)
		if failure.UpstreamStatus == http.StatusUnauthorized ||
			failure.UpstreamStatus == http.StatusForbidden {
			return connector.CredentialValidationResult{},
				&connector.CredentialValidationError{
					Code: connector.FailureAuthorizationFailed,
					SafeMessage: "provider authorization failed; " +
						"new Datadog keys may need time to propagate, retry shortly",
					UpstreamStatus: failure.UpstreamStatus,
				}
		}
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := response.DecodeJSON(&payload); err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	if payload.Status != "ok" {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(
				connector.FailureInvalidResponse,
				response.StatusCode,
				0,
			)
	}
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   "",
			DisplayName: datadogDisplayName(credentials.site),
		},
		ScopesKnown: false,
	}, nil
}

// datadogSiteDisplayNames spells out every reviewed site's public label; the
// site key itself is never surfaced.
var datadogSiteDisplayNames = map[string]string{
	"us1":     "Datadog US1",
	"us3":     "Datadog US3",
	"us5":     "Datadog US5",
	"eu":      "Datadog EU1",
	"ap1":     "Datadog AP1",
	"ap2":     "Datadog AP2",
	"uk1":     "Datadog UK1",
	"us1_fed": "Datadog US1-FED",
	"us2_fed": "Datadog US2-FED",
}

func datadogDisplayName(site string) string {
	if displayName, known := datadogSiteDisplayNames[site]; known {
		return displayName
	}
	return "Datadog"
}
