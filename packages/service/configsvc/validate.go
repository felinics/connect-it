package configsvc

import (
	"fmt"
	"regexp"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Validate 按 Definition 的 ConfigFields 校验一份完整配置：
// 必填（有默认值的非 Secret 字段除外）、Pattern、Options，拒绝未知 key。
// 所有字段值都是字符串；secrets 中空串表示删除该 key（可选字段合法，
// 必填字段会命中必填检查）。
func (s *Service) Validate(t connector.Type, public map[string]any, secrets map[string]string) error {
	def, ok := s.reg.Get(t)
	if !ok {
		return ErrUnknownConnector
	}
	fields := map[string]connector.ConfigField{}
	for _, f := range def.ConfigFields {
		fields[f.Key] = f
	}

	for key, val := range public {
		f, known := fields[key]
		if !known || f.Secret {
			return &ValidationError{Field: key, Reason: "未知的公开配置字段"}
		}
		str, isStr := val.(string)
		if !isStr {
			return &ValidationError{Field: key, Reason: "值必须是字符串"}
		}
		if err := checkValue(f, str); err != nil {
			return err
		}
	}
	for key, val := range secrets {
		f, known := fields[key]
		if !known || !f.Secret {
			return &ValidationError{Field: key, Reason: "未知的 Secret 配置字段"}
		}
		if val == "" {
			continue // 空串＝删除，必填与否由下面的必填检查兜底
		}
		if err := checkValue(f, val); err != nil {
			return err
		}
	}
	for _, f := range def.ConfigFields {
		if !f.Required {
			continue
		}
		if f.Secret {
			if secrets[f.Key] == "" {
				return &ValidationError{Field: f.Key, Reason: "必填 Secret 字段缺失"}
			}
			continue
		}
		if f.DefaultValue != nil {
			continue // 默认值兜底，永不缺失
		}
		if v, _ := public[f.Key].(string); v == "" {
			return &ValidationError{Field: f.Key, Reason: "必填字段缺失"}
		}
	}
	return nil
}

func checkValue(f connector.ConfigField, val string) error {
	if f.Validation.Pattern != "" {
		re, err := regexp.Compile(f.Validation.Pattern)
		if err != nil {
			return fmt.Errorf("字段 %q 的 Pattern 非法: %w", f.Key, err)
		}
		if !re.MatchString(val) {
			return &ValidationError{Field: f.Key, Reason: "不匹配 Pattern " + f.Validation.Pattern}
		}
	}
	if len(f.Validation.Options) > 0 {
		for _, opt := range f.Validation.Options {
			if val == opt {
				return nil
			}
		}
		return &ValidationError{Field: f.Key, Reason: "不在可选值范围内"}
	}
	return nil
}
