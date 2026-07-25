package smoketest

import (
	"context"
	"errors"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
)

// Wait is the backoff between two cleanup attempts. It reports false when the
// budget is gone, so a cleanup loop can never outlive its deadline.
type Wait func(context.Context) bool

// WaitSecond is the backoff every harness uses: Providers make a just-created
// resource visible within a second or two, and cleanup runs inside a bounded
// t.Cleanup deadline.
func WaitSecond(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Step is one bounded cleanup attempt. done means the smoke-created resource is
// verified gone; retryable means the attempt left the account in an uncertain
// state and another bounded recheck is owed before giving up.
type Step func(ctx context.Context) (
	done bool,
	retryable bool,
	failure *connector.ToolFailure,
)

// Retry is the shared resolve-verify-delete loop behind every guarded cleanup.
//
// It fails closed in both directions. A definitely-wrong attempt (invalid
// input, denied permission) stops immediately rather than hammering a real
// account; an uncertain attempt — a lost write response, a Provider 503, a
// listing that has not caught up — is rechecked until the attempt budget is
// spent, because "not found yet" is not proof that nothing was created.
// Exhaustion is an error, never a silent success.
func Retry(
	ctx context.Context,
	attempts int,
	wait Wait,
	step Step,
) *connector.ToolFailure {
	if attempts < 1 || step == nil {
		return toolfail.New(connector.FailureConfigurationError, 0, 0)
	}
	var last *connector.ToolFailure
	for attempt := 0; attempt < attempts; attempt++ {
		done, retryable, failure := step(ctx)
		if done {
			return nil
		}
		if failure != nil {
			last = failure
		}
		if !retryable {
			if failure != nil {
				return failure
			}
			return toolfail.New(connector.FailureInvalidResponse, 0, 0)
		}
		if attempt+1 == attempts {
			break
		}
		if wait == nil || !wait(ctx) {
			if ctx.Err() != nil {
				return toolfail.New(connector.FailureTimeout, 0, 0)
			}
			return toolfail.New(connector.FailureInvalidResponse, 0, 0)
		}
	}
	if last != nil {
		return last
	}
	return toolfail.New(connector.FailureInvalidResponse, 0, 0)
}

// Resolve is the error-reporting form of Retry, for cleanups that must first
// find the one exact resource they are allowed to remove. Not-found is retried;
// the last observed error survives exhaustion.
func Resolve[T any](
	ctx context.Context,
	attempts int,
	wait Wait,
	lookup func(context.Context) (T, bool, error),
) (T, error) {
	var zero T
	if attempts < 1 || lookup == nil {
		return zero, errors.New("invalid smoke cleanup lookup")
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		value, found, err := lookup(ctx)
		if err == nil && found {
			return value, nil
		}
		if err != nil {
			lastErr = err
		}
		if attempt+1 == attempts || wait == nil || !wait(ctx) {
			break
		}
	}
	if lastErr != nil {
		return zero, lastErr
	}
	return zero, errors.New("smoke resource was not found")
}

// Retryable classifies a cleanup failure as uncertain. These are the codes
// under which the Provider may still be holding the smoke-created resource, so
// abandoning cleanup on any of them risks leaking a real object into a real
// account. Everything else — invalid input, denied authorization, policy —
// means another attempt would fail the same way.
func Retryable(failure *connector.ToolFailure) bool {
	if failure == nil {
		return true
	}
	switch failure.Code {
	case connector.FailureNotFound,
		connector.FailureRateLimited,
		connector.FailureProviderError,
		connector.FailureInvalidResponse,
		connector.FailureUpstreamUnavailable,
		connector.FailureTimeout,
		connector.FailureResponseTooLarge:
		return true
	default:
		return false
	}
}
