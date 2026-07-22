# connect-it 设计文档

- 日期：2026-07-22
- 状态：待评审
- 参考：OpenConnector（代码 Definition 为事实源、catalog 由代码派生、executor 懒加载）

## 1. 概述与定位

connect-it 是一个内部使用的有状态 Connector 服务：

- 用 Go 实现，为内部其他应用统一管理第三方平台（GitHub、Gmail 等）的授权凭证与 Tool 执行；
- 对 chatbot 等调用方只暴露一个聚合 MCP 端点（Streamable HTTP）；
- 附带一个 Vue 3 管理界面，供管理员配置 Connector、完成授权、查看健康状态。

明确不做的事：

- 多租户／多 workspace：整个部署只有一套配置；
- 用户所属权：`connections`等表不设 user_id，同类型多连接靠唯一`alias`区分；
- 动态调用上游`tools/list`生成工具；
- stdio、SSE 传输；未经声明的任意 MCP URL。

## 2. 三层模型

```text
ConnectorDefinition  代码内固定模板（编译进二进制，事实源）
ConnectorConfig      部署管理员填写的平台配置（数据库）
Connection           完成授权后的连接实例（数据库）
```

数据库不建 connectors catalog 表，不保存 name、description、categories、auth method、MCP server 或 Tool 定义。`connector_type`是稳定的平台标识（如`github`、`one_drive`），只用于查代码 Registry，不表达行为分类；一个 Connector 可拥有多种 auth method 并混合多种 Tool backend。

## 3. 技术栈

| 层 | 选型 |
|---|---|
| HTTP Server | Go＋Echo |
| 数据库 | PostgreSQL，驱动`pgx/v5`，查询由 sqlc 生成 |
| Migration | golang-migrate，`go:embed`进二进制，启动时自动执行 |
| MCP | 官方`github.com/modelcontextprotocol/go-sdk`（客户端与聚合服务端均用它） |
| API 文档 | swaggo/swag 注释生成 OpenAPI（swagger.json 提交进仓库），echo-swagger 暴露`/swagger/*` |
| TS SDK | `packages/sdk`：`@hey-api/openapi-ts`由 swagger.json 生成 TypeScript 客户端，web 经 workspace 依赖使用 |
| 前端 | Vite＋Vue 3＋Vue Router＋Tailwind |
| UI 库 | `memohai/ui`，git submodule 挂载于`packages/ui` |
| 包管理 | pnpm workspace |
| 部署 | `docker/`目录下 Dockerfile＋docker-compose |

Migration 文件命名（golang-migrate 要求 up／down 成对）：

```text
0001_init.up.sql
0001_init.down.sql
0002_具体描述.up.sql
0002_具体描述.down.sql
```

## 4. 仓库结构（MonoRepo，多 Go module）

Go 侧拆为四个 module，与前端包一起统一放在`packages/`下。不使用 go.work：module 间通过 go.mod 的`replace`指令按相对路径互相引用，replace 提交进仓库，clone 即可构建：

```text
connect-it/
├── package.json              # pnpm workspace 根
├── pnpm-workspace.yaml       # packages/ui、packages/sdk、packages/web
├── mise.toml                 # 工具链版本（go/node/pnpm）＋任务定义
├── packages/
│   ├── ui/                   # git submodule → github.com/memohai/ui
│   ├── web/                  # Vite＋Vue 3＋Vue Router 管理界面
│   ├── core/                 # Go module：Definition 类型、Registry、加密、状态机
│   │                         #   纯库，不依赖 Echo／pgx
│   ├── connectors/           # Go module：providers
│   │   ├── github/  gmail/  onedrive/  googleads/
│   │   └── all.go            # 显式注册
│   ├── service/              # Go module：sqlc store、migrations/、sqlc.yaml、
│   │                         #   OAuth、token 刷新、Tool 执行、聚合 MCP
│   ├── sdk/                  # TypeScript SDK：@hey-api/openapi-ts 由 api 的
│   │                         #   swagger.json 生成，pnpm workspace 成员
│   └── api/                  # Go module：Echo handlers、swag 注释与 docs/、
│                             #   cmd/connect-it（前端由独立 web 容器托管）
└── docker/
    ├── Dockerfile
    └── docker-compose.yml
```

- 依赖方向单向：`connectors→core`；`service→core＋connectors`；`api→service`。反向依赖视为架构违规。
- 仓库远端为`https://github.com/memohai/connect-it`，module 路径固定为`github.com/memohai/connect-it/packages/<name>`。
- `replace`只在被构建 module 的 go.mod 中生效，因此每个 module 须列出其**全部**本地依赖（含间接）：`connectors`replace `core`；`service`replace `core`、`connectors`；`api`replace `core`、`connectors`、`service`。
- 工具链与任务统一由根目录`mise.toml`管理：`[tools]`钉住 go／node／pnpm 版本（本地与 CI 都走`mise install`对齐）；`[tasks]`定义`build`／`test`／`lint`／`dev`／`sqlc`／`swagger`（swag 生成 swagger.json）／`sdk`（openapi-ts 重新生成 TS SDK）等任务。没有 workspace 后`go build ./...`不能跨 module，这些任务内部逐 module 执行，CI 直接调用同一组 mise 任务。
- `packages/ui`按其自身文档的消费方式使用：pnpm 按路径解析、不 build 不 publish、Tailwind 直接扫描其源码；Vue 3 为 peer dependency，版本由宿主 lockfile 决定。
- clone 与 CI 必须带`--recursive`／`submodules: true`。

## 5. Definition 组织与 Registry

每个 Connector 一个独立 package，全部位于`packages/connectors` module 内：

```text
packages/connectors/github/definition.go
packages/connectors/github/managed.go     # 仅存在 Managed Tool 时出现
```

约定：

- `definition.go`只声明固定数据，不访问数据库或网络；
- `connector_type`用 snake_case（`one_drive`、`google_ads`），包目录名用去掉下划线的形式（`onedrive`、`googleads`），映射关系在注册处显式声明，由单元测试校验目录与注册一一对应；
- 不使用`go generate`，不生成 catalog JSON。Registry 类型定义在`packages/core`，实例由手写的`packages/connectors/all.go`显式注册：

```go
func RegisterAll(r *registry.Registry) {
    r.MustRegister(github.Definition)
    r.MustRegister(gmail.Definition)
    r.MustRegister(onedrive.Definition)
    r.MustRegister(googleads.Definition)
}
```

校验分两层，规则相同：

- 启动时`registry.Validate()`全量校验，失败直接 panic；
- 单元测试遍历全部 Definition 跑同样的校验。

校验内容：connector type 唯一；配置字段、auth method、MCP server key、Tool ID 唯一；Tool 引用的 MCP server 或 Managed handler 存在；Secret 字段无真实默认值；无 stdio／SSE；Provenance 规则（见第 13 节）；命名字符集（见第 11 节）。

没有 Tools 的 Connector 允许注册，状态为`catalog_only`，可浏览可配置，不能授权 MCP Session 或执行 Tool。

## 6. Definition 类型

不定义单一`ConnectorKind`，用多个正交类型组合能力：

```go
type ConnectorDefinition struct {
    Type                ConnectorType
    Name                string
    Description         string
    Categories          []string
    HomepageURL         string
    IconURL             string
    ConfigSchemaVersion int

    ConfigFields     []ConfigFieldDefinition
    AuthMethods      []AuthMethodDefinition
    RemoteMCPServers []RemoteMCPServerDefinition
    Tools            []ToolDefinition

    ConfigUpgraders []ConfigUpgrader   // 按版本顺序的升级函数链

    Deprecated bool
}
```

（原计划中的`DefinitionVersion`删除：没有消费者，YAGNI。）

### 配置字段

```go
type ConfigFieldDefinition struct {
    Key          string
    Label        string
    InputType    ConfigInputType
    Required     bool
    Secret       bool
    DefaultValue *string   // Secret 字段禁止设置
    Description  string
    Validation   FieldValidation
}
```

固定的 MCP URL、OAuth endpoint、scope 直接写在 Definition 里；只有确实需要管理员填写的参数才声明为 ConfigField；默认值只允许非 Secret 字段（如 Microsoft tenant 的`common`）。

### 认证方法

```go
const (
    AuthNone             AuthMethodType = "none"
    AuthOAuth2           AuthMethodType = "oauth2"
    AuthAPIKey           AuthMethodType = "api_key"
    AuthCustomCredential AuthMethodType = "custom_credential"
)
```

- OAuth definition 固定包含：authorization／token／refresh endpoint、scopes、PKCE 要求、token endpoint auth method、provider 专属参数、credential profile resolver。
- `AuthNone`语义：该 Connector 的 Tool 执行不需要 Connection，运行时装配跳过 credential 步骤。

### Remote MCP

```go
type RemoteMCPServerDefinition struct {
    Key            string
    Endpoint       EndpointDefinition   // 二选一：代码固定 URL；或引用某 ConfigField
    AuthBinding    MCPAuthBinding       // 用户 credential 如何呈递（如 Authorization: Bearer）
    Provenance     MCPProvenance
    RequestTimeout time.Duration
}
```

仅允许 streamable_http，类型上不提供 stdio／SSE 选项。`Provenance`记录`official | third_party | self_hosted`、publisher、文档与源码地址、审核日期、允许的 hostname、`stable | preview | experimental`。

### Tool Backend

```go
type ToolDefinition struct {
    ID             string          // ^[a-z0-9_]+$
    Name           string
    Description    string
    InputSchema    json.RawMessage
    OutputSchema   json.RawMessage
    RequiredScopes []string
    Risk           ToolRisk
    Backend        ToolBackendDefinition   // tagged union
}

type RemoteMCPToolBackend struct {
    ServerKey       string
    RemoteToolName  string
    InputMapperKey  string
    OutputMapperKey string
}

type ManagedToolBackend struct {
    HandlerKey string
}
```

同一 Connector 可混合两种 backend。Handler／Mapper 用字符串 key 引用（保持 Definition 可序列化），引用完整性由启动校验与单元测试兜底。

## 7. 数据库

### 门禁（无用户体系）

服务是内部工具，不做用户所属权。门禁两张小表：

```sql
create table admin_account (        -- 单行
  id integer primary key check (id = 1),
  username text not null,
  password_hash text not null,      -- argon2id，不存明文
  updated_at timestamptz not null
);

create table api_tokens (           -- 内部应用机器调用凭证
  id uuid primary key,
  name text not null,
  token_hash text not null unique,  -- sha256
  created_at timestamptz not null,
  revoked_at timestamptz
);
```

管理界面用密码登录（HMAC 签名 cookie，secret 来自环境变量）；内部应用用静态 Bearer token 调`/v1/*`。两者都可在管理界面更改。

### ConnectorConfig

每个`connector_type`整个部署只允许一套配置：

```sql
create table connector_configs (
  connector_type text primary key,
  config_schema_version integer not null,
  public_config jsonb not null default '{}',
  secret_config bytea not null,          -- 允许空密文（无 Secret 字段的 Connector）
  secret_key_version integer not null,
  mcp_verified_endpoint text,            -- self_hosted endpoint 通过 verify 时记录
  mcp_verified_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null
);
```

新增 App ID、Tenant、Developer Token 等字段只改代码 Definition，不做数据库 migration。

### 状态表

```sql
create table connections (
  id uuid primary key,
  connector_type text not null,
  alias text not null unique,            -- ^[a-z0-9][a-z0-9-]{0,31}$
  auth_method text not null,
  credential bytea not null,             -- 加密
  secret_key_version integer not null,
  profile jsonb not null default '{}',
  scopes text[] not null default '{}',
  status text not null,                  -- active | reauth_required | disabled
  access_token_expires_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table oauth_authorizations (
  id uuid primary key,
  connector_type text not null,
  state_hash text not null unique,
  pkce_verifier bytea not null,          -- 加密
  connection_id uuid,                    -- 重授权时指向既有连接
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
  input jsonb,                           -- 截断存储（上限 64KB）
  output_summary text,                   -- 截断存储
  duration_ms integer,
  created_at timestamptz not null
);
```

- 这些表只保存稳定的`connector_type`字符串，不对代码 Registry 建外键；未知 type 的行保留（支持代码回滚）。
- `tool_runs`保留 90 天，启动时＋每日定时清理。
- 第一期不建`idempotency_records`（MCP 无标准幂等键来源；被重复执行问题实际咬到后再补）。

## 8. Secret 加密

- AES-256-GCM；KEK 为 32 字节，来自环境变量`CONNECT_IT_SECRET_KEY`；
- nonce 随机生成、前置存储在密文中；
- AAD 绑定防密文串换：`connector_configs`绑`connector_type`，`connections`绑 connection id；
- `secret_key_version`支持轮换：允许同时配置新旧两把 key，读旧写新，后台不强制批量重加密。

## 9. 状态机与运行时装配

运行状态由代码实时计算，输入为 Definition＋Config＋health 行：

| 状态 | 条件 |
|---|---|
| `catalog_only` | Definition 没有 Tools |
| `needs_config` | 缺少必填管理员配置，或 self_hosted endpoint 未通过 verify |
| `config_incompatible` | 数据库`config_schema_version`比代码认识的新（版本回滚场景），禁授权禁执行，保留配置 |
| `ready` | 配置完整且至少一个 Tool 可执行 |
| `degraded` | `connector_health.consecutive_failures >= 3`且`last_error_at`在 15 分钟内；一次成功即清零 |
| `deprecated` | Definition 标记弃用 |
| `definition_missing` | 数据库存在的 type 在当前代码版本不存在，禁授权禁执行，保留配置 |

`connector_health`没有后台探测任务，只有两个写入方：Tool 执行结果顺带写入（piggyback）；管理端`mcp:verify`主动验证。

每次使用 Connector 时动态装配，不把合并结果写回数据库：

```text
connector_type
  → 代码 Registry 取 Definition
  → connector_configs 取管理员配置
  → 按 ConfigSchema 校验、合并默认值（必要时先跑 ConfigUpgraders，升级结果在内存生效，下次保存落盘）
  → connections 取用户 credential（AuthNone 跳过）
  → 生成 ResolvedConnector
  → 执行 Remote MCP 或 Managed Tool
```

Definition 变更兼容规则：

- 改 name／description／categories：只改代码；
- 新增可选字段：只改代码；
- 新增必填字段：现有 Connector 自动变为`needs_config`；
- 删除字段：读取时忽略，下次保存清理；
- 重命名字段：提供 ConfigUpgrader，升级 JSONB 后更新`config_schema_version`；
- 删除 Tool：已有 MCP Session 下次调用返回`tool_unavailable`；
- 删除 auth method：相关 Connection 标记`reauth_required`；
- Connector 下线：先标记`deprecated`，不直接删 Definition。

## 10. OAuth 与 token 刷新

授权流程：管理界面发起→跳转 provider→固定回调`/v1/oauth/callback`→按`state_hash`核对`oauth_authorizations`、验证 PKCE→创建或更新 Connection。

**惰性刷新，对调用方完全透明**（refresh token 永不离开本服务）：

1. 执行 Tool 前加载 Connection，若 access token 已过期或将在 60 秒内过期，进入刷新；
2. 以 connection id 做进程内 single-flight，并发调用只有一个真正刷新，其余等待结果；
3. 开事务`SELECT … FOR UPDATE`重读该行，**先重查过期时间**——其他实例可能刚刷完，直接用新 token；
4. 确实过期才调 token endpoint，新 access token＋refresh token 一起写回、提交；
5. 刷新失败（refresh token 被吊销）→Connection 标记`reauth_required`。

之所以惰性刷新仍需并发控制：很多 provider 的 refresh token 一次性轮转，两个并发调用同时刷新会互相覆盖，导致连接作废。

不提供 credential 透传 API（见第 18 节）。

## 11. MCP 聚合端点与 Tool 执行

### Session

- `POST /v1/mcp-sessions`（api_token 鉴权）：传入 alias→connection 绑定列表＋Tool allowlist＋TTL（默认 1 小时，上限 24 小时），返回一次性 session token（库中只存 sha256）；
- Session 无法访问未绑定的 Connection 或 allowlist 之外的 Tool。

### 聚合 /mcp

- 官方 go-sdk 实现 Streamable HTTP 服务端，session token 鉴权；
- 暴露的工具名为`{alias}__{tool_id}`。alias 不含下划线、tool_id 不含连字符，因此`__`分隔可逆解析，且满足 MCP 工具名字符集。

### Remote MCP Tool 执行

1. 读取固定 Remote MCP Definition；
2. 合并允许使用的管理员配置；
3. 读取 Connection credential（先走第 10 节刷新流程）；
4. 官方 go-sdk 建立 Streamable HTTP 连接；
5. 调用固定映射的上游 Tool；
6. 校验、转换输出；
7. 关闭上游 MCP session。

每次调用新建并关闭上游 session，正确性优先；按（connection，server）缓存上游会话＋TTL 留作后续优化，不改变对外接口。

### Managed Tool 执行

1. 相同的 Definition、scope、风险检查；
2. 调用注册的 Go handler；
3. Handler 通过受控 HTTP Client 调 REST／GraphQL／SDK；
4. 返回与 Remote MCP Tool 相同的统一结果。

两种 backend 的执行结果统一 piggyback 写入`connector_health`与`tool_runs`。

## 12. HTTP API 一览

对内部应用（Bearer api_token）：

```http
GET  /v1/connectors                      # catalog＋状态合并，永不含 Secret
GET  /v1/connectors/{connector_type}
POST /v1/mcp-sessions
GET  /v1/oauth/callback                  # OAuth 回调（无鉴权，靠 state）
```

管理端（cookie session，`POST /admin/login`登录）：

```http
GET    /admin/connectors/{connector_type}/config    # Secret 只回显「已设置」标记
PUT    /admin/connectors/{connector_type}/config    # If-Match（updated_at）乐观并发
DELETE /admin/connectors/{connector_type}/config
POST   /admin/connectors/{connector_type}/config:validate
POST   /admin/connectors/{connector_type}/mcp:verify
GET/POST/DELETE /admin/connections…                 # 列表、发起 OAuth、提交 API key、删除、重授权
GET/POST/DELETE /admin/api-tokens…
PUT    /admin/account/password
```

`mcp:verify`做真实握手＋`tools/list`与 Definition mapping 比对，成功后写`mcp_verified_at`；self_hosted endpoint 变更后该标记重置。

聚合端点（session token）：

```http
POST /mcp
```

## 13. 自托管 MCP endpoint 规则

管理员填写的 MCP URL 绕过了「代码审查兑现 URL 可信」的路径，用三条规则约束：

1. 仅当 Definition 显式声明「endpoint 取自某 ConfigField」且该 server 的 Provenance 为`self_hosted`时，才允许从配置读取 endpoint；其余 Connector 一律硬编码；
2. 强制`https`；内网确需`http`时须显式打开非默认开关`allow_insecure_http`；
3. 填写或修改后必须通过`mcp:verify`才可用，否则停留在`needs_config`。

不做内网 IP 过滤：内部部署，管理员即运维，SSRF 不在威胁模型内。

## 14. 前端管理界面（packages/web）

页面：

- 登录；
- Connector 列表（状态徽标：ready／needs_config／degraded 等）；
- Connector 配置：由 ConfigFields 元数据驱动的动态表单，Secret 字段只写不读；
- 连接管理：发起 OAuth、提交 API key、重授权、删除；
- 健康与验证：connector_health 展示、触发 mcp:verify；
- API token 管理、修改密码。

规范约束：

- 严格遵守`packages/ui`的`AGENTS.md`与`skills/web/`规范，写前端代码前先读这两处；
- API 调用一律通过`packages/sdk`（`@hey-api/openapi-ts`从`packages/api/docs/swagger.json`生成，生成产物提交进仓库），web 内禁止手写 fetch 端点；OpenAPI 由 api 模块的 swag 注释生成，改路由必须`mise run swagger && mise run sdk`同步；
- Tailwind 配置扫描`../ui/src`；
- **前端独立容器部署**（2026-07-22 用户裁定，替代原 go:embed 方案）：构建产物由
  web 容器的 nginx 托管（SPA 回退），API 路径反代到 server 容器；开发模式 Vite
  proxy 到本地 server。

## 15. Docker

- `docker/server.Dockerfile`：在`packages/api`内`go build`（replace 解析本地依赖）→精简运行镜像；
- `docker/web.Dockerfile`：node＋pnpm 构建`packages/web`→nginx 镜像托管静态产物，`docker/nginx.conf`做 SPA 回退与 API 反代（`/mcp`关闭缓冲以支持 SSE）；
- `docker/docker-compose.yml`：`postgres:17`＋`connect-it`（server，不对外发布端口）＋`web`（对外 8080）三个服务；环境变量`DATABASE_URL`、`CONNECT_IT_SECRET_KEY`、`COOKIE_SECRET`等；
- 构建上下文为仓库根（需 submodule 已检出）。

## 16. 首批 Connector

| Connector | 配置字段 | 验证点 |
|---|---|---|
| GitHub | OAuth Client ID／Secret 可选，支持 PAT | 多 auth method；Remote MCP Tools |
| Gmail | Client ID、Client Secret、Project ID | 同一 Connector 混合 Remote MCP＋Managed Tool |
| OneDrive | Client ID、Client Secret、Tenant（默认`common`） | Managed Tools；非 Secret 默认值 |
| Google Ads | Client ID、Client Secret、Project ID、Developer Token、Customer ID、自托管 MCP URL | 公开／秘密扩展字段；self_hosted Streamable HTTP |

## 17. 验证标准

- 修改 Connector description、category、MCP endpoint 或 Tool mapping 不需要数据库 migration；
- 新增 Connector 只需新增 Definition package、可选 handler、`all.go`一行注册；
- 缺少 Tool 的 Connector 可浏览、可配置，但不可执行；
- Secret 不出现在 catalog、配置读取、日志或错误响应中；
- 未知`connector_type`不导致启动失败，也不误用其他 Definition；
- Config schema 升级不要求新增 Connector 专属数据库列；
- Remote MCP 与 Managed Tool 通过同一 Tool 调用接口工作；
- stdio、SSE 无法进入有效 Definition；self_hosted endpoint 未通过 verify 不可用；
- MCP Session 无法访问未绑定的 Connection 或未授权 Tool；
- 并发 Tool 调用触发 token 刷新时不丢失轮转的 refresh token；
- 数据库`config_schema_version`比代码新时安全降级为`config_incompatible`。

## 18. 第一期范围外

- 写操作幂等（`idempotency_records`）；
- 后台健康探测任务；
- 上游 MCP 会话缓存；
- 多租户；
- credential 透传 API（`GET /connections/{id}/credentials`类）；
- Connector 使用方 SDK。
