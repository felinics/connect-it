package providerkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

const requestTestBaseURL = "https://api.example.com/v1"

func TestBuildRequestSupportsOnlyDocumentedMethods(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		method string
		want   string
	}{
		{name: "get", method: "get", want: http.MethodGet},
		{name: "head", method: " HEAD ", want: http.MethodHead},
		{name: "post", method: "post", want: http.MethodPost},
		{name: "put", method: "PUT", want: http.MethodPut},
		{name: "patch", method: "patch", want: http.MethodPatch},
		{name: "delete", method: "delete", want: http.MethodDelete},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			built, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				Request{Method: test.method, URL: "resources"},
			)
			if err != nil {
				t.Fatalf("buildRequest() error = %v", err)
			}
			if built.request.Method != test.want {
				t.Fatalf(
					"request method = %q, want %q",
					built.request.Method,
					test.want,
				)
			}
		})
	}

	for _, method := range []string{"", " ", http.MethodConnect, http.MethodOptions, "TRACE"} {
		t.Run("reject_"+strings.TrimSpace(method), func(t *testing.T) {
			t.Parallel()

			_, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				Request{Method: method},
			)
			assertFailureCode(t, err, connector.FailureInvalidInput)
		})
	}
}

func TestBuildRequestEncodesReplayableBodies(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		input       Request
		wantBody    string
		wantType    string
		wantGetBody bool
	}{
		{
			name: "json",
			input: Request{
				Method: http.MethodPost,
				JSON:   map[string]any{"enabled": true, "name": "demo"},
			},
			wantBody:    `{"enabled":true,"name":"demo"}`,
			wantType:    "application/json",
			wantGetBody: true,
		},
		{
			name: "form",
			input: Request{
				Method: http.MethodPost,
				Form: url.Values{
					"name":  {"a b"},
					"scope": {"read", "write"},
				},
			},
			wantBody:    "name=a+b&scope=read&scope=write",
			wantType:    "application/x-www-form-urlencoded",
			wantGetBody: true,
		},
		{
			name: "raw",
			input: Request{
				Method:      http.MethodPut,
				Body:        []byte("opaque"),
				ContentType: "application/octet-stream",
			},
			wantBody:    "opaque",
			wantType:    "application/octet-stream",
			wantGetBody: true,
		},
		{
			name: "empty",
			input: Request{
				Method: http.MethodPost,
			},
		},
		{
			name: "explicit empty raw body",
			input: Request{
				Method:      http.MethodPost,
				Body:        []byte{},
				ContentType: "application/octet-stream",
			},
			wantType: "application/octet-stream",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			built, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				test.input,
			)
			if err != nil {
				t.Fatalf("buildRequest() error = %v", err)
			}
			if got := string(built.body); got != test.wantBody {
				t.Fatalf("built body = %q, want %q", got, test.wantBody)
			}
			if got := built.request.Header.Get("Content-Type"); got != test.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, test.wantType)
			}
			if got := built.request.GetBody != nil; got != test.wantGetBody {
				t.Fatalf("GetBody presence = %v, want %v", got, test.wantGetBody)
			}
			if test.wantGetBody {
				replayed, replayErr := built.request.GetBody()
				if replayErr != nil {
					t.Fatalf("GetBody() error = %v", replayErr)
				}
				defer replayed.Close()
				data, readErr := io.ReadAll(replayed)
				if readErr != nil {
					t.Fatalf("read replayed body: %v", readErr)
				}
				if got := string(data); got != test.wantBody {
					t.Fatalf("replayed body = %q, want %q", got, test.wantBody)
				}
			}
		})
	}
}

func TestBuildRequestRejectsAmbiguousBodies(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input Request
	}{
		{
			name: "json and form",
			input: Request{
				Method: http.MethodPost,
				JSON:   map[string]string{"a": "b"},
				Form:   url.Values{"a": {"b"}},
			},
		},
		{
			name: "json and raw",
			input: Request{
				Method:      http.MethodPost,
				JSON:        map[string]string{"a": "b"},
				Body:        []byte("body"),
				ContentType: "text/plain",
			},
		},
		{
			name: "form and raw",
			input: Request{
				Method:      http.MethodPost,
				Form:        url.Values{},
				Body:        []byte{},
				ContentType: "text/plain",
			},
		},
		{
			name: "all three",
			input: Request{
				Method:      http.MethodPost,
				JSON:        map[string]string{},
				Form:        url.Values{},
				Body:        []byte{},
				ContentType: "text/plain",
			},
		},
		{
			name: "raw without content type",
			input: Request{
				Method: http.MethodPost,
				Body:   []byte("body"),
			},
		},
		{
			name: "content type without body",
			input: Request{
				Method:      http.MethodPost,
				ContentType: "application/json",
			},
		},
		{
			name: "unencodable json",
			input: Request{
				Method: http.MethodPost,
				JSON:   func() {},
			},
		},
		{
			name: "get with body",
			input: Request{
				Method: http.MethodGet,
				JSON:   map[string]string{},
			},
		},
		{
			name: "head with body",
			input: Request{
				Method: http.MethodHead,
				Form:   url.Values{"a": {"b"}},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				test.input,
			)
			assertFailureCode(t, err, connector.FailureInvalidInput)
		})
	}
}

func TestResolveRequestURLPreservesBasePathAndEncoding(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		target      string
		wantPath    string
		wantEscaped string
	}{
		{
			name:        "empty target",
			target:      "",
			wantPath:    "/v1",
			wantEscaped: "/v1",
		},
		{
			name:        "relative path",
			target:      "users",
			wantPath:    "/v1/users",
			wantEscaped: "/v1/users",
		},
		{
			name:        "leading slash remains base scoped",
			target:      "/users",
			wantPath:    "/v1/users",
			wantEscaped: "/v1/users",
		},
		{
			name:        "encoded segment slash",
			target:      "users/a%2Fb",
			wantPath:    "/v1/users/a/b",
			wantEscaped: "/v1/users/a%2Fb",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolved, err := resolveRequestURL(requestTestBaseURL, test.target)
			if err != nil {
				t.Fatalf("resolveRequestURL() error = %v", err)
			}
			if resolved.Path != test.wantPath {
				t.Fatalf("Path = %q, want %q", resolved.Path, test.wantPath)
			}
			if got := resolved.EscapedPath(); got != test.wantEscaped {
				t.Fatalf("EscapedPath = %q, want %q", got, test.wantEscaped)
			}
			if resolved.Scheme != "https" || resolved.Host != "api.example.com:443" {
				t.Fatalf(
					"authority = %s://%s, want https://api.example.com:443",
					resolved.Scheme,
					resolved.Host,
				)
			}
		})
	}

	if got := PathSegment("a/b c?d"); got != "a%2Fb%20c%3Fd" {
		t.Fatalf("PathSegment() = %q", got)
	}
}

func TestResolveRequestURLRejectsTraversalAndAmbiguousEncoding(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"../admin",
		"./admin",
		"%2e%2e/admin",
		"%252e%252e/admin",
		"%252525252e%252525252e/admin",
		"users/%2e%2e/admin",
		"%2e%2e%2fadmin",
		"..%5cadmin",
		"users#credential",
		"//evil.example/admin",
		"https://api.example.com/%2e%2e/admin",
		"https://user:secret@api.example.com/admin",
		"custom:opaque",
	} {
		t.Run(url.PathEscape(target), func(t *testing.T) {
			t.Parallel()

			_, err := resolveRequestURL(requestTestBaseURL, target)
			assertFailureCode(t, err, connector.FailureInvalidInput)
		})
	}
}

func TestBuildRequestOverridesQueryAndRestrictsHeaders(t *testing.T) {
	t.Parallel()

	inputHeaders := http.Header{
		"X-Request-ID": {"one", "two"},
	}
	inputQuery := url.Values{
		"filter": {"a b"},
		"repeat": {"request"},
	}
	built, err := buildRequest(
		context.Background(),
		requestTestBaseURL,
		Request{
			Method:  http.MethodGet,
			URL:     "users?existing=one&repeat=target",
			Query:   inputQuery,
			Headers: inputHeaders,
		},
	)
	if err != nil {
		t.Fatalf("buildRequest() error = %v", err)
	}
	if got := built.request.URL.Query(); !equalURLValues(got, url.Values{
		"existing": {"one"},
		"filter":   {"a b"},
		"repeat":   {"request"},
	}) {
		t.Fatalf("query = %#v", got)
	}
	if got := built.request.Header.Values("X-Request-ID"); !equalStrings(
		got,
		[]string{"one", "two"},
	) {
		t.Fatalf("X-Request-ID = %#v", got)
	}

	inputHeaders.Set("X-Request-ID", "mutated")
	inputQuery.Set("filter", "mutated")
	if got := built.request.Header.Values("X-Request-ID"); !equalStrings(
		got,
		[]string{"one", "two"},
	) {
		t.Fatalf("built headers aliased caller input: %#v", got)
	}
	if got := built.request.URL.Query().Get("filter"); got != "a b" {
		t.Fatalf("built query aliased caller input: %q", got)
	}

	for _, header := range []string{
		"Authorization",
		"authorization",
		"Content-Type",
		"Content-Length",
		"Cookie",
		"Host",
		"Proxy-Authorization",
		"Referer",
		"Accept-Encoding",
	} {
		t.Run("blocked_"+header, func(t *testing.T) {
			t.Parallel()

			_, buildErr := buildRequest(
				context.Background(),
				requestTestBaseURL,
				Request{
					Method:  http.MethodGet,
					Headers: http.Header{header: {"value"}},
				},
			)
			assertFailureCode(t, buildErr, connector.FailureInvalidInput)
		})
	}

	for _, headers := range []http.Header{
		{"": {"value"}},
		{"Bad Header": {"value"}},
		{"X-Test": {"safe\r\nInjected: value"}},
	} {
		_, buildErr := buildRequest(
			context.Background(),
			requestTestBaseURL,
			Request{Method: http.MethodGet, Headers: headers},
		)
		assertFailureCode(t, buildErr, connector.FailureInvalidInput)
	}

	for _, query := range []url.Values{
		{"repeated": {"one", "two"}},
		{"missing": nil},
		{"bad\nname": {"value"}},
	} {
		_, buildErr := buildRequest(
			context.Background(),
			requestTestBaseURL,
			Request{Method: http.MethodGet, Query: query},
		)
		assertFailureCode(t, buildErr, connector.FailureInvalidInput)
	}

	_, malformedQueryErr := buildRequest(
		context.Background(),
		requestTestBaseURL,
		Request{Method: http.MethodGet, URL: "users?key=%zz"},
	)
	assertFailureCode(t, malformedQueryErr, connector.FailureInvalidInput)
}

func TestBuildRequestValidatesAuthorizerFootprint(t *testing.T) {
	t.Parallel()

	t.Run("declared header and query changes", func(t *testing.T) {
		t.Parallel()

		auth := requestTestAuthorizer{
			footprint: CredentialFootprint{
				HeaderNames:     []string{"x-api-key", "X-API-Key"},
				QueryParamNames: []string{"token", "token"},
			},
			apply: func(request *http.Request) error {
				request.Header.Set("X-API-Key", "header-secret")
				query := request.URL.Query()
				query.Set("token", "query-secret")
				request.URL.RawQuery = query.Encode()
				return nil
			},
		}
		built, err := buildRequest(
			context.Background(),
			requestTestBaseURL,
			Request{
				Method:     http.MethodGet,
				URL:        "users?token=attacker",
				Authorizer: auth,
			},
		)
		if err != nil {
			t.Fatalf("buildRequest() error = %v", err)
		}
		if got := built.request.Header.Get("X-API-Key"); got != "header-secret" {
			t.Fatalf("X-API-Key = %q", got)
		}
		if got := built.request.URL.Query()["token"]; !equalStrings(
			got,
			[]string{"query-secret"},
		) {
			t.Fatalf("token query = %#v", got)
		}
		if !equalStrings(built.footprint.HeaderNames, []string{"X-Api-Key"}) ||
			!equalStrings(built.footprint.QueryParamNames, []string{"token"}) {
			t.Fatalf("normalized footprint = %#v", built.footprint)
		}
	})

	for _, test := range []struct {
		name string
		auth Authorizer
	}{
		{
			name: "undeclared header",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Header.Set("X-Secret", "secret")
					return nil
				},
			},
		},
		{
			name: "undeclared query",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					query := request.URL.Query()
					query.Set("secret", "secret")
					request.URL.RawQuery = query.Encode()
					return nil
				},
			},
		},
		{
			name: "invalid footprint header",
			auth: requestTestAuthorizer{
				footprint: CredentialFootprint{HeaderNames: []string{"Bad Header"}},
			},
		},
		{
			name: "invalid footprint query",
			auth: requestTestAuthorizer{
				footprint: CredentialFootprint{QueryParamNames: []string{"bad\nquery"}},
			},
		},
		{
			name: "footprint panic",
			auth: requestTestAuthorizer{
				footprintPanic: true,
			},
		},
		{
			name: "apply panic",
			auth: requestTestAuthorizer{
				apply: func(*http.Request) error {
					panic("credential must not escape")
				},
			},
		},
		{
			name: "apply error",
			auth: requestTestAuthorizer{
				apply: func(*http.Request) error {
					return errors.New("secret upstream signer detail")
				},
			},
		},
		{
			name: "method mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Method = http.MethodDelete
					return nil
				},
			},
		},
		{
			name: "host mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.URL.Host = "evil.example"
					return nil
				},
			},
		},
		{
			name: "path mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.URL.Path = "/admin"
					return nil
				},
			},
		},
		{
			name: "context mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					*request = *request.WithContext(context.WithValue(
						request.Context(),
						struct{}{},
						true,
					))
					return nil
				},
			},
		},
		{
			name: "trailer mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Trailer = http.Header{
						"X-Secret-Trailer": {"secret"},
					}
					return nil
				},
			},
		},
		{
			name: "connection close mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Close = true
					return nil
				},
			},
		},
		{
			name: "undeclared lowercase header mutation",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Header["x-hidden-key"] = []string{"secret"}
					return nil
				},
			},
		},
		{
			name: "empty referer header",
			auth: requestTestAuthorizer{
				footprint: CredentialFootprint{
					HeaderNames: []string{"Referer"},
				},
				apply: func(request *http.Request) error {
					request.Header["referer"] = []string{""}
					return nil
				},
			},
		},
		{
			name: "body mutation without body footprint",
			auth: requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Body = io.NopCloser(strings.NewReader(`{"b":2}`))
					return nil
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				Request{
					Method:     http.MethodPost,
					JSON:       map[string]int{"a": 1},
					Authorizer: test.auth,
				},
			)
			assertFailureCode(t, err, connector.FailureConfigurationError)
			if strings.Contains(err.Error(), "secret") ||
				strings.Contains(err.Error(), "credential") {
				t.Fatalf("safe error exposed authorizer detail: %q", err)
			}
		})
	}

	t.Run("declared body mutation", func(t *testing.T) {
		t.Parallel()

		built, err := buildRequest(
			context.Background(),
			requestTestBaseURL,
			Request{
				Method: http.MethodPost,
				JSON:   map[string]int{"a": 1},
				Authorizer: requestTestAuthorizer{
					footprint: CredentialFootprint{Body: true},
					apply: func(request *http.Request) error {
						request.Body = io.NopCloser(strings.NewReader(`{"b":2}`))
						return nil
					},
				},
			},
		)
		if err != nil {
			t.Fatalf("declared body authorizer error = %v", err)
		}
		if got := string(built.body); got != `{"b":2}` {
			t.Fatalf("effective authorized body = %q", got)
		}
		replayed, replayErr := built.request.GetBody()
		if replayErr != nil {
			t.Fatalf("GetBody() error = %v", replayErr)
		}
		defer replayed.Close()
		data, readErr := io.ReadAll(replayed)
		if readErr != nil {
			t.Fatalf("read replayed authorized body: %v", readErr)
		}
		if got := string(data); got != `{"b":2}` {
			t.Fatalf("replayed authorized body = %q", got)
		}
	})
}

func TestStreamingAuthorizerFencePreservesProtocolBody(t *testing.T) {
	t.Parallel()

	t.Run("body replacement is rejected", func(t *testing.T) {
		t.Parallel()

		request, err := http.NewRequest(
			http.MethodPost,
			requestTestBaseURL,
			strings.NewReader("protocol-body"),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = applyAuthorizer(
			request,
			requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.Body = io.NopCloser(strings.NewReader("credential"))
					return nil
				},
			},
			CredentialFootprint{},
			nil,
		)
		assertFailureCode(t, err, connector.FailureConfigurationError)
	})

	t.Run("replay function replacement is discarded", func(t *testing.T) {
		t.Parallel()

		request, err := http.NewRequest(
			http.MethodPost,
			requestTestBaseURL,
			strings.NewReader("protocol-body"),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = applyAuthorizer(
			request,
			requestTestAuthorizer{
				apply: func(request *http.Request) error {
					request.GetBody = func() (io.ReadCloser, error) {
						return io.NopCloser(
							strings.NewReader("credential"),
						), nil
					}
					return nil
				},
			},
			CredentialFootprint{},
			nil,
		)
		if err != nil {
			t.Fatalf("applyAuthorizer() error = %v", err)
		}
		replay, err := request.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = replay.Close() }()
		body, err := io.ReadAll(replay)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "protocol-body" {
			t.Fatalf("replayed body = %q", body)
		}
	})
}

func TestBuildRequestIdempotencyContract(t *testing.T) {
	t.Parallel()

	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			built, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				Request{
					Method:               method,
					Body:                 []byte("replayable"),
					ContentType:          "text/plain",
					IdempotencyKeyHeader: "Idempotency-Key",
					IdempotencyKey:       "request-123",
				},
			)
			if err != nil {
				t.Fatalf("buildRequest() error = %v", err)
			}
			if !built.retryWrite {
				t.Fatal("retryWrite = false, want true")
			}
			if got := built.request.Header.Get("Idempotency-Key"); got != "request-123" {
				t.Fatalf("Idempotency-Key = %q", got)
			}
			if built.request.GetBody == nil {
				t.Fatal("idempotent body is not replayable")
			}
		})
	}

	for _, test := range []struct {
		name   string
		method string
		header string
		key    string
	}{
		{
			name:   "missing key",
			method: http.MethodPost,
			header: "Idempotency-Key",
		},
		{
			name:   "missing header",
			method: http.MethodPost,
			key:    "request-123",
		},
		{
			name:   "get does not accept key",
			method: http.MethodGet,
			header: "Idempotency-Key",
			key:    "request-123",
		},
		{
			name:   "head does not accept key",
			method: http.MethodHead,
			header: "Idempotency-Key",
			key:    "request-123",
		},
		{
			name:   "sensitive header",
			method: http.MethodPost,
			header: "Authorization",
			key:    "request-123",
		},
		{
			name:   "invalid header",
			method: http.MethodPost,
			header: "Bad Header",
			key:    "request-123",
		},
		{
			name:   "newline key",
			method: http.MethodPost,
			header: "Idempotency-Key",
			key:    "request-123\r\nInjected: true",
		},
		{
			name:   "nul key",
			method: http.MethodPost,
			header: "Idempotency-Key",
			key:    "request-\x00-123",
		},
		{
			name:   "delete key",
			method: http.MethodPost,
			header: "Idempotency-Key",
			key:    "request-\x7f-123",
		},
		{
			name:   "oversize key",
			method: http.MethodPost,
			header: "Idempotency-Key",
			key:    strings.Repeat("k", maxIdempotencyKeyBytes+1),
		},
		{
			name:   "cookie2 header",
			method: http.MethodPost,
			header: "Cookie2",
			key:    "request-123",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildRequest(
				context.Background(),
				requestTestBaseURL,
				Request{
					Method:               test.method,
					IdempotencyKeyHeader: test.header,
					IdempotencyKey:       test.key,
				},
			)
			assertFailureCode(t, err, connector.FailureInvalidInput)
		})
	}
}

type requestTestAuthorizer struct {
	footprint      CredentialFootprint
	footprintPanic bool
	apply          func(*http.Request) error
}

func (authorizer requestTestAuthorizer) Apply(request *http.Request) error {
	if authorizer.apply == nil {
		return nil
	}
	return authorizer.apply(request)
}

func (authorizer requestTestAuthorizer) Footprint() CredentialFootprint {
	if authorizer.footprintPanic {
		panic("credential must not escape")
	}
	return authorizer.footprint
}

func equalURLValues(left, right url.Values) bool {
	if len(left) != len(right) {
		return false
	}
	for key, rightValues := range right {
		if !equalStrings(left[key], rightValues) {
			return false
		}
	}
	return true
}
