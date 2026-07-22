# connect-it 计划 4：Tool 执行引擎（Managed＋Remote MCP）、connector_health 与 tool_runs

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现统一的 Tool 执行引擎：装配 Definition＋管理员配置＋credential 后，把调用分派到 Managed handler 或 Remote MCP（官方 go-sdk Streamable HTTP 客户端），执行结果 piggyback 写入`tool_runs`与`connector_health`，并提供管理端`mcp:verify`路由。

**Architecture:** core 新增 handler 类型（纯类型包）；connectors 暴露`AllHandlers`并把 handler key 传入注册校验；service 新增`mcpclient`（每次调用新建并关闭上游 session）与`exec`（执行引擎，MCP 客户端经`MCPCaller`接口注入以便测试）；api 新增`mcp:verify`路由并在 main.go 接线。依赖方向不变：`connectors→core`；`service→core＋connectors`；`api→service`。

**Tech Stack:** Go 1.25、官方`github.com/modelcontextprotocol/go-sdk` v1.6.1（仅 Streamable HTTP）、sqlc＋pgx v5、Echo v4、`github.com/google/uuid`。集成测试读`TEST_DATABASE_URL`，未设置一律`t.Skip`。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 6、9、11、12、13 节是本计划的依据）

## Global Constraints

- module 路径固定`github.com/memohai/connect-it/packages/<name>`；无 go.work，go.mod 用`replace`按相对路径列出全部本地依赖（计划 1 已配置，本计划不改 replace）。
- 本计划为`packages/service`新增第三方依赖：`github.com/modelcontextprotocol/go-sdk v1.6.1`。
- **go-sdk API 查证结果**（2026-07-22 经 WebFetch 查证 pkg.go.dev 与 v1.6.1 源码，任务书描述与真实 API 的差异以下面为准）：
  - 客户端：`mcp.NewClient(&mcp.Implementation{Name, Version}, nil) *Client`；`(*Client).Connect(ctx, Transport, nil) (*ClientSession, error)`；`(*ClientSession).CallTool(ctx, *mcp.CallToolParams) (*mcp.CallToolResult, error)`；`(*ClientSession).Tools(ctx, *ListToolsParams) iter.Seq2[*Tool, error]`（自动翻页）；`(*ClientSession).Close() error`。
  - `mcp.CallToolParams{Name string; Arguments any}`——`Arguments`传`json.RawMessage`会按原文序列化；`mcp.CallToolResult{Content []mcp.Content; IsError bool; StructuredContent any}`——**`Content`是接口切片而非 raw JSON**，需`json.Marshal`成数组再放进`ToolResultData.Content`。
  - 传输：`mcp.StreamableClientTransport{Endpoint string; HTTPClient *http.Client; MaxRetries int; …}`；**没有 Headers 字段**，自定义`Authorization`头须通过`HTTPClient`挂自定义`http.RoundTripper`；`HTTPClient`为 nil 时用`http.DefaultClient`。
  - 服务端（测试用）：`mcp.NewServer(&mcp.Implementation{…}, nil) *Server`；泛型`mcp.AddTool[In, Out any](s *Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out])`（schema 自动从 In 推导，依赖`github.com/google/jsonschema-go`，无需直接 import）；`mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server, nil) *StreamableHTTPHandler`（实现`http.Handler`，可直接挂进`httptest.NewServer`）。
- **2026-07-22 跨计划仲裁（本条覆盖本计划后文一切与之矛盾的代码段）**：`connector.ToolCallContext.Arguments`定为`map[string]any`（managed handler 直接按 map 取参；remote backend 由 exec 用`json.Marshal(Arguments)`重新序列化后透传给`mcp.CallToolParams.Arguments`）；`connector.ToolResultData`定为`{Text string; Structured json.RawMessage; IsError bool}`（与计划 5 的`toolResultToMCP`、计划 6 的 handler 输出对齐）。mcpclient 适配：上游`CallToolResult`的全部`TextContent.Text`以换行拼接进`Text`，`StructuredContent`经`json.Marshal`进`Structured`。后文凡按旧形状（`Content`字段、`json.RawMessage`参数）书写的代码，实现时一律按本条改写，测试断言随之调整。
- Echo 路由中的字面冒号须用反斜杠转义：注册路径写`"/admin/connectors/:type/mcp\\:verify"`（已查证 Echo v4 router.go 的`path[i-1] == '\\'`转义逻辑）。
- 契约签名一字不差（计划 5、6 依赖）：`connector.ToolCallContext`／`ToolResultData`／`ManagedHandler`／`HandlerMap`；`connectors.AllHandlers()`；`mcpclient.CallTool`／`ListTools`；`exec.New`／`Engine.Execute`／`MCPCaller`；`api.Deps.Exec`。
- 错误文案强约束：mapper 非空的错误必须包含「mapper 未实现」；self_hosted 未验证的拒绝必须包含「mcp:verify」。
- API JSON 用 snake_case；错误响应统一`{"error":"code","message":"…"}`。
- sqlc 生成类型按计划 2 的`sqlc.yaml`为准；本计划按约定书写：`uuid→uuid.UUID`、可空`uuid→*uuid.UUID`、`timestamptz→time.Time`／可空`*time.Time`、`jsonb→[]byte`、可空`text→*string`。若实际生成类型不同（如 pgtype），只做等价类型适配，不得改 SQL 语义与对外签名。
- 计划 2／3 的构造函数签名以既有代码为准：本计划按`configsvc.New(q, reg, kr)`书写、`configsvc.Resolved(ctx, t connector.Type)`；若签名不同只改调用处。
- credential 解密的 AAD 约定为`[]byte(connectionID.String())`（spec §8「connections 绑 connection id」）；若计划 3 写入侧实现不同，以计划 3 为准。
- 执行者决策（已定，不要再改）：`connector_health`只记录 backend 真正被调用后的成败（配置类拒绝不污染健康数据，但`mcp:verify`按 spec §9 也是写入方）；上游返回`IsError=true`算连通成功（health 记成功），`tool_runs.status`记`error`；`input`超 64KB 时存`{"truncated":true,"original_bytes":N}`标记对象（截断原文会破坏 jsonb 合法性）；`output_summary`截断到 2048 字节；`mcp:verify`用空 bearer token 实测握手。
- 集成测试运行前先`mise run db-up`（数据库环境变量按计划 2 约定）；`TEST_DATABASE_URL`未设置一律`t.Skip`。
- 注释与错误信息沿用计划 1 的中文风格；提交用 conventional commits，每个 task 至少一次提交。

**范围外（本计划不做）：** Input／Output mapper 的实现（引用即报错）；`tool_runs`90 天清理任务；上游 MCP 会话缓存；聚合`/mcp`端点（计划 5）；真实 provider handler（计划 6 填充）。

---

### Task 1: core handler 类型包

**Files:**
- Create: `packages/core/connector/handler.go`
- Test: `packages/core/connector/handler_test.go`

**Interfaces:**
- Consumes: 计划 1 的`connector.Type`（`packages/core/connector/types.go`）
- Produces（计划 5、6 与本计划 Task 2、5 依赖，签名一字不差）:
  - `connector.ToolCallContext{ConnectorType Type; ToolID string; Arguments json.RawMessage; Config map[string]any; Credential map[string]any; AccessToken string}`
  - `connector.ToolResultData{Content json.RawMessage; IsError bool}`
  - `type ManagedHandler func(ctx context.Context, call ToolCallContext) (ToolResultData, error)`
  - `type HandlerMap map[string]ManagedHandler`

- [ ] **Step 1: 写失败测试**

`packages/core/connector/handler_test.go`：

```go
package connector_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// 类型包无行为，测试锁定：HandlerMap 按 key 取用；
// ToolCallContext 完整传入 handler；ToolResultData 原样返回。
func TestManagedHandlerInvocation(t *testing.T) {
	var got connector.ToolCallContext
	hm := connector.HandlerMap{
		"echo": func(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
			got = call
			return connector.ToolResultData{Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)}, nil
		},
	}
	h, ok := hm["echo"]
	if !ok {
		t.Fatal("HandlerMap 应能按 key 取出 handler")
	}
	res, err := h(context.Background(), connector.ToolCallContext{
		ConnectorType: "github",
		ToolID:        "echo",
		Arguments:     json.RawMessage(`{"a":1}`),
		Config:        map[string]any{"client_id": "x"},
		Credential:    map[string]any{"token": "pat"},
		AccessToken:   "tok",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("不应为 IsError")
	}
	if string(res.Content) != `[{"type":"text","text":"hi"}]` {
		t.Fatalf("content: %s", res.Content)
	}
	if got.ConnectorType != "github" || got.ToolID != "echo" || got.AccessToken != "tok" ||
		string(got.Arguments) != `{"a":1}` || got.Config["client_id"] != "x" || got.Credential["token"] != "pat" {
		t.Fatalf("handler 收到的 ToolCallContext 不完整: %+v", got)
	}
	if _, ok := hm["nope"]; ok {
		t.Fatal("不存在的 key 不应命中")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/core && go test ./connector/
```

预期：编译失败，`undefined: connector.HandlerMap`。

- [ ] **Step 3: 写实现**

`packages/core/connector/handler.go`：

```go
// Managed Tool 的运行时类型。Definition 通过 ManagedBackend.HandlerKey
// 以字符串引用 HandlerMap 中的 handler（保持 Definition 可序列化）。
package connector

import (
	"context"
	"encoding/json"
)

// ToolCallContext 是一次 Tool 调用装配完成后的全部输入（spec §9 运行时装配）。
// Arguments 为 MCP CallTool arguments 的反序列化结果（2026-07-22 跨计划仲裁：
// managed handler 直接按 map 取参，remote backend 由 exec 重新序列化透传）。
type ToolCallContext struct {
	ConnectorType Type
	ToolID        string
	Arguments     map[string]any
	// Config 是合并默认值后的管理员配置（含 Secret 明文），来自 configsvc.Resolved。
	Config map[string]any
	// Credential 是 api_key / custom_credential 连接解密后的凭证字段；
	// OAuth 与 AuthNone 连接为 nil。
	Credential map[string]any
	// AccessToken 是 OAuth 连接经惰性刷新后的有效 access token；其他连接为空。
	AccessToken string
}

// ToolResultData 是两种 backend 统一的执行结果（2026-07-22 跨计划仲裁定稿，
// 与计划 5 的 toolResultToMCP 对齐）：Text 是人类可读输出（映射 MCP
// TextContent）；Structured 是结构化 JSON 输出（映射 structuredContent），
// 可为空；IsError 表示业务失败（区别于 Go error 的传输/装配失败）。
// mcpclient 侧适配：上游 CallToolResult 的全部 TextContent 拼接进 Text，
// StructuredContent 序列化进 Structured。
type ToolResultData struct {
	Text       string
	Structured json.RawMessage
	IsError    bool
}

// ManagedHandler 是 Managed Tool 的执行入口。
type ManagedHandler func(ctx context.Context, call ToolCallContext) (ToolResultData, error)

// HandlerMap 按 HandlerKey 索引一个 Connector 的全部 Managed handler。
type HandlerMap map[string]ManagedHandler
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/core && go test ./...
```

预期：全部包`ok`。

- [ ] **Step 5: 提交**

```bash
git add packages/core/connector
git commit -m "feat(core): add managed tool handler types"
```

---

### Task 2: connectors.AllHandlers 与 RegisterAll 改造

**Files:**
- Create: `packages/connectors/handlers.go`
- Modify: `packages/connectors/all.go`（计划 1 产出的全文替换；若计划 2／3 之后该文件已新增其他 provider 注册行，保留那些行并同样改为传入对应 handler key）
- Test: `packages/connectors/handlers_test.go`

**Interfaces:**
- Consumes: Task 1 的`connector.HandlerMap`；计划 1 的`registry.Registry`、`github.Definition`
- Produces（计划 6 与本计划 Task 7 依赖）:
  - `connectors.AllHandlers() map[connector.Type]connector.HandlerMap`（当前返回空 map，计划 6 填充）
  - `connectors.RegisterAll(r *registry.Registry)`（行为不变，但注册时把各 provider HandlerMap 的 key 传入`MustRegister`）

- [ ] **Step 1: 写失败测试**

`packages/connectors/handlers_test.go`：

```go
package connectors_test

import (
	"testing"

	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/registry"
)

// AllHandlers 与 Registry 的一致性：
// AllHandlers 出现的每个 connector type 都必须是已注册 Definition；
// handler 不允许为 nil。RegisterAll 不 panic 即证明每个 ManagedBackend
// 引用的 HandlerKey 都已随注册传入（计划 1 的 Registry 校验兜底）。
func TestAllHandlersConsistentWithRegistry(t *testing.T) {
	hs := connectors.AllHandlers()
	if hs == nil {
		t.Fatal("AllHandlers 应返回非 nil map")
	}
	r := registry.New()
	connectors.RegisterAll(r)
	for typ, hm := range hs {
		if _, ok := r.Get(typ); !ok {
			t.Errorf("AllHandlers 含未注册的 connector %q", typ)
		}
		for key, h := range hm {
			if h == nil {
				t.Errorf("connector %q 的 handler %q 为 nil", typ, key)
			}
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/connectors && go test ./...
```

预期：编译失败，`undefined: connectors.AllHandlers`。

- [ ] **Step 3: 写实现**

`packages/connectors/handlers.go`：

```go
package connectors

import "github.com/memohai/connect-it/packages/core/connector"

// AllHandlers 返回全部 provider 的 Managed handler，按 connector type 索引。
// 计划 6 为各 provider 填充（约定：provider 包内定义 Handlers 变量，在这里挂入）。
// 当前没有任何 Managed Tool，返回空 map。
func AllHandlers() map[connector.Type]connector.HandlerMap {
	return map[connector.Type]connector.HandlerMap{}
}

// handlerKeys 把 HandlerMap 的 key 展开为 MustRegister 需要的变长参数。
func handlerKeys(hm connector.HandlerMap) []string {
	keys := make([]string, 0, len(hm))
	for k := range hm {
		keys = append(keys, k)
	}
	return keys
}
```

`packages/connectors/all.go`（全文替换）：

```go
// Package connectors 显式注册全部 provider 的 Definition。
// 新增 Connector：加目录、加 definition.go、在这里加一行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/core/registry"
)

func RegisterAll(r *registry.Registry) {
	handlers := AllHandlers()
	r.MustRegister(github.Definition, handlerKeys(handlers[github.Definition.Type])...)
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/connectors && go test ./...
```

预期：全部`ok`（含计划 1 的`all_test.go`）。

- [ ] **Step 5: 提交**

```bash
git add packages/connectors
git commit -m "feat(connectors): expose managed handler maps to registration"
```

---

### Task 3: store queries（connector_health、tool_runs、verified 字段）

**Files:**
- Create: `packages/service/store/queries/exec.sql`（若`packages/service/sqlc.yaml`配置的 queries 目录不同，放入实际配置的目录）
- Create（仅当缺表时）: `packages/service/migrations/000N_health_and_tool_runs.up.sql`／`.down.sql`
- Test: `packages/service/store/exec_queries_test.go`
- 生成物: `packages/service/store/`下 sqlc 重新生成的代码（`mise run sqlc`）

**Interfaces:**
- Consumes: 计划 2 的`store.Queries`（sqlc）、`mise run sqlc`／`db-up`任务
- Produces（本计划 Task 5、6 依赖的生成方法，类型按 Global Constraints 的映射约定）:
  - `q.GetConnectionByID(ctx, id uuid.UUID) (store.Connection, error)`
  - `q.GetConnectorHealth(ctx, connectorType string) (store.ConnectorHealth, error)`
  - `q.UpsertConnectorHealthSuccess(ctx, connectorType string) error`
  - `q.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{ConnectorType string; LastError string}) error`
  - `q.InsertToolRun(ctx, store.InsertToolRunParams{ID uuid.UUID; ConnectorType string; ConnectionID *uuid.UUID; ToolID string; SessionID *uuid.UUID; Status string; Error *string; Input []byte; OutputSummary *string; DurationMs int32}) error`
  - `q.SetConnectorConfigVerified(ctx, store.SetConnectorConfigVerifiedParams{Endpoint *string; ConnectorType string}) error`
  - `q.GetConnectorConfigVerification(ctx, connectorType string) (store.GetConnectorConfigVerificationRow{McpVerifiedAt *time.Time; McpVerifiedEndpoint *string}, error)`

- [ ] **Step 1: 确认表与目录现状**

```bash
cat packages/service/sqlc.yaml
ls packages/service/migrations/
grep -l "create table connector_health" packages/service/migrations/*.up.sql || echo "HEALTH MISSING"
grep -l "create table tool_runs" packages/service/migrations/*.up.sql || echo "TOOL_RUNS MISSING"
grep -l "mcp_verified_at" packages/service/migrations/*.up.sql || echo "VERIFIED COLUMNS MISSING"
```

记下 sqlc 的 queries 目录（后续步骤以它为准）与现有 migration 最大序号。

- [ ] **Step 2: 仅当上一步输出 MISSING 时新增 migration**

若三个 grep 都命中（计划 2 已按 spec §7 建全表），跳过本步。否则取现有最大序号加一作为`N`（4 位零填充），创建：

`packages/service/migrations/000N_health_and_tool_runs.up.sql`（只保留缺失的部分）：

```sql
create table connector_health (
  connector_type text primary key,
  last_ok_at timestamptz,
  last_error_at timestamptz,
  consecutive_failures integer not null default 0,
  last_error text
);

create table tool_runs (
  id uuid primary key,
  connector_type text not null,
  connection_id uuid,
  tool_id text not null,
  session_id uuid,
  status text not null,
  error text,
  input jsonb,
  output_summary text,
  duration_ms integer,
  created_at timestamptz not null
);

create index tool_runs_created_at_idx on tool_runs (created_at);

-- 仅当 VERIFIED COLUMNS MISSING 时追加：
alter table connector_configs
  add column mcp_verified_endpoint text,
  add column mcp_verified_at timestamptz;
```

`packages/service/migrations/000N_health_and_tool_runs.down.sql`：

```sql
drop table tool_runs;
drop table connector_health;
-- 仅当 up 中执行了 alter 时追加：
alter table connector_configs
  drop column mcp_verified_endpoint,
  drop column mcp_verified_at;
```

然后应用：`mise run db-up`。

- [ ] **Step 3: 写失败测试**

`packages/service/store/exec_queries_test.go`：

```go
package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/service/store"
)

func ptr[T any](v T) *T { return &v }

func testQueries(t *testing.T) (*store.Queries, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return store.New(pool), pool
}

func TestConnectorHealthUpsert(t *testing.T) {
	q, pool := testQueries(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `delete from connector_health where connector_type = 'test_health'`); err != nil {
		t.Fatal(err)
	}

	fail := func(msg string) {
		t.Helper()
		if err := q.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{
			ConnectorType: "test_health", LastError: msg,
		}); err != nil {
			t.Fatal(err)
		}
	}
	fail("boom1")
	fail("boom2")
	h, err := q.GetConnectorHealth(ctx, "test_health")
	if err != nil {
		t.Fatal(err)
	}
	if h.ConsecutiveFailures != 2 {
		t.Fatalf("两次失败后 consecutive_failures 应为 2, got %d", h.ConsecutiveFailures)
	}
	if h.LastError == nil || *h.LastError != "boom2" || h.LastErrorAt == nil {
		t.Fatalf("last_error 应为最后一次错误: %+v", h)
	}

	if err := q.UpsertConnectorHealthSuccess(ctx, "test_health"); err != nil {
		t.Fatal(err)
	}
	h, err = q.GetConnectorHealth(ctx, "test_health")
	if err != nil {
		t.Fatal(err)
	}
	if h.ConsecutiveFailures != 0 || h.LastOkAt == nil {
		t.Fatalf("成功应清零并记 last_ok_at: %+v", h)
	}
}

func TestInsertToolRun(t *testing.T) {
	q, pool := testQueries(t)
	ctx := context.Background()
	id := uuid.New()
	connID := uuid.New()
	err := q.InsertToolRun(ctx, store.InsertToolRunParams{
		ID:            id,
		ConnectorType: "test_runs",
		ConnectionID:  &connID,
		ToolID:        "list_items",
		SessionID:     nil,
		Status:        "ok",
		Error:         nil,
		Input:         []byte(`{"a":1}`),
		OutputSummary: ptr("done"),
		DurationMs:    12,
	})
	if err != nil {
		t.Fatal(err)
	}
	var status, toolID string
	var sessionID *uuid.UUID
	if err := pool.QueryRow(ctx,
		`select status, tool_id, session_id from tool_runs where id = $1`, id).
		Scan(&status, &toolID, &sessionID); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || toolID != "list_items" || sessionID != nil {
		t.Fatalf("行内容不符: %s %s %v", status, toolID, sessionID)
	}
}

func TestSetConnectorConfigVerified(t *testing.T) {
	q, pool := testQueries(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `delete from connector_configs where connector_type = 'test_verify'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into connector_configs
	  (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	  values ('test_verify', 1, '{}', ''::bytea, 1, now(), now())`); err != nil {
		t.Fatal(err)
	}
	if err := q.SetConnectorConfigVerified(ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: "test_verify",
		Endpoint:      ptr("https://mcp.internal/mcp"),
	}); err != nil {
		t.Fatal(err)
	}
	v, err := q.GetConnectorConfigVerification(ctx, "test_verify")
	if err != nil {
		t.Fatal(err)
	}
	if v.McpVerifiedAt == nil || v.McpVerifiedEndpoint == nil || *v.McpVerifiedEndpoint != "https://mcp.internal/mcp" {
		t.Fatalf("verify 字段未写入: %+v", v)
	}
}
```

- [ ] **Step 4: 运行确认失败**

```bash
cd packages/service && go test ./store/
```

预期：编译失败，`undefined`（新方法尚未生成）。

- [ ] **Step 5: 写 SQL 并生成**

`packages/service/store/queries/exec.sql`：

```sql
-- 计划 4：Tool 执行引擎所需查询。
-- 注意：若计划 3 已存在同名的 connection 查询（sqlc 报 duplicate query name），
-- 删除本文件中的 GetConnectionByID，改用既有查询并同步调整 exec 包的调用。

-- name: GetConnectionByID :one
select * from connections where id = $1;

-- name: GetConnectorHealth :one
select * from connector_health where connector_type = $1;

-- name: UpsertConnectorHealthSuccess :exec
insert into connector_health (connector_type, last_ok_at, consecutive_failures)
values ($1, now(), 0)
on conflict (connector_type) do update
set last_ok_at = now(),
    consecutive_failures = 0;

-- name: UpsertConnectorHealthFailure :exec
insert into connector_health (connector_type, last_error_at, consecutive_failures, last_error)
values (sqlc.arg(connector_type), now(), 1, sqlc.arg(last_error))
on conflict (connector_type) do update
set last_error_at = now(),
    consecutive_failures = connector_health.consecutive_failures + 1,
    last_error = excluded.last_error;

-- name: InsertToolRun :exec
insert into tool_runs (
  id, connector_type, connection_id, tool_id, session_id,
  status, error, input, output_summary, duration_ms, created_at
) values (
  sqlc.arg(id), sqlc.arg(connector_type), sqlc.narg(connection_id), sqlc.arg(tool_id),
  sqlc.narg(session_id), sqlc.arg(status), sqlc.narg(error),
  sqlc.narg(input), sqlc.narg(output_summary), sqlc.arg(duration_ms), now()
);

-- name: SetConnectorConfigVerified :exec
update connector_configs
set mcp_verified_at = now(),
    mcp_verified_endpoint = sqlc.narg(endpoint),
    updated_at = now()
where connector_type = sqlc.arg(connector_type);

-- name: GetConnectorConfigVerification :one
select mcp_verified_at, mcp_verified_endpoint
from connector_configs
where connector_type = $1;
```

生成：

```bash
mise run sqlc
cd packages/service && go build ./...
```

预期：生成成功、编译通过。若生成的参数或返回类型与 Interfaces 中的约定不同（pgtype 等），按生成代码为准，后续 task 的调用处做等价适配。

- [ ] **Step 6: 运行确认通过**

```bash
mise run db-up
cd packages/service && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it_test?sslmode=disable" go test ./store/
```

（DSN 按计划 2 的`db-up`环境为准。）预期：三个测试 PASS；不设`TEST_DATABASE_URL`时 SKIP。

- [ ] **Step 7: 提交**

```bash
git add packages/service
git commit -m "feat(service): add store queries for tool runs, health and mcp verify"
```

---

### Task 4: mcpclient（官方 go-sdk Streamable HTTP 客户端）

**Files:**
- Create: `packages/service/mcpclient/client.go`
- Test: `packages/service/mcpclient/client_test.go`
- Modify: `packages/service/go.mod`（`go get`新增 go-sdk）

**Interfaces:**
- Consumes: Task 1 的`connector.ToolResultData`；go-sdk v1.6.1（见 Global Constraints 查证结果）
- Produces（计划 5、6 与本计划 Task 5、6、7 依赖，签名一字不差）:
  - `mcpclient.CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error)`
  - `mcpclient.ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error)`
  - `mcpclient.Client`（无状态适配器，方法委托上述包级函数；供注入`exec.MCPCaller`与`api.MCPToolLister`）
  - `mcpclient.CheckEndpoint(endpoint string, allowInsecure bool) error`（spec §13 规则 2 的 https 强制）

- [ ] **Step 1: 添加依赖**

```bash
cd packages/service && go get github.com/modelcontextprotocol/go-sdk@v1.6.1 && go mod tidy
```

- [ ] **Step 2: 写失败测试**

`packages/service/mcpclient/client_test.go`：

```go
package mcpclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/service/mcpclient"
)

type echoInput struct {
	Message string `json:"message"`
}

// newUpstream 用官方 SDK 起一个真实的 Streamable HTTP MCP server（httptest），
// 注册 echo 与 always_fail 两个工具；wantAuth 非空时校验 Authorization 头。
func newUpstream(t *testing.T, wantAuth string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "回显 message"},
		func(ctx context.Context, req *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "echo:" + in.Message}},
			}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "always_fail", Description: "恒返回 IsError"},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "boom"}},
			}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuth != "" && r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestCallToolEndToEnd(t *testing.T) {
	ts := newUpstream(t, "Bearer secret-token")
	res, err := mcpclient.CallTool(context.Background(), ts.URL, "secret-token",
		5*time.Second, "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("echo 不应返回 IsError")
	}
	var content []map[string]any
	if err := json.Unmarshal(res.Content, &content); err != nil {
		t.Fatalf("Content 应为 JSON 数组: %v; raw=%s", err, res.Content)
	}
	if len(content) != 1 || content[0]["text"] != "echo:hi" {
		t.Fatalf("content: %s", res.Content)
	}
}

func TestCallToolIsError(t *testing.T) {
	ts := newUpstream(t, "")
	res, err := mcpclient.CallTool(context.Background(), ts.URL, "",
		5*time.Second, "always_fail", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("always_fail 应返回 IsError")
	}
	if !strings.Contains(string(res.Content), "boom") {
		t.Fatalf("content: %s", res.Content)
	}
}

func TestCallToolWrongTokenFails(t *testing.T) {
	ts := newUpstream(t, "Bearer right")
	_, err := mcpclient.CallTool(context.Background(), ts.URL, "wrong",
		5*time.Second, "echo", json.RawMessage(`{"message":"x"}`))
	if err == nil {
		t.Fatal("错误 token 应导致握手失败")
	}
}

func TestCallToolUnknownToolErrors(t *testing.T) {
	ts := newUpstream(t, "")
	res, err := mcpclient.CallTool(context.Background(), ts.URL, "",
		5*time.Second, "nope", json.RawMessage(`{}`))
	if err == nil && !res.IsError {
		t.Fatalf("调用不存在的 tool 应报错: res=%+v", res)
	}
}

func TestListToolsEndToEnd(t *testing.T) {
	ts := newUpstream(t, "Bearer secret-token")
	names, err := mcpclient.ListTools(context.Background(), ts.URL, "secret-token", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"always_fail", "echo"}) {
		t.Fatalf("names: %v", names)
	}
}

func TestCheckEndpoint(t *testing.T) {
	cases := []struct {
		endpoint      string
		allowInsecure bool
		wantErr       bool
	}{
		{"https://mcp.internal/mcp", false, false},
		{"http://mcp.internal/mcp", false, true},
		{"http://mcp.internal/mcp", true, false},
		{"ftp://mcp.internal/mcp", true, true},
		{"not a url", false, true},
	}
	for _, tc := range cases {
		err := mcpclient.CheckEndpoint(tc.endpoint, tc.allowInsecure)
		if (err != nil) != tc.wantErr {
			t.Errorf("CheckEndpoint(%q, %v) = %v, wantErr=%v", tc.endpoint, tc.allowInsecure, err, tc.wantErr)
		}
	}
}
```

- [ ] **Step 3: 运行确认失败**

```bash
cd packages/service && go test ./mcpclient/
```

预期：编译失败，`undefined: mcpclient.CallTool`。

- [ ] **Step 4: 写实现**

`packages/service/mcpclient/client.go`：

```go
// Package mcpclient 用官方 go-sdk 以 Streamable HTTP 调用上游 MCP server。
// 每次调用新建并关闭 session（spec §11：正确性优先，会话缓存留作后续优化）。
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Client 是无状态适配器，把包级函数暴露为方法，
// 便于作为 exec.MCPCaller 与 api.MCPToolLister 注入。
type Client struct{}

func (Client) CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error) {
	return CallTool(ctx, endpoint, bearerToken, timeout, remoteToolName, args)
}

func (Client) ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) {
	return ListTools(ctx, endpoint, bearerToken, timeout)
}

// CallTool 对 endpoint 完成一次 MCP 握手、调用 remoteToolName、关闭 session。
// Content 是上游 CallToolResult.Content 的 JSON 数组序列化。
func CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error) {
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	session, err := connect(ctx, endpoint, bearerToken)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: remoteToolName, Arguments: args})
	if err != nil {
		return connector.ToolResultData{}, fmt.Errorf("mcpclient: 调用 tool %q 失败: %w", remoteToolName, err)
	}
	content, err := json.Marshal(res.Content)
	if err != nil {
		return connector.ToolResultData{}, fmt.Errorf("mcpclient: 序列化 content 失败: %w", err)
	}
	return connector.ToolResultData{Content: content, IsError: res.IsError}, nil
}

// ListTools 对 endpoint 完成一次 MCP 握手并列出全部工具名（Tools 迭代器自动翻页）。
func ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) {
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	session, err := connect(ctx, endpoint, bearerToken)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("mcpclient: tools/list 失败: %w", err)
		}
		names = append(names, tool.Name)
	}
	return names, nil
}

// CheckEndpoint 校验 self_hosted endpoint 的 URL 形态：
// 必须是 https；allowInsecure 为 true 时才放行 http（spec §13 规则 2）。
func CheckEndpoint(endpoint string, allowInsecure bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("mcpclient: endpoint %q 不是合法 URL", endpoint)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure {
			return nil
		}
		return fmt.Errorf("mcpclient: endpoint %q 使用 http，需显式开启 allow_insecure_http", endpoint)
	default:
		return fmt.Errorf("mcpclient: endpoint %q 的 scheme 必须是 https", endpoint)
	}
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func connect(ctx context.Context, endpoint, bearerToken string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "connect-it", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: bearerToken, base: http.DefaultTransport}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: 连接 %s 失败: %w", endpoint, err)
	}
	return session, nil
}

// bearerRoundTripper 给每个请求附加 Authorization: Bearer 头
// （go-sdk 的 transport 无 header 选项，只能经 http.Client 注入）。
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return rt.base.RoundTrip(req)
}
```

- [ ] **Step 5: 运行确认通过**

```bash
cd packages/service && go test ./mcpclient/
```

预期：全部 PASS（本包测试不依赖数据库，任何环境都运行）。

- [ ] **Step 6: 提交**

```bash
git add packages/service
git commit -m "feat(service): add streamable http mcp client"
```

---

### Task 5: exec 执行引擎

**Files:**
- Create: `packages/service/exec/engine.go`
- Test: `packages/service/exec/engine_test.go`

**Interfaces:**
- Consumes: Task 1 类型、Task 3 生成方法、Task 4 的`mcpclient.CheckEndpoint`；计划 1 的`registry.Registry`／`crypto.Keyring`；计划 2 的`configsvc.Service.Resolved`；计划 3 的`tokens.Refresher.AccessToken(ctx, connectionID uuid.UUID) (string, error)`
- Produces（计划 5 依赖，签名一字不差）:
  - `type MCPCaller interface { CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error) }`
  - `exec.New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, refresher *tokens.Refresher, kr *crypto.Keyring, handlers map[connector.Type]connector.HandlerMap, mcp MCPCaller) *Engine`
  - `(*Engine).Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error)`

- [ ] **Step 1: 写失败测试**

`packages/service/exec/engine_test.go`：

```go
package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
)

func ptr[T any](v T) *T { return &v }

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
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h := &harness{t: t, ctx: ctx, pool: pool, q: store.New(pool)}

	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	// "ghost" 也作为 handler key 注册通过校验，但运行时 HandlerMap 里没有它，
	// 用来模拟注册键与运行时 map 的漂移（handler 不存在用例）。
	reg.MustRegister(testDefinition(), "managed_echo", "managed_boom", "ghost")

	// 注意：configsvc.New 的签名以计划 2 实际落地为准（本计划假定为 (q, reg, kr)）。
	cfg := configsvc.New(h.q, reg, kr)

	handlers := map[connector.Type]connector.HandlerMap{
		"exec_test": {
			"managed_echo": func(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
				h.managedCalls = append(h.managedCalls, call)
				return connector.ToolResultData{Content: json.RawMessage(`[{"type":"text","text":"managed-ok"}]`)}, nil
			},
			"managed_boom": func(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
				return connector.ToolResultData{}, errors.New("handler exploded")
			},
		},
	}
	h.mcp = &fakeMCP{res: connector.ToolResultData{Content: json.RawMessage(`[{"type":"text","text":"remote-ok"}]`)}}
	// refresher 传 nil：本文件全部用例走 AuthNone，不会触发 token 刷新
	//（OAuth 惰性刷新已由计划 3 的测试覆盖）。
	h.eng = exec.New(h.q, reg, cfg, nil, kr, handlers, h.mcp)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec(`delete from tool_runs where connector_type = 'exec_test'`)
	mustExec(`delete from connector_health where connector_type = 'exec_test'`)
	mustExec(`delete from connector_configs where connector_type = 'exec_test'`)
	mustExec(`delete from connections where connector_type = 'exec_test'`)
	// 配置行：mcp_url 已填但尚未 verify
	mustExec(`insert into connector_configs
	  (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	  values ('exec_test', 1, '{"mcp_url":"https://self.internal/mcp"}', ''::bytea, 1, now(), now())`)
	h.connID = uuid.New()
	mustExec(`insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'exec_test', 'exectest', 'none', ''::bytea, 1, '{}', '{}', 'active', now(), now())`, h.connID)
	return h
}

func (h *harness) health() (found bool, failures int32, lastError string, lastOkSet, lastErrSet bool) {
	h.t.Helper()
	row, err := h.q.GetConnectorHealth(h.ctx, "exec_test")
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, "", false, false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	le := ""
	if row.LastError != nil {
		le = *row.LastError
	}
	return true, row.ConsecutiveFailures, le, row.LastOkAt != nil, row.LastErrorAt != nil
}

type runRow struct {
	status  string
	errText string
	input   string
	connID  string
}

func (h *harness) lastRun(toolID string) runRow {
	h.t.Helper()
	var r runRow
	var errText, input, connID *string
	err := h.pool.QueryRow(h.ctx,
		`select status, error, input::text, connection_id::text from tool_runs
		 where connector_type = 'exec_test' and tool_id = $1
		 order by created_at desc limit 1`, toolID).Scan(&r.status, &errText, &input, &connID)
	if err != nil {
		h.t.Fatalf("查询 tool_runs(%s): %v", toolID, err)
	}
	if errText != nil {
		r.errText = *errText
	}
	if input != nil {
		r.input = *input
	}
	if connID != nil {
		r.connID = *connID
	}
	return r
}

func TestExecuteManagedSuccess(t *testing.T) {
	h := newHarness(t)
	res, err := h.eng.Execute(h.ctx, h.connID, "managed_echo", json.RawMessage(`{"m":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(string(res.Content), "managed-ok") {
		t.Fatalf("res: %+v", res)
	}
	if len(h.managedCalls) != 1 {
		t.Fatalf("handler 应被调用一次: %d", len(h.managedCalls))
	}
	call := h.managedCalls[0]
	if call.ConnectorType != "exec_test" || call.ToolID != "managed_echo" ||
		string(call.Arguments) != `{"m":"hi"}` || call.Config["mcp_url"] != "https://self.internal/mcp" {
		t.Fatalf("ToolCallContext 不完整: %+v", call)
	}
	run := h.lastRun("managed_echo")
	if run.status != "ok" || run.connID != h.connID.String() {
		t.Fatalf("tool_runs 记录不符: %+v", run)
	}
	found, failures, _, lastOkSet, _ := h.health()
	if !found || failures != 0 || !lastOkSet {
		t.Fatalf("health 应记录成功: found=%v failures=%d lastOk=%v", found, failures, lastOkSet)
	}
}

func TestExecuteRemoteSuccess(t *testing.T) {
	h := newHarness(t)
	res, err := h.eng.Execute(h.ctx, h.connID, "remote_fixed", json.RawMessage(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Content), "remote-ok") {
		t.Fatalf("res: %+v", res)
	}
	if len(h.mcp.calls) != 1 {
		t.Fatalf("MCPCaller 应被调用一次: %d", len(h.mcp.calls))
	}
	c := h.mcp.calls[0]
	if c.endpoint != "https://mcp.example.com/mcp" || c.tool != "upstream_echo" ||
		c.timeout != 5*time.Second || c.bearer != "" || string(c.args) != `{"q":1}` {
		t.Fatalf("MCP 调用参数错误: %+v", c)
	}
	if run := h.lastRun("remote_fixed"); run.status != "ok" {
		t.Fatalf("run: %+v", run)
	}
}

func TestExecuteRejections(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name    string
		toolID  string
		wantErr string
	}{
		{"tool 不存在", "nope", "没有 tool"},
		{"managed handler 不存在", "managed_missing", "ghost"},
		{"mapper 非空报错", "remote_mapped", "mapper 未实现"},
		{"self_hosted 未 verify 拒绝", "remote_self", "mcp:verify"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.eng.Execute(h.ctx, h.connID, tc.toolID, json.RawMessage(`{}`))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 %v 应包含 %q", err, tc.wantErr)
			}
			if len(h.mcp.calls) != 0 {
				t.Fatalf("拒绝路径不应触达 MCPCaller: %+v", h.mcp.calls)
			}
			if found, failures, _, _, _ := h.health(); found && failures != 0 {
				t.Fatalf("配置类拒绝不应自增 health 失败: failures=%d", failures)
			}
			if run := h.lastRun(tc.toolID); run.status != "error" || run.errText == "" {
				t.Fatalf("拒绝也应落 tool_runs: %+v", run)
			}
		})
	}
}

func TestExecuteSelfHostedVerified(t *testing.T) {
	h := newHarness(t)
	if err := h.q.SetConnectorConfigVerified(h.ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: "exec_test",
		Endpoint:      ptr("https://self.internal/mcp"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Execute(h.ctx, h.connID, "remote_self", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(h.mcp.calls) != 1 || h.mcp.calls[0].endpoint != "https://self.internal/mcp" {
		t.Fatalf("应打到已验证的 endpoint: %+v", h.mcp.calls)
	}

	// verify 记录的 endpoint 与当前配置不一致 → 重新拒绝（spec §12：变更后标记失效）
	if err := h.q.SetConnectorConfigVerified(h.ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: "exec_test",
		Endpoint:      ptr("https://old.internal/mcp"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Execute(h.ctx, h.connID, "remote_self", json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "mcp:verify") {
		t.Fatalf("endpoint 变更后应重新要求 verify: %v", err)
	}
}

func TestExecuteHealthTransitions(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= 2; i++ {
		if _, err := h.eng.Execute(h.ctx, h.connID, "managed_boom", json.RawMessage(`{}`)); err == nil {
			t.Fatal("managed_boom 应失败")
		}
		_, failures, lastError, _, lastErrSet := h.health()
		if failures != int32(i) || !lastErrSet || !strings.Contains(lastError, "handler exploded") {
			t.Fatalf("第 %d 次失败后 health 不符: failures=%d lastError=%q", i, failures, lastError)
		}
	}
	// 远端失败同样自增
	h.mcp.err = errors.New("upstream down")
	if _, err := h.eng.Execute(h.ctx, h.connID, "remote_fixed", json.RawMessage(`{}`)); err == nil {
		t.Fatal("远端失败应报错")
	}
	if _, failures, _, _, _ := h.health(); failures != 3 {
		t.Fatalf("failures 应为 3, got %d", failures)
	}
	// 一次成功即清零（spec §9）
	h.mcp.err = nil
	if _, err := h.eng.Execute(h.ctx, h.connID, "managed_echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	found, failures, _, lastOkSet, _ := h.health()
	if !found || failures != 0 || !lastOkSet {
		t.Fatalf("成功后应清零: failures=%d", failures)
	}
}

func TestExecuteInputTruncation(t *testing.T) {
	h := newHarness(t)
	big := json.RawMessage(fmt.Sprintf(`{"blob":%q}`, strings.Repeat("x", 70*1024)))
	if _, err := h.eng.Execute(h.ctx, h.connID, "managed_echo", big); err != nil {
		t.Fatal(err)
	}
	run := h.lastRun("managed_echo")
	if !strings.Contains(run.input, `"truncated"`) || len(run.input) > 1024 {
		t.Fatalf("超限 input 应存截断标记: len=%d", len(run.input))
	}
}

func TestExecuteInactiveConnectionRejected(t *testing.T) {
	h := newHarness(t)
	disabledID := uuid.New()
	if _, err := h.pool.Exec(h.ctx, `insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'exec_test', 'exectest-off', 'none', ''::bytea, 1, '{}', '{}', 'disabled', now(), now())`, disabledID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Execute(h.ctx, disabledID, "managed_echo", json.RawMessage(`{}`)); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled connection 应被拒绝: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/service && go test ./exec/
```

预期：编译失败，`undefined: exec.New`。

- [ ] **Step 3: 写实现**

`packages/service/exec/engine.go`：

```go
// Package exec 是统一的 Tool 执行引擎（spec §11）：
// 装配 Definition＋管理员配置＋credential，按 Backend 分派到 Managed handler
// 或 Remote MCP，并把结果 piggyback 写入 tool_runs 与 connector_health。
package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

const (
	// maxInputBytes 是 tool_runs.input 的存储上限（spec §7：64KB）。
	maxInputBytes = 64 * 1024
	// maxOutputSummaryBytes 是 tool_runs.output_summary 的截断长度。
	maxOutputSummaryBytes = 2048
)

// MCPCaller 是 Engine 需要的最小 MCP 客户端能力，
// 生产实现为 mcpclient.Client，测试注入假实现。
type MCPCaller interface {
	CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error)
}

type Engine struct {
	q         *store.Queries
	reg       *registry.Registry
	cfg       *configsvc.Service
	refresher *tokens.Refresher
	kr        *crypto.Keyring
	handlers  map[connector.Type]connector.HandlerMap
	mcp       MCPCaller
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, refresher *tokens.Refresher, kr *crypto.Keyring, handlers map[connector.Type]connector.HandlerMap, mcp MCPCaller) *Engine {
	return &Engine{q: q, reg: reg, cfg: cfg, refresher: refresher, kr: kr, handlers: handlers, mcp: mcp}
}

// Execute 执行一个 Tool。每次调用都落 tool_runs；
// backend 真正被调用后的成败才写 connector_health（配置类拒绝不污染健康数据）。
func (e *Engine) Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error) {
	start := time.Now()
	conn, err := e.q.GetConnectionByID(ctx, connectionID)
	if err != nil {
		return connector.ToolResultData{}, fmt.Errorf("exec: 加载 connection %s 失败: %w", connectionID, err)
	}
	ctype := connector.Type(conn.ConnectorType)

	res, backendReached, execErr := e.dispatch(ctx, ctype, conn, toolID, args)

	e.recordRun(ctx, ctype, connectionID, toolID, args, res, execErr, time.Since(start))
	if backendReached {
		e.recordHealth(ctx, ctype, execErr)
	}
	if execErr != nil {
		return connector.ToolResultData{}, execErr
	}
	return res, nil
}

// dispatch 返回（结果，backend 是否已被真正调用，错误）。
func (e *Engine) dispatch(ctx context.Context, ctype connector.Type, conn store.Connection, toolID string, args json.RawMessage) (connector.ToolResultData, bool, error) {
	var zero connector.ToolResultData
	if conn.Status != "active" {
		return zero, false, fmt.Errorf("exec: connection %s 状态为 %q，不可执行", conn.ID, conn.Status)
	}
	def, ok := e.reg.Get(ctype)
	if !ok {
		return zero, false, fmt.Errorf("exec: connector %q 没有 Definition", ctype)
	}
	var tool *connector.Tool
	for i := range def.Tools {
		if def.Tools[i].ID == toolID {
			tool = &def.Tools[i]
			break
		}
	}
	if tool == nil {
		return zero, false, fmt.Errorf("exec: connector %q 没有 tool %q", ctype, toolID)
	}

	cfg, err := e.cfg.Resolved(ctx, ctype)
	if err != nil {
		return zero, false, fmt.Errorf("exec: 装配 connector %q 配置失败: %w", ctype, err)
	}
	cred, accessToken, err := e.credential(ctx, def, conn)
	if err != nil {
		return zero, false, err
	}
	call := connector.ToolCallContext{
		ConnectorType: ctype,
		ToolID:        toolID,
		Arguments:     args,
		Config:        cfg,
		Credential:    cred,
		AccessToken:   accessToken,
	}

	switch b := tool.Backend.(type) {
	case connector.ManagedBackend:
		h, ok := e.handlers[ctype][b.HandlerKey]
		if !ok {
			return zero, false, fmt.Errorf("exec: connector %q 的 managed handler %q 不存在", ctype, b.HandlerKey)
		}
		res, err := h(ctx, call)
		return res, true, err
	case connector.RemoteMCPBackend:
		if b.InputMapperKey != "" || b.OutputMapperKey != "" {
			return zero, false, fmt.Errorf("exec: tool %q: mapper 未实现", toolID)
		}
		var server *connector.RemoteMCPServer
		for i := range def.RemoteMCPServers {
			if def.RemoteMCPServers[i].Key == b.ServerKey {
				server = &def.RemoteMCPServers[i]
				break
			}
		}
		if server == nil {
			return zero, false, fmt.Errorf("exec: connector %q 的 MCP server %q 不存在", ctype, b.ServerKey)
		}
		endpoint, err := e.endpoint(ctx, ctype, *server, cfg)
		if err != nil {
			return zero, false, err
		}
		res, err := e.mcp.CallTool(ctx, endpoint, bearerToken(call), server.RequestTimeout, b.RemoteToolName, args)
		return res, true, err
	default:
		return zero, false, fmt.Errorf("exec: tool %q 的 Backend 类型未知", toolID)
	}
}

// credential 按 auth method 类型装配（spec §9）：
// AuthNone 跳过；OAuth 经 Refresher 惰性刷新取 access token；
// 其余解密 credential JSON（AAD 绑 connection id，spec §8）。
func (e *Engine) credential(ctx context.Context, def connector.Definition, conn store.Connection) (map[string]any, string, error) {
	var method *connector.AuthMethod
	for i := range def.AuthMethods {
		if def.AuthMethods[i].Key == conn.AuthMethod {
			method = &def.AuthMethods[i]
			break
		}
	}
	if method == nil {
		return nil, "", fmt.Errorf("exec: connection 的 auth method %q 在 Definition 中不存在", conn.AuthMethod)
	}
	switch method.Type {
	case connector.AuthNone:
		return nil, "", nil
	case connector.AuthOAuth2:
		token, err := e.refresher.AccessToken(ctx, conn.ID)
		if err != nil {
			return nil, "", fmt.Errorf("exec: 获取 access token 失败: %w", err)
		}
		return nil, token, nil
	default: // api_key / custom_credential
		plain, err := e.kr.Decrypt(conn.Credential, int(conn.SecretKeyVersion), []byte(conn.ID.String()))
		if err != nil {
			return nil, "", fmt.Errorf("exec: 解密 credential 失败: %w", err)
		}
		var cred map[string]any
		if err := json.Unmarshal(plain, &cred); err != nil {
			return nil, "", fmt.Errorf("exec: credential 不是合法 JSON: %w", err)
		}
		return cred, "", nil
	}
}

// endpoint 解析 Remote MCP server 的地址（spec §13）：
// 固定 URL 直接用；配置字段来源必须已填写、通过 scheme 检查、
// 且 mcp_verified_endpoint 与当前值一致（未 verify 一律拒绝）。
func (e *Engine) endpoint(ctx context.Context, ctype connector.Type, s connector.RemoteMCPServer, cfg map[string]any) (string, error) {
	switch s.Endpoint.Source {
	case connector.EndpointFixed:
		return s.Endpoint.URL, nil
	case connector.EndpointConfigField:
		raw, _ := cfg[s.Endpoint.ConfigFieldKey].(string)
		if raw == "" {
			return "", fmt.Errorf("exec: connector %q 的 MCP endpoint 配置 %q 未填写", ctype, s.Endpoint.ConfigFieldKey)
		}
		allowInsecure, _ := cfg["allow_insecure_http"].(bool)
		if err := mcpclient.CheckEndpoint(raw, allowInsecure); err != nil {
			return "", err
		}
		v, err := e.q.GetConnectorConfigVerification(ctx, string(ctype))
		if err != nil {
			return "", fmt.Errorf("exec: 读取 connector %q 配置失败: %w", ctype, err)
		}
		if v.McpVerifiedAt == nil || v.McpVerifiedEndpoint == nil || *v.McpVerifiedEndpoint != raw {
			return "", fmt.Errorf("exec: connector %q 的 self_hosted endpoint 未通过 mcp:verify", ctype)
		}
		return raw, nil
	default:
		return "", fmt.Errorf("exec: MCP server %q 的 Endpoint.Source 非法", s.Key)
	}
}

// bearerToken 决定呈递给上游的 Bearer 凭证：OAuth 的 access token 优先，
// 否则取 credential 中约定的 "token" 字段（如 GitHub PAT，计划 6 落实字段名）。
func bearerToken(call connector.ToolCallContext) string {
	if call.AccessToken != "" {
		return call.AccessToken
	}
	if s, ok := call.Credential["token"].(string); ok {
		return s
	}
	return ""
}

// recordRun 落 tool_runs（best-effort：记录失败不影响调用结果）。
func (e *Engine) recordRun(ctx context.Context, ctype connector.Type, connectionID uuid.UUID, toolID string, args json.RawMessage, res connector.ToolResultData, execErr error, dur time.Duration) {
	ctx = context.WithoutCancel(ctx) // 调用方取消不应丢审计记录
	status := "ok"
	var errText *string
	if execErr != nil {
		status = "error"
		msg := execErr.Error()
		errText = &msg
	} else if res.IsError {
		// 上游 tool 层错误：连通成功但结果是错误
		status = "error"
	}
	input := []byte(args)
	if len(input) > maxInputBytes {
		// 直接截断会破坏 jsonb 合法性，存标记对象
		input = fmt.Appendf(nil, `{"truncated":true,"original_bytes":%d}`, len(args))
	}
	var summary *string
	if len(res.Content) > 0 {
		s := string(res.Content)
		if len(s) > maxOutputSummaryBytes {
			s = s[:maxOutputSummaryBytes]
		}
		summary = &s
	}
	_ = e.q.InsertToolRun(ctx, store.InsertToolRunParams{
		ID:            uuid.New(),
		ConnectorType: string(ctype),
		ConnectionID:  &connectionID,
		ToolID:        toolID,
		SessionID:     nil, // 计划 5 的聚合端点接入后填充
		Status:        status,
		Error:         errText,
		Input:         input,
		OutputSummary: summary,
		DurationMs:    int32(dur.Milliseconds()),
	})
}

// recordHealth 把 backend 调用结果 piggyback 进 connector_health（spec §9）。
// IsError=true 属于连通成功，不算失败。
func (e *Engine) recordHealth(ctx context.Context, ctype connector.Type, execErr error) {
	ctx = context.WithoutCancel(ctx)
	if execErr == nil {
		_ = e.q.UpsertConnectorHealthSuccess(ctx, string(ctype))
		return
	}
	_ = e.q.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{
		ConnectorType: string(ctype),
		LastError:     execErr.Error(),
	})
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/service && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it_test?sslmode=disable" go test ./exec/
```

预期：全部 PASS。再跑一次不带`TEST_DATABASE_URL`确认 SKIP。

- [ ] **Step 5: 提交**

```bash
git add packages/service
git commit -m "feat(service): add tool execution engine"
```

---

### Task 6: api mcp:verify 路由与 Deps 增补

**Files:**
- Create: `packages/api/mcpverify.go`
- Modify: `packages/api/api.go`（`Deps`结构体与`api.New`所在文件；若计划 2 放在其他文件名，以实际为准）
- Test: `packages/api/mcpverify_test.go`

**Interfaces:**
- Consumes: Task 3 的`SetConnectorConfigVerified`／health upsert、Task 4 的`mcpclient.CheckEndpoint`、Task 5 的`exec.Engine`（仅 Deps 字段类型）；计划 2 的`api.Deps`／`api.New`／`RequireAdminSession`、`configsvc.Service.Resolved`
- Produces（计划 5、7 依赖）:
  - `api.Deps`新增字段：`Exec *exec.Engine`；`Store *store.Queries`；`MCPTools MCPToolLister`（后两者即「MCPVerify 所需依赖」的补充定义）
  - `type MCPToolLister interface { ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) }`
  - 路由`POST /admin/connectors/:type/mcp:verify`（cookie 鉴权）

- [ ] **Step 1: 写失败测试**

`packages/api/mcpverify_test.go`（白盒，`package api`）：

```go
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

// Echo 路由中字面冒号的转义写法必须能精确匹配（不依赖数据库，恒运行）。
func TestEscapedColonRoute(t *testing.T) {
	e := echo.New()
	e.POST("/admin/connectors/:type/mcp\\:verify", func(c echo.Context) error {
		return c.String(http.StatusOK, c.Param("type"))
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/connectors/github/mcp:verify", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "github" {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/admin/connectors/github/mcpverify", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusOK && rec2.Body.String() == "github" {
		t.Fatal("mcpverify 不应匹配 mcp:verify 路由")
	}
}

type fakeLister struct {
	names []string
	err   error
	calls int
}

func (f *fakeLister) ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.names, nil
}

func verifyTestDefinition() connector.Definition {
	return connector.Definition{
		Type:                "verify_test",
		Name:                "Verify Test",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "mcp_url", Label: "MCP URL", InputType: connector.InputURL},
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
			{ID: "fixed_tool", Name: "Fixed tool", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "fixed", RemoteToolName: "upstream_a"}},
			{ID: "self_tool", Name: "Self tool", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "self", RemoteToolName: "upstream_b"}},
		},
	}
}

func newVerifyDeps(t *testing.T, lister *fakeLister) (Deps, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := store.New(pool)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	reg.MustRegister(verifyTestDefinition())
	// configsvc.New 的签名以计划 2 实际落地为准（本计划假定为 (q, reg, kr)）。
	cfg := configsvc.New(q, reg, kr)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec(`delete from connector_health where connector_type = 'verify_test'`)
	mustExec(`delete from connector_configs where connector_type = 'verify_test'`)
	mustExec(`insert into connector_configs
	  (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	  values ('verify_test', 1, '{"mcp_url":"https://self.internal/mcp"}', ''::bytea, 1, now(), now())`)
	return Deps{Registry: reg, Config: cfg, Store: q, MCPTools: lister}, pool
}

func doVerify(t *testing.T, d Deps, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/admin/connectors/"+ctype+"/mcp:verify", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("type")
	c.SetParamValues(ctype)
	if err := mcpVerifyHandler(d)(c); err != nil {
		t.Fatalf("handler 返回 error: %v", err)
	}
	return rec
}

func TestMCPVerifySuccess(t *testing.T) {
	lister := &fakeLister{names: []string{"upstream_a", "upstream_b", "extra"}}
	d, pool := newVerifyDeps(t, lister)
	rec := doVerify(t, d, "verify_test")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if lister.calls != 2 {
		t.Fatalf("两个 server 都应实测: calls=%d", lister.calls)
	}
	var verifiedAt *time.Time
	var endpoint *string
	if err := pool.QueryRow(context.Background(),
		`select mcp_verified_at, mcp_verified_endpoint from connector_configs
		 where connector_type = 'verify_test'`).Scan(&verifiedAt, &endpoint); err != nil {
		t.Fatal(err)
	}
	if verifiedAt == nil || endpoint == nil || *endpoint != "https://self.internal/mcp" {
		t.Fatalf("verify 结果未落库: at=%v endpoint=%v", verifiedAt, endpoint)
	}
	var failures int32
	if err := pool.QueryRow(context.Background(),
		`select consecutive_failures from connector_health
		 where connector_type = 'verify_test'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("verify 成功应写 health 成功: %d", failures)
	}
}

func TestMCPVerifyMissingUpstreamTool(t *testing.T) {
	lister := &fakeLister{names: []string{"upstream_a"}} // 缺 upstream_b
	d, pool := newVerifyDeps(t, lister)
	rec := doVerify(t, d, "verify_test")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "mcp_verify_failed" || !strings.Contains(body.Message, "upstream_b") {
		t.Fatalf("body: %+v", body)
	}
	var verifiedAt *time.Time
	if err := pool.QueryRow(context.Background(),
		`select mcp_verified_at from connector_configs
		 where connector_type = 'verify_test'`).Scan(&verifiedAt); err != nil {
		t.Fatal(err)
	}
	if verifiedAt != nil {
		t.Fatal("失败不应写 mcp_verified_at")
	}
	var failures int32
	if err := pool.QueryRow(context.Background(),
		`select consecutive_failures from connector_health
		 where connector_type = 'verify_test'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("verify 失败应自增 health: %d", failures)
	}
}

func TestMCPVerifyHandshakeFailure(t *testing.T) {
	lister := &fakeLister{err: errors.New("connect refused")}
	d, _ := newVerifyDeps(t, lister)
	rec := doVerify(t, d, "verify_test")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "mcp_verify_failed") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestMCPVerifyUnknownConnector(t *testing.T) {
	d, _ := newVerifyDeps(t, &fakeLister{})
	rec := doVerify(t, d, "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d", rec.Code)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/api && go mod tidy && go test ./...
```

预期：编译失败，`undefined: mcpVerifyHandler`（以及 Deps 缺新字段）。

- [ ] **Step 3: 增补 Deps 字段**

在`packages/api/api.go`的`Deps`结构体末尾追加（保留既有字段不动），并在该文件 import 块加入`exec`与`store`：

```go
	// 计划 4 新增：
	// Exec 是统一 Tool 执行引擎（计划 5 的聚合 /mcp 使用）。
	Exec *exec.Engine
	// Store 与 MCPTools 是 mcp:verify 路由的依赖：
	// Store 写 verified 字段与 connector_health；MCPTools 实测上游 tools/list。
	Store    *store.Queries
	MCPTools MCPToolLister
```

import 追加：

```go
	"github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/store"
```

- [ ] **Step 4: 写 handler 实现**

`packages/api/mcpverify.go`：

```go
package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/store"
)

// MCPToolLister 是 mcp:verify 所需的最小 MCP 客户端接口，
// 生产实现为 mcpclient.Client，测试注入假实现。
type MCPToolLister interface {
	ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error)
}

// mcpVerifyHandler 对 connector 的每个 RemoteMCPServer 实测握手＋tools/list，
// 比对 Definition 中 RemoteMCPBackend.RemoteToolName 是否都在上游列表；
// 全部通过后写 connector_configs.mcp_verified_at / mcp_verified_endpoint，
// 并按 spec §9 把结果 piggyback 进 connector_health（verify 是两个写入方之一）。
// 验证用空 bearer token：self_hosted 上游的 tools/list 不要求用户凭证，
// 需要凭证的上游会在握手阶段失败并把原因反馈给管理员。
func mcpVerifyHandler(d Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx := c.Request().Context()
		ctype := connector.Type(c.Param("type"))
		def, ok := d.Registry.Get(ctype)
		if !ok {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error": "connector_not_found", "message": fmt.Sprintf("connector %q 不存在", ctype)})
		}
		if len(def.RemoteMCPServers) == 0 {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": "no_remote_mcp", "message": fmt.Sprintf("connector %q 没有 Remote MCP server", ctype)})
		}
		cfg, err := d.Config.Resolved(ctx, ctype)
		if err != nil {
			return c.JSON(http.StatusConflict, map[string]string{
				"error": "config_unresolved", "message": err.Error()})
		}

		// 注意：数据库只有一列 mcp_verified_endpoint；若未来一个 Connector 声明
		// 多个 config_field 来源的 server，需要扩表。首批 Connector 只有一个。
		var verifiedEndpoint *string
		checked := 0
		for _, server := range def.RemoteMCPServers {
			endpoint := server.Endpoint.URL
			if server.Endpoint.Source == connector.EndpointConfigField {
				endpoint, _ = cfg[server.Endpoint.ConfigFieldKey].(string)
				if endpoint == "" {
					return c.JSON(http.StatusConflict, map[string]string{
						"error": "endpoint_not_configured",
						"message": fmt.Sprintf("MCP server %q 的 endpoint 配置 %q 未填写",
							server.Key, server.Endpoint.ConfigFieldKey)})
				}
				allowInsecure, _ := cfg["allow_insecure_http"].(bool)
				if err := mcpclient.CheckEndpoint(endpoint, allowInsecure); err != nil {
					return c.JSON(http.StatusConflict, map[string]string{
						"error": "endpoint_invalid", "message": err.Error()})
				}
				ep := endpoint
				verifiedEndpoint = &ep
			}
			names, err := d.MCPTools.ListTools(ctx, endpoint, "", server.RequestTimeout)
			if err != nil {
				return verifyFailed(c, d, ctype, fmt.Sprintf("MCP server %q 握手失败: %v", server.Key, err))
			}
			upstream := make(map[string]bool, len(names))
			for _, n := range names {
				upstream[n] = true
			}
			for _, tool := range def.Tools {
				b, ok := tool.Backend.(connector.RemoteMCPBackend)
				if !ok || b.ServerKey != server.Key {
					continue
				}
				if !upstream[b.RemoteToolName] {
					return verifyFailed(c, d, ctype, fmt.Sprintf(
						"上游 %q 缺少 tool %q（Definition tool %q 引用）",
						server.Key, b.RemoteToolName, tool.ID))
				}
			}
			checked++
		}

		if err := d.Store.SetConnectorConfigVerified(ctx, store.SetConnectorConfigVerifiedParams{
			ConnectorType: string(ctype),
			Endpoint:      verifiedEndpoint,
		}); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "verify_persist_failed", "message": err.Error()})
		}
		_ = d.Store.UpsertConnectorHealthSuccess(ctx, string(ctype))
		return c.JSON(http.StatusOK, map[string]any{
			"verified":        true,
			"servers_checked": checked,
		})
	}
}

// verifyFailed 把失败写入 connector_health 并返回 502。
func verifyFailed(c echo.Context, d Deps, ctype connector.Type, msg string) error {
	_ = d.Store.UpsertConnectorHealthFailure(c.Request().Context(), store.UpsertConnectorHealthFailureParams{
		ConnectorType: string(ctype),
		LastError:     msg,
	})
	return c.JSON(http.StatusBadGateway, map[string]string{
		"error": "mcp_verify_failed", "message": msg})
}
```

- [ ] **Step 5: 注册路由**

在`api.New`中注册其他`/admin/connectors`路由的同一位置（同一 route group、已挂`RequireAdminSession`的地方）追加一行；字面冒号必须用`\\:`转义，参数名与既有路由保持一致（本计划假定为`:type`）：

```go
	admin.POST("/connectors/:type/mcp\\:verify", mcpVerifyHandler(d))
```

若计划 2 不用 group 而是逐条挂中间件，则按既有写法：

```go
	e.POST("/admin/connectors/:type/mcp\\:verify", mcpVerifyHandler(d), /* 与既有 admin 路由相同的中间件 */)
```

- [ ] **Step 6: 运行确认通过**

```bash
cd packages/api && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it_test?sslmode=disable" go test ./...
```

预期：`TestEscapedColonRoute`恒运行且 PASS；其余 verify 测试 PASS；不设环境变量时 verify 测试 SKIP。

- [ ] **Step 7: 提交**

```bash
git add packages/api
git commit -m "feat(api): add mcp:verify admin route"
```

---

### Task 7: main.go 接线与全量验证

**Files:**
- Modify: `packages/api/cmd/connect-it/main.go`（计划 2／3 产出；以下按锚点描述改动，变量名以文件中实际名称为准）

**Interfaces:**
- Consumes: Task 2 的`connectors.AllHandlers`、Task 4 的`mcpclient.Client`、Task 5 的`exec.New`、Task 6 的 Deps 新字段
- Produces: 完整接线的可运行服务（计划 5 在此基础上挂聚合`/mcp`）

- [ ] **Step 1: 修改 main.go**

import 块追加：

```go
	"github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/mcpclient"
```

在既有依赖构建完成之后、构造`api.Deps`之前插入（`queries`／`reg`／`cfgSvc`／`refresher`／`keyring`对应 main.go 中计划 2／3 创建的`*store.Queries`／`*registry.Registry`／`*configsvc.Service`／`*tokens.Refresher`／`*crypto.Keyring`实例，以实际变量名为准）：

```go
	mcpClient := mcpclient.Client{}
	engine := exec.New(queries, reg, cfgSvc, refresher, keyring, connectors.AllHandlers(), mcpClient)
```

在构造`api.Deps`字面量处追加三个字段（既有字段保持不变）：

```go
		Exec:     engine,
		Store:    queries,
		MCPTools: mcpClient,
```

- [ ] **Step 2: 全模块编译与测试**

```bash
cd packages/api && go build ./... && cd ../..
mise run vet
mise run test
```

预期：四个 module 编译通过、vet 无告警；无`TEST_DATABASE_URL`时集成测试 SKIP、其余全绿。

- [ ] **Step 3: 带数据库全量验证**

```bash
mise run db-up
cd packages/service && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it_test?sslmode=disable" go test ./... && cd ../..
cd packages/api && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it_test?sslmode=disable" go test ./... && cd ../..
```

（DSN 按计划 2 的`db-up`环境为准。）预期：全部 PASS。

- [ ] **Step 4: 手工冒烟（可选）**

启动服务（按计划 2 的启动方式与端口），未登录时调用应返回 401：

```bash
curl -i -X POST http://localhost:8080/admin/connectors/github/mcp:verify
```

预期：`401`（`RequireAdminSession`拦截），证明路由注册且鉴权生效。

- [ ] **Step 5: 提交**

```bash
git add packages/api
git commit -m "feat(api): wire exec engine and mcp client into main"
```

---

## 完成标准（对照 spec）

- [ ] Remote MCP 与 Managed Tool 通过同一个`Engine.Execute`接口工作，返回统一`ToolResultData`（spec §11、§17）；
- [ ] Remote MCP 每次调用新建并关闭上游 session，仅 Streamable HTTP（spec §11）；
- [ ] `InputMapperKey`／`OutputMapperKey`非空一律报「mapper 未实现」；
- [ ] self_hosted endpoint 未通过`mcp:verify`或 endpoint 已变更时执行被拒绝（spec §13 规则 3、§17）；配置来源 endpoint 强制 https，`allow_insecure_http`才放行 http（§13 规则 2）；
- [ ] 执行结果 piggyback 写`tool_runs`（input 64KB 截断标记、output_summary 截断、duration_ms）与`connector_health`（成功清零＋last_ok_at；失败自增＋last_error_at／last_error）（spec §7、§9、§11）；
- [ ] `mcp:verify`真实握手＋`tools/list`与 Definition mapping 比对，成功写`mcp_verified_at`／`mcp_verified_endpoint`，成败均写`connector_health`（spec §9、§12）；
- [ ] mcpclient 测试用官方 go-sdk 在 httptest 里起真实 Streamable HTTP server 端到端验证（含 Bearer 头呈递）；
- [ ] 集成测试`TEST_DATABASE_URL`未设置一律 SKIP；`mise run test`／`mise run vet`全绿；
- [ ] Secret 不出现在日志与错误响应中（本计划错误信息只含 type／key／URL，不含凭证明文）。
