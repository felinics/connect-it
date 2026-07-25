package providerkit

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBuiltInAuthorizers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		build     func() (Authorizer, error)
		wantAuth  string
		wantKey   string
		wantValue string
		wantQuery string
	}{
		{
			name:     "bearer",
			build:    func() (Authorizer, error) { return Bearer("secret-token") },
			wantAuth: "Bearer secret-token",
		},
		{
			name: "header API key",
			build: func() (Authorizer, error) {
				return HeaderAPIKey("x-api-key", "secret-key")
			},
			wantKey:   "X-Api-Key",
			wantValue: "secret-key",
		},
		{
			name: "query API key",
			build: func() (Authorizer, error) {
				return QueryAPIKey("api_key", "secret-query")
			},
			wantQuery: "secret-query",
		},
		{
			name: "basic",
			build: func() (Authorizer, error) {
				return Basic("some-user", "secret-password")
			},
			wantAuth: "Basic " + base64.StdEncoding.EncodeToString(
				[]byte("some-user:secret-password"),
			),
		},
		{
			name: "basic empty password",
			build: func() (Authorizer, error) {
				return Basic("stripe-style-key", "")
			},
			wantAuth: "Basic " + base64.StdEncoding.EncodeToString(
				[]byte("stripe-style-key:"),
			),
		},
		{
			name: "custom authorization",
			build: func() (Authorizer, error) {
				return Authorization(`Token token="`, "secret-token", `"`)
			},
			wantAuth: `Token token="secret-token"`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			auth, err := test.build()
			if err != nil {
				t.Fatalf("build authorizer: %v", err)
			}
			req := newAuthTestRequest(t)
			req.URL.RawQuery = "keep=value&api_key=attacker&api_key=duplicate"
			if err := auth.Apply(req); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if got := req.Header.Get("Authorization"); got != test.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, test.wantAuth)
			}
			if got := req.Header.Get(test.wantKey); got != test.wantValue {
				t.Fatalf("%s = %q, want %q", test.wantKey, got, test.wantValue)
			}
			if test.wantQuery != "" {
				if got := req.URL.Query()["api_key"]; len(got) != 1 ||
					got[0] != test.wantQuery {
					t.Fatalf("api_key = %#v, want one redacted value", got)
				}
				if got := req.URL.Query().Get("keep"); got != "value" {
					t.Fatalf("unrelated query = %q, want value", got)
				}
			}
		})
	}
}

func TestAuthorizerFootprintIsCompleteAndDefensive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		build       func() (Authorizer, error)
		wantHeaders []string
		wantQueries []string
	}{
		{
			name:        "bearer",
			build:       func() (Authorizer, error) { return Bearer("secret") },
			wantHeaders: []string{"Authorization"},
		},
		{
			name: "header",
			build: func() (Authorizer, error) {
				return HeaderAPIKey("x-api-key", "secret")
			},
			wantHeaders: []string{"X-Api-Key"},
		},
		{
			name: "query",
			build: func() (Authorizer, error) {
				return QueryAPIKey("api_key", "secret")
			},
			wantQueries: []string{"api_key"},
		},
		{
			name:        "basic",
			build:       func() (Authorizer, error) { return Basic("user", "secret") },
			wantHeaders: []string{"Authorization"},
		},
		{
			name: "authorization",
			build: func() (Authorizer, error) {
				return Authorization("Token ", "secret", "")
			},
			wantHeaders: []string{"Authorization"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			auth, err := test.build()
			if err != nil {
				t.Fatalf("build authorizer: %v", err)
			}
			first := auth.Footprint()
			if !equalStrings(first.HeaderNames, test.wantHeaders) ||
				!equalStrings(first.QueryParamNames, test.wantQueries) ||
				first.Body {
				t.Fatalf("Footprint() = %#v", first)
			}

			if len(first.HeaderNames) > 0 {
				first.HeaderNames[0] = "Mutated"
			}
			if len(first.QueryParamNames) > 0 {
				first.QueryParamNames[0] = "mutated"
			}
			second := auth.Footprint()
			if !equalStrings(second.HeaderNames, test.wantHeaders) ||
				!equalStrings(second.QueryParamNames, test.wantQueries) {
				t.Fatalf("Footprint aliases caller slice: %#v", second)
			}
		})
	}

	original := CredentialFootprint{
		HeaderNames:     []string{"Authorization"},
		QueryParamNames: []string{"api_key"},
		Body:            true,
	}
	cloned := original.Clone()
	cloned.HeaderNames[0] = "Changed"
	cloned.QueryParamNames[0] = "changed"
	if original.HeaderNames[0] != "Authorization" ||
		original.QueryParamNames[0] != "api_key" {
		t.Fatal("CredentialFootprint.Clone aliases input")
	}
}

func TestNoAuthDoesNotModifyRequest(t *testing.T) {
	t.Parallel()

	req := newAuthTestRequest(t)
	req.Header.Set("X-Existing", "value")
	beforeURL := req.URL.String()
	if err := NoAuth().Apply(req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if req.URL.String() != beforeURL || req.Header.Get("X-Existing") != "value" {
		t.Fatal("NoAuth modified request")
	}
	if got := NoAuth().Footprint(); len(got.HeaderNames) != 0 ||
		len(got.QueryParamNames) != 0 || got.Body {
		t.Fatalf("NoAuth footprint = %#v", got)
	}
	if err := NoAuth().Apply(nil); !errors.Is(err, ErrInvalidAuthorizerConfig) {
		t.Fatalf("NoAuth.Apply(nil) error = %v", err)
	}
}

func TestAuthorizerConstructorsRejectUnsafeInputWithoutEchoingIt(t *testing.T) {
	t.Parallel()

	secret := "do-not-echo-secret"
	tests := []struct {
		name  string
		build func() (Authorizer, error)
	}{
		{name: "empty bearer", build: func() (Authorizer, error) { return Bearer("") }},
		{
			name: "bearer newline",
			build: func() (Authorizer, error) {
				return Bearer(secret + "\r\nX-Evil: injected")
			},
		},
		{
			name: "invalid header name",
			build: func() (Authorizer, error) {
				return HeaderAPIKey("X Bad", secret)
			},
		},
		{
			name: "reserved host",
			build: func() (Authorizer, error) {
				return HeaderAPIKey("Host", secret)
			},
		},
		{
			name: "reserved content length",
			build: func() (Authorizer, error) {
				return HeaderAPIKey("Content-Length", secret)
			},
		},
		{
			name: "reserved referer",
			build: func() (Authorizer, error) {
				return HeaderAPIKey("Referer", secret)
			},
		},
		{
			name: "empty query name",
			build: func() (Authorizer, error) {
				return QueryAPIKey("", secret)
			},
		},
		{
			name: "query whitespace",
			build: func() (Authorizer, error) {
				return QueryAPIKey("api key", secret)
			},
		},
		{
			name: "basic colon user",
			build: func() (Authorizer, error) {
				return Basic("bad:user", secret)
			},
		},
		{
			name: "basic password newline",
			build: func() (Authorizer, error) {
				return Basic("user", "\n")
			},
		},
		{
			name: "authorization newline",
			build: func() (Authorizer, error) {
				return Authorization("Token ", secret, "\n")
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			auth, err := test.build()
			if auth != nil || !errors.Is(err, ErrInvalidAuthorizerConfig) {
				t.Fatalf("build = (%T, %v), want invalid config", auth, err)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("constructor error leaked credential")
			}
		})
	}
}

func TestAuthorizerApplyRejectsNilRequest(t *testing.T) {
	t.Parallel()

	builders := []func() (Authorizer, error){
		func() (Authorizer, error) { return Bearer("secret") },
		func() (Authorizer, error) { return HeaderAPIKey("X-Key", "secret") },
		func() (Authorizer, error) { return QueryAPIKey("key", "secret") },
		func() (Authorizer, error) { return Basic("user", "secret") },
		func() (Authorizer, error) { return Authorization("Token ", "secret", "") },
	}
	for _, build := range builders {
		auth, err := build()
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if err := auth.Apply(nil); !errors.Is(err, ErrInvalidAuthorizerConfig) {
			t.Fatalf("Apply(nil) = %v", err)
		}
	}
}

func newAuthTestRequest(t *testing.T) *http.Request {
	t.Helper()
	parsed, err := url.Parse("https://provider.example/v1/resource")
	if err != nil {
		t.Fatalf("parse request URL: %v", err)
	}
	return &http.Request{Method: http.MethodGet, URL: parsed, Header: make(http.Header)}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
