package providerkit

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestRetryableMethodRequiresExplicitWriteIdempotency(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		method     string
		retryWrite bool
		want       bool
	}{
		{method: http.MethodGet, want: true},
		{method: http.MethodHead, want: true},
		{method: http.MethodGet, retryWrite: true, want: true},
		{method: http.MethodPost, want: false},
		{method: http.MethodPost, retryWrite: true, want: true},
		{method: http.MethodPut, want: false},
		{method: http.MethodPut, retryWrite: true, want: true},
		{method: http.MethodPatch, want: false},
		{method: http.MethodPatch, retryWrite: true, want: true},
		{method: http.MethodDelete, want: false},
		{method: http.MethodDelete, retryWrite: true, want: true},
		{method: http.MethodOptions, retryWrite: true, want: false},
		{method: "get", retryWrite: true, want: false},
		{method: "", retryWrite: true, want: false},
	} {
		test := test
		t.Run(
			test.method+"_"+boolTestName(test.retryWrite),
			func(t *testing.T) {
				t.Parallel()

				if got := retryableMethod(test.method, test.retryWrite); got != test.want {
					t.Fatalf(
						"retryableMethod(%q, %v) = %v, want %v",
						test.method,
						test.retryWrite,
						got,
						test.want,
					)
				}
			},
		)
	}
}

func TestRetryPolicyCanExplicitlyDisableRetries(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeRetryPolicy(RetryPolicy{Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !normalized.Disabled || normalized.MaxRetries != 0 {
		t.Fatalf("normalized disabled retry = %#v", normalized)
	}
	decision := decideRetry(
		normalized,
		http.MethodGet,
		false,
		0,
		http.StatusServiceUnavailable,
		nil,
		nil,
		time.Now(),
	)
	if decision.retry {
		t.Fatal("explicitly disabled policy retried GET 503")
	}
}

func TestDecideRetryStatusMatrix(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{
		MaxRetries:     2,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     time.Second,
	}
	for _, test := range []struct {
		name       string
		method     string
		retryWrite bool
		status     int
		completed  int
		want       bool
	}{
		{
			name:   "get 429",
			method: http.MethodGet,
			status: http.StatusTooManyRequests,
			want:   true,
		},
		{
			name:   "get 502",
			method: http.MethodGet,
			status: http.StatusBadGateway,
			want:   true,
		},
		{
			name:   "get 503",
			method: http.MethodGet,
			status: http.StatusServiceUnavailable,
			want:   true,
		},
		{
			name:   "get 504",
			method: http.MethodGet,
			status: http.StatusGatewayTimeout,
			want:   true,
		},
		{
			name:   "get 500",
			method: http.MethodGet,
			status: http.StatusInternalServerError,
		},
		{
			name:   "get 401",
			method: http.MethodGet,
			status: http.StatusUnauthorized,
		},
		{
			name:   "get 403",
			method: http.MethodGet,
			status: http.StatusForbidden,
		},
		{
			name:   "get 404",
			method: http.MethodGet,
			status: http.StatusNotFound,
		},
		{
			name:   "get 409",
			method: http.MethodGet,
			status: http.StatusConflict,
		},
		{
			name:   "get 422",
			method: http.MethodGet,
			status: http.StatusUnprocessableEntity,
		},
		{
			name:       "idempotent post 503",
			method:     http.MethodPost,
			retryWrite: true,
			status:     http.StatusServiceUnavailable,
			want:       true,
		},
		{
			name:   "non idempotent post 503",
			method: http.MethodPost,
			status: http.StatusServiceUnavailable,
		},
		{
			name:   "non idempotent post 429",
			method: http.MethodPost,
			status: http.StatusTooManyRequests,
		},
		{
			name:       "idempotent delete 429",
			method:     http.MethodDelete,
			retryWrite: true,
			status:     http.StatusTooManyRequests,
			want:       true,
		},
		{
			name:      "retry budget exhausted",
			method:    http.MethodGet,
			status:    http.StatusServiceUnavailable,
			completed: 2,
		},
		{
			name:      "retry budget exceeded",
			method:    http.MethodGet,
			status:    http.StatusServiceUnavailable,
			completed: 3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := decideRetry(
				policy,
				test.method,
				test.retryWrite,
				test.completed,
				test.status,
				make(http.Header),
				nil,
				time.Unix(1_700_000_000, 0),
			)
			if got.retry != test.want {
				t.Fatalf("decision = %#v, retry want %v", got, test.want)
			}
		})
	}
}

func TestDecideRetryNetworkErrors(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{
		MaxRetries:     2,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     time.Second,
	}
	upstreamWithNetworkCause := mustRetryTestError(
		t,
		connector.FailureUpstreamUnavailable,
		WithCause(retryTestNetworkError{}),
	)
	configWithNetworkCause := mustRetryTestError(
		t,
		connector.FailureConfigurationError,
		WithCause(retryTestNetworkError{}),
	)
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "network error",
			err:  retryTestNetworkError{},
			want: true,
		},
		{
			name: "wrapped network error",
			err:  errors.Join(errors.New("request failed"), retryTestNetworkError{}),
			want: true,
		},
		{
			name: "unexpected eof",
			err:  io.ErrUnexpectedEOF,
			want: true,
		},
		{
			name: "eof before response",
			err:  io.EOF,
			want: true,
		},
		{
			name: "wrapped unexpected eof",
			err:  errors.Join(errors.New("request failed"), io.ErrUnexpectedEOF),
			want: true,
		},
		{
			name: "wrapped eof before response",
			err:  errors.Join(errors.New("request failed"), io.EOF),
			want: true,
		},
		{
			name: "normalized upstream with network cause",
			err:  upstreamWithNetworkCause,
			want: true,
		},
		{
			name: "url wrapped transient network error",
			err: &url.Error{
				Op:  "Get",
				URL: "https://provider.test",
				Err: retryTestNetworkError{},
			},
			want: true,
		},
		{
			name: "url wrapped malformed transport error",
			err: &url.Error{
				Op:  "Get",
				URL: "https://provider.test",
				Err: errors.New("malformed transport configuration"),
			},
		},
		{
			name: "url wrapped certificate failure",
			err: &url.Error{
				Op:  "Get",
				URL: "https://provider.test",
				Err: x509.UnknownAuthorityError{
					Cert: &x509.Certificate{},
				},
			},
		},
		{
			name: "permanent DNS not found",
			err: &url.Error{
				Op:  "Get",
				URL: "https://provider.test",
				Err: &net.DNSError{
					Err:        "no such host",
					Name:       "provider.test",
					IsNotFound: true,
				},
			},
		},
		{
			name: "temporary DNS error",
			err: &url.Error{
				Op:  "Get",
				URL: "https://provider.test",
				Err: &net.DNSError{
					Err:         "temporary resolver failure",
					Name:        "provider.test",
					IsTemporary: true,
				},
			},
			want: true,
		},
		{
			name: "dial operation error",
			err: &url.Error{
				Op:  "Get",
				URL: "https://provider.test",
				Err: &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: errors.New("connection refused"),
				},
			},
			want: true,
		},
		{
			name: "caller canceled",
			err:  context.Canceled,
		},
		{
			name: "caller deadline",
			err:  context.DeadlineExceeded,
		},
		{
			name: "configuration error with network cause",
			err:  configWithNetworkCause,
		},
		{
			name: "ordinary error",
			err:  errors.New("ordinary failure"),
		},
		{
			name: "nil",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := retryableNetworkError(test.err); got != test.want {
				t.Fatalf(
					"retryableNetworkError(%T) = %v, want %v",
					test.err,
					got,
					test.want,
				)
			}

			decision := decideRetry(
				policy,
				http.MethodGet,
				false,
				0,
				0,
				nil,
				test.err,
				time.Unix(1_700_000_000, 0),
			)
			if decision.retry != test.want {
				t.Fatalf("decision = %#v, retry want %v", decision, test.want)
			}
		})
	}
}

func TestDecideRetryBackoffAndRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	policy := RetryPolicy{
		MaxRetries:     10,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
	}
	for _, test := range []struct {
		name      string
		completed int
		status    int
		retry     string
		want      time.Duration
	}{
		{
			name:   "first exponential delay",
			status: http.StatusServiceUnavailable,
			want:   100 * time.Millisecond,
		},
		{
			name:      "second exponential delay",
			completed: 1,
			status:    http.StatusServiceUnavailable,
			want:      200 * time.Millisecond,
		},
		{
			name:      "third exponential delay",
			completed: 2,
			status:    http.StatusServiceUnavailable,
			want:      400 * time.Millisecond,
		},
		{
			name:      "exponential delay capped",
			completed: 8,
			status:    http.StatusServiceUnavailable,
			want:      2 * time.Second,
		},
		{
			name:   "retry after seconds",
			status: http.StatusTooManyRequests,
			retry:  "1",
			want:   time.Second,
		},
		{
			name:   "retry after seconds capped",
			status: http.StatusTooManyRequests,
			retry:  "60",
			want:   2 * time.Second,
		},
		{
			name:   "retry after zero",
			status: http.StatusTooManyRequests,
			retry:  "0",
		},
		{
			name:   "retry after date",
			status: http.StatusTooManyRequests,
			retry:  now.Add(1500 * time.Millisecond).Format(http.TimeFormat),
			want:   time.Second,
		},
		{
			name:   "invalid retry after uses exponential",
			status: http.StatusTooManyRequests,
			retry:  "invalid",
			want:   100 * time.Millisecond,
		},
		{
			name:   "retry after ignored for 503",
			status: http.StatusServiceUnavailable,
			retry:  "1",
			want:   100 * time.Millisecond,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			header := make(http.Header)
			header.Set("Retry-After", test.retry)
			got := decideRetry(
				policy,
				http.MethodGet,
				false,
				test.completed,
				test.status,
				header,
				nil,
				now,
			)
			if !got.retry || got.delay != test.want {
				t.Fatalf(
					"decision = %#v, want retry with %s",
					got,
					test.want,
				)
			}
		})
	}
}

func TestParseRetryAfterStrictRFCForms(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		raw  string
		want time.Duration
		ok   bool
	}{
		{name: "seconds", raw: "15", want: 15 * time.Second, ok: true},
		{name: "zero", raw: "0", ok: true},
		{name: "leading zero", raw: "001", want: time.Second, ok: true},
		{
			name: "future date",
			raw:  now.Add(20 * time.Second).Format(http.TimeFormat),
			want: 20 * time.Second,
			ok:   true,
		},
		{
			name: "past date",
			raw:  now.Add(-time.Second).Format(http.TimeFormat),
			ok:   true,
		},
		{name: "empty"},
		{name: "whitespace", raw: " \t "},
		{name: "negative", raw: "-1"},
		{name: "signed positive", raw: "+1"},
		{name: "signed negative zero", raw: "-0"},
		{name: "fraction", raw: "1.5"},
		{name: "unit", raw: "1s"},
		{name: "invalid date", raw: "not-a-date"},
		{name: "overflow", raw: "9223372036854775808"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, ok := ParseRetryAfter(test.raw, now)
			if ok != test.ok || got != test.want {
				t.Fatalf(
					"ParseRetryAfter(%q) = (%s, %v), want (%s, %v)",
					test.raw,
					got,
					ok,
					test.want,
					test.ok,
				)
			}
		})
	}
}

func TestExponentialBackoffHandlesBoundsWithoutSleeping(t *testing.T) {
	t.Parallel()

	policy := RetryPolicy{
		MaxRetries:     100,
		InitialBackoff: time.Second,
		MaxBackoff:     3 * time.Second,
	}
	if got := exponentialBackoff(policy, 0); got != time.Second {
		t.Fatalf("first backoff = %s", got)
	}
	if got := exponentialBackoff(policy, 1); got != 2*time.Second {
		t.Fatalf("second backoff = %s", got)
	}
	if got := exponentialBackoff(policy, 100); got != policy.MaxBackoff {
		t.Fatalf("overflow-safe backoff = %s, want %s", got, policy.MaxBackoff)
	}
	if got := exponentialBackoff(
		RetryPolicy{InitialBackoff: 0, MaxBackoff: time.Second},
		10,
	); got != 0 {
		t.Fatalf("zero initial backoff = %s", got)
	}
}

type retryTestNetworkError struct{}

func (retryTestNetworkError) Error() string   { return "network unavailable" }
func (retryTestNetworkError) Timeout() bool   { return false }
func (retryTestNetworkError) Temporary() bool { return true }

func mustRetryTestError(
	t *testing.T,
	code connector.FailureCode,
	options ...ErrorOption,
) *Error {
	t.Helper()
	err, createErr := NewError(code, "safe retry test error", options...)
	if createErr != nil {
		t.Fatalf("NewError() error = %v", createErr)
	}
	return err
}

func boolTestName(value bool) string {
	if value {
		return "idempotent"
	}
	return "default"
}
