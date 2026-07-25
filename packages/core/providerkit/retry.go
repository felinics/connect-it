package providerkit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Clock makes retry delays and Retry-After handling deterministic in tests.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

type retryDecision struct {
	retry bool
	delay time.Duration
}

func decideRetry(
	policy RetryPolicy,
	method string,
	retryWrite bool,
	retriesCompleted int,
	status int,
	header http.Header,
	requestErr error,
	now time.Time,
) retryDecision {
	if retriesCompleted >= policy.MaxRetries || !retryableMethod(method, retryWrite) {
		return retryDecision{}
	}

	retry := false
	switch {
	case requestErr != nil:
		retry = retryableNetworkError(requestErr)
	case status == http.StatusTooManyRequests:
		retry = true
	case status == http.StatusBadGateway,
		status == http.StatusServiceUnavailable,
		status == http.StatusGatewayTimeout:
		retry = true
	}
	if !retry {
		return retryDecision{}
	}

	delay := exponentialBackoff(policy, retriesCompleted)
	if status == http.StatusTooManyRequests {
		if parsed, ok := ParseRetryAfter(header.Get("Retry-After"), now); ok {
			delay = parsed
		}
	}
	if delay > policy.MaxBackoff {
		delay = policy.MaxBackoff
	}
	if delay < 0 {
		delay = 0
	}
	return retryDecision{retry: true, delay: delay}
}

func retryableMethod(method string, retryWrite bool) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return retryWrite
	default:
		return false
	}
}

func retryableNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var providerErr *Error
	if errors.As(err, &providerErr) {
		switch providerErr.Code() {
		case connector.FailurePolicyDenied,
			connector.FailureInvalidInput,
			connector.FailureConfigurationError,
			connector.FailureAuthorizationFailed,
			connector.FailurePermissionDenied,
			connector.FailureNotFound,
			connector.FailureConflict,
			connector.FailureRateLimited,
			connector.FailureInvalidResponse,
			connector.FailureResponseTooLarge,
			connector.FailureTimeout,
			connector.FailureCanceled:
			return false
		}
	}

	// url.Error itself implements net.Error even when its cause is permanent.
	// Peel it before classifying the actual transport failure.
	var urlErr *url.Error
	for errors.As(err, &urlErr) && urlErr != nil && urlErr.Err != nil {
		err = urlErr.Err
		urlErr = nil
	}

	// Certificate/protocol failures are deterministic for the same endpoint
	// and configuration. Retrying them only burns the request budget.
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	var verificationError *tls.CertificateVerificationError
	var recordHeaderError tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) ||
		errors.As(err, &certificateInvalid) ||
		errors.As(err, &verificationError) ||
		errors.As(err, &recordHeaderError) {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Timeout() || dnsErr.Temporary()
	}
	var unknownNetwork net.UnknownNetworkError
	if errors.As(err, &unknownNetwork) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
		if temporary, ok := netErr.(interface{ Temporary() bool }); ok {
			return temporary.Temporary()
		}
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func exponentialBackoff(policy RetryPolicy, retriesCompleted int) time.Duration {
	if policy.InitialBackoff <= 0 {
		return 0
	}
	if retriesCompleted <= 0 {
		return policy.InitialBackoff
	}
	multiplier := math.Pow(2, float64(retriesCompleted))
	if multiplier >= float64(math.MaxInt64)/float64(policy.InitialBackoff) {
		return policy.MaxBackoff
	}
	return time.Duration(float64(policy.InitialBackoff) * multiplier)
}

// ParseRetryAfter 解析 RFC 9110 Retry-After 的 delta-seconds 或 HTTP-date。
func ParseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			if _, err := http.ParseTime(raw); err != nil {
				return 0, false
			}
			break
		}
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	at, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	if !at.After(now) {
		return 0, true
	}
	return at.Sub(now), true
}
