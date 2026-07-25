package exec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

const (
	maxSafeJSONInteger    = int64(1<<53 - 1)
	maxJSONNumberBytes    = 128
	maxJSONNumberExponent = 128
)

func exceedsInputLimit(raw json.RawMessage, limit int64) bool {
	return int64(len(raw)) > limit
}

func normalizeToolArguments(
	raw json.RawMessage,
	schema *registry.CompiledSchema,
) (map[string]any, json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	value, err := decodeNormalizedJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("arguments must be a JSON object")
	}
	if schema == nil {
		return nil, nil, fmt.Errorf("compiled input schema is missing")
	}
	if err := schema.ApplyDefaultsAndValidate(object); err != nil {
		return nil, nil, fmt.Errorf("input schema validation failed: %w", err)
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal normalized arguments: %w", err)
	}
	return object, normalized, nil
}

func validateStructuredOutput(
	raw json.RawMessage,
	schema *registry.CompiledSchema,
) error {
	if schema == nil {
		return nil
	}
	if len(raw) == 0 {
		return fmt.Errorf("structured output is required")
	}
	value, err := decodeNormalizedJSON(raw)
	if err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("structured output must be a JSON object")
	}
	if err := schema.Validate(object); err != nil {
		return fmt.Errorf("output schema validation failed: %w", err)
	}
	return nil
}

func validateBackendResult(
	result connector.ToolResultData,
	output *registry.CompiledSchema,
) (connector.ToolResultData, error) {
	if result.Failure != nil && len(result.Structured) > 0 {
		return connector.ToolResultData{}, ErrInvalidToolResult
	}
	if result.Failed() || output == nil {
		return result, nil
	}
	if err := validateStructuredOutput(result.Structured, output); err != nil {
		return failureResult(
			connector.FailureInvalidResponse,
			"provider returned an invalid response",
		), nil
	}
	return result, nil
}

func failureResult(
	code connector.FailureCode,
	message string,
) connector.ToolResultData {
	return connector.ToolResultData{
		Failure: &connector.ToolFailure{
			Code:    code,
			Message: message,
		},
	}
}

func decodeNormalizedJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("JSON must contain exactly one value")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return normalizeJSONNumbers(value, "$")
}

func normalizeJSONNumbers(value any, path string) (any, error) {
	switch value := value.(type) {
	case json.Number:
		numberText := value.String()
		if !boundedJSONNumber(numberText) {
			return nil, fmt.Errorf("%s: JSON number exceeds parsing limits", path)
		}
		rational, ok := new(big.Rat).SetString(numberText)
		if !ok {
			return nil, fmt.Errorf("%s: invalid JSON number", path)
		}
		number, err := strconv.ParseFloat(numberText, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, fmt.Errorf("%s: invalid JSON number", path)
		}
		if math.IsInf(number, 0) || math.IsNaN(number) {
			return nil, fmt.Errorf("%s: JSON number is not a finite float64", path)
		}
		if rational.IsInt() {
			absolute := new(big.Int).Abs(rational.Num())
			if absolute.Cmp(big.NewInt(maxSafeJSONInteger)) > 0 {
				return nil, fmt.Errorf("%s: JSON integer exceeds the safe-integer range", path)
			}
		}
		return number, nil
	case map[string]any:
		for key, child := range value {
			normalized, err := normalizeJSONNumbers(child, path+"/"+escapeJSONPointer(key))
			if err != nil {
				return nil, err
			}
			value[key] = normalized
		}
		return value, nil
	case []any:
		for index, child := range value {
			normalized, err := normalizeJSONNumbers(child, fmt.Sprintf("%s/%d", path, index))
			if err != nil {
				return nil, err
			}
			value[index] = normalized
		}
		return value, nil
	default:
		return value, nil
	}
}

// boundedJSONNumber limits attacker-controlled work before math/big sees the
// literal. A short token such as 1e9999999 would otherwise make SetString
// allocate work proportional to the exponent before the safe-integer and
// finite-float checks can reject it.
func boundedJSONNumber(value string) bool {
	if value == "" || len(value) > maxJSONNumberBytes {
		return false
	}
	exponentAt := strings.IndexAny(value, "eE")
	if exponentAt < 0 {
		return true
	}
	exponent, err := strconv.ParseInt(
		value[exponentAt+1:],
		10,
		16,
	)
	return err == nil &&
		exponent >= -maxJSONNumberExponent &&
		exponent <= maxJSONNumberExponent
}

func escapeJSONPointer(value string) string {
	var buffer bytes.Buffer
	for _, r := range value {
		switch r {
		case '~':
			buffer.WriteString("~0")
		case '/':
			buffer.WriteString("~1")
		default:
			buffer.WriteRune(r)
		}
	}
	return buffer.String()
}
