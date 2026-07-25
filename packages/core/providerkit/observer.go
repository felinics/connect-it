package providerkit

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

// PolicyOutcome is a fixed, low-cardinality egress policy result.
type PolicyOutcome string

const (
	PolicyOutcomeAllowed         PolicyOutcome = "allowed"
	PolicyOutcomeBlockedScheme   PolicyOutcome = "blocked_scheme"
	PolicyOutcomeBlockedOrigin   PolicyOutcome = "blocked_origin"
	PolicyOutcomeBlockedIP       PolicyOutcome = "blocked_ip"
	PolicyOutcomeBlockedRedirect PolicyOutcome = "blocked_redirect"
)

// Valid reports whether outcome belongs to the stable low-cardinality set.
func (outcome PolicyOutcome) Valid() bool {
	switch outcome {
	case PolicyOutcomeAllowed,
		PolicyOutcomeBlockedScheme,
		PolicyOutcomeBlockedOrigin,
		PolicyOutcomeBlockedIP,
		PolicyOutcomeBlockedRedirect:
		return true
	default:
		return false
	}
}

// RequestLabels identify the logical caller without containing request or
// response content. Exactly one of ToolID/Operation and exactly one of
// ConnectionID/AuthorizationID must be set.
type RequestLabels struct {
	ConnectorType   string
	ToolID          string
	Operation       string
	ConnectionID    string
	AuthorizationID string
}

// RequestEvent is the secret-free, per-attempt Provider request observation.
type RequestEvent struct {
	Labels         RequestLabels
	ProviderHost   string
	Method         string
	Duration       time.Duration
	Attempt        int
	UpstreamStatus int
	ErrorCode      connector.FailureCode
	ResponseBytes  int64
	PolicyOutcome  PolicyOutcome
}

// Validate rejects malformed or high-cardinality-shaped RequestEvents before
// they reach an Observer.
func (event RequestEvent) Validate() error {
	if event.Labels.ConnectorType == "" ||
		(event.Labels.ToolID == "") == (event.Labels.Operation == "") ||
		(event.Labels.ConnectionID == "") ==
			(event.Labels.AuthorizationID == "") ||
		event.ProviderHost == "" || unsafeProviderHost(event.ProviderHost) ||
		event.Method == "" || event.Method != strings.ToUpper(event.Method) ||
		event.Attempt < 1 || event.Duration < 0 || event.ResponseBytes < 0 ||
		!event.PolicyOutcome.Valid() {
		return ErrInvalidRequestEvent
	}
	if event.UpstreamStatus != 0 &&
		(event.UpstreamStatus < 100 || event.UpstreamStatus > 599) {
		return ErrInvalidRequestEvent
	}
	if event.ErrorCode != "" && !event.ErrorCode.Valid() {
		return ErrInvalidRequestEvent
	}
	return nil
}

// ErrInvalidRequestEvent is deliberately free of event field values.
var ErrInvalidRequestEvent = errors.New("invalid provider request event")

// Observer receives a secret-free event for each actual Provider attempt.
type Observer interface {
	Observe(context.Context, RequestEvent)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(context.Context, RequestEvent)

// Observe implements Observer.
func (observe ObserverFunc) Observe(ctx context.Context, event RequestEvent) {
	observe(ctx, event)
}

// ObserveSafely validates an event and invokes observer behind a panic guard.
// Invalid, nil, panicking, or dropping observers never change Provider results.
func ObserveSafely(ctx context.Context, observer Observer, event RequestEvent) {
	_ = observeSafely(ctx, observer, event)
}

// observeSafely is the result-bearing form used by asynchronous observers.
// A false result means the event was invalid, the observer was nil, or the
// observer panicked. It deliberately does not expose the panic value.
func observeSafely(
	ctx context.Context,
	observer Observer,
	event RequestEvent,
) (observed bool) {
	if observer == nil || event.Validate() != nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	defer func() {
		if recover() != nil {
			observed = false
		}
	}()
	observer.Observe(ctx, event)
	return true
}

func unsafeProviderHost(host string) bool {
	if strings.TrimSpace(host) != host ||
		strings.ContainsAny(host, "/@?#") {
		return true
	}
	for _, r := range host {
		if r <= 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
