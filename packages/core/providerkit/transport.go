package providerkit

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

const (
	defaultDialTimeout           = 10 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultResponseHeaderTimeout = 15 * time.Second
	defaultIdleConnTimeout       = 90 * time.Second
	defaultMaxResponseHeader     = 1 << 20
)

// Resolver is the DNS boundary used by the guarded transport. Every returned
// address is validated and the accepted answer is pinned to the subsequent
// connection attempt.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type defaultResolver struct{}

func (defaultResolver) LookupNetIP(
	ctx context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

// DialContextFunc matches net.Dialer's DialContext method. Tests can inject a
// deterministic dialer without changing package globals.
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

func defaultDialContext() DialContextFunc {
	dialer := &net.Dialer{
		Timeout:   defaultDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	return dialer.DialContext
}

type pinnedResolution struct {
	host      string
	port      string
	addresses []netip.Addr
}

type pinnedResolutionContextKey struct{}

type redirectApproval struct {
	origin string
}

type redirectApprovalContextKey struct{}

type policyViolationError struct {
	err     *Error
	outcome PolicyOutcome
}

func (e *policyViolationError) Error() string {
	if e == nil || e.err == nil {
		return defaultSafeMessage(connector.FailurePolicyDenied)
	}
	return e.err.Error()
}

func (e *policyViolationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type guardedTransport struct {
	policy       Policy
	transport    *http.Transport
	resolver     Resolver
	dialContext  DialContextFunc
	allowPrivate bool
}

func newGuardedTransport(
	policy Policy,
	resolver Resolver,
	dialContext DialContextFunc,
	allowPrivate bool,
	rootCAs ...*x509.CertPool,
) *guardedTransport {
	responseHeaderTimeout := defaultResponseHeaderTimeout
	if policy.RequestTimeout > 0 && policy.RequestTimeout < responseHeaderTimeout {
		responseHeaderTimeout = policy.RequestTimeout
	}

	guarded := &guardedTransport{
		policy:       policy,
		resolver:     resolver,
		dialContext:  dialContext,
		allowPrivate: allowPrivate,
	}
	var roots *x509.CertPool
	if len(rootCAs) > 0 && rootCAs[0] != nil {
		roots = rootCAs[0].Clone()
	}
	guarded.transport = &http.Transport{
		// Environment proxy variables are deliberately ignored. A proxy could
		// resolve the hostname again and invalidate the DNS pin.
		Proxy:                  nil,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    defaultTLSHandshakeTimeout,
		ResponseHeaderTimeout:  responseHeaderTimeout,
		IdleConnTimeout:        defaultIdleConnTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: defaultMaxResponseHeader,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
	guarded.transport.DialContext = guarded.dialPinned
	return guarded
}

func (transport *guardedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.transport == nil || request == nil ||
		request.URL == nil {
		return nil, policyViolation(
			PolicyOutcomeBlockedOrigin,
			"provider request was denied by policy",
		)
	}
	if hasHeaderFold(request.Header, "Referer") {
		return nil, policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider request was denied by policy",
		)
	}
	validated, err := ParseAndValidateURL(request.URL.String())
	if err != nil {
		return nil, policyViolation(
			PolicyOutcomeBlockedOrigin,
			"provider request was denied by policy",
		)
	}
	if validated.Scheme == "http" && !transport.policy.AllowPlainHTTP {
		return nil, policyViolation(
			PolicyOutcomeBlockedScheme,
			"provider request was denied by policy",
		)
	}

	origin, err := CanonicalOrigin(validated)
	if err != nil {
		return nil, policyViolation(
			PolicyOutcomeBlockedOrigin,
			"provider request was denied by policy",
		)
	}
	if request.Host != "" {
		hostOrigin, hostErr := CanonicalOrigin(&url.URL{
			Scheme: validated.Scheme,
			Host:   request.Host,
		})
		if hostErr != nil || hostOrigin != origin {
			return nil, policyViolation(
				PolicyOutcomeBlockedOrigin,
				"provider request was denied by policy",
			)
		}
	}
	allowed := slices.Contains(transport.policy.AllowedOrigins, origin)
	if approval, ok := request.Context().Value(
		redirectApprovalContextKey{},
	).(redirectApproval); ok && approval.origin == origin {
		allowed = true
	}
	if !allowed {
		return nil, policyViolation(
			PolicyOutcomeBlockedOrigin,
			"provider request was denied by policy",
		)
	}

	_, host, port, err := canonicalAuthority(validated)
	if err != nil {
		return nil, policyViolation(
			PolicyOutcomeBlockedOrigin,
			"provider request was denied by policy",
		)
	}
	addresses, err := transport.resolve(request.Context(), host)
	if err != nil {
		var rejected *ResolvedIPError
		if errors.As(err, &rejected) {
			return nil, policyViolation(
				PolicyOutcomeBlockedIP,
				"provider request was denied by policy",
			)
		}
		return nil, knownError(
			connector.FailureUpstreamUnavailable,
			defaultSafeMessage(connector.FailureUpstreamUnavailable),
			WithCause(err),
		)
	}

	clone := request.Clone(context.WithValue(
		request.Context(),
		pinnedResolutionContextKey{},
		pinnedResolution{
			host:      host,
			port:      port,
			addresses: append([]netip.Addr(nil), addresses...),
		},
	))
	clone.URL = validated
	return transport.transport.RoundTrip(clone)
}

func (transport *guardedTransport) resolve(
	ctx context.Context,
	host string,
) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		addresses := []netip.Addr{literal}
		if err := ValidateResolvedIPs(addresses, transport.allowPrivate); err != nil {
			return nil, err
		}
		return addresses, nil
	}
	if transport.resolver == nil {
		return nil, errors.New("providerkit: resolver is unavailable")
	}
	addresses, err := transport.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if err := ValidateResolvedIPs(addresses, transport.allowPrivate); err != nil {
		return nil, err
	}
	return append([]netip.Addr(nil), addresses...), nil
}

func (transport *guardedTransport) dialPinned(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	pin, ok := ctx.Value(pinnedResolutionContextKey{}).(pinnedResolution)
	if !ok || transport.dialContext == nil {
		return nil, errors.New("providerkit: missing pinned DNS resolution")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("providerkit: malformed dial target")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host != pin.host || port != pin.port {
		return nil, errors.New("providerkit: dial target does not match DNS pin")
	}

	var lastErr error
	for _, candidate := range pin.addresses {
		if network == "tcp4" && !candidate.Is4() {
			continue
		}
		if network == "tcp6" && !candidate.Is6() {
			continue
		}
		target := net.JoinHostPort(candidate.String(), pin.port)
		connection, dialErr := transport.dialContext(ctx, network, target)
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("providerkit: DNS pin has no compatible address")
	}
	return nil, lastErr
}

func (transport *guardedTransport) CloseIdleConnections() {
	if transport != nil && transport.transport != nil {
		transport.transport.CloseIdleConnections()
	}
}

func policyViolation(outcome PolicyOutcome, message string) error {
	return &policyViolationError{
		err: knownError(
			connector.FailurePolicyDenied,
			message,
		),
		outcome: outcome,
	}
}

func policyOutcomeForError(err error) PolicyOutcome {
	var violation *policyViolationError
	if errors.As(err, &violation) && violation != nil &&
		violation.outcome.Valid() {
		return violation.outcome
	}
	return PolicyOutcomeAllowed
}

func hasHeaderFold(header http.Header, name string) bool {
	for rawName := range header {
		if strings.EqualFold(rawName, name) {
			return true
		}
	}
	return false
}

func deleteHeaderFold(header http.Header, name string) {
	for rawName := range header {
		if strings.EqualFold(rawName, name) {
			delete(header, rawName)
		}
	}
}

func approveRedirect(request *http.Request, origin string) {
	if request == nil {
		return
	}
	*request = *request.WithContext(context.WithValue(
		request.Context(),
		redirectApprovalContextKey{},
		redirectApproval{origin: origin},
	))
}

var _ http.RoundTripper = (*guardedTransport)(nil)
