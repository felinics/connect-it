package configsvc

import (
	"fmt"
	"sort"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/internal/fieldnorm"
	"github.com/memohai/connect-it/packages/service/store"
)

func (s *Service) normalizedWriteValues(
	def connector.Definition,
	public map[string]any,
	storedSecrets map[string]string,
	secretPatch map[string]string,
) (map[string]any, map[string]string, map[string]string, error) {
	fields := make(map[string]connector.ConfigField, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		fields[field.Key] = field
	}

	combined := make(map[string]string, len(public)+len(storedSecrets)+len(def.ConfigFields))
	for key, value := range public {
		field, known := fields[key]
		if !known || field.Secret {
			return nil, nil, nil, &ValidationError{
				Field:  key,
				Reason: "未知的公开配置字段",
			}
		}
		text, ok := value.(string)
		if !ok {
			return nil, nil, nil, &ValidationError{
				Field:  key,
				Reason: "值必须是字符串",
			}
		}
		combined[key] = text
	}
	// Existing fields removed by a Definition upgrade are pruned. This is
	// intentionally different from caller-supplied unknown keys, which fail.
	for key, value := range storedSecrets {
		field, known := fields[key]
		if known && field.Secret {
			combined[key] = value
		}
	}
	for key, value := range secretPatch {
		field, known := fields[key]
		if !known || !field.Secret {
			return nil, nil, nil, &ValidationError{
				Field:  key,
				Reason: "未知的 Secret 配置字段",
			}
		}
		if value == "" {
			delete(combined, key)
		} else {
			combined[key] = value
		}
	}

	normalized, err := fieldnorm.Normalize(def.ConfigFields, combined)
	if err != nil {
		return nil, nil, nil, validationErrorFromNormalization(err)
	}
	normalizedPublic := make(map[string]any, len(normalized))
	normalizedSecrets := make(map[string]string, len(normalized))
	for _, field := range def.ConfigFields {
		value, present := normalized[field.Key]
		if !present {
			continue
		}
		if field.Secret {
			normalizedSecrets[field.Key] = value
		} else {
			normalizedPublic[field.Key] = value
		}
	}
	return normalizedPublic, normalizedSecrets, normalized, nil
}

func validationErrorFromNormalization(err error) error {
	if fieldErr, ok := err.(*fieldnorm.Error); ok {
		return &ValidationError{Field: fieldErr.Field, Reason: fieldErr.Reason}
	}
	return err
}

// policyValuesFromRow produces the current Definition/config projection used
// by policy reconciliation. Missing rows are represented by defaults only;
// required fields may therefore be absent without inventing values.
func (s *Service) policyValuesFromRow(
	def connector.Definition,
	row store.ConnectorConfig,
	exists bool,
) (map[string]string, error) {
	values := make(map[string]string, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		if field.DefaultValue != nil {
			values[field.Key] = *field.DefaultValue
		}
	}
	if !exists {
		return values, nil
	}
	if int(row.ConfigSchemaVersion) > def.ConfigSchemaVersion {
		return nil, ErrIncompatible
	}
	public, err := unmarshalPublic(row.PublicConfig)
	if err != nil {
		return nil, err
	}
	secrets, err := s.decryptSecrets(row, def.Type)
	if err != nil {
		return nil, err
	}

	public, secrets, err = upgradeStoredConfig(
		def,
		int(row.ConfigSchemaVersion),
		public,
		secrets,
	)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]connector.ConfigField, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		fields[field.Key] = field
	}
	for key, value := range public {
		field, known := fields[key]
		if !known {
			continue
		}
		if field.Secret {
			return nil, fmt.Errorf(
				"configsvc: public_config 将 Secret 字段 %q 存为明文",
				key,
			)
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf(
				"configsvc: public_config 字段 %q 不是字符串",
				key,
			)
		}
		values[key] = text
	}
	for key, value := range secrets {
		field, known := fields[key]
		if !known {
			continue
		}
		if !field.Secret {
			return nil, fmt.Errorf(
				"configsvc: secret_config 包含非 Secret 字段 %q",
				key,
			)
		}
		values[key] = value
	}
	return values, nil
}

func upgradeStoredConfig(
	def connector.Definition,
	current int,
	public map[string]any,
	secrets map[string]string,
) (map[string]any, map[string]string, error) {
	if current >= def.ConfigSchemaVersion {
		return public, secrets, nil
	}
	upgraders := append([]connector.ConfigUpgrader(nil), def.ConfigUpgraders...)
	sort.Slice(upgraders, func(i, j int) bool {
		return upgraders[i].FromVersion < upgraders[j].FromVersion
	})
	secretValues := make(map[string]any, len(secrets))
	for key, value := range secrets {
		secretValues[key] = value
	}
	for _, upgrader := range upgraders {
		if upgrader.FromVersion < current {
			continue
		}
		if upgrader.FromVersion != current {
			return nil, nil, fmt.Errorf(
				"configsvc: 缺少从 config schema v%d 的 upgrader",
				current,
			)
		}
		var err error
		if upgrader.Upgrade != nil {
			public, secretValues, err = upgrader.Upgrade(public, secretValues)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"configsvc: upgrader v%d: %w",
					upgrader.FromVersion,
					err,
				)
			}
		}
		current++
		if current == def.ConfigSchemaVersion {
			break
		}
	}
	if current != def.ConfigSchemaVersion {
		return nil, nil, fmt.Errorf(
			"configsvc: 配置 schema 只能从 v%d 升级到 v%d，目标是 v%d",
			int(def.ConfigSchemaVersion)-1,
			current,
			def.ConfigSchemaVersion,
		)
	}
	upgradedSecrets := make(map[string]string, len(secretValues))
	for key, value := range secretValues {
		text, ok := value.(string)
		if !ok {
			return nil, nil, fmt.Errorf(
				"configsvc: upgrader 将 Secret 字段 %q 变成非字符串",
				key,
			)
		}
		upgradedSecrets[key] = text
	}
	return public, upgradedSecrets, nil
}
