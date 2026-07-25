package exec

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
)

// newSchemaHarness 装配一对 schema 相同的 managed / remote tool，
// 用内存 fake 断言"请求时 instance 校验之前不得越界到 config 或 backend"。
func newSchemaHarness(
	t *testing.T,
	input string,
	output string,
	maxInputBytes int64,
) *harness {
	t.Helper()
	return newHarness(t, harnessOpt{
		def:        schemaDefinition(input, output, maxInputBytes),
		registered: []string{"managed"},
	})
}

const passthroughSchema = `{"type":"object","additionalProperties":true}`

func TestAbsoluteInputLimitRunsBeforeConnectionLookup(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	lookupErr := errors.New("connection lookup reached")
	h.connections.err = lookupErr
	arguments := make(json.RawMessage, connector.AbsoluteMaxInputBytes+1)

	request := h.request(h.connID, "managed", arguments)
	result, err := h.engine.Execute(h.ctx, request)
	if err != nil {
		t.Fatalf("hard cap returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailureInputTooLarge)
	if h.connections.calls != 0 {
		t.Fatalf("connection lookup called %d times before hard cap", h.connections.calls)
	}

	request.Arguments = arguments[:connector.AbsoluteMaxInputBytes]
	_, err = h.engine.Execute(h.ctx, request)
	if !errors.Is(err, lookupErr) || h.connections.calls != 1 {
		t.Fatalf(
			"exact hard-cap input did not reach lookup: calls=%d err=%v",
			h.connections.calls,
			err,
		)
	}
}

func TestInputLimitBoundaryIsInclusive(t *testing.T) {
	raw := json.RawMessage(`12345`)
	if exceedsInputLimit(raw, int64(len(raw))) {
		t.Fatal("an input exactly at the limit must be accepted")
	}
	if !exceedsInputLimit(raw, int64(len(raw)-1)) {
		t.Fatal("an input one byte above the limit must be rejected")
	}
}

func TestToolInputLimitRunsBeforeDecodeAndConfig(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	arguments := make(json.RawMessage, connector.DefaultMaxInputBytes+1)

	result, err := h.execute("managed", arguments)
	if err != nil {
		t.Fatalf("tool cap returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailureInputTooLarge)
	if h.connections.calls != 1 {
		t.Fatalf("connection lookups = %d, want 1", h.connections.calls)
	}
	if h.configs.calls != 0 || len(h.managedCalls) != 0 {
		t.Fatalf(
			"over-limit input reached config/backend: configs=%d handlers=%d",
			h.configs.calls,
			len(h.managedCalls),
		)
	}

	override := newSchemaHarness(
		t,
		passthroughSchema,
		"",
		connector.DefaultMaxInputBytes+1,
	)
	result, err = override.execute("managed", arguments)
	if err != nil {
		t.Fatalf("override returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailureInvalidInput)
	if override.configs.calls != 0 {
		t.Fatal("invalid JSON was decoded only after passing the Tool override")
	}
}

func TestManagedAndRemoteReceiveSameNormalizedArguments(t *testing.T) {
	h := newSchemaHarness(t, `{
		"type":"object",
		"properties":{
			"limit":{"type":"integer","minimum":1,"default":20},
			"query":{"type":"string"}
		},
		"additionalProperties":false
	}`, "", 0)

	result, err := h.execute("managed", nil)
	if err != nil || result.Failed() {
		t.Fatalf("managed result=%+v err=%v", result, err)
	}
	if len(h.managedCalls) != 1 {
		t.Fatalf("managed calls = %d, want 1", len(h.managedCalls))
	}
	if got := h.managedCalls[0].Arguments["limit"]; got != float64(20) {
		t.Fatalf("managed default limit = %#v", got)
	}

	result, err = h.execute("remote", nil)
	if err != nil || result.Failed() {
		t.Fatalf("remote result=%+v err=%v", result, err)
	}
	if len(h.mcp.calls) != 1 ||
		string(h.mcp.calls[0].Arguments) != `{"limit":20}` {
		t.Fatalf("remote normalized args = %+v", h.mcp.calls)
	}

	result, err = h.execute("managed", json.RawMessage(`{"limit":7,"query":"hello"}`))
	if err != nil || result.Failed() {
		t.Fatalf("explicit managed result=%+v err=%v", result, err)
	}
	last := h.managedCalls[len(h.managedCalls)-1].Arguments
	if last["limit"] != float64(7) || last["query"] != "hello" {
		t.Fatalf("explicit arguments changed: %#v", last)
	}
}

func TestInputJSONNumberAndSingleValueBoundary(t *testing.T) {
	h := newSchemaHarness(t, `{
		"type":"object",
		"properties":{"value":{"type":"number"}},
		"required":["value"],
		"additionalProperties":false
	}`, "", 0)

	for _, raw := range []string{
		`{"value":0.1}`,
		`{"value":9007199254740991}`,
		`{"value":9.007199254740991e15}`,
	} {
		result, err := h.execute("managed", json.RawMessage(raw))
		if err != nil || result.Failed() {
			t.Fatalf("safe input %s result=%+v err=%v", raw, result, err)
		}
		got, ok := h.managedCalls[len(h.managedCalls)-1].Arguments["value"].(float64)
		if !ok || math.IsInf(got, 0) || math.IsNaN(got) {
			t.Fatalf("normalized value = %#v", got)
		}
	}

	handlerCalls := len(h.managedCalls)
	configCalls := h.configs.calls
	for _, raw := range []string{
		`{"value":1,"unknown":true}`,
		`{"value":9007199254740992}`,
		`{"value":9.007199254740992e15}`,
		`{"value":1e400}`,
		`{"value":1e9999999}`,
		`{"value":0.` + strings.Repeat("0", 256) + `}`,
		`{"value":1} {}`,
		`[1]`,
		`{"value":"secret-input-sentinel"}`,
	} {
		result, err := h.execute("managed", json.RawMessage(raw))
		if err != nil {
			t.Fatalf("invalid input %s returned Go error: %v", raw, err)
		}
		assertFailureCode(t, result, connector.FailureInvalidInput)
		if strings.Contains(result.Failure.Message, "secret-input-sentinel") ||
			strings.Contains(result.Text, "secret-input-sentinel") ||
			len(result.Structured) != 0 {
			t.Fatalf("invalid input leaked caller content: %+v", result)
		}
	}
	if len(h.managedCalls) != handlerCalls {
		t.Fatal("invalid input reached managed handler")
	}
	if h.configs.calls != configCalls {
		t.Fatal("invalid input reached config lookup before schema rejection")
	}
}

func TestSuccessOutputSchemaValidation(t *testing.T) {
	h := newSchemaHarness(
		t,
		`{"type":"object","additionalProperties":false}`,
		`{
			"type":"object",
			"properties":{
				"id":{"type":"string"},
				"count":{"type":"number"}
			},
			"required":["id"],
			"additionalProperties":false
		}`,
		0,
	)

	h.managed = connector.ToolResultData{
		Text:       "ok",
		Structured: json.RawMessage(`{"id":"item_1","count":0.1}`),
	}
	result, err := h.execute("managed", nil)
	if err != nil || result.Failed() || string(result.Structured) == "" {
		t.Fatalf("valid output result=%+v err=%v", result, err)
	}

	for _, structured := range []string{
		"",
		`{"id":1}`,
		`{"id":"item_1","count":9007199254740992}`,
		`{"id":"item_1","count":1e9999999}`,
		`{"id":"item_1","count":0.` + strings.Repeat("0", 256) + `}`,
		`{"id":"item_1"} {}`,
		`["item_1"]`,
		`{"id":"item_1","secret":"must-not-leak"}`,
	} {
		h.managed = connector.ToolResultData{
			Text:       "provider text must not leak",
			Structured: json.RawMessage(structured),
		}
		result, err = h.execute("managed", nil)
		if err != nil {
			t.Fatalf("invalid output %q returned Go error: %v", structured, err)
		}
		assertFailureCode(t, result, connector.FailureInvalidResponse)
		if len(result.Structured) != 0 ||
			strings.Contains(result.Failure.Message, "must-not-leak") ||
			strings.Contains(result.Text, "provider text") {
			t.Fatalf("invalid response leaked backend content: %+v", result)
		}
	}

}

// TestOutputSchemaBindsRemoteBackendSameAsManaged 钉住一个一直是隐式的能力：
// Remote MCP backend 的输出**同样**受 Tool.OutputSchema 约束。
// callManaged 与 callRemote 都收敛到 run.finish，finish 无条件调用
// validateBackendResult(result, r.schemas.Output)，两条路径共用同一份输出契约。
//
// 之所以看上去"Remote 型只是参数直通、没有输出契约"，仅仅是因为现有 Remote
// provider（github / googleads 等）的 Definition 没有声明 OutputSchema —— 那是
// 没人写，不是引擎不支持。注意与 OutputMapperKey 区分：那个是重塑输出形状，
// engine.go 里显式拒绝，与本测试无关。
//
// 给现有 Remote tool 补 OutputSchema 必须先用真实账号采样上游响应：写错一个
// schema，那个 tool 会在运行时直接返回 FailureInvalidResponse，等于写死。
func TestOutputSchemaBindsRemoteBackendSameAsManaged(t *testing.T) {
	const outputSchema = `{
		"type":"object",
		"properties":{"id":{"type":"string"}},
		"required":["id"],
		"additionalProperties":false
	}`
	compliant := json.RawMessage(`{"id":"item_1"}`)
	violating := json.RawMessage(`{"id":123}`)

	for _, backend := range []struct {
		tool string
		set  func(*harness, connector.ToolResultData)
	}{
		{"managed", func(h *harness, r connector.ToolResultData) { h.managed = r }},
		{"remote", func(h *harness, r connector.ToolResultData) { h.mcp.result = r }},
	} {
		t.Run(backend.tool, func(t *testing.T) {
			// 已声明 OutputSchema ＋ 合规输出：成功，结构化输出原样透传。
			h := newSchemaHarness(t, passthroughSchema, outputSchema, 0)
			backend.set(h, connector.ToolResultData{Structured: compliant})
			result, err := h.execute(backend.tool, nil)
			if err != nil ||
				result.Failed() ||
				string(result.Structured) != string(compliant) {
				t.Fatalf("compliant output result=%+v err=%v", result, err)
			}

			// 已声明 OutputSchema ＋ 违规输出：收敛成 invalid_response，
			// 且不回显上游内容。这条证明 Remote 路径的输出契约真的在生效。
			h = newSchemaHarness(t, passthroughSchema, outputSchema, 0)
			backend.set(h, connector.ToolResultData{
				Text:       "upstream text must not leak",
				Structured: violating,
			})
			result, err = h.execute(backend.tool, nil)
			if err != nil {
				t.Fatalf("violating output returned Go error: %v", err)
			}
			assertFailureCode(t, result, connector.FailureInvalidResponse)
			if len(result.Structured) != 0 ||
				strings.Contains(result.Text, "upstream text") {
				t.Fatalf("invalid response leaked backend content: %+v", result)
			}

			// 未声明 OutputSchema：同一份违规输出被放行 —— 这正是今天所有
			// 现有 Remote tool 的状态，缺的是 Definition，不是引擎能力。
			h = newSchemaHarness(t, passthroughSchema, "", 0)
			backend.set(h, connector.ToolResultData{Structured: violating})
			result, err = h.execute(backend.tool, nil)
			if err != nil ||
				result.Failed() ||
				string(result.Structured) != string(violating) {
				t.Fatalf("unconstrained output result=%+v err=%v", result, err)
			}
		})
	}
}

func TestFailedResultSkipsSuccessOutputValidation(t *testing.T) {
	h := newSchemaHarness(
		t,
		`{"type":"object","additionalProperties":false}`,
		`{
			"type":"object",
			"properties":{"id":{"type":"string"}},
			"required":["id"],
			"additionalProperties":false
		}`,
		0,
	)

	failure := &connector.ToolFailure{
		Code:    connector.FailureProviderError,
		Message: "provider request failed",
	}
	h.managed = connector.ToolResultData{Failure: failure}
	result, err := h.execute("managed", nil)
	if err != nil || result.Failure != failure {
		t.Fatalf("typed failure result=%+v err=%v", result, err)
	}

	h.managed = connector.ToolResultData{
		Failure:    failure,
		Structured: json.RawMessage(`{"id":"contradiction"}`),
	}
	result, err = h.execute("managed", nil)
	if !errors.Is(err, ErrInvalidToolResult) || result.Failure != nil {
		t.Fatalf("contradictory result=%+v err=%v", result, err)
	}
}

func TestExecuteRejectsStructurallyInvalidGrantBeforeLookup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ExecuteRequest)
	}{
		{"zero grant", func(r *ExecuteRequest) { r.Authorization = ExecutionGrant{} }},
		{"zero session", func(r *ExecuteRequest) { r.Authorization.SessionID = uuid.Nil }},
		{"zero request connection", func(r *ExecuteRequest) { r.ConnectionID = uuid.Nil }},
		{"connection mismatch", func(r *ExecuteRequest) { r.Authorization.ConnectionID = uuid.New() }},
		{"empty tool", func(r *ExecuteRequest) { r.ToolID = "" }},
		{"tool mismatch", func(r *ExecuteRequest) { r.Authorization.ToolID = "remote" }},
		{"empty exposed name", func(r *ExecuteRequest) { r.Authorization.ExposedToolName = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSchemaHarness(t, passthroughSchema, "", 0)
			request := h.request(h.connID, "managed", nil)
			tc.mutate(&request)

			result, err := h.engine.Execute(h.ctx, request)
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			assertFailureCode(t, result, connector.FailurePolicyDenied)
			if h.connections.calls != 0 ||
				len(h.authorizer.calls) != 0 ||
				len(h.managedCalls) != 0 {
				t.Fatalf(
					"invalid grant crossed boundary: lookups=%d authorizations=%d backends=%d",
					h.connections.calls,
					len(h.authorizer.calls),
					len(h.managedCalls),
				)
			}
		})
	}
}

func TestExecuteFailsClosedWithoutAuthorizer(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	h.engine.authorizer = nil
	result, err := h.execute("managed", nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailurePolicyDenied)
	if h.configs.calls != 0 || len(h.managedCalls) != 0 {
		t.Fatalf(
			"nil authorizer reached config/backend: configs=%d backend=%d",
			h.configs.calls,
			len(h.managedCalls),
		)
	}
}

func TestExecuteReauthorizesAtFinalBackendFence(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	h.authorizer.results = []error{nil, ErrExecutionUnauthorized}
	request := h.request(h.connID, "managed", nil)

	result, err := h.engine.Execute(h.ctx, request)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailurePolicyDenied)
	if len(h.authorizer.calls) != 2 {
		t.Fatalf(
			"authorization calls = %d, want initial check + final fence",
			len(h.authorizer.calls),
		)
	}
	for _, call := range h.authorizer.calls {
		if call.sessionID != request.Authorization.SessionID ||
			call.exposed != request.Authorization.ExposedToolName ||
			call.connection != request.ConnectionID ||
			call.tool != request.ToolID ||
			call.risk != connector.RiskRead ||
			call.generation != 1 {
			t.Fatalf("authorization observation = %+v", call)
		}
	}
	if h.configs.calls != 1 || len(h.managedCalls) != 0 {
		t.Fatalf(
			"final fence state: configs=%d backend=%d",
			h.configs.calls,
			len(h.managedCalls),
		)
	}
}

func TestExecuteRemoteFinalAuthorizationDenialMakesZeroUpstreamCalls(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	h.authorizer.results = []error{nil, ErrExecutionUnauthorized}

	result, err := h.execute("remote", nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailurePolicyDenied)
	if len(h.authorizer.calls) != 2 {
		t.Fatalf(
			"authorization calls = %d, want initial check + final fence",
			len(h.authorizer.calls),
		)
	}
	if h.configs.calls != 1 ||
		len(h.mcp.calls) != 0 ||
		len(h.managedCalls) != 0 {
		t.Fatalf(
			"remote final denial crossed boundary: configs=%d upstream=%d managed=%d",
			h.configs.calls,
			len(h.mcp.calls),
			len(h.managedCalls),
		)
	}
}

func TestExecuteInitialAuthorizationDenialSkipsConfigAndBackend(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	h.authorizer.results = []error{ErrExecutionUnauthorized}
	refresher := &fakeRefresher{}
	h.engine.refresher = refresher
	h.connections.row.AuthMethod = "oauth"

	result, err := h.execute("managed", nil)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	assertFailureCode(t, result, connector.FailurePolicyDenied)
	if len(h.authorizer.calls) != 1 ||
		h.configs.calls != 0 ||
		refresher.calls != 0 ||
		len(h.managedCalls) != 0 {
		t.Fatalf(
			"denial crossed boundary: auth=%d configs=%d refreshes=%d backend=%d",
			len(h.authorizer.calls),
			h.configs.calls,
			refresher.calls,
			len(h.managedCalls),
		)
	}
}

func TestExecutePropagatesAuthorizerInfrastructureError(t *testing.T) {
	h := newSchemaHarness(t, passthroughSchema, "", 0)
	infrastructureError := errors.New(
		"database unavailable with raw internal detail",
	)
	h.authorizer.results = []error{infrastructureError}

	result, err := h.execute("managed", nil)
	if !errors.Is(err, infrastructureError) {
		t.Fatalf("Execute error = %v, want infrastructure error", err)
	}
	if result.Failed() ||
		len(h.authorizer.calls) != 1 ||
		h.configs.calls != 0 ||
		len(h.managedCalls) != 0 {
		t.Fatalf(
			"infrastructure error crossed boundary: result=%+v auth=%d configs=%d backend=%d",
			result,
			len(h.authorizer.calls),
			h.configs.calls,
			len(h.managedCalls),
		)
	}
}
