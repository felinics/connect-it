package smoketest

import (
	"context"
	"errors"
	"testing"

	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
)

// This is the one table test of the env gate. Nine connectors used to carry a
// private copy of it; each connector now tests only its own extra rules.
func TestValuesArmsOnAnyNameAndThenRequiresAll(t *testing.T) {
	t.Parallel()
	const (
		token = "CONNECT_IT_FIXTURE_TOKEN"
		scope = "CONNECT_IT_FIXTURE_SCOPE"
	)
	tests := []struct {
		name           string
		values         map[string]string
		wantConfigured bool
		wantError      bool
	}{
		{name: "none configured means skip"},
		{
			name:           "one name arms the harness",
			values:         map[string]string{token: "redacted"},
			wantConfigured: true,
			wantError:      true,
		},
		{
			name: "an armed harness rejects an empty name",
			values: map[string]string{
				token: "redacted",
				scope: "",
			},
			wantConfigured: true,
			wantError:      true,
		},
		{
			name: "complete configuration parses",
			values: map[string]string{
				token: "redacted",
				scope: "fixture",
			},
			wantConfigured: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values, configured, err := Values(
				"Fixture",
				MapLookup(test.values),
				token,
				scope,
			)
			if configured != test.wantConfigured ||
				(err != nil) != test.wantError {
				t.Fatalf(
					"configured=%t err=%v, want configured=%t error=%t",
					configured,
					err,
					test.wantConfigured,
					test.wantError,
				)
			}
			if err == nil && configured && values[token] != "redacted" {
				t.Fatal("a complete configuration lost a value")
			}
		})
	}
}

func TestExactlyRejectsTruthyLookalikeGates(t *testing.T) {
	t.Parallel()
	const gate = "CONNECT_IT_FIXTURE_ALLOW_WRITES"
	for _, value := range []string{"", "1", "TRUE", "True", "yes", " true"} {
		if err := Exactly(
			map[string]string{gate: value},
			gate,
			"true",
		); err == nil {
			t.Errorf("write gate accepted a non-exact value")
		}
	}
	if err := Exactly(
		map[string]string{gate: "true"},
		gate,
		"true",
	); err != nil {
		t.Fatalf("exact write gate = %v", err)
	}
	if err := Exactly(
		map[string]string{gate: "false"},
		gate,
		"true",
		"false",
	); err != nil {
		t.Fatalf("exact two-valued gate = %v", err)
	}
}

// This is the one test of the shared cleanup retry loop. Six connectors used to
// carry a private copy of it; each connector now tests only its own request
// mapping and its own exact-match rules.
func TestRetryRechecksUncertainCleanupAndFailsClosedOnExhaustion(t *testing.T) {
	t.Parallel()
	noWait := func(context.Context) bool { return true }

	steps := 0
	failure := Retry(t.Context(), 5, noWait, func(context.Context) (bool, bool, *connector.ToolFailure) {
		steps++
		// A lost write response looks exactly like this: nothing found yet,
		// no failure, and no proof that nothing was created.
		return steps == 3, steps != 3, nil
	})
	if failure != nil || steps != 3 {
		t.Fatalf("uncertain cleanup = steps %d failure %+v", steps, failure)
	}

	waits := 0
	steps = 0
	failure = Retry(t.Context(), 5, func(context.Context) bool {
		waits++
		return true
	}, func(context.Context) (bool, bool, *connector.ToolFailure) {
		steps++
		return false, true, nil
	})
	if failure == nil ||
		failure.Code != connector.FailureInvalidResponse ||
		steps != 5 || waits != 4 {
		t.Fatalf(
			"exhausted cleanup = steps %d waits %d failure %+v",
			steps,
			waits,
			failure,
		)
	}

	steps = 0
	denied := toolfail.New(connector.FailurePermissionDenied, 403, 0)
	failure = Retry(t.Context(), 5, noWait, func(context.Context) (bool, bool, *connector.ToolFailure) {
		steps++
		return false, Retryable(denied), denied
	})
	if failure == nil ||
		failure.Code != connector.FailurePermissionDenied ||
		steps != 1 {
		t.Fatalf("denied cleanup = steps %d failure %+v", steps, failure)
	}

	if failure := Retry(t.Context(), 0, noWait, nil); failure == nil ||
		failure.Code != connector.FailureConfigurationError {
		t.Fatalf("unusable cleanup loop = %+v", failure)
	}
}

func TestRetryableCoversEveryUncertainCleanupOutcome(t *testing.T) {
	t.Parallel()
	for _, code := range []connector.FailureCode{
		connector.FailureNotFound,
		connector.FailureRateLimited,
		connector.FailureProviderError,
		connector.FailureInvalidResponse,
		connector.FailureUpstreamUnavailable,
		connector.FailureTimeout,
		connector.FailureResponseTooLarge,
	} {
		if !Retryable(toolfail.New(code, 0, 0)) {
			t.Errorf("cleanup failure %q should be rechecked", code)
		}
	}
	for _, code := range []connector.FailureCode{
		connector.FailureInvalidInput,
		connector.FailureAuthorizationFailed,
		connector.FailurePermissionDenied,
		connector.FailurePolicyDenied,
	} {
		if Retryable(toolfail.New(code, 0, 0)) {
			t.Errorf("cleanup failure %q should fail immediately", code)
		}
	}
}

func TestResolveRetriesMissingResourceAndKeepsTheLastError(t *testing.T) {
	t.Parallel()
	noWait := func(context.Context) bool { return true }

	attempts := 0
	value, err := Resolve(
		t.Context(),
		5,
		noWait,
		func(context.Context) (string, bool, error) {
			attempts++
			return "resource-1", attempts == 2, nil
		},
	)
	if err != nil || value != "resource-1" || attempts != 2 {
		t.Fatalf("resolve = %q attempts %d err %v", value, attempts, err)
	}

	sentinel := errors.New("fixture lookup failure")
	attempts = 0
	if _, err := Resolve(
		t.Context(),
		3,
		noWait,
		func(context.Context) (string, bool, error) {
			attempts++
			return "", false, sentinel
		},
	); !errors.Is(err, sentinel) || attempts != 3 {
		t.Fatalf("exhausted resolve = attempts %d err %v", attempts, err)
	}

	if _, err := Resolve(
		t.Context(),
		2,
		noWait,
		func(context.Context) (string, bool, error) {
			return "", false, nil
		},
	); err == nil {
		t.Fatal("a never-found resource must not resolve")
	}
}

func TestMarkerIsUniqueAndGreppable(t *testing.T) {
	t.Parallel()
	first := Marker(t)
	second := Marker(t)
	if first == second || len(first) != len("20060102T150405.000000000Z")+1+32 {
		t.Fatalf("smoke markers = %q and %q", first, second)
	}
}
