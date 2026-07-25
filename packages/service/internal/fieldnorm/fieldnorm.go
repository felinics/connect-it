// Package fieldnorm normalizes Definition-declared string fields without I/O.
// Config and credential boundaries share this implementation so validators and
// encrypted persistence receive the same values.
package fieldnorm

import (
	"fmt"
	"regexp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

// Error identifies one invalid input field without including its value.
type Error struct {
	Field  string
	Reason string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Field == "" {
		return "field normalization failed: " + e.Reason
	}
	return fmt.Sprintf("field %q: %s", e.Field, e.Reason)
}

// Normalize validates and returns a defensive, normalized field map.
//
// Defaults apply only when a key is omitted. An explicitly supplied empty
// value remains explicit and therefore cannot bypass required, Pattern, or
// Options checks. Unknown keys are rejected. InputURL values use providerkit's
// canonical base-URL parser so connector config and per-connection credential
// endpoints have one representation at validation, persistence, and runtime.
// Other values remain byte-for-byte stable because credentials may contain
// meaningful whitespace.
func Normalize(
	definitions []connector.ConfigField,
	input map[string]string,
) (map[string]string, error) {
	byKey := make(map[string]connector.ConfigField, len(definitions))
	for _, field := range definitions {
		if field.Key == "" {
			return nil, &Error{Reason: "definition contains an empty key"}
		}
		if _, duplicate := byKey[field.Key]; duplicate {
			return nil, &Error{
				Field:  field.Key,
				Reason: "definition contains a duplicate key",
			}
		}
		byKey[field.Key] = field
	}

	normalized := make(map[string]string, len(input)+len(definitions))
	for key, value := range input {
		field, known := byKey[key]
		if !known {
			return nil, &Error{Field: key, Reason: "field is not declared"}
		}
		normalizedValue, err := normalizeValue(field, value)
		if err != nil {
			return nil, err
		}
		if err := validateValue(field, normalizedValue); err != nil {
			return nil, err
		}
		normalized[key] = normalizedValue
	}

	for _, field := range definitions {
		value, present := normalized[field.Key]
		if !present && field.DefaultValue != nil {
			var err error
			value, err = normalizeValue(field, *field.DefaultValue)
			if err != nil {
				return nil, err
			}
			if err := validateValue(field, value); err != nil {
				return nil, err
			}
			normalized[field.Key] = value
			present = true
		}
		if field.Required && (!present || value == "") {
			return nil, &Error{
				Field:  field.Key,
				Reason: "required field is missing",
			}
		}
	}
	return normalized, nil
}

func normalizeValue(field connector.ConfigField, value string) (string, error) {
	if field.InputType != connector.InputURL {
		return value, nil
	}
	normalized, err := providerkit.NormalizeBaseURL(value)
	if err != nil {
		return "", &Error{
			Field:  field.Key,
			Reason: "value is not a valid Provider base URL",
		}
	}
	return normalized, nil
}

func validateValue(field connector.ConfigField, value string) error {
	if field.Validation.Pattern != "" {
		expression, err := regexp.Compile(field.Validation.Pattern)
		if err != nil {
			return &Error{
				Field:  field.Key,
				Reason: "definition contains an invalid Pattern",
			}
		}
		if !expression.MatchString(value) {
			return &Error{
				Field:  field.Key,
				Reason: "value does not match Pattern",
			}
		}
	}
	if len(field.Validation.Options) > 0 {
		for _, option := range field.Validation.Options {
			if value == option {
				return nil
			}
		}
		return &Error{
			Field:  field.Key,
			Reason: "value is not one of the declared Options",
		}
	}
	return nil
}
