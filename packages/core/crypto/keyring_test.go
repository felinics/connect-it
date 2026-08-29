package crypto_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/felinics/connect-it/packages/core/crypto"
)

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.ParseKeyring(
		"1:" + strings.Repeat("11", 32) + ",2:" + strings.Repeat("22", 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	k := testKeyring(t)
	aad := []byte("gmail")
	ct, ver, err := k.Encrypt([]byte(`{"client_secret":"x"}`), aad)
	if err != nil {
		t.Fatal(err)
	}
	if ver != 2 {
		t.Fatalf("expected encryption with highest version 2, got %d", ver)
	}
	pt, err := k.Decrypt(ct, ver, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, []byte(`{"client_secret":"x"}`)) {
		t.Fatalf("roundtrip failed: %s", pt)
	}
}

func TestDecryptWrongAADFails(t *testing.T) {
	k := testKeyring(t)
	ct, ver, _ := k.Encrypt([]byte("data"), []byte("gmail"))
	if _, err := k.Decrypt(ct, ver, []byte("github")); err == nil {
		t.Fatal("decryption with a wrong AAD should fail")
	}
}

func TestDecryptOldVersion(t *testing.T) {
	// Data encrypted by an old version-1-only keyring is still decryptable by
	// a newer keyring holding versions 1 and 2.
	old, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	ct, ver, _ := old.Encrypt([]byte("data"), []byte("a"))
	if ver != 1 {
		t.Fatalf("got %d", ver)
	}
	pt, err := testKeyring(t).Decrypt(ct, 1, []byte("a"))
	if err != nil || string(pt) != "data" {
		t.Fatalf("decrypting an older version failed: %v %q", err, pt)
	}
}

func TestDecryptUnknownVersionFails(t *testing.T) {
	k := testKeyring(t)
	ct, _, _ := k.Encrypt([]byte("data"), nil)
	if _, err := k.Decrypt(ct, 9, nil); err == nil {
		t.Fatal("an unknown version should return an error")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	k := testKeyring(t)
	ct, ver, _ := k.Encrypt([]byte("data"), nil)
	ct[len(ct)-1] ^= 0xff
	if _, err := k.Decrypt(ct, ver, nil); err == nil {
		t.Fatal("tampered ciphertext should fail to decrypt")
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	// Connectors without secret fields store an empty config, so empty
	// plaintext must be allowed.
	k := testKeyring(t)
	ct, ver, err := k.Encrypt(nil, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := k.Decrypt(ct, ver, []byte("x"))
	if err != nil || len(pt) != 0 {
		t.Fatalf("empty plaintext roundtrip failed: %v %q", err, pt)
	}
}

func TestParseKeyringRejectsBadInput(t *testing.T) {
	for _, spec := range []string{
		"",
		"abc",
		"1:zz",
		"1:" + strings.Repeat("11", 16),
		"0:" + strings.Repeat("11", 32),
		"1:" + strings.Repeat("11", 32) + ",1:" + strings.Repeat("22", 32),
	} {
		if _, err := crypto.ParseKeyring(spec); err == nil {
			t.Fatalf("spec %q should have been rejected", spec)
		}
	}
}
