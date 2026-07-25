package providerkit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestRequestEventValidate(t *testing.T) {
	t.Parallel()

	validTool := validRequestEvent()
	if err := validTool.Validate(); err != nil {
		t.Fatalf("valid tool event: %v", err)
	}

	validOperation := validTool
	validOperation.Labels.ToolID = ""
	validOperation.Labels.Operation = "oauth_exchange"
	validOperation.Labels.ConnectionID = ""
	validOperation.Labels.AuthorizationID = "authz-random-id"
	validOperation.UpstreamStatus = 0
	validOperation.ErrorCode = ""
	if err := validOperation.Validate(); err != nil {
		t.Fatalf("valid operation event: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RequestEvent)
	}{
		{name: "missing connector", mutate: func(e *RequestEvent) {
			e.Labels.ConnectorType = ""
		}},
		{name: "missing tool and operation", mutate: func(e *RequestEvent) {
			e.Labels.ToolID = ""
		}},
		{name: "tool and operation", mutate: func(e *RequestEvent) {
			e.Labels.Operation = "oauth_exchange"
		}},
		{name: "connection and authorization", mutate: func(e *RequestEvent) {
			e.Labels.AuthorizationID = "authz"
		}},
		{name: "missing connection and authorization", mutate: func(e *RequestEvent) {
			e.Labels.ConnectionID = ""
		}},
		{name: "URL instead of host", mutate: func(e *RequestEvent) {
			e.ProviderHost = "https://provider.example/path?secret=yes"
		}},
		{name: "lowercase method", mutate: func(e *RequestEvent) {
			e.Method = "get"
		}},
		{name: "zero attempt", mutate: func(e *RequestEvent) {
			e.Attempt = 0
		}},
		{name: "negative duration", mutate: func(e *RequestEvent) {
			e.Duration = -time.Nanosecond
		}},
		{name: "invalid status", mutate: func(e *RequestEvent) {
			e.UpstreamStatus = 99
		}},
		{name: "invalid error code", mutate: func(e *RequestEvent) {
			e.ErrorCode = connector.FailureCode("secret-dynamic-code")
		}},
		{name: "negative bytes", mutate: func(e *RequestEvent) {
			e.ResponseBytes = -1
		}},
		{name: "dynamic policy outcome", mutate: func(e *RequestEvent) {
			e.PolicyOutcome = PolicyOutcome("blocked_" + e.ProviderHost)
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			event := validTool
			test.mutate(&event)
			if err := event.Validate(); err != ErrInvalidRequestEvent {
				t.Fatalf("Validate = %v, want ErrInvalidRequestEvent", err)
			}
		})
	}
}

func TestObserveSafely(t *testing.T) {
	t.Parallel()

	event := validRequestEvent()
	var observed RequestEvent
	ObserveSafely(
		context.Background(),
		ObserverFunc(func(_ context.Context, got RequestEvent) {
			observed = got
		}),
		event,
	)
	if observed != event {
		t.Fatalf("observed = %#v, want %#v", observed, event)
	}

	invalid := event
	invalid.Labels.Operation = "also_set"
	var calls atomic.Int64
	ObserveSafely(
		context.Background(),
		ObserverFunc(func(context.Context, RequestEvent) { calls.Add(1) }),
		invalid,
	)
	if calls.Load() != 0 {
		t.Fatal("invalid event reached observer")
	}

	// A panic, nil observer, nil ObserverFunc, and nil context must not escape.
	ObserveSafely(context.Background(), nil, event)
	ObserveSafely(context.Background(), ObserverFunc(nil), event)
	ObserveSafely(nil, ObserverFunc(func(ctx context.Context, _ RequestEvent) {
		if ctx == nil {
			panic("nil context")
		}
		panic("observer failure")
	}), event)
}

func TestObserveSafelyConcurrent(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	observer := ObserverFunc(func(context.Context, RequestEvent) {
		calls.Add(1)
	})
	event := validRequestEvent()

	const goroutines = 64
	var group sync.WaitGroup
	group.Add(goroutines)
	for range goroutines {
		go func() {
			defer group.Done()
			ObserveSafely(context.Background(), observer, event)
		}()
	}
	group.Wait()
	if calls.Load() != goroutines {
		t.Fatalf("observer calls = %d, want %d", calls.Load(), goroutines)
	}
}

func TestPolicyOutcomeValid(t *testing.T) {
	t.Parallel()

	for _, outcome := range []PolicyOutcome{
		PolicyOutcomeAllowed,
		PolicyOutcomeBlockedScheme,
		PolicyOutcomeBlockedOrigin,
		PolicyOutcomeBlockedIP,
		PolicyOutcomeBlockedRedirect,
	} {
		if !outcome.Valid() {
			t.Fatalf("%q should be valid", outcome)
		}
	}
	if PolicyOutcome("").Valid() || PolicyOutcome("blocked_secret-host").Valid() {
		t.Fatal("dynamic/empty policy outcome should be invalid")
	}
}

func validRequestEvent() RequestEvent {
	return RequestEvent{
		Labels: RequestLabels{
			ConnectorType: "github",
			ToolID:        "github.search",
			ConnectionID:  "connection-id",
		},
		ProviderHost:   "api.github.com",
		Method:         "GET",
		Duration:       25 * time.Millisecond,
		Attempt:        1,
		UpstreamStatus: 429,
		ErrorCode:      connector.FailureRateLimited,
		ResponseBytes:  128,
		PolicyOutcome:  PolicyOutcomeAllowed,
	}
}
