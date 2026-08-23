package configsvc

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/store"
)

// Enabled reports whether a registered connector can be used. Connectors are
// enabled by default so existing installations retain their current behavior.
func (s *Service) Enabled(ctx context.Context, t connector.Type) (bool, error) {
	if _, ok := s.reg.Get(t); !ok {
		return false, ErrUnknownConnector
	}
	enabled, err := s.q.GetConnectorEnabled(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// RequireEnabled rejects use of a connector that an administrator disabled.
func (s *Service) RequireEnabled(ctx context.Context, t connector.Type) error {
	enabled, err := s.Enabled(ctx, t)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrConnectorDisabled
	}
	return nil
}

// EnabledStates returns persisted connector enablement overrides. Missing
// registered connectors are interpreted as enabled by callers.
func (s *Service) EnabledStates(ctx context.Context) (map[connector.Type]bool, error) {
	rows, err := s.q.ListConnectorSettings(ctx)
	if err != nil {
		return nil, err
	}
	states := make(map[connector.Type]bool, len(rows))
	for _, row := range rows {
		states[connector.Type(row.ConnectorType)] = row.Enabled
	}
	return states, nil
}

// SetEnabled persists whether a registered connector can be used.
func (s *Service) SetEnabled(ctx context.Context, t connector.Type, enabled bool) error {
	if _, ok := s.reg.Get(t); !ok {
		return ErrUnknownConnector
	}
	_, err := s.q.UpsertConnectorEnabled(ctx, store.UpsertConnectorEnabledParams{
		ConnectorType: string(t),
		Enabled:       enabled,
	})
	return err
}
