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
	def, spec, err := s.policyDefinition(t)
	if err != nil {
		return ConfigView{}, err
	}
	if public == nil {
		public = map[string]any{}
	}
	if secrets == nil {
		secrets = map[string]string{}
	}

	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	defer tx.Rollback(ctx) // no-op after Commit

	state, err := lockPolicyIdentity(ctx, qtx, t, spec.Version)
	if err != nil {
		return ConfigView{}, err
	}
	row, exists, err := getConfigForUpdate(ctx, qtx, t)
	if err != nil {
		return ConfigView{}, err
	}
	if exists {
		if int(row.ConfigSchemaVersion) > def.ConfigSchemaVersion {
			return ConfigView{}, ErrIncompatible
		}
		if !ifMatch.IsZero() && !row.UpdatedAt.Equal(ifMatch) {
			return ConfigView{}, ErrConflict
		}
	} else if !ifMatch.IsZero() {
		// 客户端以为行存在（带了 If-Match），实际已被删除。
		return ConfigView{}, ErrConflict
	}

	storedSecrets := map[string]string{}
	if exists {
		if storedSecrets, err = s.decryptSecrets(row, t); err != nil {
			return ConfigView{}, err
		}
		if int(row.ConfigSchemaVersion) < def.ConfigSchemaVersion {
			storedPublic, decodeErr := unmarshalPublic(row.PublicConfig)
			if decodeErr != nil {
				return ConfigView{}, decodeErr
			}
			_, storedSecrets, err = upgradeStoredConfig(
				def,
				int(row.ConfigSchemaVersion),
				storedPublic,
				storedSecrets,
			)
			if err != nil {
				return ConfigView{}, err
			}
		}
	}
	normalizedPublic, normalizedSecrets, normalizedValues, err :=
		s.normalizedWriteValues(def, public, storedSecrets, secrets)
	if err != nil {
		return ConfigView{}, err
	}
	newPolicy, err := projectPolicyIdentity(def, spec, normalizedValues)
	if err != nil {
		return ConfigView{}, err
	}

	pubJSON, err := json.Marshal(normalizedPublic)
	if err != nil {
		return ConfigView{}, err
	}
	secJSON, err := json.Marshal(normalizedSecrets)
	if err != nil {
		return ConfigView{}, err
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(secJSON, []byte(t))
	if err != nil {
		return ConfigView{}, err
	}

	var out store.ConnectorConfig
	if exists {
		// Always recheck the exact locked old value. If-Match is an additional
		// caller precondition, not the only protection against stale writes.
		out, err = qtx.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
			ConnectorType:       string(t),
			ConfigSchemaVersion: int32(def.ConfigSchemaVersion),
			PublicConfig:        pubJSON,
			SecretConfig:        ciphertext,
			SecretKeyVersion:    int32(keyVersion),
			UpdatedAt:           row.UpdatedAt,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ConfigView{}, ErrConflict
		}
	} else {
		out, err = qtx.CreateConnectorConfig(ctx, store.CreateConnectorConfigParams{
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

	// 唯一的判定口径：落库的 policy identity 就是授权总线；它与新 identity
	// 不一致（含未初始化、版本或 Definition 漂移）就必须失效旧授权。
	policyChanged := !matchesPolicyIdentity(state, newPolicy)
	if _, err := qtx.SetConnectorPolicyIdentity(
		ctx,
		store.SetConnectorPolicyIdentityParams{
			ConnectorType:    string(t),
			IdentityVersion:  int32(newPolicy.version),
			IdentityDigest:   newPolicy.identity[:],
			DefinitionDigest: newPolicy.definition[:],
		},
	); err != nil {
		return ConfigView{}, err
	}
	if policyChanged {
		if err := qtx.ClearConnectorConfigVerification(ctx, string(t)); err != nil {
			return ConfigView{}, err
		}
		if _, err := qtx.BumpConnectorAuthorizationGenerations(ctx, string(t)); err != nil {
			return ConfigView{}, err
		}
		// ClearConnectorConfigVerification changed the row returned above.
		out.McpVerifiedAt = nil
		out.McpVerifiedEndpoint = nil
	}
	view, err := s.viewFromRow(out, t)
	if err != nil {
		return ConfigView{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConfigView{}, err
	}
	return view, nil
}

func (s *Service) Delete(ctx context.Context, t connector.Type) error {
	def, spec, err := s.policyDefinition(t)
	if err != nil {
		return err
	}
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	state, err := lockPolicyIdentity(ctx, qtx, t, spec.Version)
	if err != nil {
		return err
	}
	row, exists, err := getConfigForUpdate(ctx, qtx, t)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	newValues, err := s.policyValuesFromRow(def, store.ConnectorConfig{}, false)
	if err != nil {
		return err
	}
	newPolicy, err := projectPolicyIdentity(def, spec, newValues)
	if err != nil {
		return err
	}
	n, err := qtx.DeleteConnectorConfigIfMatch(
		ctx,
		store.DeleteConnectorConfigIfMatchParams{
			ConnectorType: string(t),
			UpdatedAt:     row.UpdatedAt,
		},
	)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrConflict
	}
	policyChanged := !matchesPolicyIdentity(state, newPolicy)
	if _, err := qtx.SetConnectorPolicyIdentity(
		ctx,
		store.SetConnectorPolicyIdentityParams{
			ConnectorType:    string(t),
			IdentityVersion:  int32(newPolicy.version),
			IdentityDigest:   newPolicy.identity[:],
			DefinitionDigest: newPolicy.definition[:],
		},
	); err != nil {
		return err
	}
	if policyChanged {
		if _, err := qtx.BumpConnectorAuthorizationGenerations(ctx, string(t)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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
