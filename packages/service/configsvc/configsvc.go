// Package configsvc manages connector_configs: validation, AES-GCM encrypted
// storage, If-Match optimistic concurrency, and merged reads at runtime.
package configsvc

import (
	"errors"
	"fmt"
	"time"

	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/store"
)

var (
	// ErrUnknownConnector means connector_type is not in the code registry.
	ErrUnknownConnector = errors.New("unknown connector type")
	// ErrNotFound means connector_configs has no such row.
	ErrNotFound = errors.New("config not found")
	// ErrConflict means If-Match does not match updated_at.
	ErrConflict = errors.New("config conflict")
	// ErrIncompatible means the stored config_schema_version is newer than the
	// code, so an overwriting write is refused.
	ErrIncompatible = errors.New("config schema newer than code")
	// ErrConnectorDisabled means the connector has been disabled by an
	// administrator and cannot be used through downstream APIs.
	ErrConnectorDisabled = errors.New("connector is disabled")
)

// ValidationError describes a validation failure on one field.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("field %q: %s", e.Field, e.Reason)
}

// ConfigView is the config as the admin UI sees it. For secrets it exposes
// only the list of keys that have been set.
type ConfigView struct {
	ConnectorType string
	SchemaVersion int
	Public        map[string]any
	SecretKeysSet []string
	UpdatedAt     time.Time
}

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	kr  *crypto.Keyring
}

func New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service {
	return &Service{q: q, reg: reg, kr: kr}
}
