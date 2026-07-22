package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type fakeCall struct {
	endpoint string
	bearer   string
	timeout  time.Duration
	tool     string
	args     json.RawMessage
}

type fakeMCP struct {
	calls []fakeCall
	res   connector.ToolResultData
	err   error
}

func (f *fakeMCP) CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error) {
	f.calls = append(f.calls, fakeCall{endpoint, bearerToken, timeout, remoteToolName, args})
	if f.err != nil {
		return connector.ToolResultData{}, f.err
	}
	return f.res, nil
}

// testDefinition 覆盖全部分派路径：managed 成功/失败/缺 handler、
// remote 固定 endpoint、remote self_hosted、remote 带 mapper。
func testDefinition() connector.Definition {
	return connector.Definition{
		Type:                "exec_test",
		Name:                "Exec Test",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "mcp_url", Label: "MCP URL", InputType: connector.InputURL},
		},
		AuthMethods: []connector.AuthMethod{
			{Key: "none", Type: connector.AuthNone, Label: "None"},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{
			{Key: "fixed",
				Endpoint:       connector.Endpoint{Source: connector.EndpointFixed, URL: "https://mcp.example.com/mcp"},
				Provenance:     connector.Provenance{Kind: connector.ProvenanceOfficial},
				RequestTimeout: 5 * time.Second},
			{Key: "self",
				Endpoint:       connector.Endpoint{Source: connector.EndpointConfigField, ConfigFieldKey: "mcp_url"},
				Provenance:     connector.Provenance{Kind: connector.ProvenanceSelfHosted},
				RequestTimeout: 5 * time.Second},
		},
		Tools: []connector.Tool{
			{ID: "managed_echo", Name: "Managed echo", Risk: connector.RiskRead,
				Backend: connector.ManagedBackend{HandlerKey: "managed_echo"}},
			{ID: "managed_boom", Name: "Managed boom", Risk: connector.RiskRead,
				Backend: connector.ManagedBackend{HandlerKey: "managed_boom"}},
			{ID: "managed_missing", Name: "Managed missing handler", Risk: connector.RiskRead,
				Backend: connector.ManagedBackend{HandlerKey: "ghost"}},
			{ID: "remote_fixed", Name: "Remote fixed", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "fixed", RemoteToolName: "upstream_echo"}},
			{ID: "remote_self", Name: "Remote self hosted", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "self", RemoteToolName: "upstream_echo"}},
			{ID: "remote_mapped", Name: "Remote mapped", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "fixed", RemoteToolName: "upstream_echo", InputMapperKey: "in"}},
		},
	}
}

type harness struct {
	t            *testing.T
	ctx          context.Context
	pool         *pgxpool.Pool
	q            *store.Queries
	mcp          *fakeMCP
	eng          *exec.Engine
	connID       uuid.UUID
	managedCalls []connector.ToolCallContext
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testutil.NewDB(t)
	ctx := context.Background()
	h := &harness{t: t, ctx: ctx, pool: pool, q: store.New(pool)}

	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	// "ghost" 也作为 handler key 注册通过校验，但运行时 HandlerMap 里没有它，
	// 用来模拟注册键与运行时 map 的漂移（handler 不存在用例）。
	reg.MustRegister(testDefinition(), "managed_echo", "managed_boom", "ghost")
	cfg := configsvc.New(h.q, reg, kr)

	handlers := map[connector.Type]connector.HandlerMap{
		"exec_test": {
			"managed_echo": func(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
				h.managedCalls = append(h.managedCalls, call)
				return connector.ToolResultData{Text: "managed-ok"}, nil
			},
			"managed_boom": func(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
				return connector.ToolResultData{}, errors.New("handler exploded")
			},
		},
	}
	h.mcp = &fakeMCP{res: connector.ToolResultData{Text: "remote-ok"}}
	// refresher 传 nil：本文件全部用例走 AuthNone，不会触发 token 刷新
	//（OAuth 惰性刷新已由计划 3 的测试覆盖）。
	h.eng = exec.New(h.q, reg, cfg, nil, kr, handlers, h.mcp)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// 配置行：mcp_url 已填但尚未 verify
	mustExec(`insert into connector_configs
	  (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	  values ('exec_test', 1, '{"mcp_url":"https://self.internal/mcp"}', '\x'::bytea, 1, now(), now())`)
	h.connID = uuid.New()
	mustExec(`insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'exec_test', 'exectest', 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`, h.connID)
	return h
}

func (h *harness) health() (found bool, failures int32) {
	h.t.Helper()
	row, err := h.q.GetConnectorHealth(h.ctx, "exec_test")
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return true, row.ConsecutiveFailures
}

func (h *harness) lastRun() (status string, errText *string) {
	h.t.Helper()
	err := h.pool.QueryRow(h.ctx,
		`select status, error from tool_runs where connector_type='exec_test' order by created_at desc limit 1`).
		Scan(&status, &errText)
	if err != nil {
		h.t.Fatal(err)
	}
	return status, errText
}

func TestManagedSuccess(t *testing.T) {
	h := newHarness(t)
	res, err := h.eng.Execute(h.ctx, h.connID, "managed_echo", json.RawMessage(`{"x":1}`))
	if err != nil || res.Text != "managed-ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(h.managedCalls) != 1 {
		t.Fatal("handler 未被调用")
	}
	call := h.managedCalls[0]
	if call.ToolID != "managed_echo" || call.Arguments["x"] != float64(1) ||
		call.Config["mcp_url"] != "https://self.internal/mcp" || call.AccessToken != "" {
		t.Fatalf("ToolCallContext 不符: %+v", call)
	}
	if found, failures := h.health(); !found || failures != 0 {
		t.Fatalf("health 应记成功: found=%v failures=%d", found, failures)
	}
	if status, _ := h.lastRun(); status != "ok" {
		t.Fatalf("tool_runs 应为 ok: %s", status)
	}
}

func TestManagedHandlerError(t *testing.T) {
	h := newHarness(t)
	if _, err := h.eng.Execute(h.ctx, h.connID, "managed_boom", nil); err == nil {
		t.Fatal("handler 报错应透传")
	}
	if found, failures := h.health(); !found || failures != 1 {
		t.Fatalf("health 应记失败: found=%v failures=%d", found, failures)
	}
	if status, errText := h.lastRun(); status != "error" || errText == nil {
		t.Fatalf("tool_runs 应为 error: %s %v", status, errText)
	}
}

func TestManagedHandlerMissingIsConfigError(t *testing.T) {
	h := newHarness(t)
	_, err := h.eng.Execute(h.ctx, h.connID, "managed_missing", nil)
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("应为 handler 未注册错误: %v", err)
	}
	if found, _ := h.health(); found {
		t.Fatal("配置类拒绝不应写 health")
	}
	if status, _ := h.lastRun(); status != "error" {
		t.Fatal("tool_runs 仍应记录")
	}
}

func TestRemoteFixed(t *testing.T) {
	h := newHarness(t)
	res, err := h.eng.Execute(h.ctx, h.connID, "remote_fixed", json.RawMessage(`{"m":"hi"}`))
	if err != nil || res.Text != "remote-ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(h.mcp.calls) != 1 {
		t.Fatal("MCPCaller 未被调用")
	}
	c := h.mcp.calls[0]
	if c.endpoint != "https://mcp.example.com/mcp" || c.tool != "upstream_echo" ||
		c.timeout != 5*time.Second || string(c.args) != `{"m":"hi"}` {
		t.Fatalf("调用参数不符: %+v", c)
	}
	if found, failures := h.health(); !found || failures != 0 {
		t.Fatal("health 应记成功")
	}
}

func TestRemoteIsErrorCountsAsHealthy(t *testing.T) {
	h := newHarness(t)
	h.mcp.res = connector.ToolResultData{Text: "biz-fail", IsError: true}
	res, err := h.eng.Execute(h.ctx, h.connID, "remote_fixed", nil)
	if err != nil || !res.IsError {
		t.Fatalf("IsError 应透传: %+v err=%v", res, err)
	}
	if found, failures := h.health(); !found || failures != 0 {
		t.Fatal("IsError 算连通成功，health 记成功")
	}
	if status, _ := h.lastRun(); status != "error" {
		t.Fatalf("tool_runs 应记 error: %s", status)
	}
}

func TestRemoteTransportErrorFailsHealth(t *testing.T) {
	h := newHarness(t)
	h.mcp.err = errors.New("connection refused")
	if _, err := h.eng.Execute(h.ctx, h.connID, "remote_fixed", nil); err == nil {
		t.Fatal("传输错误应返回")
	}
	if found, failures := h.health(); !found || failures != 1 {
		t.Fatal("传输错误应写 health 失败")
	}
}

func TestSelfHostedRequiresVerify(t *testing.T) {
	h := newHarness(t)
	_, err := h.eng.Execute(h.ctx, h.connID, "remote_self", nil)
	if err == nil || !strings.Contains(err.Error(), "mcp:verify") {
		t.Fatalf("未验证应拒绝并提示 mcp:verify: %v", err)
	}
	if found, _ := h.health(); found {
		t.Fatal("配置类拒绝不应写 health")
	}

	// 验证通过后可执行
	if err := h.q.SetConnectorConfigVerified(h.ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: "exec_test", Endpoint: strPtr("https://self.internal/mcp"),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := h.eng.Execute(h.ctx, h.connID, "remote_self", nil)
	if err != nil || res.Text != "remote-ok" {
		t.Fatalf("验证后应可执行: %+v err=%v", res, err)
	}
	if h.mcp.calls[len(h.mcp.calls)-1].endpoint != "https://self.internal/mcp" {
		t.Fatalf("应打配置的 endpoint: %+v", h.mcp.calls)
	}

	// endpoint 变更后重新拒绝
	if _, err := h.pool.Exec(h.ctx,
		`update connector_configs set public_config='{"mcp_url":"https://other.internal/mcp"}' where connector_type='exec_test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Execute(h.ctx, h.connID, "remote_self", nil); err == nil {
		t.Fatal("endpoint 变更后应重新要求验证")
	}
}

func TestMapperUnimplemented(t *testing.T) {
	h := newHarness(t)
	_, err := h.eng.Execute(h.ctx, h.connID, "remote_mapped", nil)
	if err == nil || !strings.Contains(err.Error(), "mapper 未实现") {
		t.Fatalf("mapper 应报未实现: %v", err)
	}
	if found, _ := h.health(); found {
		t.Fatal("配置类拒绝不应写 health")
	}
}

func TestUnknownTool(t *testing.T) {
	h := newHarness(t)
	_, err := h.eng.Execute(h.ctx, h.connID, "nope", nil)
	if !errors.Is(err, exec.ErrToolUnavailable) {
		t.Fatalf("未知 tool 应 ErrToolUnavailable: %v", err)
	}
}

func TestUnknownConnection(t *testing.T) {
	h := newHarness(t)
	if _, err := h.eng.Execute(h.ctx, uuid.New(), "managed_echo", nil); !errors.Is(err, exec.ErrConnectionNotFound) {
		t.Fatalf("want ErrConnectionNotFound, got %v", err)
	}
}

func TestOversizeInputTruncated(t *testing.T) {
	h := newHarness(t)
	big, _ := json.Marshal(map[string]string{"blob": strings.Repeat("x", 70*1024)})
	if _, err := h.eng.Execute(h.ctx, h.connID, "managed_echo", big); err != nil {
		t.Fatal(err)
	}
	var input []byte
	if err := h.pool.QueryRow(h.ctx,
		`select input from tool_runs where connector_type='exec_test' order by created_at desc limit 1`).
		Scan(&input); err != nil {
		t.Fatal(err)
	}
	var marker struct {
		Truncated     bool `json:"truncated"`
		OriginalBytes int  `json:"original_bytes"`
	}
	if err := json.Unmarshal(input, &marker); err != nil || !marker.Truncated || marker.OriginalBytes != len(big) {
		t.Fatalf("超限 input 应存标记对象: %s err=%v", input, err)
	}
}

func strPtr(s string) *string { return &s }
