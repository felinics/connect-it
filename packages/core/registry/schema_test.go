package registry_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestToolSchemasCacheAppliesDefaultsAndValidates(t *testing.T) {
	def := makeValid()
	def.Tools[0].InputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{
			"limit":{"type":"integer","minimum":1,"default":20}
		},
		"additionalProperties":false
	}`)
	def.Tools[0].OutputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{"id":{"type":"string"}},
		"required":["id"],
		"additionalProperties":false
	}`)
	def.Tools[1].OutputSchema = nil

	r := registry.New()
	if err := r.Register(def, "create_item"); err != nil {
		t.Fatal(err)
	}

	schemas, ok := r.ToolSchemas(def.Type, def.Tools[0].ID)
	if !ok {
		t.Fatal("registered tool schema cache missing")
	}
	if schemas.Input == nil || schemas.Output == nil {
		t.Fatalf("compiled schemas = %+v, want input and output", schemas)
	}
	if schemas.MaxInputBytes != connector.DefaultMaxInputBytes {
		t.Fatalf("MaxInputBytes = %d, want %d", schemas.MaxInputBytes, connector.DefaultMaxInputBytes)
	}

	input := map[string]any{}
	if err := schemas.Input.ApplyDefaultsAndValidate(input); err != nil {
		t.Fatalf("apply defaults and validate: %v", err)
	}
	if got, ok := input["limit"].(float64); !ok || got != 20 {
		t.Fatalf("default limit = %#v, want float64(20)", input["limit"])
	}
	if err := schemas.Input.Validate(map[string]any{"limit": "twenty"}); err == nil {
		t.Fatal("invalid input should fail compiled schema validation")
	}
	if err := schemas.Input.ApplyDefaultsAndValidate(nil); err == nil {
		t.Fatal("nil input map should return an error instead of panicking")
	}
	if err := schemas.Output.Validate(map[string]any{"id": "item_1"}); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}
	if err := schemas.Output.Validate(map[string]any{}); err == nil {
		t.Fatal("missing required output property should fail")
	}

	withoutOutput, ok := r.ToolSchemas(def.Type, def.Tools[1].ID)
	if !ok || withoutOutput.Input == nil || withoutOutput.Output != nil {
		t.Fatalf("tool without output schema cache = %+v, ok=%v", withoutOutput, ok)
	}
	if _, ok := r.ToolSchemas(def.Type, "missing"); ok {
		t.Fatal("unknown tool should not have a schema cache entry")
	}
	if _, ok := r.ToolSchemas("missing", def.Tools[0].ID); ok {
		t.Fatal("unknown connector should not have a schema cache entry")
	}

	// The returned struct is a value snapshot; changing it cannot alter Registry.
	schemas.MaxInputBytes = 1
	again, _ := r.ToolSchemas(def.Type, def.Tools[0].ID)
	if again.MaxInputBytes != connector.DefaultMaxInputBytes {
		t.Fatalf("mutating snapshot changed cache: got %d", again.MaxInputBytes)
	}
}

func TestToolMaxInputBytesRegistration(t *testing.T) {
	t.Run("zero uses default", func(t *testing.T) {
		def := makeValid()
		def.Tools[0].MaxInputBytes = 0
		r := registry.New()
		if err := r.Register(def, "create_item"); err != nil {
			t.Fatal(err)
		}
		schemas, _ := r.ToolSchemas(def.Type, def.Tools[0].ID)
		if schemas.MaxInputBytes != connector.DefaultMaxInputBytes {
			t.Fatalf("got %d, want %d", schemas.MaxInputBytes, connector.DefaultMaxInputBytes)
		}
	})

	t.Run("absolute limit accepted", func(t *testing.T) {
		def := makeValid()
		def.Tools[0].MaxInputBytes = connector.AbsoluteMaxInputBytes
		r := registry.New()
		if err := r.Register(def, "create_item"); err != nil {
			t.Fatal(err)
		}
		schemas, _ := r.ToolSchemas(def.Type, def.Tools[0].ID)
		if schemas.MaxInputBytes != connector.AbsoluteMaxInputBytes {
			t.Fatalf("got %d, want %d", schemas.MaxInputBytes, connector.AbsoluteMaxInputBytes)
		}
	})

	for _, tc := range []struct {
		name  string
		limit int64
	}{
		{name: "negative", limit: -1},
		{name: "above absolute limit", limit: connector.AbsoluteMaxInputBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValid()
			def.Tools[0].MaxInputBytes = tc.limit
			err := registry.New().Register(def, "create_item")
			if err == nil || !strings.Contains(err.Error(), "MaxInputBytes") {
				t.Fatalf("limit %d error = %v, want MaxInputBytes error", tc.limit, err)
			}
		})
	}
}

func TestSchemaRegistrationRejectsUnsafeDefinitions(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		output  bool
		wantErr string
	}{
		{name: "missing input", schema: "", wantErr: "不能为空"},
		{name: "malformed JSON", schema: `{"type":`, wantErr: "JSON 无效"},
		{name: "second JSON value", schema: `{"type":"object"} {}`, wantErr: "一个 JSON value"},
		{name: "invalid trailing bytes", schema: `{"type":"object"} nope`, wantErr: "尾随"},
		{name: "boolean top level", schema: `true`, wantErr: "顶层"},
		{name: "array top level", schema: `[]`, wantErr: "顶层"},
		{name: "missing top-level type", schema: `{}`, wantErr: "type"},
		{name: "top-level union", schema: `{"type":["object","null"]}`, wantErr: "type"},
		{name: "unknown root keyword", schema: `{"type":"object","x-extension":true}`, wantErr: "未知"},
		{name: "unknown nested keyword", schema: `{"type":"object","properties":{"x":{"type":"string","format":"email"}},"additionalProperties":false}`, wantErr: "未知"},
		{name: "dynamic ref", schema: `{"type":"object","$dynamicRef":"#node"}`, wantErr: "dynamic"},
		{name: "dynamic anchor", schema: `{"type":"object","$dynamicAnchor":"node"}`, wantErr: "dynamic"},
		{name: "root ref", schema: `{"type":"object","$ref":"#"}`, wantErr: "$ref"},
		{name: "nested ref", schema: `{"type":"object","properties":{"value":{"$ref":"#"}},"additionalProperties":false}`, wantErr: "$ref"},
		{name: "defs", schema: `{"type":"object","$defs":{"value":{"type":"string"}}}`, wantErr: "未知"},
		{name: "draft seven definitions", schema: `{"type":"object","definitions":{"value":{"type":"string"}}}`, wantErr: "未知"},
		{name: "anchor", schema: `{"type":"object","$anchor":"root"}`, wantErr: "未知"},
		{name: "id", schema: `{"type":"object","$id":"https://example.com/schema"}`, wantErr: "未知"},
		{name: "unsupported draft", schema: `{"$schema":"https://json-schema.org/draft/2019-09/schema","type":"object"}`, wantErr: "draft"},
		{name: "root default", schema: `{"type":"object","default":{}}`, wantErr: "default"},
		{
			name: "required property default",
			schema: `{
				"type":"object",
				"properties":{"limit":{"type":"integer","default":20}},
				"required":["limit"],
				"additionalProperties":false
			}`,
			wantErr: "required",
		},
		{
			name: "array item default",
			schema: `{
				"type":"object",
				"properties":{"values":{"type":"array","items":{"type":"integer","default":1}}},
				"additionalProperties":false
			}`,
			wantErr: "object property",
		},
		{
			name: "default below required property",
			schema: `{
				"type":"object",
				"properties":{
					"options":{
						"type":"object",
						"properties":{"limit":{"type":"integer","default":20}},
						"additionalProperties":false
					}
				},
				"required":["options"],
				"additionalProperties":false
			}`,
			wantErr: "object property",
		},
		{
			name: "composition branch default",
			schema: `{
				"type":"object",
				"allOf":[
					{"type":"object","properties":{"limit":{"type":"integer","default":20}}}
				],
				"additionalProperties":false
			}`,
			wantErr: "object property",
		},
		{
			name: "property combining default and allOf",
			schema: `{
				"type":"object",
				"properties":{
					"limit":{"type":"integer","default":20,"allOf":[{"minimum":1}]}
				},
				"additionalProperties":false
			}`,
			wantErr: "allOf",
		},
		{
			name: "invalid default value",
			schema: `{
				"type":"object",
				"properties":{"limit":{"type":"integer","default":"twenty"}},
				"additionalProperties":false
			}`,
			wantErr: "resolve",
		},
		{
			name:    "output top-level non-object",
			schema:  `{"type":"array","items":{"type":"string"}}`,
			output:  true,
			wantErr: "type",
		},
		{name: "whitespace output", schema: " \n ", output: true, wantErr: "不能为空"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValid()
			if tc.output {
				def.Tools[0].OutputSchema = json.RawMessage(tc.schema)
			} else {
				def.Tools[0].InputSchema = json.RawMessage(tc.schema)
			}
			err := registry.New().Register(def, "create_item")
			if err == nil {
				t.Fatal("unsafe schema should fail registration")
			}
			normalized := strings.ReplaceAll(err.Error(), "-", " ")
			if !strings.Contains(strings.ToLower(normalized), strings.ToLower(tc.wantErr)) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestDefaultsAlongOptionalPropertiesAreApplied(t *testing.T) {
	def := makeValid()
	def.Tools[0].InputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{
			"options":{
				"type":"object",
				"properties":{"limit":{"type":"integer","default":20}},
				"additionalProperties":false
			}
		},
		"additionalProperties":false
	}`)
	r := registry.New()
	if err := r.Register(def, "create_item"); err != nil {
		t.Fatal(err)
	}
	schemas, _ := r.ToolSchemas(def.Type, def.Tools[0].ID)
	input := map[string]any{}
	if err := schemas.Input.ApplyDefaultsAndValidate(input); err != nil {
		t.Fatal(err)
	}
	options, ok := input["options"].(map[string]any)
	if !ok || options["limit"] != float64(20) {
		t.Fatalf("nested defaults = %#v", input)
	}
}

// closedValueObject wraps a single property schema in the closed object
// contract every registered Tool schema must satisfy.
func closedValueObject(valueSchema string) string {
	return `{"type":"object","properties":{"value":` + valueSchema +
		`},"additionalProperties":false}`
}

func TestSafeIntegerSchemaLint(t *testing.T) {
	const (
		maxSafe   = "9007199254740991"
		firstBad  = "9007199254740992"
		badExp    = "9.007199254740992e15"
		fraction  = "0.1"
		hugeValue = "1e400"
	)

	accepted := []struct {
		name   string
		schema string
	}{
		{
			name: "maximum safe default",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","default":%s}`,
				maxSafe,
			)),
		},
		{
			name:   "fractional default",
			schema: closedValueObject(`{"type":"number","default":0.1}`),
		},
		{
			name:   "large integer represented as string",
			schema: closedValueObject(`{"type":"string","default":"9007199254740992"}`),
		},
		{
			name: "safe integer in enum const and boundary",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","enum":[%[1]s],"const":%[1]s,"minimum":-%[1]s,"maximum":%[1]s}`,
				maxSafe,
			)),
		},
		{
			name: "non-integer boundary follows float64 semantics",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"number","minimum":%s}`,
				fraction,
			)),
		},
	}
	for _, tc := range accepted {
		t.Run("accept "+tc.name, func(t *testing.T) {
			def := makeValid()
			def.Tools[0].InputSchema = json.RawMessage(tc.schema)
			if err := registry.New().Register(def, "create_item"); err != nil {
				t.Fatalf("safe schema rejected: %v", err)
			}
		})
	}

	rejected := []struct {
		name   string
		schema string
		output bool
		want   string
	}{
		{
			name: "default",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","default":%s}`,
				firstBad,
			)),
			want: "safe-integer",
		},
		{
			name: "enum",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","enum":[%s]}`,
				firstBad,
			)),
			want: "safe-integer",
		},
		{
			name: "nested const",
			schema: closedValueObject(fmt.Sprintf(
				`{"const":{"nested":%s}}`,
				firstBad,
			)),
			want: "safe-integer",
		},
		{
			name: "minimum",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","minimum":%s}`,
				firstBad,
			)),
			want: "safe-integer",
		},
		{
			name: "exponent integer",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","maximum":%s}`,
				badExp,
			)),
			want: "safe-integer",
		},
		{
			name: "float overflow",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"number","minimum":%s}`,
				hugeValue,
			)),
			want: "有限 float64",
		},
		{
			name: "output integer",
			schema: closedValueObject(fmt.Sprintf(
				`{"type":"integer","maximum":%s}`,
				firstBad,
			)),
			output: true,
			want:   "safe-integer",
		},
	}
	for _, tc := range rejected {
		t.Run("reject "+tc.name, func(t *testing.T) {
			def := makeValid()
			if tc.output {
				def.Tools[0].OutputSchema = json.RawMessage(tc.schema)
			} else {
				def.Tools[0].InputSchema = json.RawMessage(tc.schema)
			}
			err := registry.New().Register(def, "create_item")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe number error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFailedRegistrationIsAtomic(t *testing.T) {
	r := registry.New()
	invalid := makeValid()
	invalid.Tools[0].InputSchema = json.RawMessage(`{"type":"array"}`)
	if err := r.Register(invalid, "create_item"); err == nil {
		t.Fatal("invalid registration should fail")
	}
	if _, ok := r.Get(invalid.Type); ok {
		t.Fatal("failed registration published Definition")
	}
	if _, ok := r.ToolSchemas(invalid.Type, invalid.Tools[0].ID); ok {
		t.Fatal("failed registration published schema cache")
	}

	valid := makeValid()
	if err := r.Register(valid, "create_item"); err != nil {
		t.Fatalf("valid retry after failed registration should succeed: %v", err)
	}
}

func TestMustRegisterPanicDoesNotPublishPartialState(t *testing.T) {
	r := registry.New()
	invalid := makeValid()
	invalid.Tools[0].InputSchema = nil
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("MustRegister should panic")
			}
		}()
		r.MustRegister(invalid, "create_item")
	}()
	if _, ok := r.Get(invalid.Type); ok {
		t.Fatal("panicking MustRegister published Definition")
	}
	if _, ok := r.ToolSchemas(invalid.Type, invalid.Tools[0].ID); ok {
		t.Fatal("panicking MustRegister published schema cache")
	}
}

func TestRegistryConcurrentReadsAndDuplicateRegistration(t *testing.T) {
	r := registry.New()
	def := makeValid()
	def.Tools[0].InputSchema = json.RawMessage(`{
		"type":"object",
		"properties":{"limit":{"type":"integer","default":20}},
		"additionalProperties":false
	}`)

	var successes atomic.Int32
	var start sync.WaitGroup
	start.Add(2)
	var registrations sync.WaitGroup
	registrations.Add(2)
	for range 2 {
		go func() {
			defer registrations.Done()
			start.Done()
			start.Wait()
			if err := r.Register(def, "create_item"); err == nil {
				successes.Add(1)
			}
		}()
	}
	registrations.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent duplicate registrations succeeded %d times, want 1", successes.Load())
	}

	var readers sync.WaitGroup
	for range 32 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				if _, ok := r.Get(def.Type); !ok {
					t.Error("Get missed registered connector")
					return
				}
				if got := r.All(); len(got) != 1 {
					t.Errorf("All len = %d, want 1", len(got))
					return
				}
				schemas, ok := r.ToolSchemas(def.Type, def.Tools[0].ID)
				if !ok {
					t.Error("ToolSchemas missed registered tool")
					return
				}
				instance := map[string]any{}
				if err := schemas.Input.ApplyDefaultsAndValidate(instance); err != nil {
					t.Errorf("concurrent schema use: %v", err)
					return
				}
			}
		}()
	}
	readers.Wait()
}

// 封闭契约规则：值位置必须显式约束值类型、object 必须拒绝未声明属性、array
// 必须声明 items。规则本身住在 registry，调用方（connectors）用它覆盖每一个
// 已注册 Provider，取代原来手工列举 5 个 Provider 的测试。
func TestClosedSchemaRulesRejectOpenContracts(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr string
	}{
		{
			name:    "accept-all boolean property",
			schema:  closedValueObject(`true`),
			wantErr: "accept-all",
		},
		{
			name:    "empty property schema",
			schema:  closedValueObject(`{}`),
			wantErr: "空 schema",
		},
		{
			name:    "open root object",
			schema:  `{"type":"object","properties":{},"additionalProperties":true}`,
			wantErr: "additionalProperties",
		},
		{
			name:    "root without additionalProperties",
			schema:  `{"type":"object","properties":{}}`,
			wantErr: "additionalProperties",
		},
		{
			name:    "nested open object",
			schema:  closedValueObject(`{"type":"object","properties":{}}`),
			wantErr: "additionalProperties",
		},
		{
			name:    "patternProperties open object",
			schema:  `{"type":"object","patternProperties":{"^x-":{"type":"object","properties":{}}},"additionalProperties":false}`,
			wantErr: "additionalProperties",
		},
		{
			name:    "array without items",
			schema:  closedValueObject(`{"type":"array"}`),
			wantErr: "缺少 items",
		},
		{
			name:    "tuple without closed additionalItems",
			schema:  closedValueObject(`{"type":"array","items":[{"type":"string"}]}`),
			wantErr: "additionalItems",
		},
		{
			name:    "tuple item accepts anything",
			schema:  closedValueObject(`{"type":"array","items":[true],"additionalItems":false}`),
			wantErr: "accept-all",
		},
		{
			name:    "property without value kind",
			schema:  closedValueObject(`{"maxLength":10}`),
			wantErr: "值类型",
		},
		{
			name:    "allOf branch decides the value kind",
			schema:  closedValueObject(`{"allOf":[{"type":"object","properties":{}}]}`),
			wantErr: "additionalProperties",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := registry.LintClosedSchema(json.RawMessage(tc.schema), "schema")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("open schema error = %v, want %q", err, tc.wantErr)
			}
			// 同一份 schema 只违反封闭契约，仍然是合法的注册 schema。
			def := makeValid()
			def.Tools[1].InputSchema = json.RawMessage(tc.schema)
			if err := registry.New().Register(def, "create_item"); err != nil {
				t.Fatalf("keyword lint rejected a well-formed schema: %v", err)
			}
		})
	}
}

// 谓词位置（not/if）与叠加位置（then/else/dependentSchemas/allOf 分支）不是
// 调用方填写的契约，不要求封闭；分支集合只要每个分支都约束了值类型即可。
func TestClosedSchemaRulesAllowNonValuePositions(t *testing.T) {
	for _, schema := range []string{
		closedValueObject(`{"type":"string","not":{"maxLength":3}}`),
		closedValueObject(`{"type":"object","properties":{},"additionalProperties":false,"if":{"minProperties":1},"then":{"maxProperties":2}}`),
		closedValueObject(`{"type":"string","allOf":[{"maxLength":3}]}`),
		closedValueObject(`{"oneOf":[{"type":"string"},{"type":"integer"}]}`),
		`{"type":"object","properties":{},"additionalProperties":false,"dependentSchemas":{"value":{"minProperties":1}}}`,
	} {
		if err := registry.LintClosedSchema(
			json.RawMessage(schema),
			"schema",
		); err != nil {
			t.Errorf("closed schema rejected: %v\n%s", err, schema)
		}
	}
}
