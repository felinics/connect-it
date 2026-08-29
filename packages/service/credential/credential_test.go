package credential_test

import (
	"testing"
	"time"

	"github.com/felinics/connect-it/packages/service/credential"
)

func TestOAuthRoundtrip(t *testing.T) {
	exp := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	data, err := credential.OAuth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: exp}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := credential.UnmarshalOAuth(data)
	if err != nil || got.AccessToken != "at" || got.RefreshToken != "rt" || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("roundtrip failed: %+v err=%v", got, err)
	}
}

func TestOAuthZeroExpiry(t *testing.T) {
	data, err := credential.OAuth{AccessToken: "at"}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := credential.UnmarshalOAuth(data)
	if err != nil || !got.ExpiresAt.IsZero() {
		t.Fatalf("a zero expiry should be preserved: %+v err=%v", got, err)
	}
}

func TestFieldsRoundtrip(t *testing.T) {
	data, err := credential.Fields{Fields: map[string]string{"token": "abc"}}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := credential.UnmarshalFields(data)
	if err != nil || got.Fields["token"] != "abc" {
		t.Fatalf("roundtrip failed: %+v err=%v", got, err)
	}
}

func TestUnmarshalGarbage(t *testing.T) {
	if _, err := credential.UnmarshalOAuth([]byte("nope")); err == nil {
		t.Fatal("non-JSON input should return an error")
	}
	if _, err := credential.UnmarshalFields([]byte("nope")); err == nil {
		t.Fatal("non-JSON input should return an error")
	}
}
