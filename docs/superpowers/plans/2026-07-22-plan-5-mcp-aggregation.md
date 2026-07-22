# connect-it 计划 5：mcp_sessions 与聚合 /mcp 端点

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 MCP Session 的签发与解析（`service/sessions`包）、`POST /v1/mcp-sessions`路由，以及基于官方 go-sdk Streamable HTTP 的聚合`/mcp`端点（`tools/list`聚合与`tools/call`分发）。

**Architecture:** `sessions`包（service module）负责 token 签发（256bit 随机 hex、库中只存 sha256）与`SessionView`解析；api module 把`Deps.Exec`抽象为`ToolExecutor`接口以便测试注入假执行引擎；`/mcp`用 go-sdk 的 Stateless Streamable HTTP handler，对每个请求按 session token 动态构建一个只含该 session 可见工具的`mcp.Server`，工具以`{alias}__{tool_id}`暴露，Definition 中已删除的 Tool 通过接收中间件返回`tool_unavailable`。共 3 个 task。

**Tech Stack:** Go 1.25、Echo v4、pgx v5、sqlc、`github.com/google/uuid`、`github.com/modelcontextprotocol/go-sdk` v1.6.1（附带`github.com/google/jsonschema-go`）。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 7、11、12 节是本计划的依据；§9 的「删除 Tool→`tool_unavailable`」规则在本计划落地）

## Global Constraints

- Go module 路径固定为`github.com/memohai/connect-it/packages/<name>`；无 go.work；api 的 go.mod 已由计划 2 用`replace`列出 core、connectors、service，本计划不新增本地 replace。
- 依赖方向单向：`api→service→core`；`sessions`包位于 service module，只依赖`store`、标准库、`uuid`与 pgx。
- 本计划不建新表：`mcp_sessions`／`mcp_session_connections`已在计划 2 的`0001_init`迁移中建好（列见 spec §7）。
- alias 正则`^[a-z0-9][a-z0-9-]{0,31}$`（不含下划线）；tool ID 正则`^[a-z0-9_]+$`；暴露名`{alias}__{tool_id}`按**第一个**`__`切分，可逆解析。
- session token：随机 256bit（32 字节）hex 共 64 字符；库中只存其 sha256 的 hex；ttl 默认 1h、上限 24h（含 24h 本身）。
- API JSON 一律 snake_case；错误响应固定`{"error":"code","message":"…"}`。
- 集成测试读`TEST_DATABASE_URL`，未设置一律`t.Skip`；运行前先`mise run db-up`（目标库已含全部迁移）。
- MCP 依赖钉版本：`github.com/modelcontextprotocol/go-sdk` v1.6.1（2026-05-22 发布）。**已用 WebFetch 对照 pkg.go.dev 与源码查证真实 API**，与早期版本的差异以下节清单为准，不得凭记忆改写签名。
- go-sdk 低阶`ToolHandler`返回非 nil error 会成为**协议错误**（JSON-RPC error），因此业务失败（含`tool_unavailable`、执行引擎报错）一律返回`*mcp.CallToolResult{IsError: true}`并返回 nil error。
- `/mcp`使用`StreamableHTTPOptions{Stateless: true}`：Stateless 模式下 SDK 对**每个 POST**调用`getServer`，与契约「每个 MCP 请求按 session token 动态构建 server 实例（无进程内长驻状态）」一致；协议 session 生命周期由 SDK handler 管理；`getServer`返回 nil 时 SDK 直接回 400。
- 提交信息用 conventional commits（feat:／test:／chore:），每个 task 至少一次提交。

## go-sdk v1.6.1 已查证 API 清单（本计划以此为准）

```go
// 服务端
func NewServer(impl *Implementation, options *ServerOptions) *Server
func (s *Server) AddTool(t *Tool, h ToolHandler)
type ToolHandler func(context.Context, *CallToolRequest) (*CallToolResult, error)
type CallToolRequest = ServerRequest[*CallToolParamsRaw]   // req.Params.Arguments 为 json.RawMessage
type CallToolParamsRaw struct {
    Meta      `json:"_meta,omitempty"`
    Name      string          `json:"name"`
    Arguments json.RawMessage `json:"arguments,omitempty"`
}
type Tool struct {              // 仅列本计划用到的字段
    Name         string
    Description  string
    InputSchema  *jsonschema.Schema   // github.com/google/jsonschema-go/jsonschema
    OutputSchema *jsonschema.Schema
}
func NewStreamableHTTPHandler(getServer func(*http.Request) *Server,
    opts *StreamableHTTPOptions) *StreamableHTTPHandler    // 实现 http.Handler
type StreamableHTTPOptions struct { Stateless bool; JSONResponse bool /* 其余字段本计划不用 */ }

// 接收中间件（用于 tool_unavailable 拦截）
func (s *Server) AddReceivingMiddleware(middleware ...Middleware)
type Middleware func(MethodHandler) MethodHandler
type MethodHandler func(ctx context.Context, method string, req Request) (result Result, err error)
type Request interface { GetSession() Session; GetParams() Params; GetExtra() *RequestExtra /* isRequest() */ }
// *CallToolResult 实现 Result，可从中间件直接返回

// 客户端（测试用）
func NewClient(impl *Implementation, options *ClientOptions) *Client
func (c *Client) Connect(ctx context.Context, t Transport, opts *ClientSessionOptions) (*ClientSession, error)
type StreamableClientTransport struct { Endpoint string; HTTPClient *http.Client /* 其余字段本计划不用 */ }
func (cs *ClientSession) ListTools(ctx context.Context, params *ListToolsParams) (*ListToolsResult, error)
func (cs *ClientSession) CallTool(ctx context.Context, params *CallToolParams) (*CallToolResult, error)
func (cs *ClientSession) Close() error
type CallToolParams struct { Name string `json:"name"`; Arguments any `json:"arguments,omitempty"` }
type CallToolResult struct { Content []Content; IsError bool; StructuredContent any }
type ListToolsResult struct { Tools []*Tool }
type TextContent struct { Text string /* … */ }            // 指针实现 Content 接口
type Implementation struct { Name string; Version string }
```

## 对计划 1–4 落地代码的适配说明

本计划假设计划 1–4 的代码均已按跨计划契约落地。契约未钉死的细节按下列假定执行，若与实际落地不符，**只在标注的单点适配，本计划导出的接口一律不变**：

1. `connector.ToolResultData`字段`{Text string; Structured json.RawMessage; IsError bool}`——2026-07-22 跨计划仲裁已把该形状定为契约（计划 4 的 handler.go 按此定义），本计划的`toolResultToMCP`与测试无需再适配。
2. sqlc：新查询文件放入计划 2 的 queries 目录（本计划按`packages/service/store/queries/`假定，以`packages/service/sqlc.yaml`实际配置为准）；假定 sqlc.yaml 已配置 overrides 把`uuid`映射为`github.com/google/uuid.UUID`、`timestamptz`映射为`time.Time`、`jsonb`按默认映射`[]byte`（`exec.Engine.Execute`签名使用`uuid.UUID`表明 store 层已如此工作）。若生成类型为`pgtype.*`，在`sessions`包内做类型转换，导出接口不变。
3. `packages/api/api.go`：假定`Deps`定义与`api.New`的路由注册在此文件（若计划 2 拆成`deps.go`等，以实际文件为准）；本计划对它的全部修改是「`Exec`字段改接口＋新增`Sessions`字段＋在`api.New`中各加一行`registerMCPSessions(v1, deps)`与`registerMCP(e, deps)`」，`v1`指已挂`RequireAPIToken`的既有`/v1`路由组变量（变量名以实际为准）。
4. 测试 helper `newTestEnv`的`Deps`装配方式（尤其`Auth`字段的类型与构造）以`packages/api/cmd/connect-it/main.go`的真实装配为准；helper 是唯一装配点，出入只改它。
5. `api.New(deps)`假定返回`*echo.Echo`（可直接作`http.Handler`交给`httptest.NewServer`）；若返回类型不同，只改 helper。
6. `api_tokens.token_hash`假定为 sha256 的 hex 编码（与本计划`mcp_sessions.token_hash`同一约定）。

---

### Task 1: service/sessions 包（token 签发与解析）

**Files:**
- Create: `packages/service/store/queries/mcp_sessions.sql`（目录以计划 2 的 sqlc.yaml 为准）
- Create: `packages/service/sessions/service.go`
- Test: `packages/service/sessions/service_test.go`
- 生成：`mise run sqlc`更新`packages/service/store`下生成代码

**Interfaces:**
- Consumes:
  - `store.Queries`（计划 2；`store.New(db store.DBTX) *Queries`，`*pgxpool.Pool`满足`DBTX`）
  - `mcp_sessions`／`mcp_session_connections`／`connections`表（计划 2 迁移，列见 spec §7）
  - `github.com/google/uuid`、`github.com/jackc/pgx/v5`（`pgx.ErrNoRows`）
- Produces（契约钉死部分，签名一字不差）:
  - `sessions.New(q *store.Queries) *Service`
  - `type SessionView struct{ ID uuid.UUID; Bindings map[string]uuid.UUID; Allowlist map[string]bool; ExpiresAt time.Time }`（`Allowlist`的 key 是暴露名`alias__tool_id`；空 map 表示允许全部绑定连接的全部 tool）
  - `(*Service).Create(ctx context.Context, bindings map[string]uuid.UUID, toolAllowlist []string, ttl time.Duration) (token string, err error)`
  - `(*Service).Resolve(ctx context.Context, token string) (SessionView, error)`
- Produces（本计划补充定义，Task 2／3 依赖）:
  - `sessions.ErrInvalidSession`（token 不存在、过期、吊销的统一错误）
  - `type ValidationError struct{ Code, Message string }`（`Code`直接用作 API 错误码：`empty_bindings`／`invalid_alias`／`invalid_allowlist`／`invalid_ttl`／`unknown_connection`）
  - `sessions.SplitExposedName(name string) (alias, toolID string, ok bool)`
  - `(*Service).ConnectionConnectorTypes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)`
  - `sessions.DefaultTTL = time.Hour`、`sessions.MaxTTL = 24 * time.Hour`
  - store 新查询（sqlc 生成，预期签名）：
    - `CreateMCPSession(ctx, CreateMCPSessionParams{ID uuid.UUID; TokenHash string; ToolAllowlist []byte; ExpiresAt time.Time}) error`
    - `AddMCPSessionConnection(ctx, AddMCPSessionConnectionParams{SessionID uuid.UUID; Alias string; ConnectionID uuid.UUID}) error`
    - `GetMCPSessionByTokenHash(ctx, tokenHash string) (McpSession, error)`
    - `ListMCPSessionConnections(ctx, sessionID uuid.UUID) ([]McpSessionConnection, error)`
    - `ConnectionExistsByID(ctx, id uuid.UUID) (bool, error)`
    - `GetConnectionConnectorTypes(ctx, ids []uuid.UUID) ([]GetConnectionConnectorTypesRow, error)`

- [ ] **Step 1: 写 sqlc 查询文件**

`packages/service/store/queries/mcp_sessions.sql`：

```sql
-- name: CreateMCPSession :exec
INSERT INTO mcp_sessions (id, token_hash, tool_allowlist, status, expires_at, created_at)
VALUES ($1, $2, $3, 'active', $4, now());

-- name: AddMCPSessionConnection :exec
INSERT INTO mcp_session_connections (session_id, alias, connection_id)
VALUES ($1, $2, $3);

-- name: GetMCPSessionByTokenHash :one
SELECT * FROM mcp_sessions WHERE token_hash = $1;

-- name: ListMCPSessionConnections :many
SELECT * FROM mcp_session_connections WHERE session_id = $1;

-- name: ConnectionExistsByID :one
SELECT EXISTS (SELECT 1 FROM connections WHERE id = $1) AS found;

-- name: GetConnectionConnectorTypes :many
SELECT id, connector_type FROM connections WHERE id = ANY(@ids::uuid[]);
```

- [ ] **Step 2: 运行 sqlc 并核对生成签名**

```bash
mise run sqlc
grep -n "func (q \*Queries) CreateMCPSession\|func (q \*Queries) GetMCPSessionByTokenHash\|func (q \*Queries) ConnectionExistsByID\|func (q \*Queries) GetConnectionConnectorTypes" packages/service/store/*.go
```

预期：生成上面 Interfaces 列出的六个方法。若模型名不是`McpSession`／`McpSessionConnection`或参数类型不是`uuid.UUID`／`time.Time`（见「适配说明」第 2 条），记录实际名字，后续步骤按实际引用。

- [ ] **Step 3: 写失败测试**

`packages/service/sessions/service_test.go`：

```go
package sessions_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
)

// ---------- 无数据库的纯单元测试 ----------

func TestSplitExposedName(t *testing.T) {
	cases := []struct {
		in            string
		alias, toolID string
		ok            bool
	}{
		{"gh__list_repos", "gh", "list_repos", true},
		{"mail-1__send__msg", "mail-1", "send__msg", true}, // 按第一个 __ 切分
		{"a__t", "a", "t", true},
		{"noseparator", "", "", false},
		{"gh__", "", "", false},          // tool_id 为空
		{"__tool", "", "", false},        // alias 为空
		{"Gh__tool", "", "", false},      // alias 大写
		{"gh__Tool-X", "", "", false},    // tool_id 含大写与连字符
		{"has_underscore__tool", "", "", false}, // alias 不含下划线
	}
	for _, tc := range cases {
		alias, toolID, ok := sessions.SplitExposedName(tc.in)
		if alias != tc.alias || toolID != tc.toolID || ok != tc.ok {
			t.Fatalf("%q → (%q,%q,%v), want (%q,%q,%v)",
				tc.in, alias, toolID, ok, tc.alias, tc.toolID, tc.ok)
		}
	}
}

// ---------- 集成测试基础设施 ----------

func testService(t *testing.T) (*sessions.Service, *pgxpool.Pool) {
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
	return sessions.New(store.New(pool)), pool
}

// insertConnection 直接插一行 connection（alias 随机保证唯一，无需清理）。
func insertConnection(t *testing.T, pool *pgxpool.Pool, connectorType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	alias := "c-" + hex.EncodeToString(id[0:6])
	_, err := pool.Exec(context.Background(), `
		INSERT INTO connections
		  (id, connector_type, alias, auth_method, credential, secret_key_version,
		   profile, scopes, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'api_key', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`,
		id, connectorType, alias)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func assertValidationCode(t *testing.T, fn func() error, code string) {
	t.Helper()
	err := fn()
	var verr *sessions.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("应返回 *sessions.ValidationError, got %v", err)
	}
	if verr.Code != code {
		t.Fatalf("code=%s, want %s", verr.Code, code)
	}
}

// ---------- Create ----------

func TestCreateStoresSHA256NotPlaintext(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	token, err := svc.Create(context.Background(),
		map[string]uuid.UUID{"app": connID}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("token 应为 64 位 hex（256bit）: %q", token)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mcp_sessions WHERE token_hash = $1`,
		tokenHash(token)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应按 sha256 hex 存储, got %d 行", n)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM mcp_sessions WHERE token_hash = $1`,
		token).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("明文 token 不得入库")
	}
}

func TestCreateTTLBoundaries(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	bindings := map[string]uuid.UUID{"app": connID}
	ctx := context.Background()

	// ttl = 0 → 默认 1 小时
	token, err := svc.Create(ctx, bindings, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(view.ExpiresAt); d < 55*time.Minute || d > 65*time.Minute {
		t.Fatalf("默认 ttl 应为 1h, 实际剩余 %s", d)
	}

	// ttl = 24h → 恰好可用（上限含 24h）
	token, err = svc.Create(ctx, bindings, nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	view, err = svc.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(view.ExpiresAt); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("24h ttl 不符, 实际剩余 %s", d)
	}

	// ttl > 24h → invalid_ttl
	assertValidationCode(t, func() error {
		_, err := svc.Create(ctx, bindings, nil, 24*time.Hour+time.Second)
		return err
	}, "invalid_ttl")

	// ttl < 0 → invalid_ttl
	assertValidationCode(t, func() error {
		_, err := svc.Create(ctx, bindings, nil, -time.Second)
		return err
	}, "invalid_ttl")
}

func TestCreateValidatesAlias(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	for _, alias := range []string{"Bad", "has_underscore", "-lead", "", strings.Repeat("a", 33)} {
		assertValidationCode(t, func() error {
			_, err := svc.Create(context.Background(),
				map[string]uuid.UUID{alias: connID}, nil, 0)
			return err
		}, "invalid_alias")
	}
}

func TestCreateRejectsUnknownConnection(t *testing.T) {
	svc, _ := testService(t)
	assertValidationCode(t, func() error {
		_, err := svc.Create(context.Background(),
			map[string]uuid.UUID{"app": uuid.New()}, nil, 0)
		return err
	}, "unknown_connection")
}

func TestCreateRejectsEmptyBindings(t *testing.T) {
	svc, _ := testService(t)
	assertValidationCode(t, func() error {
		_, err := svc.Create(context.Background(), map[string]uuid.UUID{}, nil, 0)
		return err
	}, "empty_bindings")
}

func TestCreateValidatesAllowlist(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	bindings := map[string]uuid.UUID{"app": connID}
	for _, entry := range []string{"noseparator", "app__Bad-Tool", "other__tool"} {
		assertValidationCode(t, func() error {
			_, err := svc.Create(context.Background(), bindings, []string{entry}, 0)
			return err
		}, "invalid_allowlist")
	}
}

// ---------- Resolve ----------

func TestResolveRoundtrip(t *testing.T) {
	svc, pool := testService(t)
	a := insertConnection(t, pool, "example_app")
	b := insertConnection(t, pool, "other_app")
	token, err := svc.Create(context.Background(),
		map[string]uuid.UUID{"app-a": a, "app-b": b},
		[]string{"app-a__list_items"}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	view, err := svc.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if view.ID == uuid.Nil {
		t.Fatal("ID 不应为零值")
	}
	if len(view.Bindings) != 2 || view.Bindings["app-a"] != a || view.Bindings["app-b"] != b {
		t.Fatalf("bindings=%v", view.Bindings)
	}
	if len(view.Allowlist) != 1 || !view.Allowlist["app-a__list_items"] {
		t.Fatalf("allowlist=%v", view.Allowlist)
	}
}

func TestResolveEmptyAllowlistMeansAll(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	token, err := svc.Create(context.Background(),
		map[string]uuid.UUID{"app": connID}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Allowlist) != 0 {
		t.Fatalf("空 allowlist 应解析为空 map: %v", view.Allowlist)
	}
}

func TestResolveUnknownToken(t *testing.T) {
	svc, _ := testService(t)
	if _, err := svc.Resolve(context.Background(), strings.Repeat("ab", 32)); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("got %v, want ErrInvalidSession", err)
	}
}

func TestResolveExpired(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	token, err := svc.Create(context.Background(),
		map[string]uuid.UUID{"app": connID}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE mcp_sessions SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`,
		tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(context.Background(), token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("过期 session 应返回 ErrInvalidSession, got %v", err)
	}
}

func TestResolveRevoked(t *testing.T) {
	svc, pool := testService(t)
	connID := insertConnection(t, pool, "example_app")
	token, err := svc.Create(context.Background(),
		map[string]uuid.UUID{"app": connID}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE mcp_sessions SET status = 'revoked' WHERE token_hash = $1`,
		tokenHash(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(context.Background(), token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("吊销 session 应返回 ErrInvalidSession, got %v", err)
	}
}

// ---------- ConnectionConnectorTypes ----------

func TestConnectionConnectorTypes(t *testing.T) {
	svc, pool := testService(t)
	a := insertConnection(t, pool, "example_app")
	b := insertConnection(t, pool, "other_app")
	got, err := svc.ConnectionConnectorTypes(context.Background(),
		[]uuid.UUID{a, b, uuid.New()}) // 第三个不存在，应被静默省略
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[a] != "example_app" || got[b] != "other_app" {
		t.Fatalf("got=%v", got)
	}
}
```

- [ ] **Step 4: 运行确认失败**

```bash
cd packages/service && go test ./sessions/
```

预期：编译失败，`undefined: sessions.New`等。

- [ ] **Step 5: 写实现**

`packages/service/sessions/service.go`：

```go
// Package sessions 管理聚合 /mcp 的 MCP Session：
// Create 签发一次性 token（库中只存 sha256），Resolve 把 token 解析为绑定视图。
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/memohai/connect-it/packages/service/store"
)

const (
	// DefaultTTL 在调用方传 ttl == 0 时生效。
	DefaultTTL = time.Hour
	// MaxTTL 是 ttl 上限（含）。
	MaxTTL = 24 * time.Hour
)

var (
	aliasPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	toolIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// ErrInvalidSession 统一表示 token 不存在、已过期或已吊销，避免向调用方泄露具体状态。
var ErrInvalidSession = errors.New("sessions: invalid or expired session token")

// ValidationError 是 Create 的入参校验错误；Code 直接用作 API 错误码。
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

// SessionView 是一个已解析 session 的只读视图。
// Allowlist 的 key 是暴露名 alias__tool_id；空 map 表示允许全部绑定连接的全部 tool。
type SessionView struct {
	ID        uuid.UUID
	Bindings  map[string]uuid.UUID
	Allowlist map[string]bool
	ExpiresAt time.Time
}

type Service struct {
	q *store.Queries
}

func New(q *store.Queries) *Service { return &Service{q: q} }

// SplitExposedName 把暴露名 {alias}__{tool_id} 按第一个 "__" 切开并校验两段字符集。
func SplitExposedName(name string) (alias, toolID string, ok bool) {
	i := strings.Index(name, "__")
	if i < 0 {
		return "", "", false
	}
	alias, toolID = name[:i], name[i+2:]
	if !aliasPattern.MatchString(alias) || !toolIDPattern.MatchString(toolID) {
		return "", "", false
	}
	return alias, toolID, true
}

// Create 签发一个新的 MCP Session token（256bit 随机 hex，库中只存 sha256 hex）。
// ttl == 0 时取 DefaultTTL；ttl < 0 或 > MaxTTL 报 invalid_ttl。
func (s *Service) Create(ctx context.Context, bindings map[string]uuid.UUID, toolAllowlist []string, ttl time.Duration) (token string, err error) {
	if len(bindings) == 0 {
		return "", &ValidationError{Code: "empty_bindings", Message: "at least one alias binding is required"}
	}
	for alias := range bindings {
		if !aliasPattern.MatchString(alias) {
			return "", &ValidationError{Code: "invalid_alias",
				Message: fmt.Sprintf("alias %q must match %s", alias, aliasPattern)}
		}
	}
	for _, name := range toolAllowlist {
		alias, _, ok := SplitExposedName(name)
		if !ok {
			return "", &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is not a valid {alias}__{tool_id} name", name)}
		}
		if _, bound := bindings[alias]; !bound {
			return "", &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q references unbound alias %q", name, alias)}
		}
	}
	switch {
	case ttl < 0:
		return "", &ValidationError{Code: "invalid_ttl", Message: "ttl must not be negative"}
	case ttl == 0:
		ttl = DefaultTTL
	case ttl > MaxTTL:
		return "", &ValidationError{Code: "invalid_ttl", Message: "ttl must not exceed 24h"}
	}
	for alias, connID := range bindings {
		found, err := s.q.ConnectionExistsByID(ctx, connID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", &ValidationError{Code: "unknown_connection",
				Message: fmt.Sprintf("connection %s (alias %q) does not exist", connID, alias)}
		}
	}

	raw := make([]byte, 32) // 256bit
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	allowlist := toolAllowlist
	if allowlist == nil {
		allowlist = []string{}
	}
	allowlistJSON, err := json.Marshal(allowlist)
	if err != nil {
		return "", err
	}

	// 无事务：session 行先落库，连接绑定行随后插入。
	// 中途失败时 token 不会返回给调用方，孤儿 session 行不可达、无害。
	sessionID := uuid.New()
	if err := s.q.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:            sessionID,
		TokenHash:     hex.EncodeToString(sum[:]),
		ToolAllowlist: allowlistJSON,
		ExpiresAt:     time.Now().UTC().Add(ttl),
	}); err != nil {
		return "", err
	}
	for alias, connID := range bindings {
		if err := s.q.AddMCPSessionConnection(ctx, store.AddMCPSessionConnectionParams{
			SessionID:    sessionID,
			Alias:        alias,
			ConnectionID: connID,
		}); err != nil {
			return "", err
		}
	}
	return token, nil
}

// Resolve 把 session token 解析为 SessionView；token 不存在、过期或吊销返回 ErrInvalidSession。
func (s *Service) Resolve(ctx context.Context, token string) (SessionView, error) {
	sum := sha256.Sum256([]byte(token))
	row, err := s.q.GetMCPSessionByTokenHash(ctx, hex.EncodeToString(sum[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionView{}, ErrInvalidSession
	}
	if err != nil {
		return SessionView{}, err
	}
	if row.Status != "active" {
		return SessionView{}, ErrInvalidSession
	}
	if !row.ExpiresAt.After(time.Now()) {
		return SessionView{}, ErrInvalidSession
	}

	var list []string
	if err := json.Unmarshal(row.ToolAllowlist, &list); err != nil {
		return SessionView{}, fmt.Errorf("sessions: corrupt tool_allowlist for session %s: %w", row.ID, err)
	}
	allowlist := make(map[string]bool, len(list))
	for _, name := range list {
		allowlist[name] = true
	}

	conns, err := s.q.ListMCPSessionConnections(ctx, row.ID)
	if err != nil {
		return SessionView{}, err
	}
	bindings := make(map[string]uuid.UUID, len(conns))
	for _, c := range conns {
		bindings[c.Alias] = c.ConnectionID
	}

	return SessionView{
		ID:        row.ID,
		Bindings:  bindings,
		Allowlist: allowlist,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// ConnectionConnectorTypes 批量查询连接的 connector_type，供 /mcp 构建工具列表。
// 不存在的连接（已被删除）不会出现在结果里，由调用方决定如何降级。
func (s *Service) ConnectionConnectorTypes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	rows, err := s.q.GetConnectionConnectorTypes(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.ConnectorType
	}
	return out, nil
}
```

- [ ] **Step 6: 无数据库运行（单元过、集成 skip）**

```bash
cd packages/service && go test ./sessions/ -v
```

预期：`TestSplitExposedName`PASS；其余集成用例全部 SKIP（提示`TEST_DATABASE_URL 未设置`）；包结果`ok`。

- [ ] **Step 7: 带数据库运行全部通过**

```bash
mise run db-up
cd packages/service && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it?sslmode=disable" go test ./sessions/ -v
```

连接串按本地`db-up`实际输出调整。预期：全部用例 PASS。

- [ ] **Step 8: vet 与提交**

```bash
mise run vet
git add packages/service/store/queries/mcp_sessions.sql packages/service/store packages/service/sessions
git commit -m "feat(service): add mcp sessions service with token issuance and resolve"
```

---

### Task 2: api Deps 改造与 POST /v1/mcp-sessions

**Files:**
- Modify: `packages/api/api.go`（`Deps`定义与`api.New`路由注册；若在其他文件以实际为准）
- Modify: `packages/api/cmd/connect-it/main.go`
- Create: `packages/api/mcpsessions.go`
- Test: `packages/api/mcptest_test.go`（集成测试共享 helper，Task 3 复用）
- Test: `packages/api/mcpsessions_test.go`

**Interfaces:**
- Consumes:
  - Task 1 的`sessions.Service`／`ValidationError`
  - 计划 2 的`api.New(deps)`、`RequireAPIToken`（Bearer api_token）、`/v1`路由组
  - 计划 4 的`exec.Engine`（方法`Execute(ctx, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error)`）
- Produces:
  - `api.ToolExecutor`接口（契约原文）：`type ToolExecutor interface{ Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error) }`
  - `Deps.Exec`类型改为`ToolExecutor`；`Deps`新增`Sessions *sessions.Service`
  - `POST /v1/mcp-sessions`：`RequireAPIToken`；body `{"connections":{"alias":"connection_uuid",…},"tool_allowlist":["alias__tool_id",…],"ttl_seconds":3600}`→`201 {"token":"…","expires_at":"RFC3339"}`
  - 测试 helper：`testEnv{Server;Pool;Queries;Sessions;Exec}`、`newTestEnv(t, reg)`、`insertAPIToken`、`insertConnection`、`fakeExecutor`（Task 3 复用）

- [ ] **Step 1: 把 Deps.Exec 改为接口并新增 Sessions 字段**

修改`packages/api/api.go`（`Deps`所在文件）。在`Deps`定义前新增：

```go
// ToolExecutor 抽象 Tool 执行引擎：生产装配传 *exec.Engine，测试传假实现。
type ToolExecutor interface {
	Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error)
}
```

`Deps`中把`Exec *exec.Engine`一行改为下面两行，**其余字段一律不动**：

```go
	Exec     ToolExecutor
	Sessions *sessions.Service
```

import 块确保含有（缺则补）：

```go
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/sessions"
```

若`api.go`原本仅因`Exec`字段 import 了`exec`包，删除该 import（`main.go`仍引用它）。

**与计划 4 接线的相容说明（务必核对）：**
- `*exec.Engine`的方法集恰好覆盖`ToolExecutor`（契约钉死的同一签名），接口赋值兼容，`main.go`中`Exec: engine`一行**无需改动**；
- api 包内既有调用点`deps.Exec.Execute(…)`的调用形式不变，无需改动；
- 契约中`exec.Engine`只暴露`Execute`一个方法；若计划 4 实际代码在 api 包用到了`*exec.Engine`的其他方法，把该方法一并收进`ToolExecutor`接口（并同步给假实现），不要把字段改回具体类型。

- [ ] **Step 2: 编译验证接口替换无破坏**

```bash
cd packages/api && go build ./... && go vet ./...
```

预期：无错误。若报`cannot use engine (type *exec.Engine) as ToolExecutor`，说明计划 4 的`Execute`签名与契约不符，停下核对`packages/service/exec`的实际签名后修正接口（契约上不应发生）。

- [ ] **Step 3: main.go 接线 Sessions**

修改`packages/api/cmd/connect-it/main.go`：import 块加一行：

```go
	"github.com/memohai/connect-it/packages/service/sessions"
```

在`api.Deps{…}`字面量中追加一项（`q`为计划 2 起装配的`*store.Queries`变量，变量名以实际 main.go 为准）：

```go
		Sessions: sessions.New(q),
```

然后编译：

```bash
cd packages/api && go build ./...
```

预期：无错误。

- [ ] **Step 4: 写集成测试共享 helper**

`packages/api/mcptest_test.go`：

```go
package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/memohai/connect-it/packages/api"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
)

// testEnv 汇集集成测试要用的全部句柄。
type testEnv struct {
	Server   *httptest.Server
	Pool     *pgxpool.Pool
	Queries  *store.Queries
	Sessions *sessions.Service
	Exec     *fakeExecutor
}

// newTestEnv 起一个完整 api.New 实例；TEST_DATABASE_URL 未设置时跳过。
// Deps 装配方式与 cmd/connect-it/main.go 对齐：若计划 2–4 落地的字段类型
// 与此处不符（尤其 Auth），以 main.go 为准调整本函数（仅此一处）。
// 本计划未涉及的 Deps 字段留零值：对应路由不被这些测试触发。
func newTestEnv(t *testing.T, reg *registry.Registry) *testEnv {
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

	q := store.New(pool)
	fake := &fakeExecutor{}
	svc := sessions.New(q)
	deps := api.Deps{
		Registry:     reg,
		Auth:         q, // RequireAPIToken 的依赖；若 Deps.Auth 是专门类型，按 main.go 装配替换本行
		Sessions:     svc,
		Exec:         fake,
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
	}
	srv := httptest.NewServer(api.New(deps))
	t.Cleanup(srv.Close)
	return &testEnv{Server: srv, Pool: pool, Queries: q, Sessions: svc, Exec: fake}
}

// insertAPIToken 直接向 api_tokens 插入一条 token（sha256 hex），返回明文。
func insertAPIToken(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	token := "test-" + uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	_, err := pool.Exec(context.Background(), `
		INSERT INTO api_tokens (id, name, token_hash, created_at)
		VALUES ($1, 'plan5-test', $2, now())`,
		uuid.New(), hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// insertConnection 直接插一行 connection（alias 随机保证唯一，无需清理）。
func insertConnection(t *testing.T, pool *pgxpool.Pool, connectorType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	alias := "c-" + hex.EncodeToString(id[0:6])
	_, err := pool.Exec(context.Background(), `
		INSERT INTO connections
		  (id, connector_type, alias, auth_method, credential, secret_key_version,
		   profile, scopes, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'api_key', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`,
		id, connectorType, alias)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type execCall struct {
	ConnectionID uuid.UUID
	ToolID       string
	Args         string
}

// fakeExecutor 实现 api.ToolExecutor：记录调用并返回预设结果。
type fakeExecutor struct {
	mu     sync.Mutex
	calls  []execCall
	result connector.ToolResultData
	err    error
}

func (f *fakeExecutor) Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, execCall{ConnectionID: connectionID, ToolID: toolID, Args: string(args)})
	return f.result, f.err
}

func (f *fakeExecutor) setResult(r connector.ToolResultData, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = r
	f.err = err
}

func (f *fakeExecutor) recorded() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execCall(nil), f.calls...)
}

var _ api.ToolExecutor = (*fakeExecutor)(nil)
```

- [ ] **Step 5: 写失败测试**

`packages/api/mcpsessions_test.go`：

```go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/registry"
)

// postJSON 发送带可选 Bearer token 的 JSON POST，返回响应与（尽力解析的）JSON body。
func postJSON(t *testing.T, url, token string, body any) (*http.Response, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out) // 非 JSON body 时 out 为 nil，由断言自行处理
	return res, out
}

// defaultRegistry 用真实注册表（github 等）；本路由不读 Definition，够用。
func defaultRegistry() *registry.Registry {
	r := registry.New()
	connectors.RegisterAll(r)
	return r
}

func TestCreateMCPSessionRequiresAPIToken(t *testing.T) {
	env := newTestEnv(t, defaultRegistry())
	res, _ := postJSON(t, env.Server.URL+"/v1/mcp-sessions", "", map[string]any{})
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", res.StatusCode)
	}
}

func TestCreateMCPSessionSuccess(t *testing.T) {
	env := newTestEnv(t, defaultRegistry())
	apiToken := insertAPIToken(t, env.Pool)
	connID := insertConnection(t, env.Pool, "github")

	res, out := postJSON(t, env.Server.URL+"/v1/mcp-sessions", apiToken, map[string]any{
		"connections":    map[string]string{"gh": connID.String()},
		"tool_allowlist": []string{"gh__list_repos"},
		"ttl_seconds":    3600,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("got %d body %v, want 201", res.StatusCode, out)
	}
	token, _ := out["token"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(token) {
		t.Fatalf("token 应为 64 位 hex: %q", token)
	}
	expStr, _ := out["expires_at"].(string)
	exp, err := time.Parse(time.RFC3339, expStr)
	if err != nil {
		t.Fatalf("expires_at 不是 RFC3339: %q", expStr)
	}
	if d := time.Until(exp); d < 55*time.Minute || d > 65*time.Minute {
		t.Fatalf("expires_at 应约为 1 小时后: %s", expStr)
	}
	// token 可被 sessions.Resolve 解析，且 bindings 正确落库
	view, err := env.Sessions.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if view.Bindings["gh"] != connID {
		t.Fatalf("bindings 不符: %+v", view.Bindings)
	}
	if !view.Allowlist["gh__list_repos"] {
		t.Fatalf("allowlist 不符: %+v", view.Allowlist)
	}
}

func TestCreateMCPSessionValidation(t *testing.T) {
	env := newTestEnv(t, defaultRegistry())
	apiToken := insertAPIToken(t, env.Pool)
	connID := insertConnection(t, env.Pool, "github")
	url := env.Server.URL + "/v1/mcp-sessions"

	cases := []struct {
		name     string
		body     map[string]any
		wantCode string
	}{
		{"非法 alias", map[string]any{
			"connections": map[string]string{"Bad_Alias": connID.String()},
		}, "invalid_alias"},
		{"connection 不存在", map[string]any{
			"connections": map[string]string{"gh": uuid.NewString()},
		}, "unknown_connection"},
		{"connection id 不是 UUID", map[string]any{
			"connections": map[string]string{"gh": "not-a-uuid"},
		}, "invalid_connection_id"},
		{"ttl 超上限", map[string]any{
			"connections": map[string]string{"gh": connID.String()},
			"ttl_seconds": 24*3600 + 1,
		}, "invalid_ttl"},
		{"空 bindings", map[string]any{
			"connections": map[string]string{},
		}, "empty_bindings"},
		{"allowlist 引用未绑定 alias", map[string]any{
			"connections":    map[string]string{"gh": connID.String()},
			"tool_allowlist": []string{"other__tool"},
		}, "invalid_allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, out := postJSON(t, url, apiToken, tc.body)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("got %d body %v, want 400", res.StatusCode, out)
			}
			if out["error"] != tc.wantCode {
				t.Fatalf("error=%v, want %s", out["error"], tc.wantCode)
			}
		})
	}
}
```

- [ ] **Step 6: 运行确认失败**

```bash
cd packages/api && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it?sslmode=disable" go test ./ -run 'TestCreateMCPSession' -v
```

预期：编译通过（Deps 已含 Sessions／Exec 接口），但路由尚未注册，`TestCreateMCPSessionSuccess`等 FAIL（期望 201 实得 404）。`TestCreateMCPSessionRequiresAPIToken`也可能因 404 而 FAIL——RequireAPIToken 只挂在已注册路由上，404 属预期失败。

- [ ] **Step 7: 写 handler 并挂路由**

`packages/api/mcpsessions.go`：

```go
package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/memohai/connect-it/packages/service/sessions"
)

type createMCPSessionRequest struct {
	Connections   map[string]string `json:"connections"`
	ToolAllowlist []string          `json:"tool_allowlist"`
	TTLSeconds    int               `json:"ttl_seconds"`
}

type createMCPSessionResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// registerMCPSessions 在 /v1 组（已挂 RequireAPIToken）上注册 POST /mcp-sessions。
func registerMCPSessions(g *echo.Group, deps Deps) {
	g.POST("/mcp-sessions", func(c echo.Context) error {
		var req createMCPSessionRequest
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": "invalid_body", "message": "request body must be valid JSON",
			})
		}
		bindings := make(map[string]uuid.UUID, len(req.Connections))
		for alias, raw := range req.Connections {
			id, err := uuid.Parse(raw)
			if err != nil {
				return c.JSON(http.StatusBadRequest, map[string]string{
					"error": "invalid_connection_id", "message": "connection id " + raw + " is not a valid UUID",
				})
			}
			bindings[alias] = id
		}
		ctx := c.Request().Context()
		token, err := deps.Sessions.Create(ctx, bindings, req.ToolAllowlist,
			time.Duration(req.TTLSeconds)*time.Second)
		var verr *sessions.ValidationError
		if errors.As(err, &verr) {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": verr.Code, "message": verr.Message,
			})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "internal", "message": "failed to create mcp session",
			})
		}
		// 回读 expires_at：Create 只返回 token，过期时间以库中落定值为准。
		view, err := deps.Sessions.Resolve(ctx, token)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "internal", "message": "failed to load created session",
			})
		}
		return c.JSON(http.StatusCreated, createMCPSessionResponse{
			Token:     token,
			ExpiresAt: view.ExpiresAt.UTC().Format(time.RFC3339),
		})
	})
}
```

修改`packages/api/api.go`：在`api.New`内注册`/v1`组路由（挂`RequireAPIToken`的组，变量名以实际为准，下称`v1`）处追加一行：

```go
	registerMCPSessions(v1, deps)
```

- [ ] **Step 8: 运行确认通过**

```bash
cd packages/api && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it?sslmode=disable" go test ./ -run 'TestCreateMCPSession' -v
```

预期：三个用例（含全部子用例）PASS。

- [ ] **Step 9: vet 与提交**

```bash
cd packages/api && go vet ./...
git add packages/api
git commit -m "feat(api): add mcp session endpoint and tool executor interface"
```

---

### Task 3: 聚合 /mcp 端点（Streamable HTTP）

**Files:**
- Create: `packages/api/mcpserver.go`
- Test: `packages/api/mcpserver_unit_test.go`（包内单元测试）
- Test: `packages/api/mcpserver_test.go`（端到端集成测试）
- Modify: `packages/api/api.go`（`api.New`加一行`registerMCP(e, deps)`）
- Modify: `packages/api/go.mod`（新增 go-sdk 依赖）

**Interfaces:**
- Consumes:
  - Task 1：`sessions.SessionView`／`Resolve`／`SplitExposedName`／`ConnectionConnectorTypes`／`ErrInvalidSession`
  - Task 2：`ToolExecutor`、`Deps.Sessions`、测试 helper（`newTestEnv`／`insertConnection`／`fakeExecutor`）
  - 计划 1：`registry.Registry.Get(t)(connector.Definition,bool)`、`connector.Tool{ID,Description,InputSchema,…}`
  - go-sdk v1.6.1（见 Global Constraints 后的清单）
- Produces:
  - `/mcp`端点：Streamable HTTP；`Authorization: Bearer <session token>`；工具名`{alias}__{tool_id}`；`tools/list`带 Description 与 InputSchema、受 allowlist 过滤；`tools/call`经`ToolExecutor.Execute`执行、`IsError`透传；已删除 Tool 返回`tool_unavailable`
  - api 包内部（非导出）：`registerMCP(e *echo.Echo, deps Deps)`、`mcpHost`、`toolResultToMCP`、`errorResult`、`unavailableToolMiddleware`

- [ ] **Step 1: 引入 go-sdk 依赖**

```bash
cd packages/api && go get github.com/modelcontextprotocol/go-sdk@v1.6.1 && go mod tidy
```

预期：go.mod 新增`github.com/modelcontextprotocol/go-sdk v1.6.1`；`github.com/google/jsonschema-go`作为其依赖进入（下一步直接 import 后再`go mod tidy`会提升为直接依赖）。

- [ ] **Step 2: 写单元测试（结果转换）**

`packages/api/mcpserver_unit_test.go`：

```go
package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolResultToMCP(t *testing.T) {
	res := toolResultToMCP(connector.ToolResultData{
		Text:       "hi",
		Structured: json.RawMessage(`{"a":1}`),
		IsError:    true,
	})
	if !res.IsError {
		t.Fatal("IsError 应透传")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok || tc.Text != "hi" {
		t.Fatalf("content 不符: %#v", res.Content)
	}
	raw, ok := res.StructuredContent.(json.RawMessage)
	if !ok || string(raw) != `{"a":1}` {
		t.Fatalf("structured 不符: %#v", res.StructuredContent)
	}
}

func TestToolResultToMCPWithoutStructured(t *testing.T) {
	res := toolResultToMCP(connector.ToolResultData{Text: "ok"})
	if res.IsError {
		t.Fatal("IsError 应为 false")
	}
	if res.StructuredContent != nil {
		t.Fatalf("无 structured 时不应设置: %#v", res.StructuredContent)
	}
}

func TestErrorResult(t *testing.T) {
	res := errorResult("tool_unavailable", "gone")
	if !res.IsError {
		t.Fatal("errorResult 必须 IsError")
	}
	tc := res.Content[0].(*mcp.TextContent)
	if !strings.Contains(tc.Text, `"error":"tool_unavailable"`) {
		t.Fatalf("text=%q", tc.Text)
	}
	if !strings.Contains(tc.Text, `"message":"gone"`) {
		t.Fatalf("text=%q", tc.Text)
	}
}
```

- [ ] **Step 3: 写端到端集成测试**

`packages/api/mcpserver_test.go`（官方 go-sdk client 走真实`initialize→tools/list→tools/call`）：

```go
package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// exampleRegistry 注册一个专供测试的 connector（两个 managed tool），
// 不依赖真实 connectors 包的内容变化。
func exampleRegistry() *registry.Registry {
	r := registry.New()
	r.MustRegister(connector.Definition{
		Type:                "example_app",
		Name:                "Example App",
		ConfigSchemaVersion: 1,
		Tools: []connector.Tool{
			{
				ID:          "list_items",
				Name:        "List items",
				Description: "List all items",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
				Risk:        connector.RiskRead,
				Backend:     connector.ManagedBackend{HandlerKey: "list_items"},
			},
			{
				ID:          "create_item",
				Name:        "Create item",
				Description: "Create one item",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
				Risk:        connector.RiskWrite,
				Backend:     connector.ManagedBackend{HandlerKey: "create_item"},
			},
		},
	}, "list_items", "create_item")
	return r
}

// bearerTransport 给每个请求加 Authorization: Bearer <token>。
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

// connectMCP 用官方 go-sdk client 对 /mcp 走真实 initialize。
func connectMCP(t *testing.T, baseURL, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "plan5-test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   baseURL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { cs.Close() })
	return cs, nil
}

func mustConnectMCP(t *testing.T, baseURL, token string) *mcp.ClientSession {
	t.Helper()
	cs, err := connectMCP(t, baseURL, token)
	if err != nil {
		t.Fatalf("initialize 失败: %v", err)
	}
	return cs
}

// newMCPSession 直接用 sessions.Service 造 session（绕过 /v1，聚焦 /mcp 行为）。
func newMCPSession(t *testing.T, env *testEnv, bindings map[string]uuid.UUID, allowlist []string) string {
	t.Helper()
	token, err := env.Sessions.Create(context.Background(), bindings, allowlist, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMCPRequiresToken(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	res, err := http.Post(env.Server.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("error=%v", body["error"])
	}
}

func TestMCPToolsListAggregatesBindings(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	b := insertConnection(t, env.Pool, "example_app")
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a, "app-b": b}, nil)

	cs := mustConnectMCP(t, env.Server.URL, token)
	res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"app-a__create_item", "app-a__list_items", "app-b__create_item", "app-b__list_items"}
	if !slices.Equal(names, want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
	for _, tool := range res.Tools {
		if tool.Name != "app-a__list_items" {
			continue
		}
		if tool.Description != "List all items" {
			t.Fatalf("description=%q", tool.Description)
		}
		if tool.InputSchema == nil || tool.InputSchema.Properties["query"] == nil {
			t.Fatalf("InputSchema 未带上: %#v", tool.InputSchema)
		}
	}
}

func TestMCPAllowlistFiltersTools(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a},
		[]string{"app-a__list_items"})

	cs := mustConnectMCP(t, env.Server.URL, token)
	res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "app-a__list_items" {
		t.Fatalf("allowlist 过滤失败: %+v", res.Tools)
	}
}

func TestMCPCallToolExecutes(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	env.Exec.setResult(connector.ToolResultData{Text: "hello"}, nil)
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a}, nil)

	cs := mustConnectMCP(t, env.Server.URL, token)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "app-a__list_items",
		Arguments: map[string]any{"query": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("不应是错误: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok || tc.Text != "hello" {
		t.Fatalf("content=%#v", res.Content)
	}

	calls := env.Exec.recorded()
	if len(calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(calls))
	}
	if calls[0].ConnectionID != a || calls[0].ToolID != "list_items" {
		t.Fatalf("call=%+v", calls[0])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Args), &args); err != nil || args["query"] != "x" {
		t.Fatalf("args=%q", calls[0].Args)
	}
}

func TestMCPCallToolIsErrorPassthrough(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	env.Exec.setResult(connector.ToolResultData{Text: "boom", IsError: true}, nil)
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a}, nil)

	cs := mustConnectMCP(t, env.Server.URL, token)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "app-a__list_items"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("IsError 应透传为 true")
	}
}

func TestMCPCallUnboundAliasFails(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a}, nil)

	cs := mustConnectMCP(t, env.Server.URL, token)
	if _, err := cs.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "ghost__list_items"}); err == nil {
		t.Fatal("未绑定 alias 的调用应返回协议错误")
	}
	if len(env.Exec.recorded()) != 0 {
		t.Fatal("不应触达执行引擎")
	}
}

func TestMCPCallOutsideAllowlistFails(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a},
		[]string{"app-a__list_items"})

	cs := mustConnectMCP(t, env.Server.URL, token)
	if _, err := cs.CallTool(context.Background(),
		&mcp.CallToolParams{Name: "app-a__create_item"}); err == nil {
		t.Fatal("allowlist 之外的调用应返回协议错误")
	}
	if len(env.Exec.recorded()) != 0 {
		t.Fatal("不应触达执行引擎")
	}
}

func TestMCPCallDeletedToolReturnsToolUnavailable(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")

	// 模拟「session 建立时 Tool 存在、随后代码删除」：
	// allowlist 含 app-a__gone_tool，而当前 Definition 已无 gone_tool。
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a},
		[]string{"app-a__list_items", "app-a__gone_tool"})

	cs := mustConnectMCP(t, env.Server.URL, token)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "app-a__gone_tool"})
	if err != nil {
		t.Fatalf("应返回工具级错误而非协议错误: %v", err)
	}
	if !res.IsError {
		t.Fatal("应为 IsError 结果")
	}
	tc := res.Content[0].(*mcp.TextContent)
	if !strings.Contains(tc.Text, `"error":"tool_unavailable"`) {
		t.Fatalf("text=%q", tc.Text)
	}
	if len(env.Exec.recorded()) != 0 {
		t.Fatal("不应触达执行引擎")
	}
}

func TestMCPCallDeletedToolEmptyAllowlist(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a}, nil)

	cs := mustConnectMCP(t, env.Server.URL, token)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "app-a__gone_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("应为 IsError 结果")
	}
	tc := res.Content[0].(*mcp.TextContent)
	if !strings.Contains(tc.Text, `"error":"tool_unavailable"`) {
		t.Fatalf("text=%q", tc.Text)
	}
}

func TestMCPExpiredSessionRejected(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	a := insertConnection(t, env.Pool, "example_app")
	token := newMCPSession(t, env, map[string]uuid.UUID{"app-a": a}, nil)

	sum := sha256.Sum256([]byte(token))
	if _, err := env.Pool.Exec(context.Background(),
		`UPDATE mcp_sessions SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`,
		hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := connectMCP(t, env.Server.URL, token); err == nil {
		t.Fatal("过期 session 应无法完成 initialize")
	}
}

func TestMCPWrongTokenRejected(t *testing.T) {
	env := newTestEnv(t, exampleRegistry())
	if _, err := connectMCP(t, env.Server.URL, "deadbeef"); err == nil {
		t.Fatal("非法 token 应无法完成 initialize")
	}
}
```

- [ ] **Step 4: 运行确认失败**

```bash
cd packages/api && go test ./ -run 'TestToolResultToMCP|TestErrorResult' -v
```

预期：编译失败，`undefined: toolResultToMCP`、`undefined: errorResult`。

- [ ] **Step 5: 写实现**

`packages/api/mcpserver.go`：

```go
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sessionCtxKey struct{}

// mcpHost 把一次 HTTP 请求携带的 session token 变成一个只含该 session
// 可见工具的一次性 mcp.Server 实例。
type mcpHost struct {
	registry *registry.Registry
	sessions *sessions.Service
	exec     ToolExecutor
}

// registerMCP 在 Echo 上挂载聚合 /mcp 端点（Streamable HTTP，Stateless 模式）。
// Stateless 下 SDK 对每个 POST 调用 serverForRequest：每请求按 token 动态构建
// server，无进程内长驻状态；协议 session 生命周期由 SDK handler 管理。
func registerMCP(e *echo.Echo, deps Deps) {
	h := &mcpHost{registry: deps.Registry, sessions: deps.Sessions, exec: deps.Exec}
	handler := mcp.NewStreamableHTTPHandler(h.serverForRequest,
		&mcp.StreamableHTTPOptions{Stateless: true})
	e.Any("/mcp", echo.WrapHandler(handler), h.requireSession)
}

// requireSession 校验 Authorization: Bearer <session token>，
// 并把解析出的 SessionView 放进请求 context。每个请求都会重新校验。
func (h *mcpHost) requireSession(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "unauthorized", "message": "missing bearer session token",
			})
		}
		view, err := h.sessions.Resolve(c.Request().Context(), token)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "invalid_session", "message": "session token is invalid, expired or revoked",
			})
		}
		req := c.Request()
		c.SetRequest(req.WithContext(context.WithValue(req.Context(), sessionCtxKey{}, view)))
		return next(c)
	}
}

// serverForRequest 由 SDK handler 调用（Stateless 模式下每个 POST 一次）。
// 返回 nil 时 SDK 回 400。
func (h *mcpHost) serverForRequest(r *http.Request) *mcp.Server {
	view, ok := r.Context().Value(sessionCtxKey{}).(sessions.SessionView)
	if !ok {
		return nil // requireSession 未通过时不会到达；防御性兜底
	}
	srv, err := h.buildServer(r.Context(), view)
	if err != nil {
		return nil
	}
	return srv
}

// buildServer 按 session 绑定与 allowlist 构建一次性 MCP server。
func (h *mcpHost) buildServer(ctx context.Context, view sessions.SessionView) (*mcp.Server, error) {
	ids := make([]uuid.UUID, 0, len(view.Bindings))
	for _, id := range view.Bindings {
		ids = append(ids, id)
	}
	types, err := h.sessions.ConnectionConnectorTypes(ctx, ids)
	if err != nil {
		return nil, err
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "connect-it", Version: "0.1.0"}, nil)

	aliases := make([]string, 0, len(view.Bindings))
	for alias := range view.Bindings {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases) // 工具注册顺序稳定，便于测试与日志

	registered := map[string]bool{}
	for _, alias := range aliases {
		connID := view.Bindings[alias]
		ct, ok := types[connID]
		if !ok {
			continue // 连接已被删除：不暴露工具，调用走 tool_unavailable
		}
		def, ok := h.registry.Get(connector.Type(ct))
		if !ok {
			continue // definition_missing：同上
		}
		for _, tool := range def.Tools {
			exposed := alias + "__" + tool.ID
			if len(view.Allowlist) > 0 && !view.Allowlist[exposed] {
				continue
			}
			schema := &jsonschema.Schema{Type: "object"}
			if len(tool.InputSchema) > 0 {
				schema = new(jsonschema.Schema)
				if err := json.Unmarshal(tool.InputSchema, schema); err != nil {
					return nil, fmt.Errorf("connector %s tool %s: invalid input schema: %w", ct, tool.ID, err)
				}
			}
			srv.AddTool(&mcp.Tool{
				Name:        exposed,
				Description: tool.Description,
				InputSchema: schema,
			}, h.callHandler(connID, tool.ID))
			registered[exposed] = true
		}
	}

	srv.AddReceivingMiddleware(unavailableToolMiddleware(view, registered))
	return srv, nil
}

// callHandler 返回一个绑定了（connection, tool）的 tools/call 处理函数。
// go-sdk 低阶 ToolHandler 返回 error 会成为协议错误，因此业务失败一律
// 以 CallToolResult{IsError: true} 返回。
func (h *mcpHost) callHandler(connID uuid.UUID, toolID string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		data, err := h.exec.Execute(ctx, connID, toolID, args)
		if err != nil {
			return errorResult("execution_failed", err.Error()), nil
		}
		return toolResultToMCP(data), nil
	}
}

// unavailableToolMiddleware 拦截 tools/call：目标名在 session 语义下本应合法
// （alias 已绑定且通过 allowlist），但当前 Definition 已无此 Tool——返回
// tool_unavailable（spec §9「删除 Tool」规则）。其余未注册名字放行给 SDK，
// 得到标准的协议级 unknown tool 错误。
func unavailableToolMiddleware(view sessions.SessionView, registered map[string]bool) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if !ok {
				return next(ctx, method, req)
			}
			name := params.Name
			if registered[name] {
				return next(ctx, method, req)
			}
			alias, _, ok := sessions.SplitExposedName(name)
			if !ok {
				return next(ctx, method, req)
			}
			if _, bound := view.Bindings[alias]; !bound {
				return next(ctx, method, req)
			}
			if len(view.Allowlist) > 0 && !view.Allowlist[name] {
				return next(ctx, method, req)
			}
			return errorResult("tool_unavailable",
				"tool "+name+" is not available in the current definition"), nil
		}
	}
}

// toolResultToMCP 把统一执行结果转成 MCP CallToolResult，IsError 透传。
// 若计划 4 落地的 ToolResultData 字段与此不同，只需调整本函数（见适配说明第 1 条）。
func toolResultToMCP(data connector.ToolResultData) *mcp.CallToolResult {
	res := &mcp.CallToolResult{
		IsError: data.IsError,
		Content: []mcp.Content{&mcp.TextContent{Text: data.Text}},
	}
	if len(data.Structured) > 0 {
		res.StructuredContent = json.RawMessage(data.Structured)
	}
	return res
}

// errorResult 构造统一错误形态的工具级错误结果，文本体沿用项目错误 JSON 约定。
func errorResult(code, message string) *mcp.CallToolResult {
	body, _ := json.Marshal(map[string]string{"error": code, "message": message})
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}
}
```

- [ ] **Step 6: 在 api.New 挂载 /mcp**

修改`packages/api/api.go`：在`api.New`内、`registerMCPSessions(v1, deps)`之后追加一行（`e`为 Echo 实例变量，变量名以实际为准）：

```go
	registerMCP(e, deps)
```

然后整理依赖：

```bash
cd packages/api && go mod tidy && go build ./...
```

预期：`go.mod`中`github.com/google/jsonschema-go`提升为直接依赖；编译通过。

- [ ] **Step 7: 无数据库运行（单元过、集成 skip）**

```bash
cd packages/api && go test ./ -v
```

预期：`TestToolResultToMCP`／`TestToolResultToMCPWithoutStructured`／`TestErrorResult`PASS；`TestMCP*`与`TestCreateMCPSession*`全部 SKIP。

- [ ] **Step 8: 带数据库运行全部通过**

```bash
cd packages/api && TEST_DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it?sslmode=disable" go test ./ -v
```

预期：全部用例 PASS（含 Task 2 的三个用例）。若`TestMCPToolsListAggregatesBindings`因 SDK 对`tools/list`排序不同而挂，测试端已自行`sort.Strings`，不应受影响；若`initialize`失败，先核对 401 还是 400（401 查 token 注入，400 查`serverForRequest`返回 nil 的分支）。

- [ ] **Step 9: 全仓库验证与提交**

```bash
mise run test
mise run vet
git add packages/api
git commit -m "feat(api): add aggregated /mcp streamable http endpoint"
```

预期：`mise run test`各 module 全绿（无`TEST_DATABASE_URL`时集成用例 SKIP 也算绿）。

---

## 完成标准（对照 spec）

- [ ] `POST /v1/mcp-sessions`满足契约：`RequireAPIToken`鉴权；body`{"connections":…,"tool_allowlist":…,"ttl_seconds":…}`；`201 {"token","expires_at"}`（spec §11、§12）；
- [ ] token 为 256bit 随机 hex，库中只存 sha256；ttl 默认 1h、上限 24h；alias 正则与 connection 存在性校验（spec §11）；
- [ ] `/mcp`为官方 go-sdk Streamable HTTP，session token 鉴权；工具名`{alias}__{tool_id}`按第一个`__`可逆解析（spec §11）；
- [ ] `tools/list`带 Description 与 InputSchema，allowlist 非空时过滤；空 allowlist 允许全部绑定连接的全部 tool；
- [ ] Session 无法访问未绑定的 Connection 或 allowlist 之外的 Tool（spec §17），有测试用例；
- [ ] 删除的 Tool 在 call 时返回`tool_unavailable`（spec §9），有测试用例；
- [ ] `tools/call`经`ToolExecutor.Execute`执行，`ToolResultData`转`CallToolResult`且`IsError`透传；
- [ ] 每个 MCP 请求按 session token 动态构建 server 实例（Stateless），无进程内长驻状态；
- [ ] `Deps.Exec`为`ToolExecutor`接口，`main.go`传`*exec.Engine`保持兼容；`Deps`新增`Sessions *sessions.Service`并在`main.go`接线；
- [ ] 过期／吊销 session 被`Resolve`拒绝并导致`/mcp`401，有测试用例；
- [ ] 集成测试遵守`TEST_DATABASE_URL`未设置即`t.Skip`的约定；`mise run test`与`mise run vet`全绿。




