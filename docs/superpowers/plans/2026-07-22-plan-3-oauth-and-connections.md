# connect-it 计划 3：OAuth 授权、connections 与 token 惰性刷新

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 service module 内实装 OAuth 授权流（`oauthsvc`）、连接管理（`connsvc`）与 access token 惰性刷新（`tokens.Refresher`），并把连接管理路由接入 api module。

**Architecture:** 三个新服务包都构建在计划 2 的`store`（sqlc）之上：`oauthsvc`负责 state／PKCE 与授权码换 token，`connsvc`负责 api_key／custom_credential 连接的创建与列表删除，`tokens.Refresher`按 spec §10 的五步流程做惰性刷新（60 秒 skew→singleflight→事务`FOR UPDATE`二次判断→token endpoint→新旧 token 一起写回）。api module 只做参数绑定、错误码映射与路由挂载。

**Tech Stack:** Go 1.25、Echo v4、pgx v5（pgxpool）、sqlc、golang-migrate v4、`github.com/google/uuid`、`golang.org/x/sync/singleflight`、httptest（假 OAuth provider）。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 7、8、10、12 节是本计划的依据，第 10 节「OAuth 与 token 刷新」是核心）。

**前置：** 计划 1（core／connectors）与计划 2（service 的 store／migrations／configsvc、api 骨架、`cmd/connect-it/main.go`、mise 任务`db-up`／`db-down`／`sqlc`／`dev`）均已按契约落地。

## Global Constraints

- Go module 路径固定为`github.com/memohai/connect-it/packages/<name>`；无 go.work，各 go.mod 用相对路径`replace`列出全部本地依赖（含间接）。
- 依赖方向单向：`connectors→core`；`service→core＋connectors`；`api→service`。禁止反向 import。
- 所有`go`命令必须`cd`进对应 module 目录执行。
- 契约签名（`oauthsvc.New`／`Begin`／`HandleCallback`、`connsvc.New`／`ConnectionView`／`CreateAPIKey`／`List`／`Delete`、`tokens.New`／`AccessToken`、`api.Deps`增补字段`OAuth *oauthsvc.Service; Conns *connsvc.Service`）必须逐字实现，不得改名或增删参数。
- 计划 2 已有接口（`service.MigrateUp`、`store.New`、`configsvc`、`api.New`、中间件）直接使用，不重新定义。
- API JSON 一律 snake_case；错误响应固定为`{"error":"machine_code","message":"…"}`。
- 加密 AAD 约定：`connector_configs`绑`connector_type`；`connections`绑 connection 的 UUID 字符串；本计划新增：`oauth_authorizations.pkce_verifier`绑 authorization 的 UUID 字符串。
- credential 密文内 JSON（加密前明文）：OAuth 为`{"access_token":"…","refresh_token":"…","expires_at":"RFC3339"}`；api_key／custom_credential 为`{"fields":{"key":"value",…}}`。
- alias 必须匹配`^[a-z0-9][a-z0-9-]{0,31}$`，创建（含发起 OAuth）时校验。
- OAuth 回调地址固定为`CONNECT_IT_BASE_URL + /v1/oauth/callback`；state 随机 256bit，库中只存 sha256；PKCE 用 S256；授权 10 分钟过期。
- 数据库集成测试读`TEST_DATABASE_URL`，未设置则`t.Skip`；本地先`mise run db-up`（docker postgres:17，host 5433），再以该库的 URL 作为`TEST_DATABASE_URL`（具体用户名／密码／库名以计划 2 在`mise.toml`中`db-up`任务的定义为准）。
- 提交信息用 conventional commits（feat:／test:／chore:），每个 task 至少一次提交。

---

### Task 1: store 层——migration 0002、connections／oauth_authorizations queries 与 BeginTx

**Files:**
- Create: `packages/service/migrations/0002_oauth_authorization_context.up.sql`
- Create: `packages/service/migrations/0002_oauth_authorization_context.down.sql`
- Create: `packages/service/store/queries/connections.sql`
- Create: `packages/service/store/queries/oauth_authorizations.sql`
- Create: `packages/service/store/tx.go`（手写，与 sqlc 生成文件同包）
- Modify: `packages/service/sqlc.yaml`（仅当缺少类型 overrides 时）
- Test: `packages/service/store/plan3_test.go`
- Generated: `mise run sqlc`重新生成`packages/service/store/*.sql.go`

**Interfaces:**
- Consumes: `service.MigrateUp(databaseURL string) error`；`store.New(db store.DBTX) *store.Queries`（计划 2）。
- Produces（后续 task 全部依赖）:
  - sqlc 生成的方法：`CreateConnection`、`GetConnection`、`GetConnectionByAlias`、`GetConnectionForUpdate`、`ListConnections`、`UpdateConnectionCredential`、`UpdateConnectionStatus`、`DeleteConnection`（`:execrows`，返回`int64`）、`CreateOAuthAuthorization`、`GetOAuthAuthorizationByStateHash`、`CompleteOAuthAuthorization`、`DeleteExpiredOAuthAuthorizations`，以及参数结构体（字段见 SQL）。
  - 手写`(*store.Queries).BeginTx(ctx context.Context) (pgx.Tx, *store.Queries, error)`——当底层`DBTX`可开启事务（`*pgxpool.Pool`／`pgx.Tx`）时返回事务与绑定该事务的 Queries；否则返回`store.ErrNoTransactions`。
  - migration 0002 给`oauth_authorizations`补三列：`auth_method`、`alias`、`secret_key_version`（回调时需要知道用哪个 auth method、落到哪个 alias、pkce_verifier 用哪个 key 版本解密；spec §7 的原始列不含这三项，见计划头部说明）。

- [ ] **Step 1: 写 migration 0002**

`packages/service/migrations/0002_oauth_authorization_context.up.sql`：

```sql
alter table oauth_authorizations
  add column auth_method text not null default '',
  add column alias text not null default '',
  add column secret_key_version integer not null default 0;
```

`packages/service/migrations/0002_oauth_authorization_context.down.sql`：

```sql
alter table oauth_authorizations
  drop column auth_method,
  drop column alias,
  drop column secret_key_version;
```

- [ ] **Step 2: 写 queries SQL**

注意：若计划 2 的`sqlc.yaml`中 queries 目录不是`store/queries/`，把这两个文件放进它实际配置的目录，内容不变。

`packages/service/store/queries/connections.sql`：

```sql
-- name: CreateConnection :one
INSERT INTO connections (
  id, connector_type, alias, auth_method, credential, secret_key_version,
  profile, scopes, status, access_token_expires_at, created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, '{}', $7, $8, $9, now(), now()
)
RETURNING *;

-- name: GetConnection :one
SELECT * FROM connections WHERE id = $1;

-- name: GetConnectionByAlias :one
SELECT * FROM connections WHERE alias = $1;

-- name: GetConnectionForUpdate :one
SELECT * FROM connections WHERE id = $1 FOR UPDATE;

-- name: ListConnections :many
SELECT * FROM connections ORDER BY created_at, alias;

-- name: UpdateConnectionCredential :exec
UPDATE connections
SET credential = $2,
    secret_key_version = $3,
    status = $4,
    access_token_expires_at = $5,
    updated_at = now()
WHERE id = $1;

-- name: UpdateConnectionStatus :exec
UPDATE connections
SET status = $2,
    updated_at = now()
WHERE id = $1;

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE id = $1;
```

`packages/service/store/queries/oauth_authorizations.sql`：

```sql
-- name: CreateOAuthAuthorization :one
INSERT INTO oauth_authorizations (
  id, connector_type, state_hash, pkce_verifier, secret_key_version,
  auth_method, alias, connection_id, status, expires_at, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now()
)
RETURNING *;

-- name: GetOAuthAuthorizationByStateHash :one
SELECT * FROM oauth_authorizations WHERE state_hash = $1;

-- name: CompleteOAuthAuthorization :exec
UPDATE oauth_authorizations
SET status = $2,
    connection_id = $3
WHERE id = $1;

-- name: DeleteExpiredOAuthAuthorizations :exec
DELETE FROM oauth_authorizations
WHERE expires_at < now() AND status = 'pending';
```

- [ ] **Step 3: 确认 sqlc 类型 overrides**

读`packages/service/sqlc.yaml`。生成代码必须满足：非空`uuid`列→`uuid.UUID`、可空`uuid`列→`uuid.NullUUID`、非空`timestamptz`列→`time.Time`（可空`timestamptz`保持默认的`pgtype.Timestamptz`）。若 overrides 缺失或不全，在其`gen.go.overrides`处补成：

```yaml
overrides:
  - db_type: "uuid"
    go_type:
      import: "github.com/google/uuid"
      type: "UUID"
  - db_type: "uuid"
    nullable: true
    go_type:
      import: "github.com/google/uuid"
      type: "NullUUID"
  - db_type: "timestamptz"
    go_type:
      import: "time"
      type: "Time"
```

- [ ] **Step 4: 生成代码**

```bash
mise run sqlc
```

预期：`packages/service/store/`下新增`connections.sql.go`、`oauth_authorizations.sql.go`，`models.go`含`Connection`与`OauthAuthorization`结构体（`AccessTokenExpiresAt`为`pgtype.Timestamptz`，`ConnectionID`为`uuid.NullUUID`，`SecretKeyVersion`为`int32`）。若 overrides 有改动，计划 2 已有的生成代码类型也可能变化——此时进入`packages/service`跑`go build ./...`，把受影响的调用点按新类型修正后再继续。

- [ ] **Step 5: 写失败测试**

`packages/service/store/plan3_test.go`：

```go
package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/service"
	"github.com/memohai/connect-it/packages/service/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过集成测试")
	}
	if err := service.MigrateUp(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func randomAlias(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return "t-" + hex.EncodeToString(buf)
}

func createTestConnection(t *testing.T, pool *pgxpool.Pool, q *store.Queries) store.Connection {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	t.Cleanup(func() {
		pool.Exec(context.Background(), "delete from connections where id = $1", id)
	})
	conn, err := q.CreateConnection(ctx, store.CreateConnectionParams{
		ID:                   id,
		ConnectorType:        "github",
		Alias:                randomAlias(t),
		AuthMethod:           "oauth",
		Credential:           []byte{0x01},
		SecretKeyVersion:     1,
		Scopes:               []string{"repo"},
		Status:               "active",
		AccessTokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}
	return conn
}

func TestConnectionRoundtrip(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	q := store.New(pool)
	conn := createTestConnection(t, pool, q)

	got, err := q.GetConnection(ctx, conn.ID)
	if err != nil || got.ID != conn.ID || got.Alias != conn.Alias {
		t.Fatalf("GetConnection: %v %+v", err, got)
	}
	byAlias, err := q.GetConnectionByAlias(ctx, conn.Alias)
	if err != nil || byAlias.ID != conn.ID {
		t.Fatalf("GetConnectionByAlias: %v", err)
	}

	if err := q.UpdateConnectionStatus(ctx, store.UpdateConnectionStatusParams{
		ID: conn.ID, Status: "reauth_required",
	}); err != nil {
		t.Fatalf("UpdateConnectionStatus: %v", err)
	}
	if err := q.UpdateConnectionCredential(ctx, store.UpdateConnectionCredentialParams{
		ID:                   conn.ID,
		Credential:           []byte{0x02},
		SecretKeyVersion:     2,
		Status:               "active",
		AccessTokenExpiresAt: pgtype.Timestamptz{}, // NULL：无过期时间
	}); err != nil {
		t.Fatalf("UpdateConnectionCredential: %v", err)
	}
	got, err = q.GetConnection(ctx, conn.ID)
	if err != nil || got.Status != "active" || got.SecretKeyVersion != 2 ||
		got.AccessTokenExpiresAt.Valid || string(got.Credential) != "\x02" {
		t.Fatalf("更新后状态不符: %v %+v", err, got)
	}

	list, err := q.ListConnections(ctx)
	if err != nil || len(list) == 0 {
		t.Fatalf("ListConnections: %v len=%d", err, len(list))
	}

	n, err := q.DeleteConnection(ctx, conn.ID)
	if err != nil || n != 1 {
		t.Fatalf("DeleteConnection: %v n=%d", err, n)
	}
	n, err = q.DeleteConnection(ctx, conn.ID)
	if err != nil || n != 0 {
		t.Fatalf("重复删除应影响 0 行: %v n=%d", err, n)
	}
}

func TestOAuthAuthorizationRoundtrip(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	q := store.New(pool)

	id := uuid.New()
	t.Cleanup(func() {
		pool.Exec(context.Background(), "delete from oauth_authorizations where id = $1", id)
	})
	created, err := q.CreateOAuthAuthorization(ctx, store.CreateOAuthAuthorizationParams{
		ID:               id,
		ConnectorType:    "github",
		StateHash:        id.String(), // 测试里用任意唯一串充当 hash
		PkceVerifier:     []byte{0x01},
		SecretKeyVersion: 1,
		AuthMethod:       "oauth",
		Alias:            "some-alias",
		ConnectionID:     uuid.NullUUID{},
		Status:           "pending",
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateOAuthAuthorization: %v", err)
	}
	if created.AuthMethod != "oauth" || created.Alias != "some-alias" {
		t.Fatalf("0002 新增列未生效: %+v", created)
	}

	got, err := q.GetOAuthAuthorizationByStateHash(ctx, id.String())
	if err != nil || got.ID != id || got.Status != "pending" {
		t.Fatalf("GetOAuthAuthorizationByStateHash: %v %+v", err, got)
	}

	connID := uuid.New()
	if err := q.CompleteOAuthAuthorization(ctx, store.CompleteOAuthAuthorizationParams{
		ID:           id,
		Status:       "completed",
		ConnectionID: uuid.NullUUID{UUID: connID, Valid: true},
	}); err != nil {
		t.Fatalf("CompleteOAuthAuthorization: %v", err)
	}
	got, err = q.GetOAuthAuthorizationByStateHash(ctx, id.String())
	if err != nil || got.Status != "completed" || !got.ConnectionID.Valid || got.ConnectionID.UUID != connID {
		t.Fatalf("完成后状态不符: %v %+v", err, got)
	}

	if err := q.DeleteExpiredOAuthAuthorizations(ctx); err != nil {
		t.Fatalf("DeleteExpiredOAuthAuthorizations: %v", err)
	}
}

func TestBeginTxForUpdate(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	q := store.New(pool)
	conn := createTestConnection(t, pool, q)

	tx, txq, err := q.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback(ctx)

	locked, err := txq.GetConnectionForUpdate(ctx, conn.ID)
	if err != nil || locked.ID != conn.ID {
		t.Fatalf("GetConnectionForUpdate: %v", err)
	}
	if err := txq.UpdateConnectionCredential(ctx, store.UpdateConnectionCredentialParams{
		ID:                   conn.ID,
		Credential:           []byte{0x09},
		SecretKeyVersion:     1,
		Status:               "active",
		AccessTokenExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("事务内更新: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := q.GetConnection(ctx, conn.ID)
	if err != nil || string(got.Credential) != "\x09" {
		t.Fatalf("提交后读取不到更新: %v %+v", err, got)
	}
}
```

- [ ] **Step 6: 运行确认失败**

```bash
cd packages/service && TEST_DATABASE_URL=<db-up 的库 URL> go test ./store/
```

预期：编译失败，`q.BeginTx undefined`（sqlc 生成的方法已存在，缺的是手写的`BeginTx`）。

- [ ] **Step 7: 写 tx.go**

`packages/service/store/tx.go`：

```go
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoTransactions 表示底层 DBTX 不支持开启事务。
var ErrNoTransactions = errors.New("store: 底层 DBTX 不支持事务")

// beginner 由 *pgxpool.Pool 与 pgx.Tx 实现。
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// BeginTx 在底层连接上开启事务，返回事务本身与绑定该事务的 Queries。
// 调用方负责 Commit / Rollback（Commit 之后的 defer Rollback 是 no-op）。
func (q *Queries) BeginTx(ctx context.Context) (pgx.Tx, *Queries, error) {
	b, ok := q.db.(beginner)
	if !ok {
		return nil, nil, ErrNoTransactions
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	return tx, q.WithTx(tx), nil
}
```

注意：若计划 2 生成的 Queries 结构体中数据库字段名不是`db`（以`store/db.go`为准），把`q.db`改成实际字段名。

- [ ] **Step 8: 运行确认通过**

```bash
mise run db-up
cd packages/service && go mod tidy && TEST_DATABASE_URL=<db-up 的库 URL> go test ./store/
```

预期：`ok  github.com/memohai/connect-it/packages/service/store`（三个测试全过）。另跑一次不带`TEST_DATABASE_URL`的`go test ./store/`，预期全部 SKIP。

- [ ] **Step 9: 提交**

```bash
git add packages/service/migrations packages/service/store packages/service/sqlc.yaml packages/service/go.mod packages/service/go.sum
git commit -m "feat(service): add connection and oauth authorization store queries"
```

---

### Task 2: credential 编解码包与 connsvc

**Files:**
- Create: `packages/service/credential/credential.go`
- Create: `packages/service/connsvc/connsvc.go`
- Test: `packages/service/credential/credential_test.go`
- Test: `packages/service/connsvc/connsvc_test.go`

**Interfaces:**
- Consumes: Task 1 的 store 方法与`BeginTx`不涉及；用`store.CreateConnection`／`GetConnection`／`ListConnections`／`DeleteConnection`；计划 1 的`registry.Registry`、`crypto.Keyring`、`connector`类型。
- Produces:
  - `credential.OAuth{AccessToken, RefreshToken string; ExpiresAt time.Time}`（`ExpiresAt`零值＝provider 未告知过期时间）、`(OAuth).Marshal() ([]byte, error)`、`credential.UnmarshalOAuth([]byte) (OAuth, error)`——JSON 形如`{"access_token":"…","refresh_token":"…","expires_at":"RFC3339"}`，零值时`expires_at`为空串。
  - `credential.Fields{Fields map[string]string}`、`(Fields).Marshal()`、`credential.UnmarshalFields([]byte) (Fields, error)`——JSON 形如`{"fields":{…}}`。
  - `connsvc.New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service`（契约）。
  - `connsvc.ConnectionView{ID uuid.UUID; ConnectorType string; Alias string; AuthMethod string; Status string; CreatedAt time.Time}`（契约）。
  - `(*Service).CreateAPIKey(ctx, t, authMethodKey, alias string, fields map[string]string) (uuid.UUID, error)`、`List(ctx) ([]ConnectionView, error)`、`Delete(ctx, id) error`（契约）。
  - 本计划补充：`(*Service).Get(ctx context.Context, id uuid.UUID) (ConnectionView, error)`（api 回调 302 需要按 id 取 alias）。
  - `connsvc.AliasPattern`（`^[a-z0-9][a-z0-9-]{0,31}$`，oauthsvc 复用）。
  - 哨兵错误：`ErrInvalidAlias`、`ErrUnknownConnector`、`ErrUnknownAuthMethod`、`ErrWrongAuthType`、`ErrInvalidFields`、`ErrAliasTaken`、`ErrNotFound`。

- [ ] **Step 1: 写 credential 的失败测试**

`packages/service/credential/credential_test.go`：

```go
package credential_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/service/credential"
)

func TestOAuthRoundtrip(t *testing.T) {
	exp := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	in := credential.OAuth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: exp}
	data, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["access_token"] != "at" || raw["refresh_token"] != "rt" || raw["expires_at"] != "2026-07-22T12:00:00Z" {
		t.Fatalf("JSON 字段不符: %v", raw)
	}
	out, err := credential.UnmarshalOAuth(data)
	if err != nil || out.AccessToken != "at" || out.RefreshToken != "rt" || !out.ExpiresAt.Equal(exp) {
		t.Fatalf("roundtrip 失败: %v %+v", err, out)
	}
}

func TestOAuthZeroExpiry(t *testing.T) {
	data, err := credential.OAuth{AccessToken: "at"}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["expires_at"] != "" {
		t.Fatalf("零值过期时间应序列化为空串: %v", raw)
	}
	out, err := credential.UnmarshalOAuth(data)
	if err != nil || !out.ExpiresAt.IsZero() {
		t.Fatalf("空串应解析回零值: %v %+v", err, out)
	}
}

func TestFieldsRoundtrip(t *testing.T) {
	data, err := credential.Fields{Fields: map[string]string{"key": "sk-1"}}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"fields":{"key":"sk-1"}}` {
		t.Fatalf("JSON 结构不符: %s", data)
	}
	out, err := credential.UnmarshalFields(data)
	if err != nil || out.Fields["key"] != "sk-1" {
		t.Fatalf("roundtrip 失败: %v %+v", err, out)
	}
}

func TestUnmarshalRejectsGarbage(t *testing.T) {
	if _, err := credential.UnmarshalOAuth([]byte("not-json")); err == nil {
		t.Fatal("非 JSON 应报错")
	}
	if _, err := credential.UnmarshalOAuth([]byte(`{"expires_at":"not-a-time"}`)); err == nil {
		t.Fatal("非 RFC3339 的 expires_at 应报错")
	}
	if _, err := credential.UnmarshalFields([]byte("not-json")); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/service && go test ./credential/
```

预期：编译失败，`undefined: credential.OAuth`。

- [ ] **Step 3: 写 credential 实现**

`packages/service/credential/credential.go`：

```go
// Package credential 定义 connections.credential 密文内的明文 JSON 结构。
package credential

import (
	"encoding/json"
	"fmt"
	"time"
)

// OAuth 是 oauth2 connection 的 credential 明文。
// ExpiresAt 为零值表示 provider 未告知过期时间（视为长期有效）。
type OAuth struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type oauthJSON struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
}

func (c OAuth) Marshal() ([]byte, error) {
	j := oauthJSON{AccessToken: c.AccessToken, RefreshToken: c.RefreshToken}
	if !c.ExpiresAt.IsZero() {
		j.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(j)
}

func UnmarshalOAuth(data []byte) (OAuth, error) {
	var j oauthJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return OAuth{}, fmt.Errorf("credential: 解析 oauth credential: %w", err)
	}
	out := OAuth{AccessToken: j.AccessToken, RefreshToken: j.RefreshToken}
	if j.ExpiresAt != "" {
		ts, err := time.Parse(time.RFC3339, j.ExpiresAt)
		if err != nil {
			return OAuth{}, fmt.Errorf("credential: expires_at 不是 RFC3339: %w", err)
		}
		out.ExpiresAt = ts
	}
	return out, nil
}

// Fields 是 api_key / custom_credential connection 的 credential 明文。
type Fields struct {
	Fields map[string]string `json:"fields"`
}

func (c Fields) Marshal() ([]byte, error) { return json.Marshal(c) }

func UnmarshalFields(data []byte) (Fields, error) {
	var c Fields
	if err := json.Unmarshal(data, &c); err != nil {
		return Fields{}, fmt.Errorf("credential: 解析 fields credential: %w", err)
	}
	return c, nil
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/service && go test ./credential/
```

预期：`ok  github.com/memohai/connect-it/packages/service/credential`。

- [ ] **Step 5: 写 connsvc 的失败测试**

`packages/service/connsvc/connsvc_test.go`（校验类错误路径在触库前返回，用`New(nil, reg, nil)`做纯单测；增删查用集成测试）：

```go
package connsvc_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
)

// testRegistry 注册一个带 api_key 与 oauth2 两种 auth method 的假 connector。
func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	r.MustRegister(connector.Definition{
		Type:                "conn_fake",
		Name:                "Conn Fake",
		ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{
			{Key: "apikey", Type: connector.AuthAPIKey, Label: "API Key",
				CredentialFields: []connector.ConfigField{
					{Key: "key", Label: "Key", InputType: connector.InputText, Required: true, Secret: true,
						Validation: connector.FieldValidation{Pattern: `^sk-[a-z0-9]+$`}},
					{Key: "note", Label: "Note", InputType: connector.InputText},
				}},
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				UsePKCE:               true,
			}},
		},
	})
	return r
}

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func randomAlias(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return "t-" + hex.EncodeToString(buf)
}

func TestCreateAPIKeyValidation(t *testing.T) {
	ctx := context.Background()
	svc := connsvc.New(nil, testRegistry(t), nil) // 校验失败不触库，nil 依赖安全
	cases := []struct {
		name    string
		typ     connector.Type
		method  string
		alias   string
		fields  map[string]string
		wantErr error
	}{
		{"alias 大写", "conn_fake", "apikey", "Bad", map[string]string{"key": "sk-a"}, connsvc.ErrInvalidAlias},
		{"alias 连字符开头", "conn_fake", "apikey", "-bad", map[string]string{"key": "sk-a"}, connsvc.ErrInvalidAlias},
		{"alias 超长", "conn_fake", "apikey", "a" + strings.Repeat("b", 32), map[string]string{"key": "sk-a"}, connsvc.ErrInvalidAlias},
		{"未知 connector", "nope", "apikey", "ok", map[string]string{"key": "sk-a"}, connsvc.ErrUnknownConnector},
		{"未知 auth method", "conn_fake", "nope", "ok", map[string]string{"key": "sk-a"}, connsvc.ErrUnknownAuthMethod},
		{"oauth method 不允许", "conn_fake", "oauth", "ok", map[string]string{"key": "sk-a"}, connsvc.ErrWrongAuthType},
		{"缺必填字段", "conn_fake", "apikey", "ok", map[string]string{}, connsvc.ErrInvalidFields},
		{"未声明字段", "conn_fake", "apikey", "ok", map[string]string{"key": "sk-a", "x": "1"}, connsvc.ErrInvalidFields},
		{"正则不匹配", "conn_fake", "apikey", "ok", map[string]string{"key": "SK-A"}, connsvc.ErrInvalidFields},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAPIKey(ctx, tc.typ, tc.method, tc.alias, tc.fields)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func testDB(t *testing.T) (*pgxpool.Pool, *store.Queries) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过集成测试")
	}
	if err := service.MigrateUp(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, store.New(pool)
}

func TestCreateListGetDelete(t *testing.T) {
	ctx := context.Background()
	pool, q := testDB(t)
	kr := testKeyring(t)
	svc := connsvc.New(q, testRegistry(t), kr)

	alias := randomAlias(t)
	id, err := svc.CreateAPIKey(ctx, "conn_fake", "apikey", alias, map[string]string{"key": "sk-abc123"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "delete from connections where id = $1", id)
	})

	// 密文按 AAD=connection UUID 解密，明文为 {"fields":{…}}。
	row, err := q.GetConnection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "active" || row.AuthMethod != "apikey" || row.AccessTokenExpiresAt.Valid {
		t.Fatalf("行内容不符: %+v", row)
	}
	plain, err := kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(id.String()))
	if err != nil {
		t.Fatalf("按 AAD 解密失败: %v", err)
	}
	cred, err := credential.UnmarshalFields(plain)
	if err != nil || cred.Fields["key"] != "sk-abc123" {
		t.Fatalf("credential 明文不符: %v %+v", err, cred)
	}

	views, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range views {
		if v.ID == id {
			found = true
			if v.ConnectorType != "conn_fake" || v.Alias != alias || v.Status != "active" || v.CreatedAt.IsZero() {
				t.Fatalf("view 不符: %+v", v)
			}
		}
	}
	if !found {
		t.Fatal("List 未包含新建 connection")
	}

	view, err := svc.Get(ctx, id)
	if err != nil || view.Alias != alias {
		t.Fatalf("Get: %v %+v", err, view)
	}

	// alias 冲突
	if _, err := svc.CreateAPIKey(ctx, "conn_fake", "apikey", alias, map[string]string{"key": "sk-x"}); !errors.Is(err, connsvc.ErrAliasTaken) {
		t.Fatalf("重复 alias 应返回 ErrAliasTaken, got %v", err)
	}

	if err := svc.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, id); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound, got %v", err)
	}
	if _, err := svc.Get(ctx, id); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("删除后 Get 应返回 ErrNotFound, got %v", err)
	}
	_ = uuid.Nil // 保证 uuid 引用（上方已使用，此行防误删 import 时遗漏）
}
```

（最后一行`_ = uuid.Nil`仅为防手误，保留或删除均可，删除时确认`uuid`import 仍被使用。）

- [ ] **Step 6: 运行确认失败**

```bash
cd packages/service && go test ./connsvc/
```

预期：编译失败，`undefined: connsvc.New`。

- [ ] **Step 7: 写 connsvc 实现**

`packages/service/connsvc/connsvc.go`：

```go
// Package connsvc 管理 connection 的创建（api_key / custom_credential）、列表与删除。
// OAuth connection 的创建走 oauthsvc。
package connsvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
)

// AliasPattern 是 alias 的合法形式（spec §7）。
var AliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var (
	ErrInvalidAlias      = errors.New("connsvc: alias 必须匹配 ^[a-z0-9][a-z0-9-]{0,31}$")
	ErrUnknownConnector  = errors.New("connsvc: 未知 connector type")
	ErrUnknownAuthMethod = errors.New("connsvc: 未知 auth method")
	ErrWrongAuthType     = errors.New("connsvc: auth method 不是 api_key / custom_credential")
	ErrInvalidFields     = errors.New("connsvc: credential 字段不合法")
	ErrAliasTaken        = errors.New("connsvc: alias 已被占用")
	ErrNotFound          = errors.New("connsvc: connection 不存在")
)

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	kr  *crypto.Keyring
}

func New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service {
	return &Service{q: q, reg: reg, kr: kr}
}

// ConnectionView 是不含 credential 的对外视图。
type ConnectionView struct {
	ID            uuid.UUID
	ConnectorType string
	Alias         string
	AuthMethod    string
	Status        string
	CreatedAt     time.Time
}

func (s *Service) CreateAPIKey(ctx context.Context, t connector.Type, authMethodKey, alias string, fields map[string]string) (uuid.UUID, error) {
	if !AliasPattern.MatchString(alias) {
		return uuid.Nil, ErrInvalidAlias
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	var method *connector.AuthMethod
	for i := range def.AuthMethods {
		if def.AuthMethods[i].Key == authMethodKey {
			method = &def.AuthMethods[i]
			break
		}
	}
	if method == nil {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownAuthMethod, authMethodKey)
	}
	if method.Type != connector.AuthAPIKey && method.Type != connector.AuthCustomCredential {
		return uuid.Nil, fmt.Errorf("%w: %s 是 %s", ErrWrongAuthType, authMethodKey, method.Type)
	}
	if err := validateFields(method.CredentialFields, fields); err != nil {
		return uuid.Nil, err
	}

	plain, err := credential.Fields{Fields: fields}.Marshal()
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	ct, ver, err := s.kr.Encrypt(plain, []byte(id.String()))
	if err != nil {
		return uuid.Nil, err
	}
	_, err = s.q.CreateConnection(ctx, store.CreateConnectionParams{
		ID:               id,
		ConnectorType:    string(t),
		Alias:            alias,
		AuthMethod:       authMethodKey,
		Credential:       ct,
		SecretKeyVersion: int32(ver),
		Scopes:           []string{},
		Status:           "active",
		// AccessTokenExpiresAt 保持零值（NULL）：api_key 不过期
	})
	if err != nil {
		if isUniqueViolation(err) {
			return uuid.Nil, fmt.Errorf("%w: %s", ErrAliasTaken, alias)
		}
		return uuid.Nil, err
	}
	return id, nil
}

func (s *Service) List(ctx context.Context) ([]ConnectionView, error) {
	rows, err := s.q.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	row, err := s.q.GetConnection(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConnectionView{}, ErrNotFound
		}
		return ConnectionView{}, err
	}
	return toView(row), nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := s.q.DeleteConnection(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func toView(r store.Connection) ConnectionView {
	return ConnectionView{
		ID:            r.ID,
		ConnectorType: r.ConnectorType,
		Alias:         r.Alias,
		AuthMethod:    r.AuthMethod,
		Status:        r.Status,
		CreatedAt:     r.CreatedAt,
	}
}

func validateFields(defs []connector.ConfigField, got map[string]string) error {
	byKey := map[string]connector.ConfigField{}
	for _, f := range defs {
		byKey[f.Key] = f
	}
	for k := range got {
		if _, ok := byKey[k]; !ok {
			return fmt.Errorf("%w: 未声明的字段 %q", ErrInvalidFields, k)
		}
	}
	for _, f := range defs {
		v, ok := got[f.Key]
		if f.Required && (!ok || v == "") {
			return fmt.Errorf("%w: 缺少必填字段 %q", ErrInvalidFields, f.Key)
		}
		if ok && v != "" && f.Validation.Pattern != "" {
			re, err := regexp.Compile(f.Validation.Pattern)
			if err != nil {
				return fmt.Errorf("%w: 字段 %q 的校验正则非法: %v", ErrInvalidFields, f.Key, err)
			}
			if !re.MatchString(v) {
				return fmt.Errorf("%w: 字段 %q 不符合 %s", ErrInvalidFields, f.Key, f.Validation.Pattern)
			}
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

- [ ] **Step 8: 运行确认通过**

```bash
cd packages/service && go mod tidy && TEST_DATABASE_URL=<db-up 的库 URL> go test ./credential/ ./connsvc/
```

预期：两个包`ok`（不设`TEST_DATABASE_URL`时集成用例 SKIP，单测仍全绿）。

- [ ] **Step 9: 提交**

```bash
git add packages/service/credential packages/service/connsvc packages/service/go.mod packages/service/go.sum
git commit -m "feat(service): add credential codec and connection service"
```

---

### Task 3: oauthsvc——授权发起、回调与 token 交换

**Files:**
- Create: `packages/service/oauthsvc/exchange.go`
- Create: `packages/service/oauthsvc/oauthsvc.go`
- Test: `packages/service/oauthsvc/oauthsvc_test.go`

**Interfaces:**
- Consumes: Task 1 的 store 方法；Task 2 的`credential`包与`connsvc.AliasPattern`／`connsvc.ErrInvalidAlias`；计划 2 的`configsvc.Service.Resolved(ctx,t)(map[string]any,error)`。
- Produces:
  - `oauthsvc.New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring, hc *http.Client, baseURL string) *Service`（契约）。
  - `(*Service).Begin(ctx context.Context, t connector.Type, authMethodKey, alias string) (authURL string, err error)`（契约）。
  - `(*Service).HandleCallback(ctx context.Context, state, code string) (uuid.UUID, error)`（契约）。
  - 本计划补充（Task 4 复用）：
    - `oauthsvc.CallbackPath = "/v1/oauth/callback"`；
    - `oauthsvc.TokenResponse{AccessToken, RefreshToken string; ExpiresIn int64}`；
    - `oauthsvc.ExchangeToken(ctx context.Context, hc *http.Client, oc *connector.OAuthConfig, clientID, clientSecret string, form url.Values) (TokenResponse, error)`——按`OAuthConfig.TokenEndpointAuth`选 basic／post 客户端认证；
    - `oauthsvc.ClientCredentials(ctx context.Context, cfg *configsvc.Service, t connector.Type) (clientID, clientSecret string, err error)`——约定从 Resolved 配置取`client_id`／`client_secret`两个字段；
  - 哨兵错误：`ErrUnknownConnector`、`ErrUnknownAuthMethod`、`ErrNotOAuth`、`ErrAliasTaken`、`ErrInvalidState`、`ErrMissingClient`。
- 行为约定：
  - state 与 PKCE verifier 均为 256bit 随机数的 base64url（43 字符，满足 RFC 7636 长度）；库中只存 state 的 sha256 hex；verifier 以 AAD=authorization UUID 加密存入`pkce_verifier`；
  - `Begin`时若 alias 已有同 connector＋同 auth method 的 connection，则把其 id 写入`connection_id`（重授权路径）；connector 或 auth method 不一致则`ErrAliasTaken`；
  - `HandleCallback`：`connection_id`非空→UPDATE 既有 connection（AAD 用既有 id）；为空→INSERT 新 connection（AAD 用新 id）；完成后授权行置`completed`并回填`connection_id`，connection 状态置`active`。

- [ ] **Step 1: 写失败测试**

`packages/service/oauthsvc/oauthsvc_test.go`：

```go
package oauthsvc_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

const baseURL = "https://connect.example.com"

// fakeProvider 是 httptest 假 OAuth provider，记录 token endpoint 收到的请求。
type fakeProvider struct {
	srv          *httptest.Server
	lastForm     url.Values
	lastUser     string
	lastPass     string
	lastHadBasic bool
	respond      func(w http.ResponseWriter)
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600,
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token 请求解析失败: %v", err)
		}
		p.lastForm = r.PostForm
		p.lastUser, p.lastPass, p.lastHadBasic = r.BasicAuth()
		p.respond(w)
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// testEnv 组装一套注册了 oauth_fake connector 的完整依赖。
type testEnv struct {
	pool *pgxpool.Pool
	q    *store.Queries
	kr   *crypto.Keyring
	svc  *oauthsvc.Service
}

func newTestEnv(t *testing.T, p *fakeProvider, tokenAuth connector.TokenEndpointAuth) *testEnv {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("未设置 TEST_DATABASE_URL，跳过集成测试")
	}
	if err := service.MigrateUp(dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type:                "oauth_fake",
		Name:                "OAuth Fake",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
		},
		AuthMethods: []connector.AuthMethod{
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: p.srv.URL + "/authorize",
				TokenEndpoint:         p.srv.URL + "/token",
				Scopes:                []string{"read", "write"},
				UsePKCE:               true,
				TokenEndpointAuth:     tokenAuth,
				ExtraAuthParams:       map[string]string{"access_type": "offline"},
			}},
			{Key: "apikey", Type: connector.AuthAPIKey, Label: "API Key",
				CredentialFields: []connector.ConfigField{
					{Key: "key", Label: "Key", InputType: connector.InputText, Required: true, Secret: true}}},
		},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	putClientConfig(t, cfg)
	svc := oauthsvc.New(q, reg, cfg, kr, &http.Client{}, baseURL)
	return &testEnv{pool: pool, q: q, kr: kr, svc: svc}
}

// putClientConfig 写入 client_id / client_secret。
// 假定计划 2 的 configsvc.Put 签名为 Put(ctx, t, public map[string]any, secret map[string]string) error；
// 若实际签名不同（例如带 If-Match 参数），只调整本函数一处。
func putClientConfig(t *testing.T, cfg *configsvc.Service) {
	t.Helper()
	if err := cfg.Put(context.Background(), "oauth_fake",
		map[string]any{"client_id": "test-client"},
		map[string]string{"client_secret": "test-secret"},
	); err != nil {
		t.Fatalf("写入 connector 配置失败: %v", err)
	}
}

func randomAlias(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return "t-" + hex.EncodeToString(buf)
}

func cleanupConnectionByAlias(t *testing.T, pool *pgxpool.Pool, alias string) {
	t.Cleanup(func() {
		pool.Exec(context.Background(), "delete from connections where alias = $1", alias)
	})
}

func s256(verifierOrState string) string {
	sum := sha256.Sum256([]byte(verifierOrState))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestBeginBuildsAuthorizationURL(t *testing.T) {
	ctx := context.Background()
	p := newFakeProvider(t)
	env := newTestEnv(t, p, connector.TokenAuthBasic)
	alias := randomAlias(t)
	cleanupConnectionByAlias(t, env.pool, alias)

	authURL, err := env.svc.Begin(ctx, "oauth_fake", "oauth", alias)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authURL, p.srv.URL+"/authorize?") {
		t.Fatalf("authorization endpoint 不符: %s", authURL)
	}
	qs := u.Query()
	if qs.Get("response_type") != "code" || qs.Get("client_id") != "test-client" {
		t.Fatalf("基础参数不符: %v", qs)
	}
	if qs.Get("redirect_uri") != baseURL+"/v1/oauth/callback" {
		t.Fatalf("redirect_uri 不符: %s", qs.Get("redirect_uri"))
	}
	if qs.Get("scope") != "read write" || qs.Get("access_type") != "offline" {
		t.Fatalf("scope / 附加参数不符: %v", qs)
	}
	if qs.Get("code_challenge_method") != "S256" || qs.Get("code_challenge") == "" {
		t.Fatalf("PKCE 参数不符: %v", qs)
	}
	state := qs.Get("state")
	if len(state) != 43 { // 256bit 的 base64url
		t.Fatalf("state 长度应为 43: %q", state)
	}

	// 库中只存 state 的 sha256 hex，明文 state 查不到。
	sum := sha256.Sum256([]byte(state))
	stateHash := hex.EncodeToString(sum[:])
	authz, err := env.q.GetOAuthAuthorizationByStateHash(ctx, stateHash)
	if err != nil {
		t.Fatalf("按 state hash 查授权行: %v", err)
	}
	t.Cleanup(func() {
		env.pool.Exec(context.Background(), "delete from oauth_authorizations where id = $1", authz.ID)
	})
	if authz.Status != "pending" || authz.Alias != alias || authz.AuthMethod != "oauth" || authz.ConnectionID.Valid {
		t.Fatalf("授权行不符: %+v", authz)
	}
}

func TestFullAuthorizationFlow(t *testing.T) {
	ctx := context.Background()
	p := newFakeProvider(t)
	env := newTestEnv(t, p, connector.TokenAuthBasic)
	alias := randomAlias(t)
	cleanupConnectionByAlias(t, env.pool, alias)

	authURL, err := env.svc.Begin(ctx, "oauth_fake", "oauth", alias)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	qs, _ := url.Parse(authURL)
	state := qs.Query().Get("state")
	challenge := qs.Query().Get("code_challenge")

	connID, err := env.svc.HandleCallback(ctx, state, "test-code")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}

	// token endpoint 收到的请求：basic 认证＋授权码＋能对上 challenge 的 verifier。
	if !p.lastHadBasic || p.lastUser != "test-client" || p.lastPass != "test-secret" {
		t.Fatalf("client_secret_basic 认证不符: %v %q", p.lastHadBasic, p.lastUser)
	}
	if p.lastForm.Get("grant_type") != "authorization_code" || p.lastForm.Get("code") != "test-code" {
		t.Fatalf("form 不符: %v", p.lastForm)
	}
	if p.lastForm.Get("redirect_uri") != baseURL+"/v1/oauth/callback" {
		t.Fatalf("redirect_uri 不符: %v", p.lastForm)
	}
	if s256(p.lastForm.Get("code_verifier")) != challenge {
		t.Fatal("code_verifier 与 code_challenge 对不上（PKCE S256）")
	}
	if p.lastForm.Get("client_secret") != "" {
		t.Fatal("basic 模式不应在 form 中携带 client_secret")
	}

	// connection 落库：状态 active，credential 按 AAD=connection UUID 解密。
	row, err := env.q.GetConnection(ctx, connID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if row.Alias != alias || row.Status != "active" || row.AuthMethod != "oauth" || !row.AccessTokenExpiresAt.Valid {
		t.Fatalf("connection 行不符: %+v", row)
	}
	plain, err := env.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(connID.String()))
	if err != nil {
		t.Fatalf("解密 credential: %v", err)
	}
	cred, err := credential.UnmarshalOAuth(plain)
	if err != nil || cred.AccessToken != "at-1" || cred.RefreshToken != "rt-1" || cred.ExpiresAt.IsZero() {
		t.Fatalf("credential 不符: %v %+v", err, cred)
	}

	// state 一次性：重放同一 state 失败。
	if _, err := env.svc.HandleCallback(ctx, state, "test-code"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("重放 state 应返回 ErrInvalidState, got %v", err)
	}

	// 重授权：同 alias 再走一遍，绑定到同一 connection。
	p.respond = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600,
		})
	}
	authURL2, err := env.svc.Begin(ctx, "oauth_fake", "oauth", alias)
	if err != nil {
		t.Fatalf("重授权 Begin: %v", err)
	}
	qs2, _ := url.Parse(authURL2)
	connID2, err := env.svc.HandleCallback(ctx, qs2.Query().Get("state"), "test-code-2")
	if err != nil {
		t.Fatalf("重授权 HandleCallback: %v", err)
	}
	if connID2 != connID {
		t.Fatalf("重授权应复用既有 connection: %s != %s", connID2, connID)
	}
	row, _ = env.q.GetConnection(ctx, connID)
	plain, err = env.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(connID.String()))
	if err != nil {
		t.Fatalf("重授权后解密: %v", err)
	}
	cred, _ = credential.UnmarshalOAuth(plain)
	if cred.AccessToken != "at-2" || cred.RefreshToken != "rt-2" {
		t.Fatalf("重授权未更新 credential: %+v", cred)
	}
}

func TestTokenEndpointAuthPost(t *testing.T) {
	ctx := context.Background()
	p := newFakeProvider(t)
	env := newTestEnv(t, p, connector.TokenAuthPost)
	alias := randomAlias(t)
	cleanupConnectionByAlias(t, env.pool, alias)

	authURL, err := env.svc.Begin(ctx, "oauth_fake", "oauth", alias)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := url.Parse(authURL)
	if _, err := env.svc.HandleCallback(ctx, qs.Query().Get("state"), "c"); err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if p.lastHadBasic {
		t.Fatal("post 模式不应使用 Basic 认证")
	}
	if p.lastForm.Get("client_id") != "test-client" || p.lastForm.Get("client_secret") != "test-secret" {
		t.Fatalf("post 模式应在 form 中携带 client 凭证: %v", p.lastForm)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	ctx := context.Background()
	p := newFakeProvider(t)
	env := newTestEnv(t, p, connector.TokenAuthBasic)

	if _, err := env.svc.HandleCallback(ctx, "no-such-state", "c"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("未知 state 应返回 ErrInvalidState, got %v", err)
	}

	// 过期的授权行同样拒绝。
	alias := randomAlias(t)
	cleanupConnectionByAlias(t, env.pool, alias)
	authURL, err := env.svc.Begin(ctx, "oauth_fake", "oauth", alias)
	if err != nil {
		t.Fatal(err)
	}
	qs, _ := url.Parse(authURL)
	state := qs.Query().Get("state")
	sum := sha256.Sum256([]byte(state))
	if _, err := env.pool.Exec(ctx,
		"update oauth_authorizations set expires_at = now() - interval '1 minute' where state_hash = $1",
		hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.HandleCallback(ctx, state, "c"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("过期授权应返回 ErrInvalidState, got %v", err)
	}
}

func TestBeginRejectsAliasOfOtherMethod(t *testing.T) {
	ctx := context.Background()
	p := newFakeProvider(t)
	env := newTestEnv(t, p, connector.TokenAuthBasic)

	// 先用 apikey method 占住 alias（直接插行，避免依赖 connsvc）。
	alias := randomAlias(t)
	cleanupConnectionByAlias(t, env.pool, alias)
	id := uuid.New()
	ct, ver, err := env.kr.Encrypt([]byte(`{"fields":{"key":"k"}}`), []byte(id.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.q.CreateConnection(ctx, store.CreateConnectionParams{
		ID: id, ConnectorType: "oauth_fake", Alias: alias, AuthMethod: "apikey",
		Credential: ct, SecretKeyVersion: int32(ver), Scopes: []string{}, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := env.svc.Begin(ctx, "oauth_fake", "oauth", alias); !errors.Is(err, oauthsvc.ErrAliasTaken) {
		t.Fatalf("auth method 不一致的 alias 应返回 ErrAliasTaken, got %v", err)
	}
}

func TestBeginValidationNoDB(t *testing.T) {
	// 校验类错误在触库与读配置之前返回，nil 依赖安全。
	ctx := context.Background()
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type: "oauth_fake", Name: "F", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{
			{Key: "apikey", Type: connector.AuthAPIKey, Label: "K",
				CredentialFields: []connector.ConfigField{{Key: "key", Label: "K", InputType: connector.InputText, Required: true, Secret: true}}},
		},
	})
	svc := oauthsvc.New(nil, reg, nil, nil, nil, baseURL)

	if _, err := svc.Begin(ctx, "oauth_fake", "apikey", "Bad Alias"); !errors.Is(err, connsvc.ErrInvalidAlias) {
		t.Fatalf("want ErrInvalidAlias, got %v", err)
	}
	if _, err := svc.Begin(ctx, "nope", "oauth", "ok"); !errors.Is(err, oauthsvc.ErrUnknownConnector) {
		t.Fatalf("want ErrUnknownConnector, got %v", err)
	}
	if _, err := svc.Begin(ctx, "oauth_fake", "nope", "ok"); !errors.Is(err, oauthsvc.ErrUnknownAuthMethod) {
		t.Fatalf("want ErrUnknownAuthMethod, got %v", err)
	}
	if _, err := svc.Begin(ctx, "oauth_fake", "apikey", "ok"); !errors.Is(err, oauthsvc.ErrNotOAuth) {
		t.Fatalf("want ErrNotOAuth, got %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/service && go test ./oauthsvc/
```

预期：编译失败，`undefined: oauthsvc.New`。

- [ ] **Step 3: 写 exchange.go**

`packages/service/oauthsvc/exchange.go`：

```go
package oauthsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/configsvc"
)

// TokenResponse 是 token endpoint 标准响应的子集。
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ClientCredentials 从 Resolved 配置中取约定字段 client_id / client_secret。
// 约定：需要 OAuth 的 Connector 必须声明这两个 ConfigField（client_secret 为 Secret）。
func ClientCredentials(ctx context.Context, cfg *configsvc.Service, t connector.Type) (clientID, clientSecret string, err error) {
	resolved, err := cfg.Resolved(ctx, t)
	if err != nil {
		return "", "", err
	}
	clientID, _ = resolved["client_id"].(string)
	clientSecret, _ = resolved["client_secret"].(string)
	if clientID == "" {
		return "", "", ErrMissingClient
	}
	return clientID, clientSecret, nil
}

// ExchangeToken 请求 token endpoint。form 由调用方填好 grant_type 等业务参数，
// 本函数按 TokenEndpointAuth 补齐客户端认证：
// client_secret_post→写入 form；否则（含零值）→client_secret_basic。
func ExchangeToken(ctx context.Context, hc *http.Client, oc *connector.OAuthConfig, clientID, clientSecret string, form url.Values) (TokenResponse, error) {
	if oc.TokenEndpointAuth == connector.TokenAuthPost {
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if oc.TokenEndpointAuth != connector.TokenAuthPost {
		// RFC 6749 §2.3.1：Basic 认证的用户名密码须先做 form 编码。
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("oauthsvc: 请求 token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return TokenResponse{}, fmt.Errorf("oauthsvc: token endpoint 返回 %d: %s", resp.StatusCode, truncate(body, 256))
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return TokenResponse{}, fmt.Errorf("oauthsvc: token 响应不是 JSON: %w", err)
	}
	if tok.AccessToken == "" {
		return TokenResponse{}, errors.New("oauthsvc: token 响应缺少 access_token")
	}
	return tok, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
```

- [ ] **Step 4: 写 oauthsvc.go**

`packages/service/oauthsvc/oauthsvc.go`：

```go
// Package oauthsvc 实现 OAuth 授权发起（state＋PKCE）与回调处理（授权码换 token）。
package oauthsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
)

// CallbackPath 是固定回调路径，回调完整地址 = CONNECT_IT_BASE_URL + CallbackPath。
const CallbackPath = "/v1/oauth/callback"

const (
	authorizationTTL = 10 * time.Minute
	statusPending    = "pending"
	statusCompleted  = "completed"
)

var (
	ErrUnknownConnector  = errors.New("oauthsvc: 未知 connector type")
	ErrUnknownAuthMethod = errors.New("oauthsvc: 未知 auth method")
	ErrNotOAuth          = errors.New("oauthsvc: auth method 不是 oauth2")
	ErrAliasTaken        = errors.New("oauthsvc: alias 已被其他 connector 或 auth method 占用")
	ErrInvalidState      = errors.New("oauthsvc: state 无效或已过期")
	ErrMissingClient     = errors.New("oauthsvc: 配置缺少 client_id / client_secret")
)

type Service struct {
	q       *store.Queries
	reg     *registry.Registry
	cfg     *configsvc.Service
	kr      *crypto.Keyring
	hc      *http.Client
	baseURL string
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring, hc *http.Client, baseURL string) *Service {
	return &Service{q: q, reg: reg, cfg: cfg, kr: kr, hc: hc, baseURL: strings.TrimRight(baseURL, "/")}
}

// Begin 生成授权 URL：随机 state（库中只存 sha256）、PKCE S256，
// 写 oauth_authorizations（10 分钟过期）。alias 已有同 connector＋同 auth method
// 的 connection 时进入重授权路径（绑定 connection_id）。
func (s *Service) Begin(ctx context.Context, t connector.Type, authMethodKey, alias string) (string, error) {
	if !connsvc.AliasPattern.MatchString(alias) {
		return "", connsvc.ErrInvalidAlias
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authMethodKey)
	if err != nil {
		return "", err
	}
	clientID, _, err := ClientCredentials(ctx, s.cfg, t)
	if err != nil {
		return "", err
	}

	connectionID := uuid.NullUUID{}
	existing, err := s.q.GetConnectionByAlias(ctx, alias)
	switch {
	case err == nil:
		if existing.ConnectorType != string(t) || existing.AuthMethod != authMethodKey {
			return "", fmt.Errorf("%w: %s", ErrAliasTaken, alias)
		}
		connectionID = uuid.NullUUID{UUID: existing.ID, Valid: true} // 重授权
	case errors.Is(err, pgx.ErrNoRows):
		// 全新连接
	default:
		return "", err
	}

	state, err := randomToken()
	if err != nil {
		return "", err
	}
	verifier := ""
	if method.OAuth.UsePKCE {
		if verifier, err = randomToken(); err != nil {
			return "", err
		}
	}

	authzID := uuid.New()
	encVerifier, keyVersion, err := s.kr.Encrypt([]byte(verifier), []byte(authzID.String()))
	if err != nil {
		return "", err
	}
	if _, err := s.q.CreateOAuthAuthorization(ctx, store.CreateOAuthAuthorizationParams{
		ID:               authzID,
		ConnectorType:    string(t),
		StateHash:        hashToken(state),
		PkceVerifier:     encVerifier,
		SecretKeyVersion: int32(keyVersion),
		AuthMethod:       authMethodKey,
		Alias:            alias,
		ConnectionID:     connectionID,
		Status:           statusPending,
		ExpiresAt:        time.Now().Add(authorizationTTL),
	}); err != nil {
		return "", err
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", s.baseURL+CallbackPath)
	params.Set("state", state)
	if len(method.OAuth.Scopes) > 0 {
		params.Set("scope", strings.Join(method.OAuth.Scopes, " "))
	}
	if method.OAuth.UsePKCE {
		params.Set("code_challenge", s256Challenge(verifier))
		params.Set("code_challenge_method", "S256")
	}
	for k, v := range method.OAuth.ExtraAuthParams {
		params.Set(k, v)
	}
	sep := "?"
	if strings.Contains(method.OAuth.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return method.OAuth.AuthorizationEndpoint + sep + params.Encode(), nil
}

// HandleCallback 核对 state hash、用授权码换 token，创建或更新 connection 并置 active。
func (s *Service) HandleCallback(ctx context.Context, state, code string) (uuid.UUID, error) {
	authz, err := s.q.GetOAuthAuthorizationByStateHash(ctx, hashToken(state))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrInvalidState
		}
		return uuid.Nil, err
	}
	if authz.Status != statusPending || time.Now().After(authz.ExpiresAt) {
		return uuid.Nil, ErrInvalidState
	}

	t := connector.Type(authz.ConnectorType)
	def, ok := s.reg.Get(t)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authz.AuthMethod)
	if err != nil {
		return uuid.Nil, err
	}
	clientID, clientSecret, err := ClientCredentials(ctx, s.cfg, t)
	if err != nil {
		return uuid.Nil, err
	}
	verifier, err := s.kr.Decrypt(authz.PkceVerifier, int(authz.SecretKeyVersion), []byte(authz.ID.String()))
	if err != nil {
		return uuid.Nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.baseURL+CallbackPath)
	if method.OAuth.UsePKCE {
		form.Set("code_verifier", string(verifier))
	}
	tok, err := ExchangeToken(ctx, s.hc, method.OAuth, clientID, clientSecret, form)
	if err != nil {
		return uuid.Nil, err
	}

	now := time.Now()
	cred := credential.OAuth{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken}
	if tok.ExpiresIn > 0 {
		cred.ExpiresAt = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	plain, err := cred.Marshal()
	if err != nil {
		return uuid.Nil, err
	}
	var expiresAt pgtype.Timestamptz
	if !cred.ExpiresAt.IsZero() {
		expiresAt = pgtype.Timestamptz{Time: cred.ExpiresAt, Valid: true}
	}

	var connID uuid.UUID
	if authz.ConnectionID.Valid {
		// 重授权：更新既有 connection，AAD 用既有 id。
		connID = authz.ConnectionID.UUID
		ct, ver, err := s.kr.Encrypt(plain, []byte(connID.String()))
		if err != nil {
			return uuid.Nil, err
		}
		if err := s.q.UpdateConnectionCredential(ctx, store.UpdateConnectionCredentialParams{
			ID:                   connID,
			Credential:           ct,
			SecretKeyVersion:     int32(ver),
			Status:               "active",
			AccessTokenExpiresAt: expiresAt,
		}); err != nil {
			return uuid.Nil, err
		}
	} else {
		connID = uuid.New()
		ct, ver, err := s.kr.Encrypt(plain, []byte(connID.String()))
		if err != nil {
			return uuid.Nil, err
		}
		scopes := method.OAuth.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		if _, err := s.q.CreateConnection(ctx, store.CreateConnectionParams{
			ID:                   connID,
			ConnectorType:        authz.ConnectorType,
			Alias:                authz.Alias,
			AuthMethod:           authz.AuthMethod,
			Credential:           ct,
			SecretKeyVersion:     int32(ver),
			Scopes:               scopes,
			Status:               "active",
			AccessTokenExpiresAt: expiresAt,
		}); err != nil {
			return uuid.Nil, err
		}
	}

	if err := s.q.CompleteOAuthAuthorization(ctx, store.CompleteOAuthAuthorizationParams{
		ID:           authz.ID,
		Status:       statusCompleted,
		ConnectionID: uuid.NullUUID{UUID: connID, Valid: true},
	}); err != nil {
		return uuid.Nil, err
	}
	return connID, nil
}

func findOAuthMethod(def connector.Definition, key string) (connector.AuthMethod, error) {
	for _, m := range def.AuthMethods {
		if m.Key == key {
			if m.Type != connector.AuthOAuth2 || m.OAuth == nil {
				return connector.AuthMethod{}, fmt.Errorf("%w: %s", ErrNotOAuth, key)
			}
			return m, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf("%w: %s", ErrUnknownAuthMethod, key)
}

// randomToken 返回 256bit 随机数的 base64url（43 字符，可直接用作 PKCE verifier）。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
```

- [ ] **Step 5: 运行确认通过**

```bash
cd packages/service && go mod tidy && TEST_DATABASE_URL=<db-up 的库 URL> go test ./oauthsvc/
```

预期：`ok  github.com/memohai/connect-it/packages/service/oauthsvc`（六个测试全过；不设`TEST_DATABASE_URL`时仅`TestBeginValidationNoDB`运行，其余 SKIP）。

- [ ] **Step 6: 提交**

```bash
git add packages/service/oauthsvc packages/service/go.mod packages/service/go.sum
git commit -m "feat(service): add oauth authorization flow with state and PKCE"
```

---

### Task 4: tokens.Refresher——惰性刷新＋single-flight＋行锁

**Files:**
- Create: `packages/service/tokens/refresher.go`
- Test: `packages/service/tokens/refresher_test.go`（集成，`testutil.NewDB`）

**Interfaces:**
- Consumes: Task 1 的`store.BeginTx`／`GetConnectionForUpdate`／`UpdateConnectionCredential`／`UpdateConnectionStatus`、Task 2 的`credential`包、Task 3 的`oauthsvc.ExchangeToken`／`oauthsvc.ClientCredentials`
- Produces（计划 4 的 exec.Engine 依赖）:

```go
type Refresher struct{ /* q; reg; cfg; kr; hc; group singleflight.Group */ }
func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring, hc *http.Client) *Refresher
func (r *Refresher) AccessToken(ctx context.Context, connectionID uuid.UUID) (string, error)
var ErrReauthRequired = errors.New("connection requires re-authorization")
```

**语义（spec §10 五步流程）：**

1. `GetConnection`读行；行状态`reauth_required`／`disabled`→直接`ErrReauthRequired`。
2. auth method 为`api_key`／`custom_credential`：解密`credential.Fields`，按 Definition 该 method 的**第一个**`CredentialFields`的 key 取值返回（约定：单字段凭证；多字段的取首个声明字段）。
3. `oauth2`：解密`credential.OAuth`；`ExpiresAt`为零或距今>60s→直接返回`AccessToken`。
4. 否则`singleflight.Group.Do(connectionID.String(), …)`进入刷新：`store.BeginTx`开事务→`GetConnectionForUpdate`行锁重读→**重新判断过期**（他人刚刷完→commit 后直接用新 token）→仍过期则`oauthsvc.ClientCredentials`取 client 凭证、`ExchangeToken(grant_type=refresh_token)`→成功：新`credential.OAuth{AccessToken, RefreshToken(响应缺省时保留旧值), ExpiresAt=now+expires_in}`加密（AAD=connectionID 字符串）→`UpdateConnectionCredential`＋commit→返回新 token。
5. `ExchangeToken`失败：事务内`UpdateConnectionStatus(id,"reauth_required")`＋commit→返回`ErrReauthRequired`（包装原因）。注意：网络瞬断也会标记，属第一期已接受的粗粒度行为，注释注明。

- [ ] **Step 1: 失败测试**（httptest 假 token endpoint，原子计数器记请求数）：

```go
// TestAPIKeyPassthrough        api_key connection→返回字段值，token endpoint 零请求
// TestFreshTokenNoRefresh      未过期 oauth→返回旧 token，零请求
// TestExpiredTriggersRefresh   已过期→换新 token、endpoint 1 次、DB credential 已更新（新 refresh_token 落库）
// TestRefreshKeepsOldRefreshToken 响应不含 refresh_token→库中保留旧值
// TestConcurrentSingleFlight   过期后 10 个 goroutine 并发 AccessToken→endpoint 恰好 1 次、全部拿到同一新 token
// TestRefreshFailureMarksReauth endpoint 返回 400→ErrReauthRequired、行 status=reauth_required；再次调用不再打 endpoint
```

- [ ] **Step 2: 确认失败→实现→确认通过**（`cd packages/service && go test ./tokens/`；实现中 singleflight 回调内不要复用外层 ctx 的 cancel——用`context.WithoutCancel(ctx)`，避免首个调用方取消导致全组失败）
- [ ] **Step 3: 提交** `git commit -m "feat(service): add lazy token refresher with singleflight and row lock"`

---

### Task 5: API 路由——OAuth 回调与 connections 管理

**Files:**
- Create: `packages/api/handlers_connections.go`、`packages/api/handlers_oauth.go`
- Modify: `packages/api/api.go`（Deps 增字段＋挂路由）
- Modify: `packages/service/oauthsvc/oauthsvc.go`（新增`BeginReauth`）
- Modify: `packages/api/cmd/connect-it/main.go`（装配 oauthsvc／connsvc／tokens）
- Test: `packages/api/connections_test.go`
- Generate: `mise run swagger`（路由变更后重新生成并提交 docs）

**Interfaces:**
- `api.Deps`增补：`OAuth *oauthsvc.Service; Conns *connsvc.Service`（Refresher 本计划只装配进 main 备用，计划 4 的 Engine 才消费）。
- oauthsvc 新增（供 reauth）：

```go
// BeginReauth 对既有 connection 重新发起授权：复用其 connector_type／auth_method／alias，
// 授权行带 connection_id，回调时更新而非新建。
func (s *Service) BeginReauth(ctx context.Context, connectionID uuid.UUID) (authURL string, err error)
```

**路由（全部带 swag 注释；admin 组 RequireAdminSession）：**

```text
GET    /v1/oauth/callback            无鉴权（state 兜底）。query: state、code、error。
                                     成功→302 /connections?connected=<alias>
                                     provider 报 error 或 state 无效→302 /connections?error=<code>
GET    /admin/connections            → []connsvc.ConnectionView（json tag：id/connector_type/alias/auth_method/status/created_at）
POST   /admin/connections/oauth      body{"connector_type","auth_method","alias"}→200{"authorization_url"}
POST   /admin/connections/api-key    body{"connector_type","auth_method","alias","fields":{}}→201{"id"}
POST   /admin/connections/:id/reauth → 200{"authorization_url"}（BeginReauth）
DELETE /admin/connections/:id        → 204；不存在→404 not_found
```

**错误映射追加：** `connsvc`／`oauthsvc`的校验错误（alias 不合法、auth method 不存在、类型不匹配）→422`validation_failed`；alias 唯一约束冲突→409`conflict`。

- [ ] **Step 1: 失败测试**（httptest 全栈＋假 provider：Begin→取 authorization_url 中的 state→直接 GET 回调→302 且 Location 含`connected=`；`/admin/connections`列表含新 connection 且响应不含 credential；api-key 创建→列表可见；reauth 返回新 authorization_url；DELETE→404 路径）
- [ ] **Step 2: 确认失败→实现→确认通过**（`cd packages/api && go test ./...`；`BeginReauth`实现进 oauthsvc 并补其单测：授权行 connection_id 非空、回调走更新分支）
- [ ] **Step 3: swagger 重新生成＋提交**

```bash
mise run swagger
git add packages/api packages/service
git commit -m "feat(api): add oauth callback and connection management routes"
```

## 完成标准

- `mise run test`全绿；并发刷新测试稳定通过（`go test -race -count=3 ./tokens/`）；
- 完整手工链路（假 provider 由测试覆盖，真实 provider 留待计划 6）：创建 api-key connection→`/admin/connections`可见；
- refresh token 轮转在并发下不丢失（spec §17「并发 Tool 调用触发 token 刷新时不丢失轮转的 refresh token」）；
- credential 明文与 refresh token 不出现在任何响应／日志。

---
