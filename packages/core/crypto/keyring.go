// Package crypto provides AES-256-GCM encryption for secret config values and
// credentials. It supports a multi-version KEK: writes use the highest
// version and reads use the version stored alongside the ciphertext, which
// allows keys to be rotated without downtime.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// EnvSecretKey holds a keyring in the form "1:<64-char-hex>,2:<64-char-hex>".
// The highest version is the key used for new writes.
const EnvSecretKey = "CONNECT_IT_SECRET_KEY"

const keySize = 32

type Keyring struct {
	keys    map[int][]byte
	current int
}

func NewKeyring(keys map[int][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("keyring: at least one key is required")
	}
	current := 0
	for v, k := range keys {
		if v < 1 {
			return nil, fmt.Errorf("keyring: invalid version %d", v)
		}
		if len(k) != keySize {
			return nil, fmt.Errorf("keyring: key for version %d must be %d bytes", v, keySize)
		}
		if v > current {
			current = v
		}
	}
	return &Keyring{keys: keys, current: current}, nil
}

func ParseKeyring(spec string) (*Keyring, error) {
	keys := map[int][]byte{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		verStr, hexKey, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("keyring: segment %q is not in version:hex form", part)
		}
		v, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("keyring: version %q is not an integer", verStr)
		}
		raw, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("keyring: key for version %d is not valid hex", v)
		}
		if _, dup := keys[v]; dup {
			return nil, fmt.Errorf("keyring: duplicate version %d", v)
		}
		keys[v] = raw
	}
	return NewKeyring(keys)
}

func (k *Keyring) CurrentVersion() int { return k.current }

func (k *Keyring) Encrypt(plaintext, aad []byte) ([]byte, int, error) {
	gcm, err := k.gcm(k.current)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), k.current, nil
}

func (k *Keyring) Decrypt(ciphertext []byte, version int, aad []byte) ([]byte, error) {
	gcm, err := k.gcm(version)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("keyring: ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
}

func (k *Keyring) gcm(version int) (cipher.AEAD, error) {
	key, ok := k.keys[version]
	if !ok {
		return nil, fmt.Errorf("keyring: unknown key version %d", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
