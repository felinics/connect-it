package providerkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/memohai/connect-it/packages/core/connector"
)

// ErrInvalidErrorConfig reports an invalid Error constructor input. The error
// deliberately does not echo the input because it may contain Provider data.
var ErrInvalidErrorConfig = errors.New("invalid provider error configuration")

// Error is a typed Provider/runtime failure. Error returns only SafeMessage;
// the wrapped cause is available to controlled internal diagnostics through
// errors.Unwrap but is never copied into a ToolFailure.
type Error struct {
	code              connector.FailureCode
	safeMessage       string
	upstreamStatus    int
	retryAfterSeconds int
	cause             error
}

// ErrorOption configures optional internal metadata on Error.
type ErrorOption func(*errorOptions) error

type errorOptions struct {
	upstreamStatus    int
	retryAfterSeconds int
	cause             error
}

// WithCause preserves cause for controlled internal diagnostics. The cause's
// text is never exposed by Error.Error or AsToolFailure.
func WithCause(cause error) ErrorOption {
	return func(options *errorOptions) error {
		options.cause = cause
		return nil
	}
}

// WithUpstreamStatus records a valid HTTP response status for auditing.
func WithUpstreamStatus(status int) ErrorOption {
	return func(options *errorOptions) error {
		if status < 100 || status > 599 {
			return ErrInvalidErrorConfig
		}
		options.upstreamStatus = status
		return nil
	}
}

// WithRetryAfterSeconds records a non-negative retry delay.
func WithRetryAfterSeconds(seconds int) ErrorOption {
	return func(options *errorOptions) error {
		if seconds < 0 {
			return ErrInvalidErrorConfig
		}
		options.retryAfterSeconds = seconds
		return nil
	}
}

// NewError validates and constructs a typed Provider/runtime error.
func NewError(
	code connector.FailureCode,
	safeMessage string,
	options ...ErrorOption,
) (*Error, error) {
	if !code.Valid() || !validSafeMessage(safeMessage) {
		return nil, ErrInvalidErrorConfig
	}

	config := errorOptions{}
	for _, option := range options {
		if option == nil {
			return nil, ErrInvalidErrorConfig
		}
		if err := option(&config); err != nil {
			return nil, ErrInvalidErrorConfig
		}
	}

	return &Error{
		code:              code,
		safeMessage:       safeMessage,
		upstreamStatus:    config.upstreamStatus,
		retryAfterSeconds: config.retryAfterSeconds,
		cause:             config.cause,
	}, nil
}

func (e *Error) Error() string {
	if e == nil {
		return defaultSafeMessage(connector.FailureInternalError)
	}
	return e.safeMessage
}

// Unwrap exposes the original cause only to callers that deliberately inspect
// the internal error chain.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Code returns the stable failure code.
func (e *Error) Code() connector.FailureCode {
	if e == nil {
		return connector.FailureInternalError
	}
	return e.code
}

// SafeMessage returns the bounded message safe to expose to an Agent.
func (e *Error) SafeMessage() string {
	if e == nil {
		return defaultSafeMessage(connector.FailureInternalError)
	}
	return e.safeMessage
}

// UpstreamStatus returns the audited upstream HTTP status, or zero.
func (e *Error) UpstreamStatus() int {
	if e == nil {
		return 0
	}
	return e.upstreamStatus
}

// RetryAfterSeconds returns the normalized retry delay, or zero.
func (e *Error) RetryAfterSeconds() int {
	if e == nil {
		return 0
	}
	return e.retryAfterSeconds
}

// AsToolFailure maps an error to the single safe Tool failure model.
//
// Unknown errors are intentionally collapsed to internal_error. Caller-owned
// context cancellation/deadlines are normalized to canceled; a providerkit
// request timeout must be constructed explicitly with FailureTimeout.
func AsToolFailure(err error) *connector.ToolFailure {
	if err == nil {
		return nil
	}

	var providerError *Error
	if errors.As(err, &providerError) && providerError != nil {
		return &connector.ToolFailure{
			Code:              providerError.Code(),
			Message:           providerError.SafeMessage(),
			UpstreamStatus:    providerError.UpstreamStatus(),
			RetryAfterSeconds: providerError.RetryAfterSeconds(),
		}
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &connector.ToolFailure{
			Code:    connector.FailureCanceled,
			Message: defaultSafeMessage(connector.FailureCanceled),
		}
	}

	return &connector.ToolFailure{
		Code:    connector.FailureInternalError,
		Message: defaultSafeMessage(connector.FailureInternalError),
	}
}

func validSafeMessage(message string) bool {
	return connector.ValidSafeMessage(message)
}

func defaultSafeMessage(code connector.FailureCode) string {
	return connector.DefaultFailureMessage(code)
}

func knownError(
	code connector.FailureCode,
	safeMessage string,
	options ...ErrorOption,
) *Error {
	err, constructionErr := NewError(code, safeMessage, options...)
	if constructionErr == nil {
		return err
	}
	// All callers use compile-time constants. Keep even a programming mistake
	// fail-closed and non-secret.
	return &Error{
		code:        connector.FailureInternalError,
		safeMessage: defaultSafeMessage(connector.FailureInternalError),
		cause:       fmt.Errorf("%w", ErrInvalidErrorConfig),
	}
}
