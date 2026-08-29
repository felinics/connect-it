package configsvc

import (
	"fmt"
	"regexp"

	"github.com/felinics/connect-it/packages/core/connector"
)

// Validate checks a complete config against the ConfigFields of a Definition:
// required fields (except non-secret fields carrying a default), Pattern and
// Options, rejecting unknown keys. Every field value is a string. In secrets,
// an empty string deletes the key, which is valid for an optional field and
// trips the required check for a required one.
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
			return &ValidationError{Field: key, Reason: "unknown public config field"}
		}
		str, isStr := val.(string)
		if !isStr {
			return &ValidationError{Field: key, Reason: "value must be a string"}
		}
		if err := checkValue(f, str); err != nil {
			return err
		}
	}
	for key, val := range secrets {
		f, known := fields[key]
		if !known || !f.Secret {
			return &ValidationError{Field: key, Reason: "unknown secret config field"}
		}
		if val == "" {
			continue // an empty string deletes; the required check below decides if that is allowed
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
				return &ValidationError{Field: f.Key, Reason: "required secret field is missing"}
			}
			continue
		}
		if f.DefaultValue != nil {
			continue // a default is defined, so the administrator need not set it
		}
		if v, _ := public[f.Key].(string); v == "" {
			return &ValidationError{Field: f.Key, Reason: "required field is missing"}
		}
	}
	return nil
}

func checkValue(f connector.ConfigField, val string) error {
	if f.Validation.Pattern != "" {
		re, err := regexp.Compile(f.Validation.Pattern)
		if err != nil {
			return fmt.Errorf("field %q has an invalid Pattern: %w", f.Key, err)
		}
		if !re.MatchString(val) {
			return &ValidationError{Field: f.Key, Reason: "does not match Pattern " + f.Validation.Pattern}
		}
	}
	if len(f.Validation.Options) > 0 {
		for _, opt := range f.Validation.Options {
			if val == opt {
				return nil
			}
		}
		return &ValidationError{Field: f.Key, Reason: "not one of the allowed values"}
	}
	return nil
}
