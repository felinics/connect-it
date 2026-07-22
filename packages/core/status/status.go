// Package status 由 Definition＋配置状态＋健康数据实时计算 Connector 运行状态。
// 纯函数，无 I/O。
package status

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

type Status string

const (
	CatalogOnly        Status = "catalog_only"
	NeedsConfig        Status = "needs_config"
	ConfigIncompatible Status = "config_incompatible"
	Ready              Status = "ready"
	Degraded           Status = "degraded"
	Deprecated         Status = "deprecated"
	DefinitionMissing  Status = "definition_missing"
)

const (
	degradedFailureThreshold = 3
	degradedWindow           = 15 * time.Minute
)

// ConfigState 是 connector_configs 行的内存视图，不含 secret 明文。
type ConfigState struct {
	Exists        bool
	SchemaVersion int
	PublicValues  map[string]any
	// SecretKeysSet 记录哪些 Secret 字段已被设置（值不出库）。
	SecretKeysSet map[string]bool
	// MCPVerified / MCPVerifiedEndpoint 对应 mcp:verify 的结果。
	MCPVerified         bool
	MCPVerifiedEndpoint string
}

// Health 是 connector_health 行的内存视图。
type Health struct {
	ConsecutiveFailures int
	LastErrorAt         time.Time
}

func Compute(def *connector.Definition, cfg ConfigState, h Health, now time.Time) Status {
	if def == nil {
		return DefinitionMissing
	}
	if def.Deprecated {
		return Deprecated
	}
	if len(def.Tools) == 0 {
		return CatalogOnly
	}
	if cfg.Exists && cfg.SchemaVersion > def.ConfigSchemaVersion {
		return ConfigIncompatible
	}
	if missingRequired(def, cfg) || selfHostedUnverified(def, cfg) {
		return NeedsConfig
	}
	if h.ConsecutiveFailures >= degradedFailureThreshold &&
		now.Sub(h.LastErrorAt) <= degradedWindow {
		return Degraded
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
			continue // 默认值兜底，永不缺失
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

func selfHostedUnverified(def *connector.Definition, cfg ConfigState) bool {
	for _, s := range def.RemoteMCPServers {
		if s.Endpoint.Source != connector.EndpointConfigField {
			continue
		}
		current, _ := cfg.PublicValues[s.Endpoint.ConfigFieldKey].(string)
		if current == "" {
			return true // endpoint 未填也算未就绪
		}
		if !cfg.MCPVerified || cfg.MCPVerifiedEndpoint != current {
			return true
		}
	}
	return false
}
