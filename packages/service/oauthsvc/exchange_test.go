package oauthsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	providertest "github.com/memohai/connect-it/packages/core/providerkit/testkit"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func tokenConfig(
	provider *testutil.ProviderServer,
	separator connector.OAuthScopeSeparator,
) *connector.OAuthConfig {
	return &connector.OAuthConfig{
		TokenEndpoint:       provider.BaseURL + "/token",
		TokenScopeSeparator: separator,
		Egress: connector.OAuthEgressConfig{
			TokenOrigins: []string{provider.Origin},
		},
	}
}

func authorizationCorrelation() providerkit.RequestLabels {
	return providerkit.RequestLabels{
		AuthorizationID: "exchange-test-authorization",
	}
}

func connectionCorrelation() providerkit.RequestLabels {
	return providerkit.RequestLabels{
		ConnectionID: "exchange-test-connection",
	}
}

func exchangeResponse(
	t *testing.T,
	body string,
	separator connector.OAuthScopeSeparator,
) (oauthsvc.TokenValue, error) {
	t.Helper()
	provider := testutil.NewProviderServer(t, http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, body)
		},
	))
	return oauthsvc.ExchangeToken(
		t.Context(),
		provider.Factory,
		"example_app",
		tokenConfig(provider, separator),
		"client-id",
		"client-secret",
		url.Values{"grant_type": {"authorization_code"}},
		authorizationCorrelation(),
	)
}

func TestExchangeTokenRejectsInvalidCorrelationBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	provider := testutil.NewProviderServer(t, http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			calls.Add(1)
		},
	))
	for name, correlation := range map[string]providerkit.RequestLabels{
		"missing both": {},
		"both present": {
			ConnectionID:    "connection-id",
			AuthorizationID: "authorization-id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := oauthsvc.ExchangeToken(
				t.Context(),
				provider.Factory,
				"example_app",
				tokenConfig(provider, ""),
				"client-id",
				"client-secret",
				url.Values{"grant_type": {"authorization_code"}},
				correlation,
			)
			var endpointErr *oauthsvc.TokenEndpointError
			if !errors.As(err, &endpointErr) ||
				endpointErr.Code() != connector.FailureConfigurationError {
				t.Fatalf("invalid correlation error = %#v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid correlation reached Provider %d times", calls.Load())
	}
}

func TestExchangeTokenParsesAndNormalizesSpaceScopes(t *testing.T) {
	tok, err := exchangeResponse(t, `{
		"access_token":"at",
		"token_type":"bEaReR",
		"refresh_token":"rt",
		"expires_in":3600,
		"scope":"write  read write"
	}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if tok.TokenType != "Bearer" {
		t.Fatalf("token_type = %q, want Bearer", tok.TokenType)
	}
	if !tok.ScopesKnown {
		t.Fatal("响应包含 scope 时 ScopesKnown 应为 true")
	}
	if want := []string{"read", "write"}; !reflect.DeepEqual(tok.Scopes, want) {
		t.Fatalf("scopes = %#v, want %#v", tok.Scopes, want)
	}
}

func TestExchangeTokenGitHubCommaScopesAreSeparate(t *testing.T) {
	tok, err := exchangeResponse(t, `{
		"access_token":"at",
		"token_type":"Bearer",
		"scope":"repo, read:user,repo"
	}`, connector.OAuthScopeComma)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"read:user", "repo"}
	if !reflect.DeepEqual(tok.Scopes, want) {
		t.Fatalf("GitHub scopes = %#v, want %#v", tok.Scopes, want)
	}
}

func TestExchangeTokenTracksScopePresence(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		tok, err := exchangeResponse(t, `{
			"access_token":"at",
			"token_type":"Bearer"
		}`, "")
		if err != nil {
			t.Fatal(err)
		}
		if tok.ScopesKnown || tok.Scopes != nil {
			t.Fatalf("省略 scope 应保持 absent: %+v", tok)
		}
	})

	t.Run("present but empty", func(t *testing.T) {
		tok, err := exchangeResponse(t, `{
			"access_token":"at",
			"token_type":"Bearer",
			"scope":""
		}`, "")
		if err != nil {
			t.Fatal(err)
		}
		if !tok.ScopesKnown || len(tok.Scopes) != 0 {
			t.Fatalf("空 scope 是已知空集合: %+v", tok)
		}
	})
}

func TestExchangeTokenRejectsUnsupportedTokenTypeAndMalformedScope(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "missing access token",
			body: `{"token_type":"Bearer"}`,
		},
		{
			name: "empty access token",
			body: `{"access_token":"","token_type":"Bearer"}`,
		},
		{name: "missing token_type", body: `{"access_token":"at"}`},
		{
			name: "unsupported token_type",
			body: `{"access_token":"at","token_type":"DPoP"}`,
		},
		{
			name: "scope is null",
			body: `{"access_token":"at","token_type":"Bearer","scope":null}`,
		},
		{
			name: "scope is array",
			body: `{"access_token":"at","token_type":"Bearer","scope":["read"]}`,
		},
		{
			name: "empty refresh token",
			body: `{"access_token":"at","token_type":"Bearer","refresh_token":""}`,
		},
		{
			name: "null refresh token",
			body: `{"access_token":"at","token_type":"Bearer","refresh_token":null}`,
		},
		{
			name: "negative expires in",
			body: `{"access_token":"at","token_type":"Bearer","expires_in":-1}`,
		},
		{
			name: "null expires in",
			body: `{"access_token":"at","token_type":"Bearer","expires_in":null}`,
		},
		{
			name: "unrepresentable expires in",
			body: `{"access_token":"at","token_type":"Bearer","expires_in":9223372037}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := exchangeResponse(t, test.body, "")
			var endpointErr *oauthsvc.TokenEndpointError
			if !errors.As(err, &endpointErr) ||
				endpointErr.Code() != connector.FailureInvalidResponse ||
				!endpointErr.RequestUncertain {
				t.Fatalf("malformed token response = %#v", err)
			}
			if strings.Contains(err.Error(), test.body) {
				t.Fatal("raw token body escaped through error")
			}
		})
	}
}

func TestExchangeTokenClassifiesHTTPFailuresForRefreshReplay(t *testing.T) {
	for _, test := range []struct {
		name          string
		status        int
		body          string
		retryAfter    string
		wantCode      connector.FailureCode
		wantInvalid   bool
		wantTemporary bool
		wantUncertain bool
		wantRetry     int
	}{
		{
			name:        "invalid grant",
			status:      http.StatusBadRequest,
			body:        `{"error":"invalid_grant","secret":"must-not-leak"}`,
			wantCode:    connector.FailureAuthorizationFailed,
			wantInvalid: true,
		},
		{
			name:          "rate limited",
			status:        http.StatusTooManyRequests,
			body:          `{"error":"slow_down","secret":"must-not-leak"}`,
			retryAfter:    "17",
			wantCode:      connector.FailureRateLimited,
			wantTemporary: true,
			wantUncertain: true,
			wantRetry:     17,
		},
		{
			name:          "server failure",
			status:        http.StatusServiceUnavailable,
			body:          `{"error":"server_error","secret":"must-not-leak"}`,
			wantCode:      connector.FailureUpstreamUnavailable,
			wantTemporary: true,
			wantUncertain: true,
		},
		{
			name:          "client authentication failure",
			status:        http.StatusUnauthorized,
			body:          `{"error":"invalid_client","secret":"must-not-leak"}`,
			wantCode:      connector.FailureAuthorizationFailed,
			wantUncertain: true,
		},
		{
			name:          "ordinary client failure",
			status:        http.StatusUnprocessableEntity,
			body:          `{"error":"invalid_request","secret":"must-not-leak"}`,
			wantCode:      connector.FailureProviderError,
			wantUncertain: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := testutil.NewProviderServer(t, http.HandlerFunc(
				func(writer http.ResponseWriter, _ *http.Request) {
					if test.retryAfter != "" {
						writer.Header().Set("Retry-After", test.retryAfter)
					}
					writer.WriteHeader(test.status)
					_, _ = io.WriteString(writer, test.body)
				},
			))
			_, err := oauthsvc.ExchangeToken(
				t.Context(),
				provider.Factory,
				"example_app",
				tokenConfig(provider, ""),
				"client-id",
				"client-secret",
				url.Values{"grant_type": {"refresh_token"}},
				connectionCorrelation(),
			)
			var endpointErr *oauthsvc.TokenEndpointError
			if !errors.As(err, &endpointErr) ||
				endpointErr.Code() != test.wantCode ||
				endpointErr.InvalidGrant != test.wantInvalid ||
				endpointErr.Temporary != test.wantTemporary ||
				endpointErr.RetryAfter != test.wantRetry ||
				endpointErr.RequestUncertain != test.wantUncertain {
				t.Fatalf("classified error = %#v", err)
			}
			if strings.Contains(endpointErr.Error(), "must-not-leak") {
				t.Fatal("raw token response escaped through Error")
			}
		})
	}
}

func TestExchangeTokenSupportsFormJSONAndStrictClientAuthentication(
	t *testing.T,
) {
	for _, format := range []connector.TokenRequestFormat{
		connector.TokenRequestForm,
		connector.TokenRequestJSON,
	} {
		for _, auth := range []connector.TokenEndpointAuth{
			connector.TokenAuthBasic,
			connector.TokenAuthPost,
			connector.TokenAuthNone,
		} {
			t.Run(string(format)+"/"+string(auth), func(t *testing.T) {
				provider := testutil.NewProviderServer(t, http.HandlerFunc(
					func(writer http.ResponseWriter, request *http.Request) {
						params := decodeTokenRequest(t, request, format)
						if params["grant_type"] != "authorization_code" ||
							params["code"] != "core-code" ||
							params["audience"] != "tenant-audience" {
							t.Errorf("token params = %#v", params)
						}
						username, password, basic := request.BasicAuth()
						switch auth {
						case connector.TokenAuthBasic:
							if !basic ||
								username != "client-id" ||
								password != "client-secret" ||
								params["client_id"] != nil ||
								params["client_secret"] != nil {
								t.Errorf(
									"basic auth/body = %t %q %q %#v",
									basic,
									username,
									password,
									params,
								)
							}
						case connector.TokenAuthPost:
							if basic ||
								request.Header.Get("Authorization") != "" ||
								params["client_id"] != "client-id" ||
								params["client_secret"] != "client-secret" {
								t.Errorf("post auth/body = %#v", params)
							}
						case connector.TokenAuthNone:
							if basic ||
								request.Header.Get("Authorization") != "" ||
								params["client_id"] != "client-id" ||
								params["client_secret"] != nil {
								t.Errorf("none auth/body = %#v", params)
							}
						}
						_, _ = io.WriteString(
							writer,
							`{"access_token":"at","token_type":"Bearer"}`,
						)
					},
				))
				config := tokenConfig(provider, "")
				config.TokenEndpointAuth = auth
				config.TokenRequestFormat = format
				config.ExtraTokenParams = map[string]string{
					"audience": "tenant-audience",
					"code":     "must-not-override-core",
				}
				if _, err := oauthsvc.ExchangeToken(
					t.Context(),
					provider.Factory,
					"example_app",
					config,
					"client-id",
					"client-secret",
					url.Values{
						"grant_type": {"authorization_code"},
						"code":       {"core-code"},
					},
					authorizationCorrelation(),
				); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func decodeTokenRequest(
	t *testing.T,
	request *http.Request,
	format connector.TokenRequestFormat,
) map[string]any {
	t.Helper()
	switch format {
	case connector.TokenRequestForm:
		if got := request.Header.Get("Content-Type"); !strings.HasPrefix(
			got,
			"application/x-www-form-urlencoded",
		) {
			t.Errorf("form content type = %q", got)
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			return nil
		}
		out := make(map[string]any, len(request.PostForm))
		for key, values := range request.PostForm {
			switch len(values) {
			case 0:
				out[key] = []string{}
			case 1:
				out[key] = values[0]
			default:
				out[key] = append([]string(nil), values...)
			}
		}
		return out
	case connector.TokenRequestJSON:
		if got := request.Header.Get("Content-Type"); !strings.HasPrefix(
			got,
			"application/json",
		) {
			t.Errorf("JSON content type = %q", got)
		}
		var out map[string]any
		if err := json.NewDecoder(request.Body).Decode(&out); err != nil {
			t.Errorf("decode JSON: %v", err)
		}
		return out
	default:
		t.Fatalf("unsupported test format %q", format)
		return nil
	}
}

func TestExchangeTokenRefreshEndpointTakesPriority(t *testing.T) {
	var tokenCalls atomic.Int64
	var refreshCalls atomic.Int64
	provider := testutil.NewProviderServer(t, http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/token":
				tokenCalls.Add(1)
			case "/refresh":
				refreshCalls.Add(1)
			default:
				t.Errorf("unexpected path %q", request.URL.Path)
			}
			_, _ = io.WriteString(
				writer,
				`{"access_token":"at","token_type":"Bearer"}`,
			)
		},
	))
	config := tokenConfig(provider, "")
	config.RefreshTokenEndpoint = provider.BaseURL + "/refresh"
	if _, err := oauthsvc.ExchangeToken(
		t.Context(),
		provider.Factory,
		"example_app",
		config,
		"client-id",
		"client-secret",
		url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {"rt"},
		},
		connectionCorrelation(),
	); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 0 || refreshCalls.Load() != 1 {
		t.Fatalf(
			"token/refresh calls = %d/%d",
			tokenCalls.Load(),
			refreshCalls.Load(),
		)
	}
}

func TestExchangeTokenEnforcesBoundedResponseAndOrigin(t *testing.T) {
	t.Run("bounded response", func(t *testing.T) {
		provider := testutil.NewProviderServer(t, http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(writer, strings.Repeat("x", (1<<20)+1))
			},
		))
		_, err := oauthsvc.ExchangeToken(
			t.Context(),
			provider.Factory,
			"example_app",
			tokenConfig(provider, ""),
			"client-id",
			"client-secret",
			url.Values{"grant_type": {"refresh_token"}},
			connectionCorrelation(),
		)
		var endpointErr *oauthsvc.TokenEndpointError
		if !errors.As(err, &endpointErr) ||
			endpointErr.Code() != connector.FailureResponseTooLarge ||
			!endpointErr.RequestUncertain {
			t.Fatalf("oversized response = %#v", err)
		}
	})

	t.Run("origin escape before network", func(t *testing.T) {
		var calls atomic.Int64
		provider := testutil.NewProviderServer(t, http.HandlerFunc(
			func(http.ResponseWriter, *http.Request) {
				calls.Add(1)
			},
		))
		config := tokenConfig(provider, "")
		config.TokenEndpoint = "https://escape.example:443/token"
		_, err := oauthsvc.ExchangeToken(
			t.Context(),
			provider.Factory,
			"example_app",
			config,
			"client-id",
			"client-secret",
			url.Values{"grant_type": {"refresh_token"}},
			connectionCorrelation(),
		)
		var endpointErr *oauthsvc.TokenEndpointError
		if !errors.As(err, &endpointErr) ||
			endpointErr.Code() != connector.FailurePolicyDenied ||
			endpointErr.RequestUncertain {
			t.Fatalf("origin escape = %#v", err)
		}
		if calls.Load() != 0 {
			t.Fatal("origin escape reached provider")
		}
	})

	t.Run("redirect denied", func(t *testing.T) {
		var calls atomic.Int64
		provider := testutil.NewProviderServer(t, http.HandlerFunc(
			func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.Header().Set(
					"Location",
					"https://escape.example/token?secret=must-not-leak",
				)
				writer.WriteHeader(http.StatusFound)
			},
		))
		_, err := oauthsvc.ExchangeToken(
			t.Context(),
			provider.Factory,
			"example_app",
			tokenConfig(provider, ""),
			"client-id",
			"client-secret",
			url.Values{"grant_type": {"refresh_token"}},
			connectionCorrelation(),
		)
		var endpointErr *oauthsvc.TokenEndpointError
		if !errors.As(err, &endpointErr) ||
			endpointErr.Code() != connector.FailurePolicyDenied ||
			!endpointErr.RequestUncertain ||
			strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("redirect failure = %#v", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("redirect provider calls = %d, want 1", calls.Load())
		}
	})
}

func TestExchangeTokenDistinguishesNetworkAndTLSPreWriteFailures(t *testing.T) {
	t.Run("dial failure", func(t *testing.T) {
		var dialCalls atomic.Int64
		factory := providerkit.NewFactory(
			providerkit.WithResolver(providertest.Resolver{}),
			providerkit.WithDialContext(func(
				context.Context,
				string,
				string,
			) (net.Conn, error) {
				dialCalls.Add(1)
				return nil, &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: errors.New("secret dial failure"),
				}
			}),
		)
		config := &connector.OAuthConfig{
			TokenEndpoint: "https://provider.test:443/token",
			Egress: connector.OAuthEgressConfig{
				TokenOrigins: []string{"https://provider.test:443"},
			},
		}
		_, err := oauthsvc.ExchangeToken(
			t.Context(),
			factory,
			"example_app",
			config,
			"client-id",
			"client-secret",
			url.Values{"grant_type": {"refresh_token"}},
			connectionCorrelation(),
		)
		var endpointErr *oauthsvc.TokenEndpointError
		if !errors.As(err, &endpointErr) ||
			endpointErr.Code() != connector.FailureUpstreamUnavailable ||
			!endpointErr.Temporary ||
			endpointErr.RequestUncertain {
			t.Fatalf("dial failure = %#v", err)
		}
		if dialCalls.Load() == 0 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("dial calls/error = %d/%v", dialCalls.Load(), err)
		}
	})

	t.Run("untrusted TLS certificate", func(t *testing.T) {
		var handlerCalled atomic.Bool
		provider := testutil.NewProviderServer(t, http.HandlerFunc(
			func(http.ResponseWriter, *http.Request) {
				handlerCalled.Store(true)
			},
		))
		target := provider.Server.Listener.Addr().String()
		factory := providerkit.NewFactory(
			providerkit.WithResolver(providertest.Resolver{}),
			providerkit.WithDialContext(providertest.DialTarget(target)),
		)
		_, err := oauthsvc.ExchangeToken(
			t.Context(),
			factory,
			"example_app",
			tokenConfig(provider, ""),
			"client-id",
			"client-secret",
			url.Values{"grant_type": {"refresh_token"}},
			connectionCorrelation(),
		)
		var endpointErr *oauthsvc.TokenEndpointError
		if !errors.As(err, &endpointErr) ||
			endpointErr.Code() != connector.FailureUpstreamUnavailable ||
			endpointErr.RequestUncertain {
			t.Fatalf("TLS handshake failure = %#v", err)
		}
		if handlerCalled.Load() {
			t.Fatal("TLS verification failure happened after OAuth request")
		}
	})
}

func TestExchangeTokenResponseLossIsUncertain(t *testing.T) {
	provider := testutil.NewProviderServer(t, http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = connection.Close()
		},
	))
	_, err := oauthsvc.ExchangeToken(
		t.Context(),
		provider.Factory,
		"example_app",
		tokenConfig(provider, ""),
		"client-id",
		"client-secret",
		url.Values{"grant_type": {"refresh_token"}},
		connectionCorrelation(),
	)
	var endpointErr *oauthsvc.TokenEndpointError
	if !errors.As(err, &endpointErr) ||
		endpointErr.Code() != connector.FailureUpstreamUnavailable ||
		!endpointErr.Temporary ||
		!endpointErr.RequestUncertain {
		t.Fatalf("response loss = %#v", err)
	}
}
