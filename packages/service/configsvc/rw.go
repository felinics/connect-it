package configsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/store"
)

func (s *Service) Get(ctx context.Context, t connector.Type) (ConfigView, error) {
	if _, ok := s.reg.Get(t); !ok {
		return ConfigView{}, ErrUnknownConnector
	}
	row, err := s.q.GetConnectorConfig(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfigView{}, ErrNotFound
	}
	if err != nil {
		return ConfigView{}, err
	}
	return s.viewFromRow(row, t)
}

// Put 写入配置：public 为全量替换；secrets 为部分合并——出现的 key 覆盖，
// 空串表示删除，未出现的保留原值。Definition 已删除的字段在此顺带清理。
func (s *Service) Put(ctx context.Context, t connector.Type, public map[string]any, secrets map[string]string, ifMatch time.Time) (ConfigView, error) {
	def, ok := s.reg.Get(t)
	if !ok {
		return ConfigView{}, ErrUnknownConnector
	}
	if public == nil {
		public = map[string]any{}
	}

	row, err := s.q.GetConnectorConfig(ctx, string(t))
	exists := true
	if errors.Is(err, pgx.ErrNoRows) {
		exists = false
	} else if err != nil {
		return ConfigView{}, err
	}

	merged := map[string]string{}
	if exists {
		if int(row.ConfigSchemaVersion) > def.ConfigSchemaVersion {
			return ConfigView{}, ErrIncompatible
		}
		if !ifMatch.IsZero() && !row.UpdatedAt.Equal(ifMatch) {
			return ConfigView{}, ErrConflict
		}
		if merged, err = s.decryptSecrets(row, t); err != nil {
			return ConfigView{}, err
		}
	} else if !ifMatch.IsZero() {
		// 客户端以为行存在（带了 If-Match），实际已被删除。
		return ConfigView{}, ErrConflict
	}
	for k, v := range secrets {
		if v == "" {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}

	known := map[string]bool{}
	for _, f := range def.ConfigFields {
		known[f.Key] = true
	}
	for k := range public {
		if !known[k] {
			delete(public, k)
		}
	}
	for k := range merged {
		if !known[k] {
			delete(merged, k)
		}
	}

	if err := s.Validate(t, public, merged); err != nil {
		return ConfigView{}, err
	}

	pubJSON, err := json.Marshal(public)
	if err != nil {
		return ConfigView{}, err
	}
	secJSON, err := json.Marshal(merged)
	if err != nil {
		return ConfigView{}, err
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(secJSON, []byte(t))
	if err != nil {
		return ConfigView{}, err
	}

	var out store.ConnectorConfig
	if exists && !ifMatch.IsZero() {
		out, err = s.q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
			ConnectorType:       string(t),
			ConfigSchemaVersion: int32(def.ConfigSchemaVersion),
			PublicConfig:        pubJSON,
			SecretConfig:        ciphertext,
			SecretKeyVersion:    int32(keyVersion),
			UpdatedAt:           ifMatch,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ConfigView{}, ErrConflict
		}
	} else {
		out, err = s.q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
			ConnectorType:       string(t),
			ConfigSchemaVersion: int32(def.ConfigSchemaVersion),
			PublicConfig:        pubJSON,
			SecretConfig:        ciphertext,
			SecretKeyVersion:    int32(keyVersion),
		})
	}
	if err != nil {
		return ConfigView{}, err
	}
	return s.viewFromRow(out, t)
}

func (s *Service) Delete(ctx context.Context, t connector.Type) error {
	n, err := s.q.DeleteConnectorConfig(ctx, string(t))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) viewFromRow(row store.ConnectorConfig, t connector.Type) (ConfigView, error) {
	secrets, err := s.decryptSecrets(row, t)
	if err != nil {
		return ConfigView{}, err
	}
	pub, err := unmarshalPublic(row.PublicConfig)
	if err != nil {
		return ConfigView{}, err
	}
	return ConfigView{
		ConnectorType: string(t),
		SchemaVersion: int(row.ConfigSchemaVersion),
		Public:        pub,
		SecretKeysSet: sortedKeys(secrets),
		UpdatedAt:     row.UpdatedAt,
	}, nil
}

func (s *Service) decryptSecrets(row store.ConnectorConfig, t connector.Type) (map[string]string, error) {
	out := map[string]string{}
	if len(row.SecretConfig) == 0 {
		return out, nil
	}
	plaintext, err := s.kr.Decrypt(row.SecretConfig, int(row.SecretKeyVersion), []byte(t))
	if err != nil {
		return nil, fmt.Errorf("configsvc: 解密 secret_config: %w", err)
	}
	if len(plaintext) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(plaintext, &out); err != nil {
		return nil, fmt.Errorf("configsvc: secret_config 明文损坏: %w", err)
	}
	return out, nil
}

func unmarshalPublic(raw []byte) (map[string]any, error) {
	pub := map[string]any{}
	if len(raw) == 0 {
		return pub, nil
	}
	if err := json.Unmarshal(raw, &pub); err != nil {
		return nil, fmt.Errorf("configsvc: public_config 损坏: %w", err)
	}
	return pub, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
