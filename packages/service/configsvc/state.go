package configsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/status"
)

// ConfigState 把 connector_configs 行映射为状态机输入；无行时 Exists=false。
func (s *Service) ConfigState(ctx context.Context, t connector.Type) (status.ConfigState, error) {
	row, err := s.q.GetConnectorConfig(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return status.ConfigState{}, nil
	}
	if err != nil {
		return status.ConfigState{}, err
	}
	secrets, err := s.decryptSecrets(row, t)
	if err != nil {
		return status.ConfigState{}, err
	}
	pub, err := unmarshalPublic(row.PublicConfig)
	if err != nil {
		return status.ConfigState{}, err
	}
	set := map[string]bool{}
	for k := range secrets {
		set[k] = true
	}
	st := status.ConfigState{
		Exists:        true,
		SchemaVersion: int(row.ConfigSchemaVersion),
		PublicValues:  pub,
		SecretKeysSet: set,
	}
	if row.McpVerifiedAt != nil && row.McpVerifiedEndpoint != nil {
		st.MCPVerified = true
		st.MCPVerifiedEndpoint = *row.McpVerifiedEndpoint
	}
	return st, nil
}

// Resolved 返回执行层可直接使用的配置：defaults→public→secrets 依次覆盖合并；
// 行版本落后时按序应用 ConfigUpgraders（仅内存生效，不落盘）。
func (s *Service) Resolved(ctx context.Context, t connector.Type) (map[string]any, error) {
	def, ok := s.reg.Get(t)
	if !ok {
		return nil, ErrUnknownConnector
	}
	out := map[string]any{}
	for _, f := range def.ConfigFields {
		if f.DefaultValue != nil {
			out[f.Key] = *f.DefaultValue
		}
	}

	row, err := s.q.GetConnectorConfig(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	pub, err := unmarshalPublic(row.PublicConfig)
	if err != nil {
		return nil, err
	}
	secretsStr, err := s.decryptSecrets(row, t)
	if err != nil {
		return nil, err
	}
	sec := map[string]any{}
	for k, v := range secretsStr {
		sec[k] = v
	}

	if int(row.ConfigSchemaVersion) < def.ConfigSchemaVersion {
		upgraders := append([]connector.ConfigUpgrader(nil), def.ConfigUpgraders...)
		sort.Slice(upgraders, func(i, j int) bool { return upgraders[i].FromVersion < upgraders[j].FromVersion })
		current := int(row.ConfigSchemaVersion)
		for _, up := range upgraders {
			if up.FromVersion < current {
				continue
			}
			if up.Upgrade != nil {
				if pub, sec, err = up.Upgrade(pub, sec); err != nil {
					return nil, fmt.Errorf("configsvc: upgrader v%d: %w", up.FromVersion, err)
				}
			}
			current = up.FromVersion + 1
		}
	}

	for k, v := range pub {
		out[k] = v
	}
	for k, v := range sec {
		out[k] = v
	}
	return out, nil
}
