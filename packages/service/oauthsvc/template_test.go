package oauthsvc

import (
	"strings"
	"testing"
)

func TestExpandEndpointReplacesPlaceholder(t *testing.T) {
	got, err := ExpandEndpoint(
		"https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
		map[string]any{"tenant": "common"},
	)
	if err != nil || got != "https://login.microsoftonline.com/common/oauth2/v2.0/authorize" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestExpandEndpointNoPlaceholderIdentity(t *testing.T) {
	got, err := ExpandEndpoint("https://github.com/login/oauth/authorize", map[string]any{})
	if err != nil || got != "https://github.com/login/oauth/authorize" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestExpandEndpointMissingValue(t *testing.T) {
	_, err := ExpandEndpoint(
		"https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
		map[string]any{},
	)
	if err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("缺配置值应报含字段名的错误, got %v", err)
	}
}

func TestExpandEndpointNonStringValue(t *testing.T) {
	_, err := ExpandEndpoint("https://x.example.com/{tenant}/y", map[string]any{"tenant": 42})
	if err == nil {
		t.Fatal("非字符串配置值应报错")
	}
}
