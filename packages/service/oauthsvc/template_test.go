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
		t.Fatalf("a missing config value should error and name the field, got %v", err)
	}
}

func TestExpandEndpointNonStringValue(t *testing.T) {
	_, err := ExpandEndpoint("https://x.example.com/{tenant}/y", map[string]any{"tenant": 42})
	if err == nil {
		t.Fatal("a non-string config value should error")
	}
}
