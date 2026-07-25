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
	"github.com/memohai/connect-it/packages/service/svcerr"
)

var (
	// ErrUnknownConnector：connector_type 不在代码 Registry 中。
	ErrUnknownConnector = svcerr.New(svcerr.NotFound, "unknown connector type")
	// ErrNotFound：connector_configs 无该行。
	ErrNotFound = svcerr.New(svcerr.NotFound, "config not found")
	// ErrConflict：If-Match 与 updated_at 不一致。
	ErrConflict = svcerr.New(svcerr.ConfigConflict, "config conflict")
	// ErrIncompatible：数据库 config_schema_version 比代码新，拒绝覆盖写。
	ErrIncompatible = svcerr.New(svcerr.ConfigIncompatible, "config schema newer than code")
	// ErrPolicyDrift：持久化 policy identity 与当前配置/Definition 不一致。
	// 普通读取不会修复该状态；启动时的 ReconcilePolicyIdentities 负责恢复。
	// 它对调用方是内部故障，不属于对外词汇表。
	ErrPolicyDrift = errors.New("connector policy identity drift")
)

// ValidationError 描述单个字段的校验失败。
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("字段 %q: %s", e.Field, e.Reason)
}

func (e *ValidationError) ServiceError() *svcerr.Error {
	return svcerr.New(svcerr.Invalid, e.Error())
}

// ConfigView 是管理端可见的配置视图；Secret 只暴露已设置的 key 列表。
type ConfigView struct {
	ConnectorType string
	SchemaVersion int
	Public        map[string]any
	SecretKeysSet []string
	UpdatedAt     time.Time
}

// PolicySnapshot is the optimistic token that binds slow Provider validation
// or authorization preparation to the exact connector-wide policy it used.
// Creation statements compare it while holding a PostgreSQL FOR SHARE lock;
// config writers take FOR UPDATE and bump any Connection inserted first.
type PolicySnapshot struct {
	IdentityVersion  int32
	IdentityDigest   []byte
	DefinitionDigest []byte
}

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	kr  *crypto.Keyring
}

func New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service {
	return &Service{q: q, reg: reg, kr: kr}
}
