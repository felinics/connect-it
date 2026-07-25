package credentialvalidator

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/memohai/connect-it/packages/core/connector"
)

// ToolFields projects the exact credential string set supplied by Engine.
// Provider-specific code remains responsible for interpreting each value.
func ToolFields(
	call connector.ToolCallContext,
	connectorType connector.Type,
	keys ...string,
) (map[string]string, bool) {
	if call.ConnectorType != connectorType ||
		len(call.Credential) != len(keys) {
		return nil, false
	}
	fields := make(map[string]string, len(keys))
	for _, key := range keys {
		if key == "" {
			return nil, false
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, false
		}
		value, ok := call.Credential[key].(string)
		if !ok {
			return nil, false
		}
		fields[key] = value
	}
	return fields, true
}

// OpaqueSecret accepts a bounded, single-line credential without guessing a
// Provider-specific prefix or alphabet.
func OpaqueSecret(value string) bool {
	if value == "" || !utf8.ValidString(value) ||
		len(value) > 4096*utf8.UTFMax ||
		utf8.RuneCountInString(value) > 4096 ||
		strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) ||
			character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
