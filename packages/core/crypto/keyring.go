// Package crypto 提供 Secret 配置与 credential 的 AES-256-GCM 加解密，
// 支持多版本 KEK：写入用最大版本，读取按存储的版本，实现平滑轮换。
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

// EnvSecretKey 的值格式："1:<64位hex>,2:<64位hex>"，最大版本为当前写入 key。
const EnvSecretKey = "CONNECT_IT_SECRET_KEY"

const keySize = 32

type Keyring struct {
	keys    map[int][]byte
	current int
}

func NewKeyring(keys map[int][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("keyring: 至少需要一把 key")
	}
	current := 0
	for v, k := range keys {
		if v < 1 {
			return nil, fmt.Errorf("keyring: 非法版本 %d", v)
		}
		if len(k) != keySize {
			return nil, fmt.Errorf("keyring: 版本 %d 的 key 必须是 %d 字节", v, keySize)
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
			return nil, fmt.Errorf("keyring: 片段 %q 不是 version:hex 形式", part)
		}
		v, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("keyring: 版本 %q 不是整数", verStr)
		}
		raw, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("keyring: 版本 %d 的 key 不是合法 hex", v)
		}
		if _, dup := keys[v]; dup {
			return nil, fmt.Errorf("keyring: 版本 %d 重复", v)
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
		return nil, errors.New("keyring: 密文过短")
	}
	return gcm.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
}

func (k *Keyring) gcm(version int) (cipher.AEAD, error) {
	key, ok := k.keys[version]
	if !ok {
		return nil, fmt.Errorf("keyring: 未知 key 版本 %d", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
