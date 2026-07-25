package providerkit

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultRequestTimeout is the total budget for one logical Provider call.
	DefaultRequestTimeout = 30 * time.Second

	// DefaultMaxResponseBytes is the default limit applied to decoded response
	// bodies. File transfer APIs use a separate, explicitly bounded path.
	DefaultMaxResponseBytes int64 = 10 << 20

	// MaxProviderResponseBytes is the absolute in-memory response cap for the
	// ordinary JSON/text client. File transit uses a separate bounded API.
	MaxProviderResponseBytes int64 = 64 << 20

	// DefaultMaxRetries is the number of retries after the initial attempt for
	// retryable, idempotent requests.
	DefaultMaxRetries = 2

	DefaultInitialBackoff = 200 * time.Millisecond
	DefaultMaxBackoff     = 2 * time.Second
)

// NetworkMode describes the maximum network reach a Provider may request.
//
// PublicOnly is deliberately the zero value. Deployment configuration may
// further restrict a policy, but it must never widen a PublicOnly policy.
type NetworkMode uint8

const (
	PublicOnly NetworkMode = iota
	SelfHostedOptIn
)

func (m NetworkMode) String() string {
	switch m {
	case PublicOnly:
		return "public_only"
	case SelfHostedOptIn:
		return "self_hosted_opt_in"
	default:
		return "unknown"
	}
}

// RedirectMode controls whether this protocol endpoint may follow redirects.
// OAuth token/refresh endpoints use RedirectDenyAll.
type RedirectMode uint8

const (
	RedirectFollowPolicy RedirectMode = iota
	RedirectDenyAll
)

// RetryPolicy contains policy limits used by the retry implementation.
//
// MaxRetries counts attempts after the initial request. A completely zero
// RetryPolicy selects DefaultRetryPolicy.
type RetryPolicy struct {
	Disabled       bool
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// DefaultRetryPolicy returns the default retry policy.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     DefaultMaxRetries,
		InitialBackoff: DefaultInitialBackoff,
		MaxBackoff:     DefaultMaxBackoff,
	}
}

// Policy is the complete non-secret egress policy bound to a Provider client.
// Credentials and Tool arguments must never be stored in this value.
type Policy struct {
	Provider               string
	BaseURL                string
	AllowedOrigins         []string
	AllowedRedirectOrigins []string
	RedirectMode           RedirectMode
	NetworkMode            NetworkMode
	AllowPlainHTTP         bool
	RequestTimeout         time.Duration
	MaxResponseBytes       int64
	Retry                  RetryPolicy
}

// PolicyError identifies an invalid policy field without echoing its value.
// Avoiding the raw value keeps accidental query credentials out of logs.
type PolicyError struct {
	Field  string
	Reason string
}

func (e *PolicyError) Error() string {
	if e == nil {
		return "invalid provider policy"
	}
	if e.Field == "" {
		return "invalid provider policy: " + e.Reason
	}
	return fmt.Sprintf("invalid provider policy field %q: %s", e.Field, e.Reason)
}

// NormalizePolicy validates a Policy, fills safe defaults, canonicalizes all
// origins and returns a defensive copy.
//
// It intentionally does not read deployment configuration. Factory combines
// NetworkMode and AllowPlainHTTP with administrator-controlled deployment
// switches when it constructs a client.
func NormalizePolicy(in Policy) (Policy, error) {
	var out Policy

	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		return out, policyError("Provider", "must not be empty")
	}
	if provider != in.Provider {
		return out, policyError("Provider", "must not have leading or trailing whitespace")
	}
	if len(provider) > 128 || containsControl(provider) {
		return out, policyError("Provider", "must be a short printable label")
	}

	switch in.NetworkMode {
	case PublicOnly, SelfHostedOptIn:
	default:
		return out, policyError("NetworkMode", "is not supported")
	}
	switch in.RedirectMode {
	case RedirectFollowPolicy, RedirectDenyAll:
	default:
		return out, policyError("RedirectMode", "is not supported")
	}
	if in.AllowPlainHTTP && in.NetworkMode != SelfHostedOptIn {
		return out, policyError(
			"AllowPlainHTTP",
			"requires SelfHostedOptIn network mode",
		)
	}

	baseURL, err := parseBaseURL(in.BaseURL)
	if err != nil {
		return out, policyError("BaseURL", errorReason(err))
	}
	if baseURL.Scheme == "http" &&
		(in.NetworkMode != SelfHostedOptIn || !in.AllowPlainHTTP) {
		return out, policyError(
			"BaseURL",
			"plain HTTP requires SelfHostedOptIn and AllowPlainHTTP",
		)
	}

	allowed, err := normalizeOriginList(in.AllowedOrigins, "AllowedOrigins", in)
	if err != nil {
		return out, err
	}
	if len(allowed) == 0 {
		return out, policyError("AllowedOrigins", "must contain at least one origin")
	}
	if err := validateLiteralOrigins(
		allowed,
		in.NetworkMode == SelfHostedOptIn,
	); err != nil {
		return out, policyError("AllowedOrigins", "contains a blocked IP address")
	}

	baseOrigin, err := CanonicalOrigin(baseURL)
	if err != nil {
		return out, policyError("BaseURL", errorReason(err))
	}
	if !slices.Contains(allowed, baseOrigin) {
		return out, policyError("BaseURL", "origin is not present in AllowedOrigins")
	}

	redirects, err := normalizeOriginList(
		in.AllowedRedirectOrigins,
		"AllowedRedirectOrigins",
		in,
	)
	if err != nil {
		return out, err
	}
	if err := validateLiteralOrigins(
		redirects,
		in.NetworkMode == SelfHostedOptIn,
	); err != nil {
		return out, policyError(
			"AllowedRedirectOrigins",
			"contains a blocked IP address",
		)
	}

	requestTimeout := in.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = DefaultRequestTimeout
	}
	if requestTimeout < 0 {
		return out, policyError("RequestTimeout", "must be positive")
	}

	maxResponseBytes := in.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	if maxResponseBytes < 0 {
		return out, policyError("MaxResponseBytes", "must be positive")
	}
	if maxResponseBytes > MaxProviderResponseBytes {
		return out, policyError(
			"MaxResponseBytes",
			"exceeds the ordinary Provider response limit",
		)
	}

	retry, err := normalizeRetryPolicy(in.Retry)
	if err != nil {
		return out, err
	}

	out = Policy{
		Provider:               provider,
		BaseURL:                baseURL.String(),
		AllowedOrigins:         allowed,
		AllowedRedirectOrigins: redirects,
		RedirectMode:           in.RedirectMode,
		NetworkMode:            in.NetworkMode,
		AllowPlainHTTP:         in.AllowPlainHTTP,
		RequestTimeout:         requestTimeout,
		MaxResponseBytes:       maxResponseBytes,
		Retry:                  retry,
	}
	return out, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := ParseAndValidateURL(raw)
	if err != nil {
		return nil, err
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, &URLValidationError{Reason: "base URL must not contain a query"}
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, &URLValidationError{Reason: "base URL must not contain a fragment"}
	}
	return u, nil
}

func normalizeOriginList(values []string, field string, policy Policy) ([]string, error) {
	if values == nil {
		return nil, nil
	}

	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		origin, err := CanonicalizeOrigin(raw)
		if err != nil {
			return nil, policyError(field, errorReason(err))
		}
		u, err := url.Parse(origin)
		if err != nil {
			return nil, policyError(field, "contains an invalid origin")
		}
		if u.Scheme == "http" &&
			(policy.NetworkMode != SelfHostedOptIn || !policy.AllowPlainHTTP) {
			return nil, policyError(
				field,
				"plain HTTP requires SelfHostedOptIn and AllowPlainHTTP",
			)
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		out = append(out, origin)
	}
	return out, nil
}

func normalizeRetryPolicy(in RetryPolicy) (RetryPolicy, error) {
	if in.Disabled {
		if in.MaxRetries != 0 || in.InitialBackoff != 0 ||
			in.MaxBackoff != 0 {
			return RetryPolicy{}, policyError(
				"Retry",
				"Disabled cannot be combined with retry limits",
			)
		}
		return RetryPolicy{Disabled: true}, nil
	}
	if in == (RetryPolicy{}) {
		return DefaultRetryPolicy(), nil
	}
	if in.MaxRetries < 0 {
		return RetryPolicy{}, policyError("Retry.MaxRetries", "must not be negative")
	}

	out := in
	if out.InitialBackoff == 0 {
		out.InitialBackoff = DefaultInitialBackoff
	}
	if out.MaxBackoff == 0 {
		out.MaxBackoff = DefaultMaxBackoff
	}
	if out.InitialBackoff < 0 {
		return RetryPolicy{}, policyError("Retry.InitialBackoff", "must be positive")
	}
	if out.MaxBackoff < 0 {
		return RetryPolicy{}, policyError("Retry.MaxBackoff", "must be positive")
	}
	if out.MaxBackoff < out.InitialBackoff {
		return RetryPolicy{}, policyError(
			"Retry.MaxBackoff",
			"must not be shorter than InitialBackoff",
		)
	}
	return out, nil
}

func validateLiteralOrigins(origins []string, allowPrivate bool) error {
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil {
			return err
		}
		address, err := netip.ParseAddr(parsed.Hostname())
		if err != nil {
			continue
		}
		if err := ValidateResolvedIP(address, allowPrivate); err != nil {
			return err
		}
	}
	return nil
}

func policyError(field, reason string) error {
	return &PolicyError{Field: field, Reason: reason}
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < ' ' || r == '\u007f' {
			return true
		}
	}
	return false
}

func errorReason(err error) string {
	if validationErr, ok := err.(*URLValidationError); ok {
		return validationErr.Reason
	}
	return "is invalid"
}
