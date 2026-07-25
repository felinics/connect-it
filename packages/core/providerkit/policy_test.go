package providerkit_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

func TestNormalizePolicyDefaultsAndCanonicalization(t *testing.T) {
	t.Parallel()

	input := providerkit.Policy{
		Provider: "github",
		BaseURL:  "HTTPS://API.GITHUB.COM/v3",
		AllowedOrigins: []string{
			"https://api.github.com",
			"https://API.GITHUB.COM:443/",
		},
		AllowedRedirectOrigins: []string{
			"https://uploads.github.com",
			"https://UPLOADS.GITHUB.COM:443",
		},
	}

	got, err := providerkit.NormalizePolicy(input)
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}

	want := providerkit.Policy{
		Provider:               "github",
		BaseURL:                "https://api.github.com:443/v3",
		AllowedOrigins:         []string{"https://api.github.com:443"},
		AllowedRedirectOrigins: []string{"https://uploads.github.com:443"},
		NetworkMode:            providerkit.PublicOnly,
		RequestTimeout:         providerkit.DefaultRequestTimeout,
		MaxResponseBytes:       providerkit.DefaultMaxResponseBytes,
		Retry:                  providerkit.DefaultRetryPolicy(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePolicy() = %#v, want %#v", got, want)
	}
}

func TestNormalizePolicyReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()

	allowed := []string{"https://api.example.com"}
	redirects := []string{"https://download.example.com"}
	input := providerkit.Policy{
		Provider:               "example",
		BaseURL:                "https://api.example.com",
		AllowedOrigins:         allowed,
		AllowedRedirectOrigins: redirects,
	}

	first, err := providerkit.NormalizePolicy(input)
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}
	allowed[0] = "https://attacker.invalid"
	redirects[0] = "https://attacker.invalid"
	if first.AllowedOrigins[0] != "https://api.example.com:443" {
		t.Fatalf("AllowedOrigins aliases input: %q", first.AllowedOrigins[0])
	}
	if first.AllowedRedirectOrigins[0] != "https://download.example.com:443" {
		t.Fatalf(
			"AllowedRedirectOrigins aliases input: %q",
			first.AllowedRedirectOrigins[0],
		)
	}

	second, err := providerkit.NormalizePolicy(providerkit.Policy{
		Provider:               "example",
		BaseURL:                "https://api.example.com",
		AllowedOrigins:         []string{"https://api.example.com"},
		AllowedRedirectOrigins: []string{"https://download.example.com"},
	})
	if err != nil {
		t.Fatalf("second NormalizePolicy() error = %v", err)
	}
	first.AllowedOrigins[0] = "https://mutated.invalid:443"
	first.AllowedRedirectOrigins[0] = "https://mutated.invalid:443"
	if second.AllowedOrigins[0] != "https://api.example.com:443" {
		t.Fatal("NormalizePolicy() results share AllowedOrigins backing storage")
	}
	if second.AllowedRedirectOrigins[0] != "https://download.example.com:443" {
		t.Fatal("NormalizePolicy() results share redirect backing storage")
	}
}

func TestNormalizePolicySelfHostedPlainHTTP(t *testing.T) {
	t.Parallel()

	got, err := providerkit.NormalizePolicy(providerkit.Policy{
		Provider:       "gitlab",
		BaseURL:        "http://gitlab.internal/api/v4",
		AllowedOrigins: []string{"http://gitlab.internal"},
		NetworkMode:    providerkit.SelfHostedOptIn,
		AllowPlainHTTP: true,
		Retry: providerkit.RetryPolicy{
			MaxRetries:     0,
			InitialBackoff: 50 * time.Millisecond,
			MaxBackoff:     100 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}
	if got.BaseURL != "http://gitlab.internal:80/api/v4" {
		t.Fatalf("BaseURL = %q", got.BaseURL)
	}
	if got.Retry.MaxRetries != 0 {
		t.Fatalf("Retry.MaxRetries = %d, want explicit zero", got.Retry.MaxRetries)
	}
}

func TestNormalizePolicyRejectsInvalidPolicy(t *testing.T) {
	t.Parallel()

	valid := func() providerkit.Policy {
		return providerkit.Policy{
			Provider:       "example",
			BaseURL:        "https://api.example.com/v1",
			AllowedOrigins: []string{"https://api.example.com"},
		}
	}

	tests := []struct {
		name  string
		field string
		edit  func(*providerkit.Policy)
	}{
		{"empty provider", "Provider", func(p *providerkit.Policy) { p.Provider = "" }},
		{"provider whitespace", "Provider", func(p *providerkit.Policy) { p.Provider = " example" }},
		{"provider control", "Provider", func(p *providerkit.Policy) { p.Provider = "exam\nple" }},
		{"unknown mode", "NetworkMode", func(p *providerkit.Policy) { p.NetworkMode = 99 }},
		{"public plain flag", "AllowPlainHTTP", func(p *providerkit.Policy) { p.AllowPlainHTTP = true }},
		{"public HTTP base", "BaseURL", func(p *providerkit.Policy) {
			p.BaseURL = "http://api.example.com"
			p.AllowedOrigins = []string{"http://api.example.com"}
		}},
		{"self hosted HTTP without flag", "BaseURL", func(p *providerkit.Policy) {
			p.BaseURL = "http://gitlab.internal"
			p.AllowedOrigins = []string{"http://gitlab.internal"}
			p.NetworkMode = providerkit.SelfHostedOptIn
		}},
		{"empty origins", "AllowedOrigins", func(p *providerkit.Policy) { p.AllowedOrigins = nil }},
		{"base origin mismatch", "BaseURL", func(p *providerkit.Policy) {
			p.AllowedOrigins = []string{"https://other.example.com"}
		}},
		{"origin path", "AllowedOrigins", func(p *providerkit.Policy) {
			p.AllowedOrigins = []string{"https://api.example.com/v1"}
		}},
		{"redirect query", "AllowedRedirectOrigins", func(p *providerkit.Policy) {
			p.AllowedRedirectOrigins = []string{"https://download.example.com?token=x"}
		}},
		{"negative timeout", "RequestTimeout", func(p *providerkit.Policy) {
			p.RequestTimeout = -time.Second
		}},
		{"negative response limit", "MaxResponseBytes", func(p *providerkit.Policy) {
			p.MaxResponseBytes = -1
		}},
		{"negative retries", "Retry.MaxRetries", func(p *providerkit.Policy) {
			p.Retry.MaxRetries = -1
		}},
		{"negative initial backoff", "Retry.InitialBackoff", func(p *providerkit.Policy) {
			p.Retry = providerkit.RetryPolicy{
				MaxRetries:     1,
				InitialBackoff: -time.Second,
			}
		}},
		{"negative max backoff", "Retry.MaxBackoff", func(p *providerkit.Policy) {
			p.Retry = providerkit.RetryPolicy{
				MaxRetries: 1,
				MaxBackoff: -time.Second,
			}
		}},
		{"backoff order", "Retry.MaxBackoff", func(p *providerkit.Policy) {
			p.Retry = providerkit.RetryPolicy{
				MaxRetries:     1,
				InitialBackoff: 2 * time.Second,
				MaxBackoff:     time.Second,
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := valid()
			tt.edit(&input)

			_, err := providerkit.NormalizePolicy(input)
			if err == nil {
				t.Fatal("NormalizePolicy() error = nil")
			}
			var policyErr *providerkit.PolicyError
			if !errors.As(err, &policyErr) {
				t.Fatalf("error type = %T, want *PolicyError", err)
			}
			if policyErr.Field != tt.field {
				t.Fatalf("PolicyError.Field = %q, want %q", policyErr.Field, tt.field)
			}
		})
	}
}

func TestNetworkModeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode providerkit.NetworkMode
		want string
	}{
		{providerkit.PublicOnly, "public_only"},
		{providerkit.SelfHostedOptIn, "self_hosted_opt_in"},
		{providerkit.NetworkMode(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("NetworkMode(%d).String() = %q, want %q", tt.mode, got, tt.want)
		}
	}
}
