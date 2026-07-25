package smoketest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// CredentialRejected reports whether a validator turns a deliberately invalid
// credential into the exact credential-invalid signal (authorization_failed on
// HTTP 401) rather than a generic error. A connector that answers anything else
// cannot mark a Connection as needing re-auth.
func CredentialRejected(
	ctx context.Context,
	validator connector.CredentialValidator,
	input connector.CredentialValidationInput,
) bool {
	if validator == nil {
		return false
	}
	_, err := validator(ctx, input)
	var validationErr *connector.CredentialValidationError
	return errors.As(err, &validationErr) &&
		validationErr.Code == connector.FailureAuthorizationFailed &&
		validationErr.UpstreamStatus == http.StatusUnauthorized
}

// RequireRejectedCredential is the negative-auth probe every real-account
// harness runs before touching the account. The message is deliberately fixed:
// neither the rejected credential nor the Provider's response body may reach
// smoke output.
func RequireRejectedCredential(
	t *testing.T,
	validator connector.CredentialValidator,
	input connector.CredentialValidationInput,
) {
	t.Helper()
	if !CredentialRejected(t.Context(), validator, input) {
		t.Fatal("validator did not reject the negative-auth probe")
	}
}
