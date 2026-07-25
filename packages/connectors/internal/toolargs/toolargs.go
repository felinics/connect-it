// Package toolargs coerces Agent-supplied Tool arguments into bounded Go
// values. Every Managed connector needs the same handful of coercions, and
// each private copy was free to drift; the copies notably disagreed on the
// safe-integer clamp. This package is the single strict version.
//
// Migration note for the remaining connectors: the string helpers here accept
// any bounded, control-free text. Connectors whose private copy additionally
// required a trimmed, non-empty value (datadog, linear) must keep that check
// by filtering through SimpleString — do not relax it, and do not tighten the
// shared helper either, because other connectors accept padded free text such
// as a search term.
package toolargs

import (
	"encoding/json"
	"math"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

// MaxSafeInteger is the largest integer a JSON document can carry losslessly.
// Every integer coercion clamps to +-MaxSafeInteger regardless of the Go type
// the decoder happened to produce, so a value that survives here round-trips
// through any downstream JSON boundary unchanged.
const MaxSafeInteger = 1<<53 - 1

// Integer coerces a decoded JSON number to int64 within the safe-integer
// range. float64 must be finite and integral; json.Number must be an exact
// decimal integer literal.
func Integer(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) ||
			typed != math.Trunc(typed) ||
			math.Abs(typed) > MaxSafeInteger {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		integer, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, false
		}
		return safeInteger(integer)
	case int:
		return safeInteger(int64(typed))
	case int64:
		return safeInteger(typed)
	default:
		return 0, false
	}
}

// safeInteger rejects a value that cannot survive a JSON round trip, and never
// returns the out-of-range value alongside the rejection.
func safeInteger(value int64) (int64, bool) {
	if value < -MaxSafeInteger || value > MaxSafeInteger {
		return 0, false
	}
	return value, true
}

// PositiveInteger coerces value to a strictly positive safe integer.
func PositiveInteger(value any) (int64, bool) {
	integer, ok := Integer(value)
	return integer, ok && integer > 0
}

// OptionalInteger reads an optional bounded integer argument, substituting
// defaultValue when the key is absent.
func OptionalInteger(
	arguments map[string]any,
	key string,
	defaultValue int64,
	minimum int64,
	maximum int64,
) (int64, bool) {
	value, present := arguments[key]
	if !present {
		return defaultValue, true
	}
	integer, ok := Integer(value)
	return integer, ok && integer >= minimum && integer <= maximum
}

// SafeText reports whether value is valid UTF-8, bounded by maxRunes, and free
// of control characters. allowMultiline additionally permits CR, LF and TAB.
func SafeText(value string, maxRunes int, allowMultiline bool) bool {
	if !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > maxRunes ||
		len(value) > maxRunes*utf8.UTFMax {
		return false
	}
	for _, character := range value {
		if allowMultiline &&
			(character == '\r' || character == '\n' || character == '\t') {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// String coerces a Tool argument to bounded safe text.
func String(value any, maxRunes int, allowMultiline bool) (string, bool) {
	text, ok := value.(string)
	if !ok || !SafeText(text, maxRunes, allowMultiline) {
		return "", false
	}
	return text, true
}

// SimpleString reports whether value is a non-empty, bounded, single-line
// string with no leading or trailing whitespace — the shape safe to place in a
// query parameter or comma-joined list verbatim.
func SimpleString(value string, maxRunes int) bool {
	return value != "" &&
		strings.TrimSpace(value) == value &&
		SafeText(value, maxRunes, false)
}

// OptionalOutputString reports whether an optional Provider output string is
// either absent or bounded safe text. Provider display text reaches the Agent
// verbatim, so it goes through the same SafeText predicate as Agent-supplied
// text; CR/LF/TAB stay legal because the fields this guards are free-form
// (a Linear issue description, a Datadog monitor message), and only those
// three are legal — NUL, BEL, ESC and DEL are refused like everywhere else.
func OptionalOutputString(value *string, maxRunes int) bool {
	return value == nil || SafeText(*value, maxRunes, true)
}

// StringList coerces a JSON array argument to bounded non-empty strings.
// rejectComma refuses items containing "," for Providers that transmit the
// list as one comma-joined parameter.
func StringList(
	value any,
	maxItems int,
	maxItemRunes int,
	rejectComma bool,
) ([]string, bool) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []string:
		raw = make([]any, len(typed))
		for index := range typed {
			raw[index] = typed[index]
		}
	default:
		return nil, false
	}
	if len(raw) > maxItems {
		return nil, false
	}
	items := make([]string, len(raw))
	for index, item := range raw {
		text, ok := String(item, maxItemRunes, false)
		if !ok || text == "" ||
			(rejectComma && strings.Contains(text, ",")) {
			return nil, false
		}
		items[index] = text
	}
	return items, true
}

// OptionalStringQuery copies an optional string argument into query. allowed,
// when non-empty, closes the accepted value set.
func OptionalStringQuery(
	query url.Values,
	arguments map[string]any,
	argumentKey string,
	queryKey string,
	maxRunes int,
	allowed []string,
) bool {
	value, present := arguments[argumentKey]
	if !present {
		return true
	}
	text, ok := String(value, maxRunes, false)
	if !ok || text == "" || !permitted(text, allowed) {
		return false
	}
	query.Set(queryKey, text)
	return true
}

func permitted(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// OptionalBoolQuery copies an optional boolean argument into query.
func OptionalBoolQuery(
	query url.Values,
	arguments map[string]any,
	argumentKey string,
	queryKey string,
) bool {
	value, present := arguments[argumentKey]
	if !present {
		return true
	}
	boolean, ok := value.(bool)
	if !ok {
		return false
	}
	query.Set(queryKey, strconv.FormatBool(boolean))
	return true
}

// ResponseStatus reads the audited upstream status from a possibly absent
// response.
func ResponseStatus(response *providerkit.Response) int {
	if response == nil {
		return 0
	}
	return response.StatusCode
}
