// Package toolfail builds the public failure envelopes every Managed
// connector returns. It exists so the safe-message contract is written once:
// a ToolFailure carries only the platform's stable default text for its code,
// never a Provider body, and an unusable code/status/delay clamps fail-closed
// to internal_error instead of surfacing as a handler error.
//
// # Migrating a Managed connector
//
// Delete the connector's local xFailureResult / xProviderFailureResult pair
// and its status table, then rewrite the two shapes below.
//
// A representative handler, before:
//
//	func (handler *managedHandler) getProject(
//		ctx context.Context,
//		call connector.ToolCallContext,
//	) (connector.ToolResultData, error) {
//		projectID, ok := projectPath(call.Arguments["project_id"])
//		if !ok {
//			return gitLabFailureResult(connector.FailureInvalidInput, 0, 0)
//		}
//		...
//	}
//
// and after — the discarded error is gone, so the failure path can no longer
// silently turn into a nil result:
//
//	if !ok {
//		return toolfail.Result(connector.FailureInvalidInput, 0, 0), nil
//	}
//
// A representative failure site, before — 40 lines of per-status rebuilds,
// each with an ignored construction error:
//
//	func gitLabProviderFailureResult(err error) (connector.ToolResultData, error) {
//		failure := providerkit.AsToolFailure(err)
//		switch failure.UpstreamStatus {
//		case http.StatusUnauthorized:
//			failure, constructErr = connector.NewCredentialInvalidFailure(...)
//		case http.StatusBadRequest, http.StatusUnprocessableEntity:
//			failure, constructErr = connector.NewToolFailure(connector.FailureInvalidInput, ...)
//		case http.StatusRequestTimeout:
//			failure, constructErr = connector.NewToolFailure(connector.FailureTimeout, ...)
//		}
//		return connector.ToolResultData{Failure: failure}, nil
//	}
//
// and after — the connector keeps only its own status overrides as data:
//
//	var gitLabFailureRemap = map[int]connector.FailureCode{
//		http.StatusBadRequest:          connector.FailureInvalidInput,
//		http.StatusUnprocessableEntity: connector.FailureInvalidInput,
//		http.StatusRequestTimeout:      connector.FailureTimeout,
//	}
//
//	return toolfail.FromProvider(err, gitLabFailureRemap), nil
//
// Per-connector status semantics are therefore preserved, not flattened: each
// connector still declares its own remap. 401 is the one status a remap may
// not claim, because it is the credential-invalid signal that marks a
// Connection as needing re-auth.
package toolfail

import (
	"net/http"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

// New builds a safe failure carrying only the stable default message for code.
// No caller may supply a message: Provider text must never reach an Agent.
func New(
	code connector.FailureCode,
	upstreamStatus int,
	retryAfterSeconds int,
) *connector.ToolFailure {
	return connector.Failure(
		code,
		connector.DefaultFailureMessage(code),
		upstreamStatus,
		retryAfterSeconds,
	)
}

// Code adapts the repo's "" -success-sentinel FailureCode idiom to a nil-able
// failure, so pre-flight guards compose with the rest of this package.
func Code(code connector.FailureCode) *connector.ToolFailure {
	if code == "" {
		return nil
	}
	return New(code, 0, 0)
}

// CredentialInvalid builds the explicit credential-invalid signal. It is the
// only failure that marks a Connection's credential as needing re-auth; a
// generic authorization_failed stays state-neutral.
func CredentialInvalid(upstreamStatus int) *connector.ToolFailure {
	return connector.CredentialInvalidFailure(
		connector.DefaultFailureMessage(connector.FailureAuthorizationFailed),
		upstreamStatus,
	)
}

// Result is the finished handler result for a locally detected failure.
func Result(
	code connector.FailureCode,
	upstreamStatus int,
	retryAfterSeconds int,
) connector.ToolResultData {
	return Of(New(code, upstreamStatus, retryAfterSeconds))
}

// Of wraps an already-built failure as a handler result.
func Of(failure *connector.ToolFailure) connector.ToolResultData {
	return connector.ToolResultData{Failure: failure}
}

// FromProvider maps an outbound providerkit error to a public result.
//
// The upstream status decides the code: 401 always becomes the explicit
// credential-invalid signal, any other status listed in remap takes that
// connector's override while keeping the audited status and Retry-After, and
// everything else keeps providerkit's own classification. remap may be nil.
func FromProvider(
	err error,
	remap map[int]connector.FailureCode,
) connector.ToolResultData {
	failure := providerkit.AsToolFailure(err)
	if failure == nil {
		return Result(connector.FailureInternalError, 0, 0)
	}
	if failure.UpstreamStatus == http.StatusUnauthorized {
		return Of(CredentialInvalid(http.StatusUnauthorized))
	}
	if code, overridden := remap[failure.UpstreamStatus]; overridden {
		return Of(New(
			code,
			failure.UpstreamStatus,
			failure.RetryAfterSeconds,
		))
	}
	return Of(failure)
}
