package providerkit

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ErrInvalidAuthorizerConfig is returned when an authorizer is constructed with
// an empty credential, an invalid HTTP field, or another unsafe value. It never
// includes the rejected value, because that value may itself be a credential.
var ErrInvalidAuthorizerConfig = errors.New("invalid authorizer configuration")

// CredentialFootprint describes every request location that an Authorizer may
// modify with credential material.
//
// The guarded redirect and logging paths use the same footprint to remove and
// redact credentials. Implementations must therefore declare the complete
// footprint, including fields added by custom signing schemes.
type CredentialFootprint struct {
	HeaderNames     []string
	QueryParamNames []string
	Body            bool
}

// Clone returns a deep copy of f.
func (f CredentialFootprint) Clone() CredentialFootprint {
	return CredentialFootprint{
		HeaderNames:     append([]string(nil), f.HeaderNames...),
		QueryParamNames: append([]string(nil), f.QueryParamNames...),
		Body:            f.Body,
	}
}

// Authorizer adds credentials to an outbound Provider request and declares
// exactly where it placed them.
type Authorizer interface {
	Apply(*http.Request) error
	Footprint() CredentialFootprint
}

type authorizer struct {
	apply     func(*http.Request) error
	footprint CredentialFootprint
}

func (a authorizer) Apply(req *http.Request) error {
	if req == nil {
		return ErrInvalidAuthorizerConfig
	}
	return a.apply(req)
}

func (a authorizer) Footprint() CredentialFootprint {
	return a.footprint.Clone()
}

// Bearer constructs an Authorization: Bearer authorizer.
func Bearer(token string) (Authorizer, error) {
	if !validCredentialValue(token) {
		return nil, ErrInvalidAuthorizerConfig
	}

	return headerAuthorizer("Authorization", "Bearer "+token), nil
}

// HeaderAPIKey constructs an authorizer that writes value to name.
//
// Host, Content-Length and Referer are intentionally rejected: they are
// request-routing metadata rather than supported credential fields.
func HeaderAPIKey(name, value string) (Authorizer, error) {
	if !validHeaderName(name) || !validCredentialValue(value) ||
		isReservedAuthorizerHeader(name) {
		return nil, ErrInvalidAuthorizerConfig
	}

	return headerAuthorizer(http.CanonicalHeaderKey(name), value), nil
}

// QueryAPIKey constructs an authorizer that writes a Provider-mandated query
// credential. Query credentials are overwritten rather than appended so a
// caller cannot preserve an attacker-controlled duplicate.
func QueryAPIKey(name, value string) (Authorizer, error) {
	if !validQueryName(name) || !validCredentialValue(value) {
		return nil, ErrInvalidAuthorizerConfig
	}

	footprint := CredentialFootprint{QueryParamNames: []string{name}}
	return authorizer{
		apply: func(req *http.Request) error {
			if req.URL == nil {
				return ErrInvalidAuthorizerConfig
			}
			query := req.URL.Query()
			query.Set(name, value)
			req.URL.RawQuery = query.Encode()
			return nil
		},
		footprint: footprint,
	}, nil
}

// Basic constructs an HTTP Basic authorizer.
func Basic(username, password string) (Authorizer, error) {
	// RFC 7617 uses the first colon as the user/password delimiter, so a colon
	// in the user-id cannot be represented unambiguously.
	if username == "" || strings.Contains(username, ":") ||
		!validCredentialValue(username) || !validHeaderValue(password) {
		return nil, ErrInvalidAuthorizerConfig
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return headerAuthorizer("Authorization", "Basic "+encoded), nil
}

// Authorization constructs an authorizer for Provider-specific Authorization
// schemes. prefix and suffix are literal protocol syntax; value is the secret.
func Authorization(prefix, value, suffix string) (Authorizer, error) {
	if !validCredentialValue(value) ||
		!validHeaderValue(prefix) ||
		!validHeaderValue(suffix) {
		return nil, ErrInvalidAuthorizerConfig
	}

	headerValue := prefix + value + suffix
	if !validHeaderValue(headerValue) || headerValue == "" {
		return nil, ErrInvalidAuthorizerConfig
	}
	return headerAuthorizer("Authorization", headerValue), nil
}

// NoAuth constructs an authorizer that does not modify a request.
func NoAuth() Authorizer {
	return authorizer{
		apply:     func(*http.Request) error { return nil },
		footprint: CredentialFootprint{},
	}
}

func headerAuthorizer(name, value string) Authorizer {
	footprint := CredentialFootprint{HeaderNames: []string{name}}
	return authorizer{
		apply: func(req *http.Request) error {
			if req.Header == nil {
				req.Header = make(http.Header)
			}
			req.Header.Set(name, value)
			return nil
		},
		footprint: footprint,
	}
}

func validCredentialValue(value string) bool {
	return value != "" && validHeaderValue(value)
}

func validHeaderValue(value string) bool {
	for _, r := range value {
		// Header values are deliberately stricter than net/http's wire
		// validation. Credentials never need control characters.
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isHTTPTokenByte(name[i]) {
			return false
		}
	}
	return true
}

func isHTTPTokenByte(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func isReservedAuthorizerHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Length", "Referer":
		return true
	default:
		return false
	}
}

func validQueryName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}

	// url.Values.Encode is the canonical encoder used by Apply. Round-tripping
	// here catches malformed names without ever putting the credential in an
	// error message.
	encoded := url.Values{name: {"x"}}.Encode()
	parsed, err := url.ParseQuery(encoded)
	return err == nil && parsed.Get(name) == "x" && len(parsed) == 1
}
