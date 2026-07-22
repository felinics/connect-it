package crypto_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/crypto"
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
		t.Fatalf("应使用最大版本 2 加密, got %d", ver)
	}
	pt, err := k.Decrypt(ct, ver, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, []byte(`{"client_secret":"x"}`)) {
		t.Fatalf("roundtrip 失败: %s", pt)
	}
}

func TestDecryptWrongAADFails(t *testing.T) {
	k := testKeyring(t)
	ct, ver, _ := k.Encrypt([]byte("data"), []byte("gmail"))
	if _, err := k.Decrypt(ct, ver, []byte("github")); err == nil {
		t.Fatal("错误 AAD 应解密失败")
	}
}

func TestDecryptOldVersion(t *testing.T) {
	// 只有版本 1 的旧 keyring 加密的数据，新 keyring（1+2）仍能按版本 1 解密。
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
		t.Fatalf("旧版本解密失败: %v %q", err, pt)
	}
}

func TestDecryptUnknownVersionFails(t *testing.T) {
	k := testKeyring(t)
	ct, _, _ := k.Encrypt([]byte("data"), nil)
	if _, err := k.Decrypt(ct, 9, nil); err == nil {
		t.Fatal("未知版本应报错")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	k := testKeyring(t)
	ct, ver, _ := k.Encrypt([]byte("data"), nil)
	ct[len(ct)-1] ^= 0xff
	if _, err := k.Decrypt(ct, ver, nil); err == nil {
		t.Fatal("被篡改的密文应解密失败")
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	// 无 Secret 字段的 Connector 存空配置，允许空明文。
	k := testKeyring(t)
	ct, ver, err := k.Encrypt(nil, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := k.Decrypt(ct, ver, []byte("x"))
	if err != nil || len(pt) != 0 {
		t.Fatalf("空明文 roundtrip 失败: %v %q", err, pt)
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
			t.Fatalf("spec %q 应被拒绝", spec)
		}
	}
}
