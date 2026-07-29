// Package credential defines the plaintext JSON structures stored inside the
// encrypted connections.credential column.
package credential

import (
	"encoding/json"
	"fmt"
	"time"
)

// OAuth is the plaintext credential of an oauth2 connection. A zero ExpiresAt
// means the provider reported no expiry, which is treated as long-lived.
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
		return OAuth{}, fmt.Errorf("credential: parse oauth credential: %w", err)
	}
	out := OAuth{AccessToken: j.AccessToken, RefreshToken: j.RefreshToken}
	if j.ExpiresAt != "" {
		ts, err := time.Parse(time.RFC3339, j.ExpiresAt)
		if err != nil {
			return OAuth{}, fmt.Errorf("credential: expires_at is not RFC3339: %w", err)
		}
		out.ExpiresAt = ts
	}
	return out, nil
}

// Fields is the plaintext credential of an api_key or custom_credential
// connection.
type Fields struct {
	Fields map[string]string `json:"fields"`
}

func (c Fields) Marshal() ([]byte, error) { return json.Marshal(c) }

func UnmarshalFields(data []byte) (Fields, error) {
	var c Fields
	if err := json.Unmarshal(data, &c); err != nil {
		return Fields{}, fmt.Errorf("credential: parse fields credential: %w", err)
	}
	return c, nil
}
