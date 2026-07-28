// Package status 由 Definition 和管理员配置实时计算 Connector 运行状态。
// 纯函数，无 I/O。
package status

import (
	"github.com/memohai/connect-it/packages/core/connector"
)

type Status string

const (
	NeedsConfig        Status = "needs_config"
	ConfigIncompatible Status = "config_incompatible"
	Ready              Status = "ready"
	Deprecated         Status = "deprecated"
	DefinitionMissing  Status = "definition_missing"
)

// ConfigState 是 connector_configs 行的内存视图，不含 secret 明文。
type ConfigState struct {
	Exists        bool
	SchemaVersion int
	PublicValues  map[string]any
	// SecretKeysSet 记录哪些 Secret 字段已被设置（值不出库）。
	SecretKeysSet map[string]bool
}

func Compute(def *connector.Definition, cfg ConfigState) Status {
	if def == nil {
		return DefinitionMissing
	}
	if def.Deprecated {
		return Deprecated
	}
	if cfg.Exists && cfg.SchemaVersion > def.ConfigSchemaVersion {
		return ConfigIncompatible
	}
	if missingRequired(def, cfg) {
		return NeedsConfig
	}
	return Ready
}

func missingRequired(def *connector.Definition, cfg ConfigState) bool {
	for _, f := range def.ConfigFields {
		if !f.Required {
			continue
		}
		if f.Secret {
			if !cfg.SecretKeysSet[f.Key] {
				return true
			}
			continue
		}
		if f.DefaultValue != nil {
			continue // 已定义默认值，不要求管理员再次配置。
		}
		v, ok := cfg.PublicValues[f.Key]
		if !ok {
			return true
		}
		if s, isStr := v.(string); isStr && s == "" {
			return true
		}
	}
	return false
}
