package providerkit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestErrorAndAsToolFailure(t *testing.T) {
	t.Parallel()

	internalCause := errors.New("secret provider body")
	providerError, err := NewError(
		connector.FailureRateLimited,
		"provider rate limit exceeded",
		WithUpstreamStatus(429),
		WithRetryAfterSeconds(30),
		WithCause(internalCause),
	)
	if err != nil {
		t.Fatalf("NewError: %v", err)
	}
	if providerError.Error() != "provider rate limit exceeded" ||
		providerError.Code() != connector.FailureRateLimited ||
		providerError.SafeMessage() != "provider rate limit exceeded" ||
		providerError.UpstreamStatus() != 429 ||
		providerError.RetryAfterSeconds() != 30 {
		t.Fatalf("provider error = %#v", providerError)
	}
	if !errors.Is(providerError, internalCause) {
		t.Fatal("internal cause was not preserved")
	}
	if strings.Contains(providerError.Error(), internalCause.Error()) {
		t.Fatal("Error() leaked internal cause")
	}

	wrapped := fmt.Errorf("internal wrapper: %w", providerError)
	failure := AsToolFailure(wrapped)
	if failure == nil ||
		failure.Code != connector.FailureRateLimited ||
		failure.Message != "provider rate limit exceeded" ||
		failure.UpstreamStatus != 429 ||
		failure.RetryAfterSeconds != 30 {
		t.Fatalf("AsToolFailure = %#v", failure)
	}
	if strings.Contains(failure.Message, internalCause.Error()) {
		t.Fatal("ToolFailure leaked internal cause")
	}
}

func TestAsToolFailureUnknownAndContextErrorsAreSafe(t *testing.T) {
	t.Parallel()

	if got := AsToolFailure(nil); got != nil {
		t.Fatalf("AsToolFailure(nil) = %#v", got)
	}

	secret := "raw-provider-secret"
	unknown := AsToolFailure(errors.New(secret))
	if unknown.Code != connector.FailureInternalError ||
		unknown.Message != "internal error" ||
		strings.Contains(unknown.Message, secret) {
		t.Fatalf("unknown mapping = %#v", unknown)
	}

	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("wrapped: %w", context.Canceled),
	} {
		failure := AsToolFailure(err)
		if failure.Code != connector.FailureCanceled ||
			failure.Message != "request was canceled" {
			t.Fatalf("context mapping for %v = %#v", err, failure)
		}
	}

	timeout, err := NewError(
		connector.FailureTimeout,
		"provider request timed out",
		WithCause(context.DeadlineExceeded),
	)
	if err != nil {
		t.Fatalf("NewError: %v", err)
	}
	if got := AsToolFailure(timeout); got.Code != connector.FailureTimeout {
		t.Fatalf("typed provider timeout = %#v", got)
	}
}

func TestNewErrorRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		code    connector.FailureCode
		message string
		options []ErrorOption
	}{
		{
			name:    "unknown code",
			code:    connector.FailureCode("secret-code"),
			message: "safe",
		},
		{
			name:    "empty message",
			code:    connector.FailureProviderError,
			message: "",
		},
		{
			name:    "leading whitespace",
			code:    connector.FailureProviderError,
			message: " unsafe",
		},
		{
			name:    "trailing whitespace",
			code:    connector.FailureProviderError,
			message: "unsafe ",
		},
		{
			name:    "newline",
			code:    connector.FailureProviderError,
			message: "unsafe\nmessage",
		},
		{
			name:    "invalid UTF-8",
			code:    connector.FailureProviderError,
			message: string([]byte{0xff}),
		},
		{
			name:    "too long",
			code:    connector.FailureProviderError,
			message: strings.Repeat("x", connector.MaxSafeMessageBytes+1),
		},
		{
			name:    "invalid low status",
			code:    connector.FailureProviderError,
			message: "safe",
			options: []ErrorOption{WithUpstreamStatus(99)},
		},
		{
			name:    "invalid high status",
			code:    connector.FailureProviderError,
			message: "safe",
			options: []ErrorOption{WithUpstreamStatus(600)},
		},
		{
			name:    "negative retry",
			code:    connector.FailureRateLimited,
			message: "safe",
			options: []ErrorOption{WithRetryAfterSeconds(-1)},
		},
		{
			name:    "nil option",
			code:    connector.FailureProviderError,
			message: "safe",
			options: []ErrorOption{nil},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewError(test.code, test.message, test.options...)
			if got != nil || !errors.Is(err, ErrInvalidErrorConfig) {
				t.Fatalf("NewError = (%#v, %v), want invalid config", got, err)
			}
			if strings.Contains(err.Error(), test.message) && test.message != "" {
				t.Fatal("constructor error echoed rejected message")
			}
		})
	}
}

func TestNewErrorAcceptsBoundaryValues(t *testing.T) {
	t.Parallel()

	got, err := NewError(
		connector.FailureProviderError,
		strings.Repeat("x", connector.MaxSafeMessageBytes),
		WithUpstreamStatus(100),
		WithRetryAfterSeconds(0),
		WithCause(nil),
	)
	if err != nil {
		t.Fatalf("NewError boundary: %v", err)
	}
	if got.UpstreamStatus() != 100 || got.RetryAfterSeconds() != 0 {
		t.Fatalf("boundary error = %#v", got)
	}

	got, err = NewError(
		connector.FailureProviderError,
		"safe",
		WithUpstreamStatus(599),
	)
	if err != nil || got.UpstreamStatus() != 599 {
		t.Fatalf("status 599 = (%#v, %v)", got, err)
	}
}

func TestNilErrorMethodsAreSafe(t *testing.T) {
	t.Parallel()

	var providerError *Error
	if providerError.Error() != "internal error" ||
		providerError.SafeMessage() != "internal error" ||
		providerError.Code() != connector.FailureInternalError ||
		providerError.UpstreamStatus() != 0 ||
		providerError.RetryAfterSeconds() != 0 ||
		providerError.Unwrap() != nil {
		t.Fatal("nil *Error methods were not fail-closed")
	}
}
