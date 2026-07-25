package gitlab

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

// gitLabValidatorFactory 让 validator 自己按 mcp_url 派生 dynamic client，
// 同时把这个实例的 DNS/拨号钉死在本地 httptest listener 上。
func gitLabValidatorFactory(
	t *testing.T,
	handler http.HandlerFunc,
) *providerkit.Factory {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return providerkit.NewFactory(
		providerkit.WithInsecureProviderHTTP(true),
		providerkit.WithResolver(testkit.Resolver{}),
		providerkit.WithDialContext(
			testkit.DialTarget(server.Listener.Addr().String()),
		),
	)
}

func gitLabValidationInput(mcpURL string) connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   gitLabAccessTokenAuthMethod,
		AuthType:        connector.AuthAPIKey,
		AuthorizationID: "gitlab-validator-test",
		Config: map[string]any{
			gitLabMCPURLField:    mcpURL,
			gitLabAllowHTTPField: "true",
		},
		Fields: map[string]string{
			gitLabTokenField: "gloas-validator-secret",
		},
	}
}

func gitLabValidate(
	t *testing.T,
	factory *providerkit.Factory,
	input connector.CredentialValidationInput,
) (connector.CredentialValidationResult, error) {
	t.Helper()
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatalf("construct validators: %v", err)
	}
	return validators[gitLabAccessTokenAuthMethod](t.Context(), input)
}

// MCP endpoint 只接受带 mcp scope 的 OAuth token，因此校验必须打 Doorkeeper 的
// token 自省 endpoint（不需要任何 scope），而不是 /api/v4/user。
func TestGitLabCredentialValidatorIntrospectsInstanceToken(t *testing.T) {
	factory := gitLabValidatorFactory(
		t,
		func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet ||
				request.URL.EscapedPath() != "/gitlab/oauth/token/info" {
				t.Errorf("request = %s %s", request.Method, request.URL.EscapedPath())
			}
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Bearer gloas-validator-secret",
			)
			testkit.AssertNoHeader(t, request, "PRIVATE-TOKEN")
			testkit.AssertNoHeader(t, request, "Referer")
			_, _ = writer.Write([]byte(
				`{"resource_owner_id":42,"scope":["mcp","api"],"created_at":1}`,
			))
		},
	)
	input := gitLabValidationInput(
		"http://self.gitlab.example:80/gitlab/api/v4/mcp",
	)
	result, err := gitLabValidate(t, factory, input)
	if err != nil {
		t.Fatal(err)
	}
	wantAccountID := gitLabScopedAccountID(
		"http://self.gitlab.example:80/gitlab/",
		"42",
	)
	if result.Profile.AccountID != wantAccountID ||
		result.Profile.DisplayName != "GitLab user 42" {
		t.Fatalf("profile = %+v", result.Profile)
	}
	if !result.ScopesKnown ||
		strings.Join(result.GrantedScopes, ",") != "api,mcp" {
		t.Fatalf("granted scopes = %+v", result)
	}
	if strings.Contains(result.Profile.AccountID, "self.gitlab.example") ||
		strings.Contains(result.Profile.AccountID, "/gitlab") {
		t.Fatalf("AccountID leaks instance topology: %q", result.Profile.AccountID)
	}
}

// 自省结果没有 scope 时只能判为未知：空集合会被当成"确实没有任何 scope"。
func TestGitLabCredentialValidatorLeavesUnreportedScopesUnknown(t *testing.T) {
	factory := gitLabValidatorFactory(
		t,
		func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"resource_owner_id":7}`))
		},
	)
	result, err := gitLabValidate(
		t,
		factory,
		gitLabValidationInput("http://provider.test:80/api/v4/mcp"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ScopesKnown || result.GrantedScopes != nil {
		t.Fatalf("scopes must stay unknown: %+v", result)
	}
}

// 配置或凭据形状不对时，一个字节都不能发给 Provider。
func TestGitLabCredentialValidatorStopsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	factory := gitLabValidatorFactory(
		t,
		func(http.ResponseWriter, *http.Request) { requests.Add(1) },
	)
	for _, test := range []struct {
		name   string
		mutate func(*connector.CredentialValidationInput)
		code   connector.FailureCode
	}{
		{
			name: "missing token",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Fields, gitLabTokenField)
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "extra credential field",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields["base_url"] = "https://gitlab.example"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "wrong auth method",
			mutate: func(input *connector.CredentialValidationInput) {
				input.AuthMethodKey = "pat"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "token with unicode whitespace",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[gitLabTokenField] = "gloas-a b"
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "missing mcp_url",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Config, gitLabMCPURLField)
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "endpoint is not an MCP endpoint",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Config[gitLabMCPURLField] =
					"http://provider.test:80/api/v4"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "endpoint carries a query",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Config[gitLabMCPURLField] =
					"http://provider.test:80/api/v4/mcp?token=x"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "unknown insecure http value",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Config[gitLabAllowHTTPField] = "TRUE"
			},
			code: connector.FailureConfigurationError,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := gitLabValidationInput("http://provider.test:80/api/v4/mcp")
			test.mutate(&input)
			_, err := gitLabValidate(t, factory, input)
			assertGitLabCredentialError(t, err, test.code, 0)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid validator inputs made %d requests", requests.Load())
	}
}

// 明文 HTTP 实例没有显式打开 allow_insecure_http 时必须被策略挡住。
func TestGitLabCredentialValidatorHonorsInsecureHTTPGate(t *testing.T) {
	var requests atomic.Int32
	factory := gitLabValidatorFactory(
		t,
		func(http.ResponseWriter, *http.Request) { requests.Add(1) },
	)
	input := gitLabValidationInput("http://provider.test:80/api/v4/mcp")
	input.Config[gitLabAllowHTTPField] = "false"
	_, err := gitLabValidate(t, factory, input)
	assertGitLabCredentialError(t, err, connector.FailurePolicyDenied, 0)
	if requests.Load() != 0 {
		t.Fatalf("plain HTTP instance was reached %d times", requests.Load())
	}
}

func TestGitLabCredentialValidatorMapsFailuresWithoutLeaks(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		code   connector.FailureCode
	}{
		{"unauthorized", 401, connector.FailureAuthorizationFailed},
		{"forbidden", 403, connector.FailurePermissionDenied},
		{"rate limited", 429, connector.FailureRateLimited},
		{"unavailable", 503, connector.FailureUpstreamUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			const providerSecret = "raw-validator-secret"
			factory := gitLabValidatorFactory(
				t,
				func(writer http.ResponseWriter, _ *http.Request) {
					writer.WriteHeader(test.status)
					_, _ = writer.Write([]byte(
						providerSecret + ":gloas-validator-secret",
					))
				},
			)
			_, err := gitLabValidate(
				t,
				factory,
				gitLabValidationInput("http://provider.test:80/api/v4/mcp"),
			)
			assertGitLabCredentialError(t, err, test.code, test.status)
			if strings.Contains(err.Error(), providerSecret) ||
				strings.Contains(err.Error(), "gloas-validator-secret") ||
				errors.Unwrap(err) != nil {
				t.Fatal("validator error leaked body, credential, or cause")
			}
		})
	}
}

func TestGitLabCredentialValidatorRejectsMalformedIntrospection(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"malformed", `{"resource_owner_id":`},
		{"missing owner", `{"scope":["mcp"]}`},
		{"wrong owner type", `{"resource_owner_id":"42"}`},
		{"wrong scope type", `{"resource_owner_id":42,"scope":[1]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory := gitLabValidatorFactory(
				t,
				func(writer http.ResponseWriter, _ *http.Request) {
					_, _ = writer.Write([]byte(test.body))
				},
			)
			_, err := gitLabValidate(
				t,
				factory,
				gitLabValidationInput("http://provider.test:80/api/v4/mcp"),
			)
			assertGitLabCredentialError(
				t,
				err,
				connector.FailureInvalidResponse,
				0,
			)
		})
	}
}

// 同一个 user ID 在不同实例（含部署子路径）上必须是不同的账户身份。
func TestGitLabScopedAccountIDSeparatesInstancesAndUsers(t *testing.T) {
	t.Parallel()

	root := gitLabScopedAccountID("https://gitlab.example:443/", "42")
	for name, other := range map[string]string{
		"other subpath": gitLabScopedAccountID(
			"https://gitlab.example:443/other/",
			"42",
		),
		"other host": gitLabScopedAccountID("https://gitlab.test:443/", "42"),
		"other user": gitLabScopedAccountID("https://gitlab.example:443/", "43"),
	} {
		if other == root {
			t.Fatalf("%s collided with %q", name, root)
		}
	}
}

func assertGitLabCredentialError(
	t *testing.T,
	err error,
	code connector.FailureCode,
	status int,
) {
	t.Helper()
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != code ||
		(status != 0 && validationErr.UpstreamStatus != status) {
		t.Fatalf("validation error = %#v (%v)", validationErr, err)
	}
}
