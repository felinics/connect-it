package providerkit

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxProviderURLBytes       = 16 << 10
	maxProviderPathBytes      = 8 << 10
	maxPathUnescapeIterations = 32
)

// URLValidationError describes a URL policy violation without retaining or
// returning the original URL, which may contain credentials in its query.
type URLValidationError struct {
	Reason string
}

func (e *URLValidationError) Error() string {
	if e == nil || e.Reason == "" {
		return "invalid provider URL"
	}
	return "invalid provider URL: " + e.Reason
}

// ParseAndValidateURL parses an absolute Provider URL, validates its scheme,
// authority and path, and returns a copy with a canonical scheme and host.
//
// Queries are allowed because some Provider APIs use query parameters.
// Userinfo, fragments and ambiguous path traversal forms are rejected.
func ParseAndValidateURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, urlError("must not be empty")
	}
	if len(raw) > maxProviderURLBytes {
		return nil, urlError("is too long")
	}
	if strings.TrimSpace(raw) != raw {
		return nil, urlError("must not have leading or trailing whitespace")
	}
	if containsControl(raw) {
		return nil, urlError("must not contain control characters")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, urlError("cannot be parsed")
	}
	if !u.IsAbs() || u.Opaque != "" {
		return nil, urlError("must be an absolute hierarchical URL")
	}
	if u.User != nil {
		return nil, urlError("must not contain userinfo")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return nil, urlError("must not contain a fragment")
	}
	if err := validatePath(u); err != nil {
		return nil, err
	}

	scheme, host, port, err := canonicalAuthority(u)
	if err != nil {
		return nil, err
	}

	normalized := *u
	normalized.Scheme = scheme
	normalized.Host = formatHostPort(host, port)
	return &normalized, nil
}

// NormalizeBaseURL validates and returns the canonical serialized form used as
// a dynamic Provider policy identity. It rejects query and fragment data so a
// credential/config URL cannot smuggle request-specific parameters into every
// Provider call. Default ports are made explicit by ParseAndValidateURL.
func NormalizeBaseURL(raw string) (string, error) {
	u, err := parseBaseURL(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// CanonicalOrigin returns scheme://lowercase-ascii-host:effective-port for an
// absolute HTTP(S) URL. The returned origin never includes path, query or
// fragment data.
func CanonicalOrigin(u *url.URL) (string, error) {
	if u == nil {
		return "", urlError("must not be nil")
	}
	if u.User != nil {
		return "", urlError("must not contain userinfo")
	}
	scheme, host, port, err := canonicalAuthority(u)
	if err != nil {
		return "", err
	}
	return scheme + "://" + formatHostPort(host, port), nil
}

// CanonicalizeOrigin validates and canonicalizes a serialized origin. Only an
// empty path or "/" is accepted; query, fragment and userinfo are forbidden.
func CanonicalizeOrigin(raw string) (string, error) {
	u, err := ParseAndValidateURL(raw)
	if err != nil {
		return "", err
	}
	if u.Path != "" && u.Path != "/" {
		return "", urlError("origin must not contain a path")
	}
	if u.RawPath != "" && u.EscapedPath() != "/" {
		return "", urlError("origin must not contain an encoded path")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", urlError("origin must not contain a query")
	}
	return CanonicalOrigin(u)
}

// ValidateURLForOrigin validates raw and verifies that its canonical origin is
// in allowedOrigins. allowedOrigins are canonicalized before comparison, so
// callers may safely provide either implicit or explicit default ports.
func ValidateURLForOrigin(raw string, allowedOrigins []string) (*url.URL, error) {
	u, err := ParseAndValidateURL(raw)
	if err != nil {
		return nil, err
	}
	origin, err := CanonicalOrigin(u)
	if err != nil {
		return nil, err
	}

	for _, allowed := range allowedOrigins {
		canonical, canonicalErr := CanonicalizeOrigin(allowed)
		if canonicalErr != nil {
			return nil, urlError("allowed origin is invalid")
		}
		if canonical == origin {
			return u, nil
		}
	}
	return nil, urlError("origin is not allowed")
}

// IsSameOrigin compares URLs using their canonical scheme, hostname and
// effective port.
func IsSameOrigin(a, b *url.URL) (bool, error) {
	aOrigin, err := CanonicalOrigin(a)
	if err != nil {
		return false, err
	}
	bOrigin, err := CanonicalOrigin(b)
	if err != nil {
		return false, err
	}
	return aOrigin == bOrigin, nil
}

func canonicalAuthority(u *url.URL) (scheme, host, port string, err error) {
	if u == nil {
		return "", "", "", urlError("must not be nil")
	}

	scheme = strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		port = "443"
	case "http":
		port = "80"
	default:
		return "", "", "", urlError("scheme must be http or https")
	}
	if u.Host == "" {
		return "", "", "", urlError("hostname must not be empty")
	}

	rawHost, rawPort, hasPort, splitErr := splitAuthority(u.Host)
	if splitErr != nil {
		return "", "", "", splitErr
	}
	host, err = canonicalHostname(rawHost)
	if err != nil {
		return "", "", "", err
	}
	if hasPort {
		port, err = canonicalPort(rawPort)
		if err != nil {
			return "", "", "", err
		}
	}
	return scheme, host, port, nil
}

func splitAuthority(authority string) (host, port string, hasPort bool, err error) {
	if strings.HasPrefix(authority, "[") {
		closeBracket := strings.IndexByte(authority, ']')
		if closeBracket < 0 {
			return "", "", false, urlError("IPv6 hostname must have a closing bracket")
		}
		host = authority[1:closeBracket]
		remainder := authority[closeBracket+1:]
		switch {
		case remainder == "":
			return host, "", false, nil
		case strings.HasPrefix(remainder, ":"):
			if len(remainder) == 1 {
				return "", "", false, urlError("port must not be empty")
			}
			return host, remainder[1:], true, nil
		default:
			return "", "", false, urlError("authority is malformed")
		}
	}

	switch strings.Count(authority, ":") {
	case 0:
		return authority, "", false, nil
	case 1:
		host, port, _ = strings.Cut(authority, ":")
		if host == "" {
			return "", "", false, urlError("hostname must not be empty")
		}
		if port == "" {
			return "", "", false, urlError("port must not be empty")
		}
		return host, port, true, nil
	default:
		return "", "", false, urlError("IPv6 hostname must be bracketed")
	}
}

func canonicalHostname(raw string) (string, error) {
	if raw == "" {
		return "", urlError("hostname must not be empty")
	}
	if !isASCII(raw) {
		return "", urlError("hostname must be ASCII")
	}
	if strings.Contains(raw, "%") {
		return "", urlError("IPv6 zone identifiers are not allowed")
	}
	if strings.HasSuffix(raw, ".") {
		return "", urlError("hostname must not have a trailing dot")
	}

	lower := strings.ToLower(raw)
	if strings.Contains(lower, ":") {
		addr, err := netip.ParseAddr(lower)
		if err != nil || !addr.Is6() || addr.Is4In6() {
			return "", urlError("IPv6 hostname is invalid")
		}
		if addr.String() != lower {
			return "", urlError("numeric hostname must use canonical notation")
		}
		return lower, nil
	}

	if addr, err := netip.ParseAddr(lower); err == nil {
		if !addr.Is4() || addr.String() != lower {
			return "", urlError("numeric hostname must use canonical notation")
		}
		return lower, nil
	}
	if looksLikeNumericHost(lower) {
		return "", urlError("numeric hostname must use canonical dotted-decimal notation")
	}
	if err := validateDNSName(lower); err != nil {
		return "", err
	}
	return lower, nil
}

func canonicalPort(raw string) (string, error) {
	if raw == "" {
		return "", urlError("port must not be empty")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return "", urlError("port must be decimal")
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 65535 {
		return "", urlError("port must be between 1 and 65535")
	}
	if strconv.Itoa(value) != raw {
		return "", urlError("port must use canonical decimal notation")
	}
	return raw, nil
}

func validateDNSName(host string) error {
	if len(host) > 253 {
		return urlError("hostname is too long")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return urlError("hostname contains an empty label")
		}
		if len(label) > 63 {
			return urlError("hostname label is too long")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return urlError("hostname label must not start or end with a hyphen")
		}
		if strings.HasPrefix(label, "xn--") {
			return urlError("IDNA hostnames are not supported")
		}
		for _, c := range label {
			if (c >= 'a' && c <= 'z') ||
				(c >= '0' && c <= '9') ||
				c == '-' {
				continue
			}
			return urlError("hostname contains an invalid character")
		}
	}
	return nil
}

func looksLikeNumericHost(host string) bool {
	if host == "" {
		return false
	}

	allDigitsAndDots := true
	for _, c := range host {
		if (c < '0' || c > '9') && c != '.' {
			allDigitsAndDots = false
			break
		}
	}
	if allDigitsAndDots {
		return true
	}

	for _, label := range strings.Split(host, ".") {
		if strings.HasPrefix(label, "0x") && len(label) > 2 {
			if _, err := strconv.ParseUint(label[2:], 16, 32); err == nil {
				return true
			}
		}
	}
	return false
}

func validatePath(u *url.URL) error {
	if len(u.Path) > maxProviderPathBytes ||
		len(u.RawPath) > maxProviderPathBytes ||
		len(u.EscapedPath()) > maxProviderPathBytes {
		return urlError("path is too long")
	}
	if u.Path != "" && !strings.HasPrefix(u.Path, "/") {
		return urlError("path must be absolute")
	}
	if strings.Contains(u.Path, "\\") || strings.Contains(u.RawPath, "\\") {
		return urlError("path must not contain backslashes")
	}
	if u.RawPath != "" {
		decoded, err := url.PathUnescape(u.RawPath)
		if err != nil || decoded != u.Path {
			return urlError("encoded path is malformed")
		}
	}

	path := u.EscapedPath()
	for range maxPathUnescapeIterations {
		if hasDotPathSegment(path) {
			return urlError("path traversal is not allowed")
		}
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return urlError("encoded path is malformed")
		}
		if decoded == path {
			return nil
		}
		path = decoded
		if strings.Contains(path, "\\") {
			return urlError("path must not contain encoded backslashes")
		}
	}
	if hasDotPathSegment(path) || strings.Contains(path, "\\") {
		return urlError("path traversal is not allowed")
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return urlError("encoded path is malformed")
	}
	if decoded != path {
		return urlError("path encoding is too deep")
	}
	return nil
}

func hasDotPathSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func formatHostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return fmt.Sprintf("[%s]:%s", host, port)
	}
	return host + ":" + port
}

func isASCII(value string) bool {
	for i := range len(value) {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

func urlError(reason string) error {
	return &URLValidationError{Reason: reason}
}
