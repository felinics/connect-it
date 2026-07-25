package connector_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// 类型包无行为，测试锁定两件事：tagged union 可被 type switch 区分；零值可用。
func TestToolBackendTaggedUnion(t *testing.T) {
	tools := []connector.Tool{
		{ID: "a", Backend: connector.RemoteMCPBackend{ServerKey: "s", RemoteToolName: "r"}},
		{ID: "b", Backend: connector.ManagedBackend{HandlerKey: "h"}},
	}
	var kinds []string
	for _, tool := range tools {
		switch tool.Backend.(type) {
		case connector.RemoteMCPBackend:
			kinds = append(kinds, "remote")
		case connector.ManagedBackend:
			kinds = append(kinds, "managed")
		default:
			t.Fatalf("tool %s: 未知 backend", tool.ID)
		}
	}
	if kinds[0] != "remote" || kinds[1] != "managed" {
		t.Fatalf("got %v", kinds)
	}
}

func TestDefinitionZeroValue(t *testing.T) {
	var def connector.Definition
	if def.Deprecated || len(def.Tools) != 0 {
		t.Fatal("零值 Definition 应为空且未弃用")
	}
}

func TestToolEffectiveMaxInputBytes(t *testing.T) {
	if got := (connector.Tool{}).EffectiveMaxInputBytes(); got != connector.DefaultMaxInputBytes {
		t.Fatalf("zero MaxInputBytes = %d, want default %d", got, connector.DefaultMaxInputBytes)
	}
	const explicit = int64(12 << 20)
	if got := (connector.Tool{MaxInputBytes: explicit}).EffectiveMaxInputBytes(); got != explicit {
		t.Fatalf("explicit MaxInputBytes = %d, want %d", got, explicit)
	}
	if connector.DefaultMaxInputBytes != 5<<20 {
		t.Fatalf("default input limit changed: %d", connector.DefaultMaxInputBytes)
	}
	if connector.AbsoluteMaxInputBytes != 32<<20 {
		t.Fatalf("absolute input limit changed: %d", connector.AbsoluteMaxInputBytes)
	}
}

func TestOAuthConfigEffectiveTokenScopeSeparator(t *testing.T) {
	if got := (connector.OAuthConfig{}).EffectiveTokenScopeSeparator(); got != connector.OAuthScopeSpace {
		t.Fatalf("zero TokenScopeSeparator = %q, want %q", got, connector.OAuthScopeSpace)
	}
	if got := (connector.OAuthConfig{
		TokenScopeSeparator: connector.OAuthScopeComma,
	}).EffectiveTokenScopeSeparator(); got != connector.OAuthScopeComma {
		t.Fatalf("explicit TokenScopeSeparator = %q, want %q", got, connector.OAuthScopeComma)
	}
}

func TestOAuthConfigEffectiveDefaults(t *testing.T) {
	var config connector.OAuthConfig
	if got := config.EffectiveAuthorizationScopeSeparator(); got !=
		connector.OAuthScopeSpace {
		t.Fatalf("authorization separator = %q", got)
	}
	if got := config.EffectiveTokenEndpointAuth(); got !=
		connector.TokenAuthBasic {
		t.Fatalf("token endpoint auth = %q", got)
	}
	if got := config.EffectiveTokenRequestFormat(); got !=
		connector.TokenRequestForm {
		t.Fatalf("token request format = %q", got)
	}

	config.AuthorizationScopeSeparator = connector.OAuthScopeComma
	config.TokenEndpointAuth = connector.TokenAuthNone
	config.TokenRequestFormat = connector.TokenRequestJSON
	if config.EffectiveAuthorizationScopeSeparator() !=
		connector.OAuthScopeComma ||
		config.EffectiveTokenEndpointAuth() != connector.TokenAuthNone ||
		config.EffectiveTokenRequestFormat() != connector.TokenRequestJSON {
		t.Fatalf("explicit values changed: %+v", config)
	}
}

// 单一 failureMessages 表同时定义有效码集合与默认文案：此表钉死两者。
func TestFailureCodesAreStableAndValid(t *testing.T) {
	tests := []struct {
		code    connector.FailureCode
		want    string
		message string
	}{
		{connector.FailureInvalidInput, "invalid_input", "invalid input"},
		{connector.FailureInputTooLarge, "input_too_large", "input exceeds the allowed size"},
		{connector.FailureToolUnavailable, "tool_unavailable", "tool is unavailable"},
		{connector.FailureConfigurationError, "configuration_error", "provider is not configured correctly"},
		{connector.FailureAuthorizationFailed, "authorization_failed", "provider authorization failed"},
		{connector.FailurePermissionDenied, "permission_denied", "provider permission denied"},
		{connector.FailureNotFound, "not_found", "provider resource not found"},
		{connector.FailureConflict, "conflict", "provider reported a conflict"},
		{connector.FailureRateLimited, "rate_limited", "provider rate limit exceeded"},
		{connector.FailureProviderError, "provider_error", "provider request failed"},
		{connector.FailureInvalidResponse, "invalid_response", "provider returned an invalid response"},
		{connector.FailureUpstreamUnavailable, "upstream_unavailable", "provider is temporarily unavailable"},
		{connector.FailureTimeout, "timeout", "provider request timed out"},
		{connector.FailureCanceled, "canceled", "request was canceled"},
		{connector.FailureResponseTooLarge, "response_too_large", "provider response exceeds the allowed size"},
		{connector.FailurePolicyDenied, "policy_denied", "provider request was denied by policy"},
		{connector.FailureInternalError, "internal_error", "internal error"},
	}
	for _, tc := range tests {
		if string(tc.code) != tc.want {
			t.Errorf("FailureCode = %q, want %q", tc.code, tc.want)
		}
		if !tc.code.Valid() {
			t.Errorf("稳定 FailureCode %q 应有效", tc.code)
		}
		if got := connector.DefaultFailureMessage(tc.code); got != tc.message {
			t.Errorf("%q 默认文案 = %q, want %q", tc.code, got, tc.message)
		}
	}
	for _, unknown := range []connector.FailureCode{"", "unknown"} {
		if unknown.Valid() {
			t.Errorf("%q 不应有效", unknown)
		}
		if got := connector.DefaultFailureMessage(unknown); got != "internal error" {
			t.Errorf("未知码文案 = %q, want internal error", got)
		}
	}
}

func TestToolResultDataFailed(t *testing.T) {
	tests := []struct {
		name string
		data connector.ToolResultData
		want bool
	}{
		{name: "success", data: connector.ToolResultData{}, want: false},
		{
			name: "typed failure",
			data: connector.ToolResultData{Failure: &connector.ToolFailure{
				Code:    connector.FailureRateLimited,
				Message: "try again later",
			}},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.data.Failed(); got != tc.want {
				t.Fatalf("Failed() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCredentialValidatorAndScopeMatcherContracts(t *testing.T) {
	input := connector.CredentialValidationInput{
		ConnectorType: "example_app",
		AuthMethodKey: "api_key",
		AuthType:      connector.AuthAPIKey,
		ConnectionID:  "connection-id",
		Config:        map[string]any{"region": "us"},
		Fields:        map[string]string{"token": "secret"},
	}
	want := connector.CredentialValidationResult{
		Profile:       connector.CredentialProfile{AccountID: "acct_1", DisplayName: "Example"},
		GrantedScopes: []string{"items:read"},
		ScopesKnown:   true,
	}
	validator := connector.CredentialValidator(func(
		_ context.Context,
		got connector.CredentialValidationInput,
	) (connector.CredentialValidationResult, error) {
		if !reflect.DeepEqual(got, input) {
			t.Fatalf("validator input = %#v, want %#v", got, input)
		}
		return want, nil
	})
	validators := connector.CredentialValidatorMap{
		"example_app": {"api_key": validator},
	}
	got, err := validators["example_app"]["api_key"](context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("validator result = %#v, want %#v", got, want)
	}

	matcher := connector.ScopeMatcher(func(granted []string, required string) bool {
		for _, scope := range granted {
			if scope == required {
				return true
			}
		}
		return false
	})
	matchers := connector.ScopeMatcherMap{
		"example_app": {"api_key": matcher},
	}
	if !matchers["example_app"]["api_key"]([]string{"items:read"}, "items:read") {
		t.Fatal("ScopeMatcher 应匹配相同 scope")
	}
	if matchers["example_app"]["api_key"]([]string{"items:read"}, "items:write") {
		t.Fatal("ScopeMatcher 不应匹配缺失的 scope")
	}
}

func TestCredentialValidationErrorIsSafeError(t *testing.T) {
	err := &connector.CredentialValidationError{
		Code:              connector.FailureAuthorizationFailed,
		SafeMessage:       "credential is invalid",
		Temporary:         false,
		RetryAfterSeconds: 0,
		UpstreamStatus:    401,
	}
	var target *connector.CredentialValidationError
	if !errors.As(err, &target) {
		t.Fatal("CredentialValidationError 应实现 error")
	}
	if got := err.Error(); got != err.SafeMessage {
		t.Fatalf("Error() = %q, want %q", got, err.SafeMessage)
	}

	withoutMessage := &connector.CredentialValidationError{Code: connector.FailureUpstreamUnavailable}
	if got := withoutMessage.Error(); got != "upstream_unavailable" {
		t.Fatalf("空 SafeMessage 应回退到 code，got %q", got)
	}
}
