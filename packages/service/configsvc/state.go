package configsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/status"
	"github.com/memohai/connect-it/packages/service/store"
)

// ConfigState maps a connector_configs row to the status machine input. When
// no row exists, Exists is false.
func (s *Service) ConfigState(ctx context.Context, t connector.Type) (status.ConfigState, error) {
	row, err := s.q.GetConnectorConfig(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return status.ConfigState{}, nil
	}
	if err != nil {
		return status.ConfigState{}, err
	}
	return s.configState(row, t)
}

// ConfigStates reads every config in one query so the catalog can merge them
// with the code Definitions in memory. Configs whose Definition is gone need
// only Exists and SchemaVersion, so their secrets are left undecrypted.
func (s *Service) ConfigStates(ctx context.Context) (map[connector.Type]status.ConfigState, error) {
	rows, err := s.q.ListConnectorConfigs(ctx)
	if err != nil {
		return nil, err
	}
	states := make(map[connector.Type]status.ConfigState, len(rows))
	for _, row := range rows {
		t := connector.Type(row.ConnectorType)
		if _, ok := s.reg.Get(t); !ok {
			states[t] = status.ConfigState{
				Exists:        true,
				SchemaVersion: int(row.ConfigSchemaVersion),
			}
			continue
		}
		state, err := s.configState(row, t)
		if err != nil {
			return nil, err
		}
		states[t] = state
	}
	return states, nil
}

func (s *Service) configState(row store.ConnectorConfig, t connector.Type) (status.ConfigState, error) {
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
	return st, nil
}

// Resolved returns the config the execution layer can use directly: defaults,
// then public, then secrets, each overriding the previous. When the stored
// row lags behind, ConfigUpgraders are applied in order, in memory only.
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
