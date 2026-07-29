// Package status derives the runtime status of a connector from its
// Definition and the administrator config. Pure functions, no I/O.
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

// ConfigState is the in-memory view of a connector_configs row. It never
// carries decrypted secrets.
type ConfigState struct {
	Exists        bool
	SchemaVersion int
	PublicValues  map[string]any
	// SecretKeysSet records which secret fields have been set. The values
	// themselves never leave the database.
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
			continue // a default is defined, so the administrator need not set it
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
