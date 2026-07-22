// Package credential 定义 connections.credential 密文内的明文 JSON 结构。
package credential

import (
	"encoding/json"
	"fmt"
	"time"
)

// OAuth 是 oauth2 connection 的 credential 明文。
// ExpiresAt 为零值表示 provider 未告知过期时间（视为长期有效）。
type OAuth struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type oauthJSON struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
}

func (c OAuth) Marshal() ([]byte, error) {
	j := oauthJSON{AccessToken: c.AccessToken, RefreshToken: c.RefreshToken}
	if !c.ExpiresAt.IsZero() {
		j.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(j)
}

func UnmarshalOAuth(data []byte) (OAuth, error) {
	var j oauthJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return OAuth{}, fmt.Errorf("credential: 解析 oauth credential: %w", err)
	}
	out := OAuth{AccessToken: j.AccessToken, RefreshToken: j.RefreshToken}
	if j.ExpiresAt != "" {
		ts, err := time.Parse(time.RFC3339, j.ExpiresAt)
		if err != nil {
			return OAuth{}, fmt.Errorf("credential: expires_at 不是 RFC3339: %w", err)
		}
		out.ExpiresAt = ts
	}
	return out, nil
}

// Fields 是 api_key / custom_credential connection 的 credential 明文。
type Fields struct {
	Fields map[string]string `json:"fields"`
}

func (c Fields) Marshal() ([]byte, error) { return json.Marshal(c) }

func UnmarshalFields(data []byte) (Fields, error) {
	var c Fields
	if err := json.Unmarshal(data, &c); err != nil {
		return Fields{}, fmt.Errorf("credential: 解析 fields credential: %w", err)
	}
	return c, nil
}
