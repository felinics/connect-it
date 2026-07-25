package connectors

import (
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// AuthNone 没有凭据可验证，也就没有 scope 可比对。没有任何已注册 Provider 使用
// AuthNone，所以这条 fail-closed 规则只能在包内用合成 Definition 覆盖；其余
// runtime bundle 契约都由 all_test.go 用真实 Definition 覆盖。
func TestRuntimeBundleRejectsAuthNoneAuthorization(t *testing.T) {
	def := connector.Definition{
		Type: "runtime_none",
		AuthMethods: []connector.AuthMethod{{
			Key:  "none",
			Type: connector.AuthNone,
		}},
	}
	runtime := RuntimeBundle{
		Handlers: map[connector.Type]connector.HandlerMap{},
		Authorization: connector.AuthorizationRuntime{
			ScopeMatchers: connector.ScopeMatcherMap{
				def.Type: {"none": func([]string, string) bool { return true }},
			},
		},
	}
	err := validateRuntimeBundleForDefinitions(
		runtime,
		[]connector.Definition{def},
	)
	if err == nil || !strings.Contains(err.Error(), "AuthNone") {
		t.Fatalf("AuthNone scope matcher error = %v", err)
	}
}
