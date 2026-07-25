package toolfail

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

func providerError(
	t *testing.T,
	code connector.FailureCode,
	status int,
	retryAfterSeconds int,
) error {
	t.Helper()
	options := []providerkit.ErrorOption{
		providerkit.WithRetryAfterSeconds(retryAfterSeconds),
	}
	if status != 0 {
		options = append(options, providerkit.WithUpstreamStatus(status))
	}
	err, constructErr := providerkit.NewError(
		code,
		connector.DefaultFailureMessage(code),
		options...,
	)
	if constructErr != nil {
		t.Fatalf("construct provider error: %v", constructErr)
	}
	return err
}

func TestFromProviderKeepsPerConnectorStatusSemantics(t *testing.T) {
	t.Parallel()

	// A connector's own overrides, exactly as gitlab and datadog declare them.
	remap := map[int]connector.FailureCode{
		http.StatusBadRequest:          connector.FailureInvalidInput,
		http.StatusUnprocessableEntity: connector.FailureInvalidInput,
		http.StatusRequestTimeout:      connector.FailureTimeout,
	}
	tests := []struct {
		name              string
		status            int
		retryAfterSeconds int
		upstreamCode      connector.FailureCode
		code              connector.FailureCode
		credentialInvalid bool
	}{
		{
			name:         "remapped bad request",
			status:       http.StatusBadRequest,
			upstreamCode: connector.FailureProviderError,
			code:         connector.FailureInvalidInput,
		},
		{
			name:         "remapped unprocessable",
			status:       http.StatusUnprocessableEntity,
			upstreamCode: connector.FailureProviderError,
			code:         connector.FailureInvalidInput,
		},
		{
			name:         "remapped request timeout",
			status:       http.StatusRequestTimeout,
			upstreamCode: connector.FailureProviderError,
			code:         connector.FailureTimeout,
		},
		{
			name:              "unauthorized becomes the credential signal",
			status:            http.StatusUnauthorized,
			upstreamCode:      connector.FailureAuthorizationFailed,
			code:              connector.FailureAuthorizationFailed,
			credentialInvalid: true,
		},
		{
			name:         "forbidden stays state neutral",
			status:       http.StatusForbidden,
			upstreamCode: connector.FailurePermissionDenied,
			code:         connector.FailurePermissionDenied,
		},
		{
			name:              "rate limit keeps retry after",
			status:            http.StatusTooManyRequests,
			retryAfterSeconds: 9,
			upstreamCode:      connector.FailureRateLimited,
			code:              connector.FailureRateLimited,
		},
		{
			name:         "server error stays upstream unavailable",
			status:       http.StatusServiceUnavailable,
			upstreamCode: connector.FailureUpstreamUnavailable,
			code:         connector.FailureUpstreamUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := FromProvider(
				providerError(
					t,
					test.upstreamCode,
					test.status,
					test.retryAfterSeconds,
				),
				remap,
			)
			failure := result.Failure
			if failure == nil || failure.Code != test.code ||
				failure.UpstreamStatus != test.status ||
				failure.RetryAfterSeconds != test.retryAfterSeconds ||
				failure.IndicatesCredentialInvalid() !=
					test.credentialInvalid {
				t.Fatalf("failure = %+v", failure)
			}
			if failure.Message !=
				connector.DefaultFailureMessage(test.code) {
				t.Fatalf("message must stay the stable default: %q",
					failure.Message)
			}
		})
	}
}

// A remap may never claim 401: the credential-invalid signal is what marks a
// Connection as needing re-auth, and a generic authorization_failed does not.
func TestFromProviderNeverLetsRemapClaimUnauthorized(t *testing.T) {
	t.Parallel()

	result := FromProvider(
		providerError(
			t,
			connector.FailureAuthorizationFailed,
			http.StatusUnauthorized,
			0,
		),
		map[int]connector.FailureCode{
			http.StatusUnauthorized: connector.FailureProviderError,
		},
	)
	if !result.Failure.IndicatesCredentialInvalid() {
		t.Fatalf("401 must stay the credential signal: %+v", result.Failure)
	}
}

func TestFromProviderClassifiesNonHTTPErrors(t *testing.T) {
	t.Parallel()

	canceled := FromProvider(context.Canceled, nil)
	if canceled.Failure == nil ||
		canceled.Failure.Code != connector.FailureCanceled {
		t.Fatalf("canceled = %+v", canceled.Failure)
	}
	unknown := FromProvider(errors.New("opaque"), nil)
	if unknown.Failure == nil ||
		unknown.Failure.Code != connector.FailureInternalError {
		t.Fatalf("unknown = %+v", unknown.Failure)
	}
	absent := FromProvider(nil, nil)
	if absent.Failure == nil ||
		absent.Failure.Code != connector.FailureInternalError {
		t.Fatalf("nil error must still fail closed: %+v", absent.Failure)
	}
}

func TestConstructorsClampInsteadOfErroring(t *testing.T) {
	t.Parallel()

	clamped := New("not_a_code", 999, -1)
	if clamped.Code != connector.FailureInternalError ||
		clamped.UpstreamStatus != 0 || clamped.RetryAfterSeconds != 0 {
		t.Fatalf("invalid input must clamp fail-closed: %+v", clamped)
	}
	if Code("") != nil {
		t.Fatal(`the "" success sentinel must map to no failure`)
	}
	if failure := Code(connector.FailureConfigurationError); failure == nil ||
		failure.Code != connector.FailureConfigurationError {
		t.Fatalf("Code = %+v", failure)
	}
	signal := CredentialInvalid(http.StatusUnauthorized)
	if !signal.IndicatesCredentialInvalid() {
		t.Fatalf("CredentialInvalid = %+v", signal)
	}
	result := Result(connector.FailureNotFound, http.StatusNotFound, 0)
	if result.Failure == nil ||
		result.Failure.Code != connector.FailureNotFound {
		t.Fatalf("Result = %+v", result.Failure)
	}
}
