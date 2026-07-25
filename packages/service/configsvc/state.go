package configsvc

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/status"
	"github.com/memohai/connect-it/packages/service/store"
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
	resolved, _, err := s.ResolvedWithPolicy(ctx, t)
	return resolved, err
}

// ResolvedWithPolicy returns configuration together with the exact normalized
// policy digest derived from the same database row. Callers that perform slow
// network validation before creating a Connection must compare this snapshot
// in their final INSERT.
// This path is read-only: persisted identity drift returns ErrPolicyDrift;
// startup reconciliation or a config write must repair it.
func (s *Service) ResolvedWithPolicy(
	ctx context.Context,
	t connector.Type,
) (map[string]any, PolicySnapshot, error) {
	def, spec, err := s.policyDefinition(t)
	if err != nil {
		return nil, PolicySnapshot{}, err
	}

	row, err := s.q.GetConnectorConfig(ctx, string(t))
	exists := true
	if errors.Is(err, pgx.ErrNoRows) {
		row = store.ConnectorConfig{}
		exists = false
	} else if err != nil {
		return nil, PolicySnapshot{}, err
	}
	values, err := s.policyValuesFromRow(def, row, exists)
	if err != nil {
		return nil, PolicySnapshot{}, err
	}
	digests, err := projectPolicyIdentity(def, spec, values)
	if err != nil {
		return nil, PolicySnapshot{}, err
	}
	persisted, err := s.q.GetConnectorPolicyIdentity(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, PolicySnapshot{}, ErrPolicyDrift
	}
	if err != nil {
		return nil, PolicySnapshot{}, err
	}
	if !matchesPolicyIdentity(persisted, digests) {
		return nil, PolicySnapshot{}, ErrPolicyDrift
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out, PolicySnapshot{
		IdentityVersion:  int32(digests.version),
		IdentityDigest:   append([]byte(nil), digests.identity[:]...),
		DefinitionDigest: append([]byte(nil), digests.definition[:]...),
	}, nil
}
