package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

const maxSafeInteger = int64(1<<53 - 1)

// CompiledSchema is an immutable, registration-time-resolved JSON Schema.
// It deliberately hides jsonschema-go so service remains independent of that
// implementation detail. Its methods are safe for concurrent use as long as
// callers do not mutate the same instance map concurrently.
type CompiledSchema struct {
	resolved *jsonschema.Resolved
}

// Validate checks an already normalized JSON value.
func (s *CompiledSchema) Validate(instance any) error {
	if s == nil || s.resolved == nil {
		return fmt.Errorf("compiled schema is nil")
	}
	return s.resolved.Validate(instance)
}

// ApplyDefaultsAndValidate applies accepted property defaults, then validates
// the same map.
func (s *CompiledSchema) ApplyDefaultsAndValidate(instance map[string]any) error {
	if s == nil || s.resolved == nil {
		return fmt.Errorf("compiled schema is nil")
	}
	if instance == nil {
		return fmt.Errorf("instance map is nil")
	}
	if err := s.resolved.ApplyDefaults(&instance); err != nil {
		return err
	}
	return s.resolved.Validate(instance)
}

// CompiledToolSchemas is an immutable snapshot of a registered tool's schema
// execution contract. Output is nil when the Tool does not declare one.
type CompiledToolSchemas struct {
	Input         *CompiledSchema
	Output        *CompiledSchema
	MaxInputBytes int64
}

// childKind says how lintSchema must descend into a keyword's value. Every
// allowed keyword carries one, so a newly reviewed keyword cannot be added
// without deciding how its subtree is linted.
type childKind int

const (
	childNone         childKind = iota // no subtree to lint
	childNumeric                       // numeric literal, recursively
	childSchema                        // one subschema
	childSchemaArray                   // array of subschemas
	childSchemaMap                     // object whose values are subschemas
	childProperties                    // schema map that carries default reachability
	childItems                         // subschema or array of subschemas (draft-07)
	childDependencies                  // subschema or string array (draft-07)
)

// This is the intentionally reviewed first-phase keyword set. New keywords
// must be added together with compatibility and default-semantics tests.
var allowedSchemaKeywords = map[string]childKind{
	// Core.
	"$comment":     childNone,
	"$schema":      childNone,
	"dependencies": childDependencies, // draft-07

	// Annotations.
	"default":     childNone, // linted by the dedicated default block
	"deprecated":  childNone,
	"description": childNone,
	"examples":    childNone,
	"readOnly":    childNone,
	"title":       childNone,
	"writeOnly":   childNone,

	// Primitive validation.
	"const":            childNumeric,
	"enum":             childNumeric,
	"exclusiveMaximum": childNumeric,
	"exclusiveMinimum": childNumeric,
	"maxLength":        childNone,
	"maximum":          childNumeric,
	"minLength":        childNone,
	"minimum":          childNumeric,
	"multipleOf":       childNumeric,
	"pattern":          childNone,
	"type":             childNone,

	// Arrays.
	"additionalItems": childSchema, // draft-07
	"contains":        childSchema,
	"items":           childItems,
	"maxContains":     childNone,
	"maxItems":        childNone,
	"minContains":     childNone,
	"minItems":        childNone,
	"prefixItems":     childSchemaArray,
	"uniqueItems":     childNone,

	// Objects.
	"additionalProperties": childSchema,
	"dependentRequired":    childNone,
	"dependentSchemas":     childSchemaMap,
	"maxProperties":        childNone,
	"minProperties":        childNone,
	"patternProperties":    childSchemaMap,
	"properties":           childProperties,
	"propertyNames":        childSchema,
	"required":             childNone,

	// Boolean composition and conditionals.
	"allOf": childSchemaArray,
	"anyOf": childSchemaArray,
	"else":  childSchema,
	"if":    childSchema,
	"not":   childSchema,
	"oneOf": childSchemaArray,
	"then":  childSchema,
}

// schemaPosition says what a subschema does for the value it is attached to.
// Only a value position describes a value a caller can actually send, so only
// a value position must be closed: an additive position merely adds
// constraints on top of one, and a predicate position is matched against a
// value rather than accepting it.
type schemaPosition uint8

const (
	valuePosition schemaPosition = iota
	additivePosition
	predicatePosition
	// passthroughPosition marks a schema whose contract is not ours to close
	// (a Remote MCP tool proxies the upstream server's schema), so the
	// closed-contract rules apply neither to it nor to anything below it.
	passthroughPosition
)

type schemaLintContext struct {
	// defaultsReachable is true only on the Properties-only traversal that
	// jsonschema-go ApplyDefaults implements.
	defaultsReachable bool
	directProperty    bool
	position          schemaPosition
}

// childContext keeps a predicate or passthrough position sticky: nothing under
// `not`/`if` is a contract the caller fills in, and nothing under an
// upstream-owned schema is ours to close.
func childContext(parent schemaLintContext, position schemaPosition) schemaLintContext {
	if parent.position == predicatePosition ||
		parent.position == passthroughPosition {
		position = parent.position
	}
	return schemaLintContext{position: position}
}

// LintClosedSchema holds a raw schema to the closed-contract rules on top of
// the keyword lint every registered schema already gets: the accepted value
// kind is explicit, objects reject undeclared properties, and arrays declare
// their item contract. It is for the schemas whose contract this service owns
// — a schema that only proxies an upstream one cannot satisfy them.
func LintClosedSchema(raw json.RawMessage, label string) error {
	_, err := compileClosedSchema(raw, label, true)
	return err
}

func compileSchema(raw json.RawMessage, label string) (*CompiledSchema, error) {
	return compileClosedSchema(raw, label, false)
}

// compileClosedSchema lints and resolves a schema. closed says whether the
// closed-contract rules apply on top of the keyword lint.
func compileClosedSchema(
	raw json.RawMessage,
	label string,
	closed bool,
) (*CompiledSchema, error) {
	value, err := decodeSchemaJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: JSON 无效: %w", label, err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: 顶层必须是 schema object", label)
	}
	if typ, ok := root["type"].(string); !ok || typ != "object" {
		return nil, fmt.Errorf("%s: 顶层 type 必须是 object", label)
	}
	rootContext := schemaLintContext{defaultsReachable: true}
	if !closed {
		rootContext.position = passthroughPosition
	}
	if err := lintSchema(root, "$", rootContext); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}

	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%s: schema 解析失败: %w", label, err)
	}
	resolved, err := parsed.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return nil, fmt.Errorf("%s: schema resolve 失败: %w", label, err)
	}
	return &CompiledSchema{resolved: resolved}, nil
}

func decodeSchemaJSON(raw json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("不能为空")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("只能包含一个 JSON value")
	} else if err != io.EOF {
		return nil, fmt.Errorf("包含无效尾随内容: %w", err)
	}
	return value, nil
}

func lintSchema(value any, path string, ctx schemaLintContext) error {
	if boolean, ok := value.(bool); ok {
		if boolean && ctx.position == valuePosition {
			return fmt.Errorf("%s: accept-all boolean schema 不被允许", path)
		}
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s: subschema 必须是 object 或 boolean", path)
	}
	if len(object) == 0 {
		if ctx.position == valuePosition {
			return fmt.Errorf("%s: 空 schema 接受任意值", path)
		}
		return nil
	}

	keys := make([]string, 0, len(object))
	for keyword := range object {
		keys = append(keys, keyword)
	}
	sort.Strings(keys)
	for _, keyword := range keys {
		switch keyword {
		case "$ref":
			return fmt.Errorf("%s/$ref: reference 不受支持；schema 必须内联", path)
		case "$dynamicRef", "$dynamicAnchor":
			return fmt.Errorf("%s/%s: dynamic reference 不受支持", path, keyword)
		default:
			if _, allowed := allowedSchemaKeywords[keyword]; !allowed {
				return fmt.Errorf("%s/%s: 未知或未审查的 schema keyword", path, keyword)
			}
		}
	}

	if rawVersion, exists := object["$schema"]; exists {
		version, ok := rawVersion.(string)
		if !ok || !supportedSchemaVersion(version) {
			return fmt.Errorf("%s/$schema: 只支持 draft-07 或 draft 2020-12", path)
		}
	}

	if rawDefault, exists := object["default"]; exists {
		if !ctx.defaultsReachable || !ctx.directProperty {
			return fmt.Errorf("%s/default: default 只允许用于非 required 的直接 object property", path)
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf", "if", "then", "else"} {
			if _, exists := object[keyword]; exists {
				return fmt.Errorf("%s/default: 带 %s 的 property 不支持 default", path, keyword)
			}
		}
		if err := lintNumericValue(rawDefault, path+"/default"); err != nil {
			return err
		}
	}
	// Every keyword the reviewed table marks childNumeric carries JSON numbers
	// that must stay inside the safe-integer range. keys is already sorted, so
	// the first offending keyword is reported deterministically.
	for _, keyword := range keys {
		if allowedSchemaKeywords[keyword] != childNumeric {
			continue
		}
		if err := lintNumericValue(object[keyword], path+"/"+keyword); err != nil {
			return err
		}
	}

	if ctx.position == valuePosition {
		if err := lintClosedValueSchema(object, path); err != nil {
			return err
		}
	}

	required := stringSet(object["required"])
	if rawProperties, exists := object["properties"]; exists {
		properties, ok := rawProperties.(map[string]any)
		if !ok {
			return fmt.Errorf("%s/properties: 必须是 object", path)
		}
		names := sortedMapKeys(properties)
		for _, name := range names {
			propertyContext := schemaLintContext{
				// jsonschema-go does not recurse through a required property
				// while applying defaults, even when that property is present.
				defaultsReachable: ctx.defaultsReachable && !required[name],
				directProperty:    true,
				position:          childContext(ctx, valuePosition).position,
			}
			if err := lintSchema(properties[name], jsonPointer(path+"/properties", name), propertyContext); err != nil {
				return err
			}
		}
	}

	for _, schemaMap := range []struct {
		keyword  string
		position schemaPosition
	}{
		{"patternProperties", valuePosition},
		{"dependentSchemas", additivePosition},
	} {
		keyword, position := schemaMap.keyword, schemaMap.position
		if rawMap, exists := object[keyword]; exists {
			schemas, ok := rawMap.(map[string]any)
			if !ok {
				return fmt.Errorf("%s/%s: 必须是 object", path, keyword)
			}
			for _, name := range sortedMapKeys(schemas) {
				if err := lintSchema(
					schemas[name],
					jsonPointer(path+"/"+keyword, name),
					childContext(ctx, position),
				); err != nil {
					return err
				}
			}
		}
	}

	for _, keyword := range []string{
		"additionalItems", "additionalProperties", "contains", "items", "not",
		"propertyNames", "if", "then", "else",
	} {
		raw, exists := object[keyword]
		if !exists {
			continue
		}
		position := valuePosition
		switch keyword {
		case "contains", "not", "propertyNames", "if":
			position = predicatePosition
		case "then", "else":
			position = additivePosition
		}
		if keyword == "items" {
			if schemas, ok := raw.([]any); ok {
				for index, child := range schemas {
					if err := lintSchema(
						child,
						fmt.Sprintf("%s/items/%d", path, index),
						childContext(ctx, valuePosition),
					); err != nil {
						return err
					}
				}
				continue
			}
		}
		if err := lintSchema(raw, path+"/"+keyword, childContext(ctx, position)); err != nil {
			return err
		}
	}

	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		raw, exists := object[keyword]
		if !exists {
			continue
		}
		schemas, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("%s/%s: 必须是 schema array", path, keyword)
		}
		for index, child := range schemas {
			position := valuePosition
			if keyword == "allOf" {
				// An allOf branch only has to be closed when it is the thing
				// that decides the value kind for a parent that does not.
				position = additivePosition
				if ctx.position == valuePosition &&
					!constrainsValueKindWithoutAllOf(object) &&
					valueConstrainsKind(child) {
					position = valuePosition
				}
			}
			if err := lintSchema(
				child,
				fmt.Sprintf("%s/%s/%d", path, keyword, index),
				childContext(ctx, position),
			); err != nil {
				return err
			}
		}
	}

	if rawDependencies, exists := object["dependencies"]; exists {
		dependencies, ok := rawDependencies.(map[string]any)
		if !ok {
			return fmt.Errorf("%s/dependencies: 必须是 object", path)
		}
		for _, name := range sortedMapKeys(dependencies) {
			dependency := dependencies[name]
			if _, ok := dependency.([]any); ok {
				continue
			}
			if err := lintSchema(
				dependency,
				jsonPointer(path+"/dependencies", name),
				childContext(ctx, additivePosition),
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// lintClosedValueSchema enforces the closed-contract rules on a subschema that
// describes a value a caller sends or a Tool returns: the accepted value kind
// is explicit, objects reject undeclared properties, and arrays declare their
// item contract. An open contract here means unvalidated data reaches a
// Provider handler or an MCP client.
func lintClosedValueSchema(object map[string]any, path string) error {
	if !constrainsValueKind(object) {
		return fmt.Errorf("%s: 必须显式约束接受的值类型（type/const/enum）", path)
	}
	if typeContains(object["type"], "object") || hasAnyKeyword(object, []string{
		"properties", "patternProperties", "additionalProperties",
	}) {
		if closed, declared := object["additionalProperties"].(bool); !declared || closed {
			return fmt.Errorf(
				"%s: object schema 的 additionalProperties 必须显式为 false（当前 %v）",
				path,
				object["additionalProperties"],
			)
		}
	}
	if typeContains(object["type"], "array") || hasAnyKeyword(object, []string{
		"items", "prefixItems", "additionalItems",
	}) {
		items, declared := object["items"]
		if !declared {
			return fmt.Errorf("%s: array schema 缺少 items", path)
		}
		if _, tuple := items.([]any); tuple {
			if closed, declared := object["additionalItems"].(bool); !declared || closed {
				return fmt.Errorf(
					"%s: tuple array 的 additionalItems 必须显式为 false（当前 %v）",
					path,
					object["additionalItems"],
				)
			}
		}
	}
	return nil
}

func constrainsValueKind(object map[string]any) bool {
	if constrainsValueKindWithoutAllOf(object) {
		return true
	}
	children, _ := object["allOf"].([]any)
	for _, child := range children {
		if valueConstrainsKind(child) {
			return true
		}
	}
	return false
}

func constrainsValueKindWithoutAllOf(object map[string]any) bool {
	if hasAnyKeyword(object, []string{"type", "const", "enum"}) {
		return true
	}
	// A branch set only pins the value kind when every branch does.
	for _, keyword := range []string{"oneOf", "anyOf"} {
		children, ok := object[keyword].([]any)
		if !ok || len(children) == 0 {
			continue
		}
		constrained := true
		for _, child := range children {
			if !valueConstrainsKind(child) {
				constrained = false
				break
			}
		}
		if constrained {
			return true
		}
	}
	return false
}

func valueConstrainsKind(value any) bool {
	if boolean, ok := value.(bool); ok {
		return !boolean
	}
	object, ok := value.(map[string]any)
	return ok && constrainsValueKind(object)
}

func hasAnyKeyword(object map[string]any, keywords []string) bool {
	for _, keyword := range keywords {
		if _, exists := object[keyword]; exists {
			return true
		}
	}
	return false
}

func typeContains(value any, want string) bool {
	switch typed := value.(type) {
	case string:
		return typed == want
	case []any:
		for _, item := range typed {
			if item == want {
				return true
			}
		}
	}
	return false
}

func lintNumericValue(value any, path string) error {
	switch value := value.(type) {
	case json.Number:
		rational, ok := new(big.Rat).SetString(value.String())
		if !ok {
			return fmt.Errorf("%s: 数值 literal 无效", path)
		}
		floatValue, _ := strconv.ParseFloat(value.String(), 64)
		if math.IsInf(floatValue, 0) || math.IsNaN(floatValue) {
			return fmt.Errorf("%s: 数值 literal 不能表示为有限 float64", path)
		}
		if rational.IsInt() {
			absolute := new(big.Int).Abs(rational.Num())
			if absolute.Cmp(big.NewInt(maxSafeInteger)) > 0 {
				return fmt.Errorf("%s: integer literal 超出 JavaScript safe-integer 范围", path)
			}
		}
	case []any:
		for index, child := range value {
			if err := lintNumericValue(child, fmt.Sprintf("%s/%d", path, index)); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, name := range sortedMapKeys(value) {
			if err := lintNumericValue(value[name], jsonPointer(path, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func supportedSchemaVersion(version string) bool {
	switch version {
	case "http://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft-07/schema#",
		"https://json-schema.org/draft/2020-12/schema":
		return true
	default:
		return false
	}
}

func stringSet(value any) map[string]bool {
	set := map[string]bool{}
	values, _ := value.([]any)
	for _, value := range values {
		if text, ok := value.(string); ok {
			set[text] = true
		}
	}
	return set
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func jsonPointer(parent, name string) string {
	name = strings.ReplaceAll(name, "~", "~0")
	name = strings.ReplaceAll(name, "/", "~1")
	return parent + "/" + name
}
