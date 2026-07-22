// Package configsvc 管理 connector_configs：校验、AES-GCM 加密存储、
// If-Match 乐观并发与运行时读取合并。
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
	// ErrUnknownConnector：connector_type 不在代码 Registry 中。
	ErrUnknownConnector = errors.New("unknown connector type")
	// ErrNotFound：connector_configs 无该行。
	ErrNotFound = errors.New("config not found")
	// ErrConflict：If-Match 与 updated_at 不一致。
	ErrConflict = errors.New("config conflict")
	// ErrIncompatible：数据库 config_schema_version 比代码新，拒绝覆盖写。
	ErrIncompatible = errors.New("config schema newer than code")
)

// ValidationError 描述单个字段的校验失败。
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("字段 %q: %s", e.Field, e.Reason)
}

// ConfigView 是管理端可见的配置视图；Secret 只暴露已设置的 key 列表。
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
