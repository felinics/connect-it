# connect-it 计划 2：数据库层与 API 骨架

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立`packages/service`（migrations＋sqlc store＋configsvc／authsvc／catalogsvc）与`packages/api`（Echo 路由、门禁中间件、cmd/connect-it），交付一个能启动、能登录、能配置 Connector、能被内部应用查询 catalog 的最小服务。

**Architecture:** service module 承载数据访问与业务规则：migrations 经`go:embed`编译进二进制并在启动时执行；sqlc 从 SQL 生成 store 包；configsvc／authsvc／catalogsvc 三个业务包分别负责配置（校验＋AES-GCM 加密＋乐观并发）、门禁（argon2id 密码＋sha256 token）与 catalog 状态合并。api module 只做 HTTP 装配：`api.New(deps)`挂路由与中间件，业务错误统一映射为`{"error","message"}`格式，`cmd/connect-it`按「读环境变量→pgxpool→MigrateUp→RegisterAll→EnsureAdminFromEnv→api.New→启动」的顺序组装。

**Tech Stack:** Go 1.25、Echo v4（`github.com/labstack/echo/v4`）、pgx v5（`github.com/jackc/pgx/v5`，pgxpool）、sqlc 1.29（engine postgresql、sql_package pgx/v5）、golang-migrate v4（source 用 iofs＋go:embed）、`github.com/google/uuid`、`golang.org/x/crypto/argon2`、postgres:17（docker，host 端口 5433）。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 3、4、7、8、9、12 节是本计划的依据）

**前置：** 计划 1（`docs/superpowers/plans/2026-07-22-plan-1-foundation.md`）已完成：core module 的`connector`／`registry`／`crypto`／`status`包、connectors module 骨架（`connectors.RegisterAll`）、mise.toml 的`test`／`vet`任务均已存在，接口签名以计划 1 文中代码为准。

## Global Constraints

- module 路径固定`github.com/memohai/connect-it/packages/<name>`；无 go.work；每个 go.mod 用`replace`按相对路径列出**全部**本地依赖（含间接）：`service`replace `core`、`connectors`；`api`replace `core`、`connectors`、`service`。
- 依赖方向单向：`service→core＋connectors`；`api→core＋connectors＋service`。禁止反向 import。
- 所有`go`命令必须`cd`进对应 module 目录执行。
- API JSON 一律 snake_case key；错误响应统一`{"error":"machine_code","message":"人类可读说明"}`。
- Secret 永不回显值：配置 GET 只返回`secret_keys_set`（key 列表）；API token 明文只在创建响应出现一次，库中只存 sha256。
- Secret 加密用 core 的`crypto.Keyring`（AES-256-GCM）；`connector_configs`的 AAD＝connector_type 字符串。
- 数据库表列定义照抄 spec §7；不对代码 Registry 建外键；未知 type 的行保留。
- 集成测试统一模式：读`TEST_DATABASE_URL`，未设置则`t.Skip`；每个用例创建随机 schema（`test_<16hex>`）、在其中跑全量 migration、结束后`DROP SCHEMA … CASCADE`（用例间与并行包之间完全隔离）。
- 环境变量（cmd/connect-it 读取）：`DATABASE_URL`；`CONNECT_IT_SECRET_KEY`（值格式`1:<64hex>,2:<64hex>`，`crypto.ParseKeyring`解析）；`COOKIE_SECRET`；`CONNECT_IT_ADMIN_PASSWORD`（首次启动 seed，用户名固定`admin`）；`LISTEN_ADDR`（默认`:8080`）。`CONNECT_IT_BASE_URL`属计划 3，本计划不读取。
- 管理端会话 cookie：名`connect_it_admin`，HMAC-SHA256 签名，24h 过期，HttpOnly。
- 提交信息用 conventional commits（feat:／test:／chore:），每个 task 至少一次提交。

## 本计划不做（防越界）

- OAuth、connections、token 刷新（计划 3）；Tool 执行、`mcp:verify`、connector_health 写入（计划 4）；`POST /v1/mcp-sessions`与`/mcp`（计划 5）；四个 connector 的完整 Definition（计划 6）；前端（计划 7）；Docker／CI（计划 8）。
- 相关数据库表（connections、oauth_authorizations、mcp_sessions、mcp_session_connections、tool_runs）本计划**建表但不读写**；connector_health 只读（无行按零值）。

---

### Task 1: service module、migrations 与 MigrateUp

**Files:**
- Modify: `mise.toml`（追加 db-up／db-down 任务）
- Create: `packages/service/go.mod`
- Create: `packages/service/migrations/0001_init.up.sql`
- Create: `packages/service/migrations/0001_init.down.sql`
- Create: `packages/service/migrate.go`
- Create: `packages/service/testutil/db.go`
- Test: `packages/service/migrate_test.go`

**Interfaces:**
- Consumes: 无（service module 的第一个 task）
- Produces:
  - `service.MigrateUp(databaseURL string) error`（cmd/connect-it 启动时与所有集成测试使用）
  - `testutil.NewDB(t *testing.T) *pgxpool.Pool`（Task 2／4／5／6／8 的集成测试用：skip／随机 schema／migration／清理一站式完成）
  - `testutil.WithSearchPath(databaseURL, schema string) (string, error)`
  - 全部 9 张表的 schema（Task 2 的 sqlc 以`migrations/`为 schema 来源）

- [ ] **Step 1: mise.toml 追加 db-up／db-down 任务**

在现有`mise.toml`末尾追加（不改动已有的`[tools]`与`test`／`vet`任务）：

```toml
[tasks.db-up]
description = "启动本地测试 postgres:17（host 端口 5433）"
run = '''
docker run -d --name connect-it-pg \
  -e POSTGRES_PASSWORD=postgres \
  -e POSTGRES_DB=connect_it \
  -p 5433:5432 \
  postgres:17
until docker exec connect-it-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 0.5; done
echo "postgres ready on :5433"
'''

[tasks.db-down]
description = "停止并删除本地测试 postgres"
run = "docker rm -f connect-it-pg"
```

启动数据库并导出集成测试要用的环境变量（后续所有测试步骤都假设已执行）：

```bash
mise run db-up
export TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5433/connect_it?sslmode=disable'
```

- [ ] **Step 2: 初始化 service module**

```bash
mkdir -p packages/service/migrations packages/service/testutil
```

写入`packages/service/go.mod`：

```text
module github.com/memohai/connect-it/packages/service

go 1.25

require github.com/memohai/connect-it/packages/core v0.0.0

replace github.com/memohai/connect-it/packages/core => ../core

replace github.com/memohai/connect-it/packages/connectors => ../connectors
```

说明：service 的 Go 代码不 import connectors，`go mod tidy`不会为它保留 require 行，但按契约 replace 必须全量列出本地依赖，replace 行无 require 也合法且保留。

- [ ] **Step 3: 写失败测试**

`packages/service/migrate_test.go`（包内测试。不用 testutil——testutil 依赖本包，避免 import 循环，schema 辅助函数在此内联一份）：

```go
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// newTestSchema 创建随机 schema，返回带 search_path 的数据库 URL。
// 未设置 TEST_DATABASE_URL 时 t.Skip。
func newTestSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	ctx := context.Background()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(buf[:])
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("连接 TEST_DATABASE_URL: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("创建 schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

var wantTables = []string{
	"admin_account", "api_tokens", "connector_configs", "connections",
	"oauth_authorizations", "mcp_sessions", "mcp_session_connections",
	"connector_health", "tool_runs",
}

func tableExists(t *testing.T, dbURL, table string) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestMigrateUpCreatesAllTables(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, tbl := range wantTables {
		if !tableExists(t, dbURL, tbl) {
			t.Errorf("表 %s 未创建", tbl)
		}
	}
}

func TestMigrateUpIdempotent(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatal(err)
	}
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("第二次 MigrateUp 应为 no-op: %v", err)
	}
}

func TestMigrateDownDropsAllTables(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatal(err)
	}
	m, db, err := newMigrator(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := m.Down(); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	for _, tbl := range wantTables {
		if tableExists(t, dbURL, tbl) {
			t.Errorf("表 %s 未被 down migration 删除", tbl)
		}
	}
}
```

- [ ] **Step 4: 运行确认失败**

```bash
cd packages/service && go mod tidy && go test ./...
```

预期：编译失败，`undefined: MigrateUp`、`undefined: newMigrator`。

- [ ] **Step 5: 写 migration SQL（列定义照抄 spec §7）**

`packages/service/migrations/0001_init.up.sql`：

```sql
create table admin_account (
  id integer primary key check (id = 1),
  username text not null,
  password_hash text not null,
  updated_at timestamptz not null
);

create table api_tokens (
  id uuid primary key,
  name text not null,
  token_hash text not null unique,
  created_at timestamptz not null,
  revoked_at timestamptz
);

create table connector_configs (
  connector_type text primary key,
  config_schema_version integer not null,
  public_config jsonb not null default '{}',
  secret_config bytea not null,
  secret_key_version integer not null,
  mcp_verified_endpoint text,
  mcp_verified_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table connections (
  id uuid primary key,
  connector_type text not null,
  alias text not null unique,
  auth_method text not null,
  credential bytea not null,
  secret_key_version integer not null,
  profile jsonb not null default '{}',
  scopes text[] not null default '{}',
  status text not null,
  access_token_expires_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table oauth_authorizations (
  id uuid primary key,
  connector_type text not null,
  state_hash text not null unique,
  pkce_verifier bytea not null,
  connection_id uuid,
  status text not null,
  expires_at timestamptz not null,
  created_at timestamptz not null
);

create table mcp_sessions (
  id uuid primary key,
  token_hash text not null unique,
  tool_allowlist jsonb not null,
  status text not null,
  expires_at timestamptz not null,
  created_at timestamptz not null
);

create table mcp_session_connections (
  session_id uuid not null references mcp_sessions(id) on delete cascade,
  alias text not null,
  connection_id uuid not null,
  primary key (session_id, alias)
);

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
```

`packages/service/migrations/0001_init.down.sql`（按依赖逆序删除）：

```sql
drop table tool_runs;
drop table connector_health;
drop table mcp_session_connections;
drop table mcp_sessions;
drop table oauth_authorizations;
drop table connections;
drop table connector_configs;
drop table api_tokens;
drop table admin_account;
```

- [ ] **Step 6: 写 MigrateUp 实现**

`packages/service/migrate.go`：

```go
// Package service 是数据库层与业务服务的宿主 module 根包，
// 负责 schema migration：migrations/ 经 go:embed 编译进二进制，启动时执行。
package service

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrateUp 把数据库升级到最新版本；已在最新版本时不做任何事。
func MigrateUp(databaseURL string) error {
	m, db, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// newMigrator 用 pgx stdlib 打开连接（URL 中的 search_path 等运行时参数生效），
// schema_migrations 表建在 CURRENT_SCHEMA 下，因此测试的随机 schema 天然隔离。
func newMigrator(databaseURL string) (*migrate.Migrate, *sql.DB, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, nil, fmt.Errorf("migrate source: %w", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate init: %w", err)
	}
	return m, db, nil
}
```

- [ ] **Step 7: 运行确认通过**

```bash
cd packages/service && go mod tidy && go test ./...
```

预期：三个测试全部 PASS（未设置`TEST_DATABASE_URL`时显示 SKIP——本计划验收要求在已`mise run db-up`并导出变量的前提下 PASS）。

- [ ] **Step 8: 写 testutil（后续 task 的集成测试基座）**

`packages/service/testutil/db.go`：

```go
// Package testutil 为集成测试提供隔离的数据库环境。
// 约定：读 TEST_DATABASE_URL，未设置则 t.Skip；
// 每个测试创建随机 schema、在其中跑全量 migration，结束后 DROP SCHEMA CASCADE。
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	service "github.com/memohai/connect-it/packages/service"
)

// NewDB 返回一个连接到独立随机 schema、已跑完全部 migration 的连接池。
// 清理（DROP SCHEMA、关闭连接）通过 t.Cleanup 自动完成。
func NewDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	ctx := context.Background()

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(buf[:])

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("连接 TEST_DATABASE_URL: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("创建 schema: %v", err)
	}

	schemaURL, err := WithSearchPath(base, schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MigrateUp(schemaURL); err != nil {
		t.Fatalf("migration 失败: %v", err)
	}
	pool, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatalf("连接测试 schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

// WithSearchPath 给数据库 URL 追加 search_path 参数（pgx 将其作为运行时参数下发）。
func WithSearchPath(databaseURL, schema string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("解析数据库 URL: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
```

testutil 本身不写单测（纯测试基座），由 Task 2 起的所有集成测试实际使用来验证；本步先保证编译：

```bash
cd packages/service && go mod tidy && go vet ./...
```

预期：无输出，退出码 0。

- [ ] **Step 9: 提交**

```bash
git add mise.toml packages/service
git commit -m "feat(service): add migrations, MigrateUp and integration test scaffolding"
```

---

### Task 2: sqlc 配置、queries 与 store 生成

**Files:**
- Modify: `mise.toml`（`[tools]`追加 sqlc，`[tasks]`追加 sqlc 任务）
- Create: `packages/service/sqlc.yaml`
- Create: `packages/service/store/queries/configs.sql`
- Create: `packages/service/store/queries/admin_account.sql`
- Create: `packages/service/store/queries/api_tokens.sql`
- Create: `packages/service/store/queries/connector_health.sql`
- Create: `packages/service/store/`（`db.go`／`models.go`／`*.sql.go`由`sqlc generate`产出，**不手写、不在本计划抄写全文**）
- Test: `packages/service/store/store_test.go`（手写集成测试）

**Interfaces:**
- Consumes: Task 1 的 migrations（sqlc 的 schema 来源）、`testutil.NewDB`
- Produces: `store`包，Task 3–8 依赖。生成代码的预期签名（sqlc 按下方 SQL 生成，实现者核对即可，不得手改生成文件）：
  - `store.New(db store.DBTX) *store.Queries`；`store.DBTX`接口含`Exec`／`Query`／`QueryRow`（pgx v5 形态），`*pgxpool.Pool`满足该接口
  - 模型（overrides 后）：
    - `store.ConnectorConfig{ConnectorType string; ConfigSchemaVersion int32; PublicConfig []byte; SecretConfig []byte; SecretKeyVersion int32; McpVerifiedEndpoint *string; McpVerifiedAt *time.Time; CreatedAt time.Time; UpdatedAt time.Time}`
    - `store.AdminAccount{ID int32; Username string; PasswordHash string; UpdatedAt time.Time}`
    - `store.ApiToken{ID uuid.UUID; Name string; TokenHash string; CreatedAt time.Time; RevokedAt *time.Time}`（注意 sqlc 驼峰化为`ApiToken`而非`APIToken`）
    - `store.ConnectorHealth{ConnectorType string; LastOkAt *time.Time; LastErrorAt *time.Time; ConsecutiveFailures int32; LastError *string}`
  - 方法：
    - `GetConnectorConfig(ctx context.Context, connectorType string) (ConnectorConfig, error)`
    - `UpsertConnectorConfig(ctx context.Context, arg UpsertConnectorConfigParams) (ConnectorConfig, error)`，Params 字段`{ConnectorType string; ConfigSchemaVersion int32; PublicConfig []byte; SecretConfig []byte; SecretKeyVersion int32}`
    - `UpdateConnectorConfigIfMatch(ctx context.Context, arg UpdateConnectorConfigIfMatchParams) (ConnectorConfig, error)`，Params 比 Upsert 多`UpdatedAt time.Time`
    - `DeleteConnectorConfig(ctx context.Context, connectorType string) (int64, error)`
    - `GetAdminAccount(ctx context.Context) (AdminAccount, error)`
    - `InsertAdminAccountIfAbsent(ctx context.Context, arg InsertAdminAccountIfAbsentParams) (int64, error)`，Params 字段`{Username string; PasswordHash string}`
    - `UpdateAdminPassword(ctx context.Context, passwordHash string) (int64, error)`
    - `InsertAPIToken(ctx context.Context, arg InsertAPITokenParams) error`，Params 字段`{ID uuid.UUID; Name string; TokenHash string}`
    - `GetAPITokenByHash(ctx context.Context, tokenHash string) (ApiToken, error)`
    - `ListAPITokens(ctx context.Context) ([]ApiToken, error)`
    - `RevokeAPIToken(ctx context.Context, id uuid.UUID) (int64, error)`
    - `GetConnectorHealth(ctx context.Context, connectorType string) (ConnectorHealth, error)`
  - 查不到行时错误为`pgx.ErrNoRows`（调用方用`errors.Is`判断）

- [ ] **Step 1: 写失败测试**

`packages/service/store/store_test.go`：

```go
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func TestConnectorConfigCRUD(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	first, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "github",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{"client_id":"abc"}`),
		SecretConfig:        []byte{1, 2, 3},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt.IsZero() || first.CreatedAt.IsZero() {
		t.Fatal("created_at/updated_at 未写入")
	}

	got, err := q.GetConnectorConfig(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	var pub map[string]any
	if err := json.Unmarshal(got.PublicConfig, &pub); err != nil {
		t.Fatal(err)
	}
	if pub["client_id"] != "abc" || got.SecretKeyVersion != 1 {
		t.Fatalf("roundtrip 失败: %+v", got)
	}
	if got.McpVerifiedAt != nil || got.McpVerifiedEndpoint != nil {
		t.Fatal("verify 字段应为 NULL")
	}

	// upsert 更新已有行
	second, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "github",
		ConfigSchemaVersion: 2,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigSchemaVersion != 2 || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("upsert 应更新版本且保留 created_at: %+v", second)
	}

	n, err := q.DeleteConnectorConfig(ctx, "github")
	if err != nil || n != 1 {
		t.Fatalf("删除应影响 1 行: n=%d err=%v", n, err)
	}
	if n, _ := q.DeleteConnectorConfig(ctx, "github"); n != 0 {
		t.Fatal("重复删除应影响 0 行")
	}
	if _, err := q.GetConnectorConfig(ctx, "github"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("删除后应 ErrNoRows, got %v", err)
	}
}

func TestUpdateConnectorConfigIfMatch(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	row, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{"a":"b"}`),
		SecretConfig:        []byte{9},
		SecretKeyVersion:    1,
		UpdatedAt:           row.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("匹配的 updated_at 应更新成功: %v", err)
	}
	if updated.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatal("updated_at 应被推进")
	}

	// 用过期的 updated_at 再更新 → ErrNoRows
	_, err = q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
		UpdatedAt:           row.UpdatedAt,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("过期 If-Match 应 ErrNoRows, got %v", err)
	}
}

func TestAdminAccountQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	if _, err := q.GetAdminAccount(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("空表应 ErrNoRows, got %v", err)
	}
	n, err := q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: "hash1",
	})
	if err != nil || n != 1 {
		t.Fatalf("首次插入应影响 1 行: n=%d err=%v", n, err)
	}
	n, err = q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: "hash2",
	})
	if err != nil || n != 0 {
		t.Fatalf("已存在时应影响 0 行: n=%d err=%v", n, err)
	}
	acct, err := q.GetAdminAccount(ctx)
	if err != nil || acct.PasswordHash != "hash1" || acct.ID != 1 {
		t.Fatalf("已有账号不应被覆盖: %+v err=%v", acct, err)
	}
	if n, _ := q.UpdateAdminPassword(ctx, "hash3"); n != 1 {
		t.Fatal("改密应影响 1 行")
	}
	acct, _ = q.GetAdminAccount(ctx)
	if acct.PasswordHash != "hash3" {
		t.Fatalf("密码未更新: %+v", acct)
	}
}

func TestAPITokenQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	id := uuid.New()
	if err := q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: id, Name: "ci", TokenHash: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := q.GetAPITokenByHash(ctx, "deadbeef")
	if err != nil || tok.ID != id || tok.RevokedAt != nil {
		t.Fatalf("查询失败: %+v err=%v", tok, err)
	}
	list, err := q.ListAPITokens(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list 应有 1 行: %v err=%v", list, err)
	}
	if n, _ := q.RevokeAPIToken(ctx, id); n != 1 {
		t.Fatal("撤销应影响 1 行")
	}
	if _, err := q.GetAPITokenByHash(ctx, "deadbeef"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("已撤销 token 不应命中: %v", err)
	}
	if n, _ := q.RevokeAPIToken(ctx, id); n != 0 {
		t.Fatal("重复撤销应影响 0 行")
	}
}

func TestGetConnectorHealthNoRows(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	if _, err := q.GetConnectorHealth(context.Background(), "github"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("无行应 ErrNoRows, got %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/service && go test ./store/
```

预期：编译失败，`no required module provides package …/service/store`（store 包还不存在）。

- [ ] **Step 3: 写 sqlc.yaml**

`packages/service/sqlc.yaml`（sqlc 能识别 golang-migrate 命名，自动忽略`*.down.sql`）：

```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "migrations/"
    queries: "store/queries/"
    gen:
      go:
        package: "store"
        out: "store"
        sql_package: "pgx/v5"
        emit_pointers_for_null_types: true
        overrides:
          - db_type: "uuid"
            go_type:
              import: "github.com/google/uuid"
              type: "UUID"
          - db_type: "uuid"
            nullable: true
            go_type:
              import: "github.com/google/uuid"
              type: "UUID"
              pointer: true
          - db_type: "timestamptz"
            go_type:
              import: "time"
              type: "Time"
          - db_type: "timestamptz"
            nullable: true
            go_type:
              import: "time"
              type: "Time"
              pointer: true
```

- [ ] **Step 4: 写 queries SQL**

`packages/service/store/queries/configs.sql`：

```sql
-- name: GetConnectorConfig :one
SELECT * FROM connector_configs WHERE connector_type = $1;

-- name: UpsertConnectorConfig :one
INSERT INTO connector_configs (
  connector_type, config_schema_version, public_config, secret_config,
  secret_key_version, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, now(), now())
ON CONFLICT (connector_type) DO UPDATE SET
  config_schema_version = EXCLUDED.config_schema_version,
  public_config = EXCLUDED.public_config,
  secret_config = EXCLUDED.secret_config,
  secret_key_version = EXCLUDED.secret_key_version,
  updated_at = now()
RETURNING *;

-- name: UpdateConnectorConfigIfMatch :one
UPDATE connector_configs SET
  config_schema_version = $2,
  public_config = $3,
  secret_config = $4,
  secret_key_version = $5,
  updated_at = now()
WHERE connector_type = $1 AND updated_at = $6
RETURNING *;

-- name: DeleteConnectorConfig :execrows
DELETE FROM connector_configs WHERE connector_type = $1;
```

`packages/service/store/queries/admin_account.sql`：

```sql
-- name: GetAdminAccount :one
SELECT * FROM admin_account WHERE id = 1;

-- name: InsertAdminAccountIfAbsent :execrows
INSERT INTO admin_account (id, username, password_hash, updated_at)
VALUES (1, $1, $2, now())
ON CONFLICT (id) DO NOTHING;

-- name: UpdateAdminPassword :execrows
UPDATE admin_account SET password_hash = $1, updated_at = now() WHERE id = 1;
```

`packages/service/store/queries/api_tokens.sql`：

```sql
-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, name, token_hash, created_at)
VALUES ($1, $2, $3, now());

-- name: GetAPITokenByHash :one
SELECT * FROM api_tokens WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: ListAPITokens :many
SELECT * FROM api_tokens ORDER BY created_at DESC;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;
```

`packages/service/store/queries/connector_health.sql`：

```sql
-- name: GetConnectorHealth :one
SELECT * FROM connector_health WHERE connector_type = $1;
```

- [ ] **Step 5: mise 增加 sqlc 工具与任务，生成代码**

`mise.toml`的`[tools]`段追加一行（放在现有 go／node／pnpm 之后）：

```toml
sqlc = "1.29.0"
```

`[tasks]`追加：

```toml
[tasks.sqlc]
description = "生成 sqlc store 代码"
run = "cd packages/service && sqlc generate"
```

执行：

```bash
mise install
mise run sqlc
```

预期：`packages/service/store/`下生成`db.go`、`models.go`、`configs.sql.go`、`admin_account.sql.go`、`api_tokens.sql.go`、`connector_health.sql.go`。生成后核对 Interfaces 中列出的签名逐一存在（`grep "func (q \*Queries)" packages/service/store/*.sql.go`）；如有出入，修 SQL 或 sqlc.yaml 后重新生成，**不得手改生成文件**。

- [ ] **Step 6: 运行确认通过**

```bash
cd packages/service && go mod tidy && go test ./...
```

预期：`migrate_test.go`与`store/store_test.go`全部 PASS。

- [ ] **Step 7: 提交**

```bash
git add mise.toml packages/service/sqlc.yaml packages/service/store packages/service/go.mod packages/service/go.sum
git commit -m "feat(service): add sqlc store with configs, admin, tokens and health queries"
```

---

### Task 3: configsvc 骨架与 Validate

**Files:**
- Create: `packages/service/configsvc/configsvc.go`
- Create: `packages/service/configsvc/validate.go`
- Test: `packages/service/configsvc/validate_test.go`

**Interfaces:**
- Consumes: 计划 1 的`connector.ConfigField`／`connector.Definition`／`registry.Registry`、Task 2 的`store.Queries`、计划 1 的`crypto.Keyring`
- Produces（Task 4／8 依赖）:
  - `configsvc.Service`；`configsvc.New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service`
  - `configsvc.ConfigView{ConnectorType string; SchemaVersion int; Public map[string]any; SecretKeysSet []string; UpdatedAt time.Time}`
  - 错误：`ErrUnknownConnector`／`ErrNotFound`／`ErrConflict`／`ErrIncompatible`、`*ValidationError{Field, Reason}`
  - `(*Service).Validate(t connector.Type, public map[string]any, secrets map[string]string) error`

- [ ] **Step 1: 写失败测试**

`packages/service/configsvc/validate_test.go`（纯单元测试，不需要数据库）：

```go
package configsvc_test

import (
	"errors"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
)

// newValidateService 只装 registry，store 与 keyring 传 nil（Validate 不用它们）。
func newValidateService(t *testing.T) *configsvc.Service {
	t.Helper()
	r := registry.New()
	r.MustRegister(connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
			{Key: "region", Label: "Region", InputType: connector.InputSelect,
				Validation: connector.FieldValidation{Options: []string{"us", "eu"}}},
			{Key: "project_id", Label: "Project ID", InputType: connector.InputText,
				Validation: connector.FieldValidation{Pattern: `^[0-9]+$`}},
			{Key: "api_key", Label: "API Key", InputType: connector.InputText, Secret: true},
		},
	})
	return configsvc.New(nil, r, nil)
}

func TestValidateOK(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate("example_app",
		map[string]any{"client_id": "abc", "region": "eu", "project_id": "123"},
		map[string]string{"client_secret": "shh", "api_key": "k"})
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
}

func TestValidateUnknownConnector(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate("nope", nil, nil)
	if !errors.Is(err, configsvc.ErrUnknownConnector) {
		t.Fatalf("want ErrUnknownConnector, got %v", err)
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name      string
		public    map[string]any
		secrets   map[string]string
		wantField string
	}{
		{"未知公开字段", map[string]any{"client_id": "a", "bogus": "x"},
			map[string]string{"client_secret": "s"}, "bogus"},
		{"Secret 字段放进 public", map[string]any{"client_id": "a", "client_secret": "leak"},
			map[string]string{"client_secret": "s"}, "client_secret"},
		{"未知 Secret 字段", map[string]any{"client_id": "a"},
			map[string]string{"client_secret": "s", "bogus": "x"}, "bogus"},
		{"公开字段值不是字符串", map[string]any{"client_id": 42},
			map[string]string{"client_secret": "s"}, "client_id"},
		{"缺必填公开字段", map[string]any{},
			map[string]string{"client_secret": "s"}, "client_id"},
		{"缺必填 Secret 字段", map[string]any{"client_id": "a"},
			map[string]string{}, "client_secret"},
		{"必填 Secret 传空串视为缺失", map[string]any{"client_id": "a"},
			map[string]string{"client_secret": ""}, "client_secret"},
		{"Pattern 不匹配", map[string]any{"client_id": "a", "project_id": "abc"},
			map[string]string{"client_secret": "s"}, "project_id"},
		{"不在 Options 内", map[string]any{"client_id": "a", "region": "cn"},
			map[string]string{"client_secret": "s"}, "region"},
	}
	s := newValidateService(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate("example_app", tc.public, tc.secrets)
			var ve *configsvc.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if ve.Field != tc.wantField {
				t.Fatalf("错误字段 %q, want %q（reason=%s）", ve.Field, tc.wantField, ve.Reason)
			}
		})
	}
}

func TestValidateOptionalSecretMayBeEmpty(t *testing.T) {
	// 可选 Secret 传空串表示删除，应通过校验。
	s := newValidateService(t)
	err := s.Validate("example_app",
		map[string]any{"client_id": "a"},
		map[string]string{"client_secret": "s", "api_key": ""})
	if err != nil {
		t.Fatalf("可选 Secret 空串应通过: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/service && go test ./configsvc/
```

预期：编译失败，`undefined: configsvc.New`。

- [ ] **Step 3: 写骨架实现**

`packages/service/configsvc/configsvc.go`：

```go
// Package configsvc 管理 connector_configs：校验、AES-GCM 加密存储、
// If-Match 乐观并发与运行时读取合并。
package configsvc

import (
	"errors"
	"fmt"
	"time"

	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/store"
)

var (
	// ErrUnknownConnector：connector_type 不在代码 Registry 中。
	ErrUnknownConnector = errors.New("unknown connector type")
	// ErrNotFound：connector_configs 无该行。
	ErrNotFound = errors.New("config not found")
	// ErrConflict：If-Match 与 updated_at 不一致。
	ErrConflict = errors.New("config conflict")
	// ErrIncompatible：数据库 config_schema_version 比代码新，拒绝覆盖写。
	ErrIncompatible = errors.New("config schema newer than code")
)

// ValidationError 描述单个字段的校验失败。
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("字段 %q: %s", e.Field, e.Reason)
}

// ConfigView 是管理端可见的配置视图；Secret 只暴露已设置的 key 列表。
type ConfigView struct {
	ConnectorType string
	SchemaVersion int
	Public        map[string]any
	SecretKeysSet []string
	UpdatedAt     time.Time
}

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	kr  *crypto.Keyring
}

func New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service {
	return &Service{q: q, reg: reg, kr: kr}
}
```

`packages/service/configsvc/validate.go`：

```go
package configsvc

import (
	"fmt"
	"regexp"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Validate 按 Definition 的 ConfigFields 校验一份完整配置：
// 必填（有默认值的非 Secret 字段除外）、Pattern、Options，拒绝未知 key。
// 所有字段值都是字符串；secrets 中空串表示删除该 key（可选字段合法，
// 必填字段会命中必填检查）。
func (s *Service) Validate(t connector.Type, public map[string]any, secrets map[string]string) error {
	def, ok := s.reg.Get(t)
	if !ok {
		return ErrUnknownConnector
	}
	fields := map[string]connector.ConfigField{}
	for _, f := range def.ConfigFields {
		fields[f.Key] = f
	}

	for key, val := range public {
		f, known := fields[key]
		if !known || f.Secret {
			return &ValidationError{Field: key, Reason: "未知的公开配置字段"}
		}
		str, isStr := val.(string)
		if !isStr {
			return &ValidationError{Field: key, Reason: "值必须是字符串"}
		}
		if err := checkValue(f, str); err != nil {
			return err
		}
	}
	for key, val := range secrets {
		f, known := fields[key]
		if !known || !f.Secret {
			return &ValidationError{Field: key, Reason: "未知的 Secret 配置字段"}
		}
		if val == "" {
			continue // 空串＝删除，必填与否由下面的必填检查兜底
		}
		if err := checkValue(f, val); err != nil {
			return err
		}
	}
	for _, f := range def.ConfigFields {
		if !f.Required {
			continue
		}
		if f.Secret {
			if secrets[f.Key] == "" {
				return &ValidationError{Field: f.Key, Reason: "必填 Secret 字段缺失"}
			}
			continue
		}
		if f.DefaultValue != nil {
			continue // 默认值兜底，永不缺失
		}
		if v, _ := public[f.Key].(string); v == "" {
			return &ValidationError{Field: f.Key, Reason: "必填字段缺失"}
		}
	}
	return nil
}

func checkValue(f connector.ConfigField, val string) error {
	if f.Validation.Pattern != "" {
		re, err := regexp.Compile(f.Validation.Pattern)
		if err != nil {
			return fmt.Errorf("字段 %q 的 Pattern 非法: %w", f.Key, err)
		}
		if !re.MatchString(val) {
			return &ValidationError{Field: f.Key, Reason: "不匹配 Pattern " + f.Validation.Pattern}
		}
	}
	if len(f.Validation.Options) > 0 {
		for _, opt := range f.Validation.Options {
			if val == opt {
				return nil
			}
		}
		return &ValidationError{Field: f.Key, Reason: "不在可选值范围内"}
	}
	return nil
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/service && go test ./configsvc/
```

预期：全部 PASS（本 task 的测试不依赖数据库，无 SKIP）。

- [ ] **Step 5: 提交**

```bash
git add packages/service/configsvc
git commit -m "feat(service): add configsvc skeleton with field validation"
```

---

### Task 4: configsvc 完整读写（Get／Put／Delete／ConfigState／Resolved）

**Files:**
- Create: `packages/service/configsvc/rw.go`
- Create: `packages/service/configsvc/state.go`
- Test: `packages/service/configsvc/rw_test.go`（集成，`testutil.NewDB`）

**Interfaces:**
- Consumes: Task 2 的 store（`GetConnectorConfig`／`UpsertConnectorConfig`／`UpdateConnectorConfigIfMatch`／`DeleteConnectorConfig`）、Task 3 的骨架与`Validate`
- Produces（Task 6／7 与计划 3–5 依赖）:
  - `(*Service).Get(ctx context.Context, t connector.Type) (ConfigView, error)`
  - `(*Service).Put(ctx context.Context, t connector.Type, public map[string]any, secrets map[string]string, ifMatch time.Time) (ConfigView, error)`
  - `(*Service).Delete(ctx context.Context, t connector.Type) error`
  - `(*Service).ConfigState(ctx context.Context, t connector.Type) (status.ConfigState, error)`
  - `(*Service).Resolved(ctx context.Context, t connector.Type) (map[string]any, error)`

**语义（写实现时照此执行）：**

- `Put`：public 为**全量替换**；secrets 为**部分合并**——出现的 key 覆盖，值为空串表示删除该 key，未出现的保留原值。流程：Registry 查 def→读现有行（`pgx.ErrNoRows`＝新建；新建时 ifMatch 非零→`ErrConflict`）→行存在且`config_schema_version`>def 版本→`ErrIncompatible`→行存在且 ifMatch 非零且≠`updated_at`→`ErrConflict`→解密旧 secrets→合并新 secrets→按 def 字段清理 public 与 secrets 的未知 key（spec「删除字段下次保存清理」）→`Validate(t, public, merged)`→merged 序列化 JSON 加密（AAD=`[]byte(t)`，空 map 也加密，密文允许非空）→upsert（`config_schema_version`写 def 当前值）。返回新 `ConfigView`。
- `Resolved`：行不存在→仅 defaults；行存在→若行版本<def 版本，把 public／secrets 转成`map[string]any`后按`FromVersion`升序应用`def.ConfigUpgraders`（内存中，不落盘）→合并顺序 defaults→public→secrets。
- `ConfigState`：`ErrNoRows`→`{Exists:false}`；`MCPVerified = mcp_verified_at != nil && mcp_verified_endpoint != nil`；`SecretKeysSet`来自解密后的 keys；`PublicValues`原样。
- 解密助手`decryptSecrets(row, t)`：`len(secret_config)==0`→空 map（兼容手工置空）；否则`kr.Decrypt(密文, secret_key_version, []byte(t))`→`map[string]string`。

- [ ] **Step 1: 写失败测试**（`rw_test.go`，注册与 Task 3 相同的`example_app`定义＋一个带 upgrader 的`upg_app`：版本 2，upgrader FromVersion 1 把`old_name`改名`new_name`）：

```go
// TestPutGetRoundtrip     Put→Get：public 回显、secret_keys_set=[api_key client_secret]、无 secret 值
// TestPutMergesSecrets    第二次 Put 不带 client_secret→保留；带 api_key:""→删除
// TestPutIfMatchConflict  旧 updated_at→ErrConflict；新建时带 ifMatch→ErrConflict
// TestPutIncompatible     手工把行 config_schema_version 改成 99→Put 返回 ErrIncompatible
// TestPutPrunesRemovedFields 手工往 public_config 塞未知 key→Put 后消失
// TestDelete              Delete 后 Get→ErrNotFound；Delete 不存在→ErrNotFound
// TestResolvedDefaultsAndSecrets  未配置行→defaults；配置后 public+secret 覆盖 default
// TestResolvedAppliesUpgrader     造 schema_version=1 行（含 old_name）→Resolved 含 new_name；DB 原样
// TestConfigStateMapping  exists/version/public/secret keys/mcp_verified 全部映射正确
```

- [ ] **Step 2: 确认失败→实现→确认通过**（实现按上述语义写`rw.go`／`state.go`，升级链转换：`map[string]string`→`map[string]any`进 upgrader，出来后 secrets 只保留字符串值）

```bash
cd packages/service && go test ./configsvc/
```

- [ ] **Step 3: 提交**

```bash
git add packages/service/configsvc
git commit -m "feat(service): add config read/write with encryption and upgraders"
```

---

### Task 5: authsvc（admin 密码＋API token）

**Files:**
- Create: `packages/service/authsvc/authsvc.go`、`packages/service/authsvc/argon2.go`
- Test: `packages/service/authsvc/authsvc_test.go`

**Interfaces:**
- Consumes: store 的`GetAdminAccount`／`InsertAdminAccountIfAbsent`／`UpdateAdminPassword`／`InsertAPIToken`／`GetAPITokenByHash`／`ListAPITokens`／`RevokeAPIToken`
- Produces（Task 7 依赖）:

```go
func New(q *store.Queries) *Service
func (s *Service) EnsureAdminFromEnv(ctx context.Context) error   // 读 CONNECT_IT_ADMIN_PASSWORD；已有行不动；env 空且无行→报错提示必须设置
func (s *Service) VerifyAdminPassword(ctx context.Context, username, password string) (bool, error)
func (s *Service) ChangeAdminPassword(ctx context.Context, newPassword string) error
type APITokenView struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}
func (s *Service) CreateAPIToken(ctx context.Context, name string) (plaintext string, id uuid.UUID, err error)
func (s *Service) VerifyAPIToken(ctx context.Context, token string) (bool, error)
func (s *Service) ListAPITokens(ctx context.Context) ([]APITokenView, error)
func (s *Service) RevokeAPIToken(ctx context.Context, id uuid.UUID) error
```

**实现约定：** argon2id 参数`time=3, memory=64*1024, threads=2, keyLen=32`，编码`$argon2id$v=19$m=65536,t=3,p=2$<b64(salt)>$<b64(key)>`（b64 用`RawStdEncoding`），校验用`subtle.ConstantTimeCompare`；token 明文`"cit_"+hex(rand 32B)`，库存 sha256 hex；`VerifyAPIToken`对非`cit_`前缀直接 false。

- [ ] **Step 1: 失败测试**：hash→verify 真假密码；`EnsureAdminFromEnv`幂等（第二次不同 env 密码不覆盖）；token 创建→verify true→撤销→false；`ListAPITokens`含 revoked_at。
- [ ] **Step 2: 确认失败→实现→确认通过**（`cd packages/service && go test ./authsvc/`）
- [ ] **Step 3: 提交** `git commit -m "feat(service): add auth service (argon2id admin password, api tokens)"`

---

### Task 6: catalogsvc

**Files:**
- Create: `packages/service/catalogsvc/catalogsvc.go`
- Test: `packages/service/catalogsvc/catalogsvc_test.go`（集成）

**Interfaces:**
- Produces（Task 7 与计划 7 依赖；json tag 即 API 输出形状）:

```go
type Item struct {
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Categories  []string      `json:"categories"`
	HomepageURL string        `json:"homepage_url"`
	IconURL     string        `json:"icon_url"`
	Status      status.Status `json:"status"`
}
func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service) *Service
func (s *Service) List(ctx context.Context) ([]Item, error)
func (s *Service) Get(ctx context.Context, t connector.Type) (Item, error)   // 未知 type 且无配置行→configsvc.ErrNotFound
```

**实现约定：** `registry.All()`逐个取`ConfigState`＋`GetConnectorHealth`（`ErrNoRows`→零值）→`status.Compute(def, cfg, health, time.Now())`；再`ListConnectorConfigs`把不在 Registry 的 type 追加为`{Type: t, Status: definition_missing}`条目；结果按 Type 排序。

- [ ] **Step 1: 失败测试**：有 Tool 未配置→needs_config；无 Tool→catalog_only；配置完整→ready；health 行 failures=3 且 last_error_at 现在→degraded；DB 孤儿 config 行→definition_missing；`Get("nope")`→ErrNotFound。
- [ ] **Step 2: 确认失败→实现→确认通过**
- [ ] **Step 3: 提交** `git commit -m "feat(service): add catalog service with status computation"`

---

### Task 7: api 模块——Echo、门禁、路由与 Swagger

**Files:**
- Create: `packages/api/go.mod`（replace core、connectors、service 三者）
- Create: `packages/api/api.go`（Deps＋New＋路由注册）
- Create: `packages/api/session.go`、`packages/api/middleware.go`、`packages/api/errors.go`
- Create: `packages/api/handlers_catalog.go`、`handlers_config.go`、`handlers_auth.go`、`handlers_tokens.go`
- Create: `packages/api/docs/`（swag 生成，提交进仓库）
- Test: `packages/api/api_test.go`
- Modify: `mise.toml`（swagger 任务）

**Interfaces:**
- Produces（计划 3–5、7 依赖）:

```go
type Deps struct {
	Registry     *registry.Registry
	Store        *store.Queries
	Config       *configsvc.Service
	Catalog      *catalogsvc.Service
	Auth         *authsvc.Service
	CookieSecret []byte
}
func New(deps Deps) *echo.Echo
func writeError(c echo.Context, httpStatus int, code, message string) error   // {"error":code,"message":message}
// middleware.go：RequireAPIToken(auth *authsvc.Service)、RequireAdminSession(secret []byte) echo.MiddlewareFunc
// session.go：signSession(secret []byte, expires time.Time) string；verifySession(secret []byte, value string) bool
//   cookie 名 connect_it_admin，值 "admin|<unix>|<hex(hmac-sha256(secret,"admin|<unix>"))>"，24h，HttpOnly，SameSite=Lax
```

**路由与鉴权（本 task 实装）：**

```text
GET    /healthz                                  无鉴权 → {"status":"ok"}
GET    /swagger/*                                echo-swagger UI
POST   /admin/login                              无鉴权，body{"username","password"}→204＋Set-Cookie；错误→401 invalid_credentials
GET    /v1/connectors                            RequireAPIToken → []catalogsvc.Item
GET    /v1/connectors/:type                      RequireAPIToken → catalogsvc.Item；404 not_found
GET    /admin/connectors                         RequireAdminSession → []catalogsvc.Item（前端用）
GET    /admin/connectors/:type/config-schema     RequireAdminSession → []configFieldDTO
GET    /admin/connectors/:type/config            RequireAdminSession → configResponse；404 not_found
PUT    /admin/connectors/:type/config            RequireAdminSession，body{"public":{},"secrets":{},"if_match":"RFC3339|空"}
DELETE /admin/connectors/:type/config            RequireAdminSession → 204
POST   /admin/connectors/:type/config\:validate  RequireAdminSession → 204 或 422 validation_failed
GET    /admin/api-tokens                         RequireAdminSession → []authsvc.APITokenView
POST   /admin/api-tokens                         RequireAdminSession，body{"name"}→201{"id","token"}（明文仅此一次）
DELETE /admin/api-tokens/:id                     RequireAdminSession → 204
PUT    /admin/account/password                   RequireAdminSession，body{"password"}→204
```

**DTO（api 包内定义，json tag 即 swagger 模型）：**

```go
type configFieldDTO struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	InputType    string   `json:"input_type"`
	Required     bool     `json:"required"`
	Secret       bool     `json:"secret"`
	DefaultValue *string  `json:"default_value"`
	Description  string   `json:"description"`
	Pattern      string   `json:"pattern"`
	Options      []string `json:"options"`
}
type configResponse struct {
	ConnectorType string         `json:"connector_type"`
	SchemaVersion int            `json:"schema_version"`
	Public        map[string]any `json:"public"`
	SecretKeysSet []string       `json:"secret_keys_set"`
	UpdatedAt     time.Time      `json:"updated_at"`
}
type putConfigRequest struct {
	Public  map[string]any    `json:"public"`
	Secrets map[string]string `json:"secrets"`
	IfMatch string            `json:"if_match"`
}
```

**错误映射：** `ErrUnknownConnector`／`ErrNotFound`→404 `not_found`；`ErrConflict`→409 `conflict`；`ErrIncompatible`→409 `config_incompatible`；`*ValidationError`→422 `validation_failed`（message 含 Field 与 Reason）；其余→500 `internal`（message 固定「internal error」，详情只进日志）。

**Swagger：** 每个 handler 带 swag 注释（`@Summary`／`@Tags`／`@Accept json`／`@Produce json`／`@Param`／`@Success`／`@Failure`／`@Router`；`/v1/*`加`@Security BearerAuth`）。总注释块放`cmd/connect-it/main.go`（Task 8）：`@title connect-it API`、`@version 1.0`、`@BasePath /`、`@securityDefinitions.apikey BearerAuth`＋`@in header`＋`@name Authorization`。mise 任务：

```toml
[tasks.swagger]
description = "由 swag 注释生成 packages/api/docs（swagger.json 供 SDK 消费）"
run = "cd packages/api && go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/connect-it/main.go --output docs --parseDependency"
```

`api.go`导入`_ "github.com/memohai/connect-it/packages/api/docs"`＋`echoSwagger "github.com/swaggo/echo-swagger"`，注册`e.GET("/swagger/*", echoSwagger.WrapHandler)`。注意：首次编译前 docs 包不存在，实现顺序为「写 handlers（暂不挂 swagger 路由）→跑 swag 生成 docs→再挂 swagger 路由」；`config:validate`在 Echo 中注册为`config\:validate`（转义冒号）。

- [ ] **Step 1: 失败测试**（httptest＋`api.New`，DB 依赖用`testutil.NewDB`＋真实 service 装配；一个`newTestServer(t)`助手集中装配）：
  - `/healthz`200；
  - admin 路由无 cookie→401`unauthorized`；伪造签名 cookie→401；
  - 登录错误密码→401；正确→204＋cookie 随后可访问`/admin/connectors`；
  - `/v1/connectors`无 Bearer→401；无效 token→401；有效→200 且含注册的 connector；
  - config PUT→GET：`secret_keys_set`正确且响应文本不含 secret 值；`if_match`旧值→409；
  - `config:validate`非法→422 且 message 含字段名；
  - api-tokens POST→明文`cit_`前缀→用它访问`/v1/connectors`成功→DELETE 后 401。
- [ ] **Step 2: 确认失败→实现全部文件→确认通过**（`cd packages/api && go test ./...`）
- [ ] **Step 3: 生成 swagger 并挂 UI 路由**

```bash
mise run swagger      # 生成 packages/api/docs/{docs.go,swagger.json,swagger.yaml}
cd packages/api && go build ./... && go test ./...
```

- [ ] **Step 4: 提交**

```bash
git add packages/api mise.toml
git commit -m "feat(api): add echo server, auth middleware, catalog/config routes with swagger"
```

---

### Task 8: cmd/connect-it 与 dev 任务

**Files:**
- Create: `packages/api/cmd/connect-it/main.go`
- Modify: `mise.toml`（dev 任务）

- [ ] **Step 1: main.go**（顶部放 swag 总注释块）。流程：读环境变量（`DATABASE_URL`／`CONNECT_IT_SECRET_KEY`／`COOKIE_SECRET`必填，缺失则`log.Fatal`列出变量名；`LISTEN_ADDR`默认`:8080`）→`pgxpool.New`→`service.MigrateUp(DATABASE_URL)`→`registry.New()`＋`connectors.RegisterAll`→`crypto.ParseKeyring`→`store.New(pool)`→装配 configsvc／authsvc／catalogsvc→`auth.EnsureAdminFromEnv`→`api.New(deps)`→`e.Start`。
- [ ] **Step 2: dev 任务**

```toml
[tasks.dev]
description = "本地开发起 API（先 mise run db-up）"
run = '''
cd packages/api && \
DATABASE_URL='postgres://postgres:postgres@localhost:5433/connect_it?sslmode=disable' \
CONNECT_IT_SECRET_KEY='1:1111111111111111111111111111111111111111111111111111111111111111' \
COOKIE_SECRET='dev-cookie-secret' \
CONNECT_IT_ADMIN_PASSWORD='admin123' \
CONNECT_IT_BASE_URL='http://localhost:8080' \
go run ./cmd/connect-it
'''
```

- [ ] **Step 3: 手工验证＋提交**

```bash
mise run dev &
curl -s http://localhost:8080/healthz                 # {"status":"ok"}
curl -s http://localhost:8080/v1/connectors           # 401
curl -s -X POST http://localhost:8080/admin/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' -i  # 204 + Set-Cookie
git add packages/api mise.toml
git commit -m "feat(api): add main entrypoint and dev task"
```

## 完成标准

- `mise run test`全绿（无`TEST_DATABASE_URL`时 DB 用例 SKIP）；`mise run vet`干净；
- `mise run swagger`产出的`docs/swagger.json`覆盖全部路由与模型且已提交，重复执行无 diff；
- 手工链路：登录→写配置→读配置（secret 只见 key）→if_match 冲突 409→token 创建→Bearer 访问 catalog；
- Secret 值不出现在任何响应／日志／swagger 示例（spec §17）。

---
