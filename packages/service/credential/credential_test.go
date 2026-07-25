package credential_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/service/credential"
)

func TestOAuthRoundtrip(t *testing.T) {
	exp := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	data, err := credential.OAuth{
		AccessToken:  "at",
		TokenType:    "bEaReR",
		RefreshToken: "rt",
		ExpiresAt:    exp,
	}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["token_type"] != "Bearer" {
		t.Fatalf("marshal token_type = %v, want Bearer", payload["token_type"])
	}
	got, err := credential.UnmarshalOAuth(data)
	if err != nil || got.AccessToken != "at" || got.TokenType != "Bearer" ||
		got.RefreshToken != "rt" || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("roundtrip 失败: %+v err=%v", got, err)
	}
}

func TestOAuthZeroExpiry(t *testing.T) {
	data, err := credential.OAuth{AccessToken: "at"}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := credential.UnmarshalOAuth(data)
	if err != nil || got.TokenType != "Bearer" || !got.ExpiresAt.IsZero() {
		t.Fatalf("零值过期时间应保留: %+v err=%v", got, err)
	}
}

func TestOAuthLegacyMissingTokenTypeDefaultsToBearer(t *testing.T) {
	got, err := credential.UnmarshalOAuth([]byte(`{
		"access_token":"legacy-at",
		"refresh_token":"legacy-rt",
		"expires_at":""
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenType != "Bearer" {
		t.Fatalf("legacy token_type = %q, want Bearer", got.TokenType)
	}

	data, err := got.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["token_type"] != "Bearer" {
		t.Fatalf("legacy 写回未升级 token_type: %v", payload)
	}
}

func TestOAuthRejectsUnsupportedTokenType(t *testing.T) {
	if _, err := (credential.OAuth{
		AccessToken: "at",
		TokenType:   "DPoP",
	}).Marshal(); err == nil || strings.Contains(err.Error(), "DPoP") {
		t.Fatalf("marshal 不支持的 token_type 应安全报错: %v", err)
	}
	if _, err := credential.UnmarshalOAuth([]byte(`{
		"access_token":"at",
		"token_type":"MAC"
	}`)); err == nil || strings.Contains(err.Error(), "MAC") {
		t.Fatalf("unmarshal 不支持的 token_type 应安全报错: %v", err)
	}
}

func TestFieldsRoundtrip(t *testing.T) {
	data, err := credential.Fields{Fields: map[string]string{"token": "abc"}}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := credential.UnmarshalFields(data)
	if err != nil || got.Fields["token"] != "abc" {
		t.Fatalf("roundtrip 失败: %+v err=%v", got, err)
	}
}

func TestUnmarshalGarbage(t *testing.T) {
	if _, err := credential.UnmarshalOAuth([]byte("nope")); err == nil {
		t.Fatal("非 JSON 应报错")
	}
	if _, err := credential.UnmarshalFields([]byte("nope")); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}
