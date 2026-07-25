package toolargs

import (
	"encoding/json"
	"net/url"
	"strconv"
	"testing"
)

// The four private copies this package replaced disagreed on the clamp:
// one clamped json.Number/int/int64, one clamped only int64, and two clamped
// none of them. Every Go type a JSON decoder can produce must now clamp
// identically at +-2^53-1.
func TestIntegerClampsEverySafeIntegerType(t *testing.T) {
	t.Parallel()

	const (
		safe   = int64(MaxSafeInteger)
		unsafe = int64(MaxSafeInteger) + 1
	)
	tests := []struct {
		name     string
		value    any
		expected int64
		accepted bool
	}{
		{"json.Number at limit", json.Number(strconv.FormatInt(safe, 10)), safe, true},
		{"json.Number beyond limit", json.Number(strconv.FormatInt(unsafe, 10)), 0, false},
		{"json.Number at negative limit", json.Number(strconv.FormatInt(-safe, 10)), -safe, true},
		{"json.Number beyond negative limit", json.Number(strconv.FormatInt(-unsafe, 10)), 0, false},
		{"json.Number not an integer", json.Number("1.5"), 0, false},
		{"int at limit", int(safe), safe, true},
		{"int beyond limit", int(unsafe), 0, false},
		{"int at negative limit", int(-safe), -safe, true},
		{"int beyond negative limit", int(-unsafe), 0, false},
		{"int64 at limit", safe, safe, true},
		{"int64 beyond limit", unsafe, 0, false},
		{"int64 at negative limit", -safe, -safe, true},
		{"int64 beyond negative limit", -unsafe, 0, false},
		{"float64 at limit", float64(safe), safe, true},
		{"float64 beyond limit", float64(unsafe), 0, false},
		{"float64 at negative limit", float64(-safe), -safe, true},
		{"float64 beyond negative limit", float64(-unsafe), 0, false},
		{"float64 fractional", 1.5, 0, false},
		{"unsupported type", "7", 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			integer, ok := Integer(test.value)
			if ok != test.accepted || integer != test.expected {
				t.Fatalf(
					"Integer(%v) = %d, %v want %d, %v",
					test.value,
					integer,
					ok,
					test.expected,
					test.accepted,
				)
			}
		})
	}
}

func TestIntegerBounds(t *testing.T) {
	t.Parallel()

	if _, ok := PositiveInteger(float64(0)); ok {
		t.Fatal("zero must not be a positive integer")
	}
	if integer, ok := PositiveInteger(float64(3)); !ok || integer != 3 {
		t.Fatalf("PositiveInteger(3) = %d, %v", integer, ok)
	}
	arguments := map[string]any{"page": float64(5), "bad": float64(500)}
	if integer, ok := OptionalInteger(
		arguments, "missing", 20, 1, 100,
	); !ok || integer != 20 {
		t.Fatalf("absent key must use the default: %d, %v", integer, ok)
	}
	if integer, ok := OptionalInteger(
		arguments, "page", 20, 1, 100,
	); !ok || integer != 5 {
		t.Fatalf("OptionalInteger(page) = %d, %v", integer, ok)
	}
	if _, ok := OptionalInteger(arguments, "bad", 20, 1, 100); ok {
		t.Fatal("value above maximum must be rejected")
	}
}

func TestTextCoercionBoundaries(t *testing.T) {
	t.Parallel()

	if !SafeText("line\nbreak", 16, true) ||
		SafeText("line\nbreak", 16, false) {
		t.Fatal("allowMultiline must gate CR/LF/TAB")
	}
	if SafeText("bell\x07", 16, true) {
		t.Fatal("control characters must always be rejected")
	}
	if _, ok := String(7, 16, false); ok {
		t.Fatal("non-string argument must be rejected")
	}
	if SimpleString(" padded", 16) || SimpleString("", 16) {
		t.Fatal("SimpleString must reject untrimmed and empty values")
	}
	if !SimpleString("ok", 16) {
		t.Fatal("SimpleString must accept bounded single-line text")
	}
}

// OptionalOutputString once bounded length only, so a Provider could ship a
// control character straight into Agent-facing text. It now shares SafeText
// with its siblings, while keeping CR/LF/TAB legal for the free-form fields it
// guards (a Linear issue description, a Datadog monitor message).
func TestOptionalOutputStringRejectsControlCharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		maxRunes int
		accepted bool
	}{
		{"plain text", "kept", 4, true},
		{"at the rune bound", "kept", 3, false},
		{"multi-byte UTF-8 counted in runes", "中文摘要", 4, true},
		{"multi-byte UTF-8 beyond the bound", "中文摘要", 3, false},
		{"emoji outside the BMP", "🙂", 1, true},
		{"invalid UTF-8", "\xff\xfe", 8, false},
		{"free-form newline and tab", "line\n\tmore", 16, true},
		{"escape sequence", "reset\x1b[0m", 16, false},
		{"NUL", "a\x00b", 16, false},
		{"BEL", "bell\x07", 16, false},
		{"DEL", "del\x7f", 16, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if OptionalOutputString(&test.value, test.maxRunes) !=
				test.accepted {
				t.Fatalf(
					"OptionalOutputString(%q, %d) = %v, want %v",
					test.value,
					test.maxRunes,
					!test.accepted,
					test.accepted,
				)
			}
		})
	}
	if !OptionalOutputString(nil, 4) {
		t.Fatal("an absent output string must be accepted")
	}
}

func TestStringListBounds(t *testing.T) {
	t.Parallel()

	if items, ok := StringList([]string{"a", "b"}, 2, 8, true); !ok ||
		len(items) != 2 {
		t.Fatalf("[]string input = %v, %v", items, ok)
	}
	if _, ok := StringList([]any{"a", "b", "c"}, 2, 8, true); ok {
		t.Fatal("maxItems must be enforced")
	}
	if _, ok := StringList([]any{"a,b"}, 2, 8, true); ok {
		t.Fatal("rejectComma must refuse comma-joined ambiguity")
	}
	if _, ok := StringList([]any{"a,b"}, 2, 8, false); !ok {
		t.Fatal("commas are allowed when the Provider takes an array")
	}
	if _, ok := StringList([]any{""}, 2, 8, true); ok {
		t.Fatal("empty items must be rejected")
	}
}

func TestQueryHelpers(t *testing.T) {
	t.Parallel()

	arguments := map[string]any{
		"sort":    "desc",
		"flag":    true,
		"bad":     7,
		"unknown": "sideways",
	}
	query := url.Values{}
	if !OptionalStringQuery(query, arguments, "absent", "absent", 8, nil) ||
		query.Has("absent") {
		t.Fatal("absent argument must not reach the query")
	}
	if !OptionalStringQuery(
		query, arguments, "sort", "sort", 8, []string{"asc", "desc"},
	) || query.Get("sort") != "desc" {
		t.Fatalf("allowed value rejected: %v", query)
	}
	if OptionalStringQuery(
		query, arguments, "unknown", "unknown", 8, []string{"asc", "desc"},
	) {
		t.Fatal("value outside the closed set must be rejected")
	}
	if OptionalStringQuery(query, arguments, "bad", "bad", 8, nil) {
		t.Fatal("non-string argument must be rejected")
	}
	if !OptionalBoolQuery(query, arguments, "flag", "flag") ||
		query.Get("flag") != "true" {
		t.Fatalf("bool query = %v", query)
	}
	if OptionalBoolQuery(query, arguments, "sort", "sort") {
		t.Fatal("non-bool argument must be rejected")
	}
}

func TestResponseStatusOfAbsentResponse(t *testing.T) {
	t.Parallel()

	if ResponseStatus(nil) != 0 {
		t.Fatal("absent response must report status 0")
	}
}
