package providerkit_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

func TestCanonicalOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "HTTPS default port and lowercase hostname",
			raw:  "HTTPS://API.Example.COM/v1?q=ignored",
			want: "https://api.example.com:443",
		},
		{
			name: "HTTP default port",
			raw:  "http://example.com/path",
			want: "http://example.com:80",
		},
		{
			name: "non-default port",
			raw:  "https://example.com:8443/path",
			want: "https://example.com:8443",
		},
		{
			name: "IPv4",
			raw:  "https://192.0.2.1/",
			want: "https://192.0.2.1:443",
		},
		{
			name: "IPv6",
			raw:  "https://[2001:db8::1]:8443/",
			want: "https://[2001:db8::1]:8443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatalf("url.Parse() error = %v", err)
			}
			got, err := providerkit.CanonicalOrigin(u)
			if err != nil {
				t.Fatalf("CanonicalOrigin() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("CanonicalOrigin() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseAndValidateURLNormalizesAuthority(t *testing.T) {
	t.Parallel()

	got, err := providerkit.ParseAndValidateURL(
		"HTTPS://API.Example.COM/v1/items?cursor=next",
	)
	if err != nil {
		t.Fatalf("ParseAndValidateURL() error = %v", err)
	}
	if got.String() != "https://api.example.com:443/v1/items?cursor=next" {
		t.Fatalf("ParseAndValidateURL() = %q", got.String())
	}
}

func TestNormalizeBaseURLCanonicalizesAndRejectsRequestData(t *testing.T) {
	t.Parallel()

	got, err := providerkit.NormalizeBaseURL("HTTPS://GitLab.Example/api/v4")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://gitlab.example:443/api/v4" {
		t.Fatalf("NormalizeBaseURL() = %q", got)
	}
	for _, raw := range []string{
		"https://gitlab.example/api?token=secret",
		"https://gitlab.example/api#fragment",
		"https://user:secret@gitlab.example/api",
		"https://gitlab.example/a/../api",
	} {
		if _, err := providerkit.NormalizeBaseURL(raw); err == nil {
			t.Fatalf("NormalizeBaseURL(%q) error = nil", raw)
		}
	}
}

func TestParseAndValidateURLRejectsAmbiguousInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"leading whitespace", " https://example.com"},
		{"trailing whitespace", "https://example.com "},
		{"control", "https://example.com/\nsecret"},
		{"relative", "/v1/items"},
		{"opaque", "https:opaque"},
		{"FTP", "ftp://example.com/file"},
		{"userinfo username", "https://user@example.com"},
		{"userinfo password", "https://user:secret@example.com"},
		{"empty host", "https:///path"},
		{"empty port", "https://example.com:"},
		{"named port", "https://example.com:https"},
		{"zero port", "https://example.com:0"},
		{"leading zero port", "https://example.com:0443"},
		{"out of range port", "https://example.com:65536"},
		{"Unicode hostname", "https://例子.example"},
		{"IDNA hostname", "https://xn--fsqu00a.example"},
		{"trailing hostname dot", "https://example.com."},
		{"IPv6 zone", "https://[fe80::1%25eth0]"},
		{"expanded IPv6", "https://[2001:0db8:0:0:0:0:0:1]"},
		{"unbracketed IPv6", "https://2001:db8::1"},
		{"short IPv4", "https://127.1"},
		{"integer IPv4", "https://2130706433"},
		{"octal IPv4", "https://0177.0.0.1"},
		{"hex IPv4", "https://0x7f.0.0.1"},
		{"empty DNS label", "https://api..example.com"},
		{"leading hyphen", "https://-api.example.com"},
		{"underscore", "https://api_name.example.com"},
		{"fragment", "https://example.com/path#fragment"},
		{"literal dot segment", "https://example.com/a/../secret"},
		{"encoded dot segment", "https://example.com/a/%2e%2e/secret"},
		{"double encoded dot segment", "https://example.com/a/%252e%252e/secret"},
		{"deeply encoded dot segment", "https://example.com/a/%252525252e%252525252e/secret"},
		{"literal backslash", `https://example.com/a\\..\\secret`},
		{"encoded backslash", "https://example.com/a/%5c../secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := providerkit.ParseAndValidateURL(tt.raw)
			if err == nil {
				t.Fatal("ParseAndValidateURL() error = nil")
			}
			var validationErr *providerkit.URLValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("error type = %T, want *URLValidationError", err)
			}
			if validationErr.Reason == "" {
				t.Fatal("URLValidationError.Reason is empty")
			}
			if errText := err.Error(); contains(errText, tt.raw) {
				t.Fatalf("error leaks input URL: %q", errText)
			}
		})
	}
}

func TestParseAndValidateURLBoundsLengthAndDecodeDepth(t *testing.T) {
	t.Parallel()

	tooLongPath := "https://example.com/" + strings.Repeat("a", 16<<10)
	if _, err := providerkit.ParseAndValidateURL(tooLongPath); err == nil {
		t.Fatal("overlong Provider URL was accepted")
	}

	encodedDots := "%2e%2e"
	for range 40 {
		encodedDots = strings.ReplaceAll(encodedDots, "%", "%25")
	}
	if _, err := providerkit.ParseAndValidateURL(
		"https://example.com/a/" + encodedDots + "/secret",
	); err == nil {
		t.Fatal("excessively nested path encoding was accepted")
	}

	// A normal signed-query-sized URL remains well below the absolute cap.
	if _, err := providerkit.ParseAndValidateURL(
		"https://example.com/download?signature=" +
			strings.Repeat("a", 4096),
	); err != nil {
		t.Fatalf("reasonable signed URL was rejected: %v", err)
	}
}

func TestCanonicalizeOrigin(t *testing.T) {
	t.Parallel()

	valid := []struct {
		raw  string
		want string
	}{
		{"https://EXAMPLE.com", "https://example.com:443"},
		{"https://example.com/", "https://example.com:443"},
		{"http://example.com:8080", "http://example.com:8080"},
	}
	for _, tt := range valid {
		got, err := providerkit.CanonicalizeOrigin(tt.raw)
		if err != nil {
			t.Fatalf("CanonicalizeOrigin(%q) error = %v", tt.raw, err)
		}
		if got != tt.want {
			t.Errorf("CanonicalizeOrigin(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}

	invalid := []string{
		"https://example.com/v1",
		"https://example.com?key=secret",
		"https://example.com/#fragment",
		"https://user@example.com",
	}
	for _, raw := range invalid {
		if _, err := providerkit.CanonicalizeOrigin(raw); err == nil {
			t.Errorf("CanonicalizeOrigin(%q) error = nil", raw)
		}
	}
}

func TestValidateURLForOriginUsesExactCanonicalOrigin(t *testing.T) {
	t.Parallel()

	allowed := []string{"https://api.example.com"}
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"exact", "https://api.example.com/v1", false},
		{"explicit default port", "https://api.example.com:443/v1", false},
		{"subdomain", "https://sub.api.example.com/v1", true},
		{"suffix confusion", "https://api.example.com.attacker.invalid/v1", true},
		{"different scheme", "http://api.example.com/v1", true},
		{"different port", "https://api.example.com:8443/v1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := providerkit.ValidateURLForOrigin(tt.raw, allowed)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateURLForOrigin() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateURLForOrigin() error = %v", err)
			}
		})
	}
}

func TestValidateURLForOriginRejectsInvalidAllowlist(t *testing.T) {
	t.Parallel()

	_, err := providerkit.ValidateURLForOrigin(
		"https://api.example.com/v1",
		[]string{"https://api.example.com/path"},
	)
	if err == nil {
		t.Fatal("ValidateURLForOrigin() error = nil")
	}
	if contains(err.Error(), "api.example.com") {
		t.Fatalf("error leaks allowed origin: %q", err)
	}
}

func TestIsSameOrigin(t *testing.T) {
	t.Parallel()

	parse := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q) error = %v", raw, err)
		}
		return u
	}

	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"default port", "https://EXAMPLE.com/a", "https://example.com:443/b", true},
		{"scheme", "https://example.com", "http://example.com", false},
		{"port", "https://example.com", "https://example.com:8443", false},
		{"host", "https://example.com", "https://sub.example.com", false},
	}
	for _, tt := range tests {
		got, err := providerkit.IsSameOrigin(parse(tt.a), parse(tt.b))
		if err != nil {
			t.Fatalf("IsSameOrigin() error = %v", err)
		}
		if got != tt.want {
			t.Errorf("IsSameOrigin(%q, %q) = %t, want %t", tt.a, tt.b, got, tt.want)
		}
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
