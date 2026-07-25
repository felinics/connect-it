package credentialvalidator

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

// Probe describes the single request a Provider sends to prove a credential,
// plus the projection from its decoded body to a validation result. It exists
// because every Provider that only needs "one request, one JSON body" was
// repeating the same five steps: build the request, wrap the transport error,
// decode, wrap the decode error, project.
//
// Covered shapes: any method, a fixed path relative to the Client's BaseURL,
// static headers, and at most one body form (Form or JSON, per
// providerkit.Request). The Authorizer is built by the Provider, so Bearer,
// Basic, a raw header key and a Provider-local composite all work unchanged.
//
// Hand-write validateCredential instead of using Probe when any of these hold:
//   - the error path needs the response body, e.g. a Provider that reports
//     authentication or rate limiting as an error status carrying a structured
//     errors array that must be classified before the HTTP verdict wins;
//   - the result depends on response metadata rather than the body, e.g. scopes
//     read from a response header;
//   - more than one request, or a request whose target must first be derived
//     from Provider configuration in a way that also shapes the result, e.g. an
//     instance base URL that is parsed from config and then folded into the
//     account identity.
//
// Admission rule for this package: a symbol moves in only once a second real
// call site exists; a general-purpose primitive needs a third. Do not widen
// Probe to absorb a single Provider's difference — a Provider that does not fit
// keeps writing its own request, which is cheaper to read than a configuration
// knob nobody else sets.
type Probe[P any] struct {
	Method     string
	Path       string
	Headers    http.Header
	Form       url.Values
	JSON       any
	Authorizer providerkit.Authorizer

	// Project turns the decoded body into the validation result. Returning a
	// *connector.CredentialValidationError selects the failure code verbatim,
	// which is how a Provider expresses "the response was well formed and it
	// says the credential is rejected". Any other error means the body was
	// unusable and is reported as FailureInvalidResponse carrying the upstream
	// status. Project must never decide ScopesKnown from an absent field:
	// unknown stays false.
	Project func(P) (connector.CredentialValidationResult, error)
}

// Run performs probe against client and normalizes every failure into the
// cause-free validator error representation, so no Provider body, credential or
// internal cause can reach the caller.
func Run[P any](
	ctx context.Context,
	client *providerkit.Client,
	probe Probe[P],
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	if client == nil || probe.Project == nil {
		return connector.CredentialValidationResult{},
			Error(connector.FailureConfigurationError, 0, 0)
	}
	response, err := client.Do(ctx, providerkit.Request{
		Method:     probe.Method,
		URL:        probe.Path,
		Headers:    probe.Headers,
		Form:       probe.Form,
		JSON:       probe.JSON,
		Authorizer: probe.Authorizer,
		Labels:     labels,
	})
	if err != nil {
		return connector.CredentialValidationResult{}, ProviderError(err)
	}
	var payload P
	if err := response.DecodeJSON(&payload); err != nil {
		return connector.CredentialValidationResult{}, ProviderError(err)
	}
	result, err := probe.Project(payload)
	if err != nil {
		var validationErr *connector.CredentialValidationError
		if errors.As(err, &validationErr) {
			return connector.CredentialValidationResult{}, validationErr
		}
		return connector.CredentialValidationResult{},
			Error(connector.FailureInvalidResponse, response.StatusCode, 0)
	}
	return result, nil
}
