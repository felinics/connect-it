# connect-it 项目蓝图（Blueprint）

> 更新日期：2026-07-25。本文是整个项目的架构总览，也是架构层面的唯一事实源：定位、
> 模块划分、领域模型、关键流程、安全边界、部署与交付现状。行为细节以代码和测试为准；
> Provider 逐个的来源、限制与真实账号 smoke 证据以 `docs/providers/<type>.md` 为准。
> 本文只承载架构内容；`docs/` 下其余文档的职责边界与按任务导航见
> [文档索引](README.md)。

## 1. 定位：它是什么、不是什么

connect-it 是一个**内部使用的有状态 Connector 服务**（Go 单二进制 + Vue 3 管理台）：
集中保管第三方平台（GitHub、Gmail、Stripe…）的授权凭证，把各平台能力以统一的
Tool 形式经**一个聚合 MCP 端点（`/mcp`，Streamable HTTP）**暴露给内部应用/Agent，
另提供 `/v1` 机器 API 供业务后端管理连接与签发 MCP session。

三条产品级设计决策（贯穿全部实现）：

1. **代码即事实源**：Connector Definition（元信息、认证方式、Tool 与 JSON Schema）
   编译进二进制；数据库只存管理员配置与连接状态。新增/修改 Connector 不需要 DB migration。
2. **单租户、无用户模型**：一个部署一套配置；`connections` 表没有 user_id。
   **Connection ID 就是持久句柄**，属于哪个终端用户由调用方自己的库记录；alias
   只是可选、非唯一的展示标签。
3. **授权事实变化 → 一切旧授权 fail closed**：`authorization_generation` 是跨层
   失效总线（见 §4.2），MCP session 固化签发时的世代快照，凭证/配置/策略任何变化
   都让旧 session 立即失效，而不是静默降级。

**明确不做**：多租户/多 workspace、动态拉上游 `tools/list` 生成工具、
stdio/SSE 传输、任意 MCP URL、credential 透传 API、通用 proxy Tool。

## 2. 总体架构

### 2.1 部署拓扑（三容器）

```
浏览器 / 机器客户端 ──:8080──▶ web 容器（nginx 1.29-alpine）
    ├── 静态 SPA（Vue 3 管理台，try_files 回退 index.html）
    ├── location = /mcp     37MiB 粗 cap、请求/响应双向不缓冲（SSE）──▶ connect-it:8080
    └── /v1 /admin /healthz /swagger（默认 1MiB body cap）────────────▶ connect-it:8080
connect-it 容器（Go 单二进制，非 root，不对外发布端口）
    └──▶ postgres:5432（应用角色 connect_it_app，最小权限矩阵；
         每条池连接经 AfterConnect 门禁校验库身份 + 当前 fresh baseline）
```

- **server 容器零对外端口**：包括 `/v1`、`/mcp` 机器调用在内的全部流量都过 nginx。
- OAuth 回调与授权发起路径从 nginx access log 排除（query 里有 state/code）。
- Cookie `Secure` 由可信的 `CONNECT_IT_BASE_URL` 派生，**不信任 `X-Forwarded-Proto`**。

### 2.2 四个 Go module 与依赖方向

不用 go.work；module 间以 go.mod `replace ../` 相对路径互引（replace 提交进仓库），
clone 后即可构建（须 `--recursive` 带上 `packages/ui` submodule）。

```
core                纯库：Definition 类型 / Registry / AES-GCM keyring / Connector
 ▲   ▲              状态机，以及 core/providerkit —— Provider 出网运行时
 │   │              （受控 HTTP client，进程内唯一出网口）
 │   connectors     9 个 Provider 的 Definition + handler/validator
 │        ▲
 service  │         有状态业务层：sqlc store、OAuth、刷新、session、执行引擎
 ▲        │
 └─ api ──┘         Echo 路由 + MCP 端点 + swagger；cmd/connect-it 是唯一组合根
```

依赖方向由测试硬性锁定：core 生产代码只能 import 标准库 + `google/jsonschema-go`
（`packages/core/dependency_direction_test.go`）；service **禁止** import connectors
（`packages/service/dependency_direction_test.go`）——Provider 差异全部以函数 map
（`connectors.RuntimeBundle`：Handlers，以及只含 CredentialValidators /
ScopeMatchers 的 AuthorizationRuntime）在组合根注入。

### 2.3 组合根启动序列（`packages/api/cmd/connect-it/main.go`）

必填 env 校验 → 解析加密 keyring → pgxpool（AfterConnect 挂数据库身份/权限/schema
门禁，见 §7.4）→ providerkit Factory（读两个出网开关）→ `connectors.NewRuntime` +
`RegisterAll`（非法 Definition 启动即 panic）→ **双预检**（固定 Remote MCP 与 OAuth
endpoint 的网络策略，任一失败 fatal）→ `ReconcilePolicyIdentities`（Definition 漂移
则 bump 相关连接的 generation）→ 装配全部 service → OAuth 维护循环（每分钟 janitor）
→ admin seed（首启用 `CONNECT_IT_ADMIN_PASSWORD` 建 `admin` 账号）→ HTTP serve →
优雅退出。**生产启动从不执行 migration，只校验。**

### 2.4 TypeScript 侧

- `packages/sdk`：`@hey-api/openapi-ts` 从 `packages/api/docs/swagger.json` 生成的
  fetch 客户端，纯生成物、提交入库，CI 用 `git diff --exit-code` 门禁防漂移。
- `packages/web`：Vite + Vue 3.5 + Tailwind 4 管理台；无全局状态库，页面 onMounted
  自拉数据；一切请求经 `src/api/endpoints.ts` → SDK（禁止手写 fetch）；HttpOnly
  cookie 会话 + 静态 `X-Connect-It-CSRF: 1` 同源证明；secret 一律 write-only 不回显。
- `packages/ui`：`@felinic/ui` 设计系统，git submodule（pin commit），以源码方式被
  workspace 消费，host 的 Tailwind 直接扫描其 src。

## 3. 核心领域模型

### 3.1 三层模型

```
ConnectorDefinition   代码内固定模板（编译进二进制，Registry 注册时全量校验+深拷贝不可变）
ConnectorConfig       管理员平台级配置（connector_configs 行；secret 走 AES-GCM 密文列）
Connection            授权后的连接实例（connections 行；credential 密文 + 版本号组）
```

Definition 的关键结构（`packages/core/connector/types.go`）：

- **AuthMethod** 四型：`none` / `oauth2` / `api_key` /
  `custom_credential`。oauth2 支持 PKCE、显式 public-client（`TokenAuthNone`必须
  显式声明，不从缺 secret 推断）、标准 JSON token response，以及经过审查的静态
  authorization/token/refresh endpoint 与 canonical origin。
- **Tool**：`ID`、Input/OutputSchema（受审 keyword 子集的 JSON Schema，注册时编译）、
  `RequiredScopes`、`Risk`（read/write/destructive 三档）、`MaxInputBytes`
  （默认 5MiB，硬上限 32MiB）、`Backend`（tagged union：`ManagedBackend{HandlerKey}`
  或 `RemoteMCPBackend{ServerKey, RemoteToolName}`）。
- **RemoteMCPServer**：fixed endpoint（须 https、Provenance official/third_party）或
  config_field endpoint（**仅 self_hosted** 允许，须过 `mcp:verify`）；Provenance
  只保存运行时安全决策需要的 kind 与 fixed endpoint hostname allowlist。
- **Provider 来源文档**：`docs/providers/<type>.md`的 Source 段保存官方文档、
  API 版本、review date，以及钉死 commit SHA、实际审阅路径和许可证的参考实现。
  这些治理事实不进入 runtime Definition。

### 3.2 三种令牌、三个互不越权的 API 面

| 调用方 | 凭证 | 路径 | 说明 |
|---|---|---|---|
| 管理台 | HMAC 签名 cookie（HttpOnly/SameSite=Strict/24h）+ CSRF header | `/admin/*` | 单管理员 `admin`，argon2id 密码 |
| 内部应用后端 | `cit_` 前缀 API token（库存 sha256，明文只显示一次） | `/v1/*` | 机器 API |
| MCP 客户端 | 短期 MCP session token（库存 sha256） | `/mcp` | 默认 1h、上限 24h |
| （无凭证） | 一次性 OAuth state | `/v1/oauth/callback` | 公开回调 |

### 3.3 MCP Session：一次性、不可变的能力令牌

`POST /v1/mcp-sessions` 提交 `{connections: {alias→connection_id}, tool_allowlist,
ttl_seconds}`；工具暴露名为 `{alias}__{tool_id}`。三个刻意的语义：

- **allowlist 三态**：省略 = 只展开签发当时的 read Tool；显式 `[]` = 零 Tool；
  `null` 拒绝。write/destructive 必须显式列出。grant 以带版本的不可变快照
  （versioned envelope）持久化，同时固化每个绑定的 `authorization_generation`。
- **签发后不可变、不可主动吊销**：全系统没有 revoke API；数据库权限矩阵对
  `mcp_sessions` 只授 SELECT+INSERT，物理上写不了。终态只有 ① TTL 过期（DB 时间
  判定，每请求重查，无进程缓存）② generation/状态失效（等效死亡）。吊销语义被
  上移：换 credential、reauth、删连接（单连接粒度）或改动 policy 相关配置
  （connector 粒度，bump 该类型全部连接）即可让相关 session 立即失效。
- token 泄漏的最大暴露面 = 固化 grant × 绑定连接 × 剩余 TTL。

### 3.4 三个版本号（`connections` 行）

| 字段 | 语义 | 推进时机 |
|---|---|---|
| `credential_version` | 凭证写入计数，refresh CAS 用 | 每次凭证写入（含普通 token refresh） |
| `authorization_generation` | **授权事实世代**，session 失效总线 | 重授权完成、换 key、scope/token scheme 变化的 refresh、policy identity 变化 |
| `authorization_attempt_version` | OAuth 尝试单调版本 | Begin=1，每次 BeginReauth 原子 +1 |

Token-only 的 refresh 只推进 credential_version，不动 generation——旧 session 继续可用。

### 3.5 Connection 状态

`connections.status` 是代码级封闭集合（DB 无 CHECK），只有三个值：

- `pending`：仅 initial OAuth 流产生；退出路径是**删除**（失败/过期/被
  supersede）或 Complete 置 active。不可换密、不可绑 session。
- `active`：可绑 session、可执行。api_key 连接创建即 active（先验证后落库）。
- `reauth_required`：只能由**确定性失败证据 + 版本快照 CAS** 写入（refresh 拿到
  invalid_grant、执行期 Provider 明确报告凭证失效、过期且无 refresh token、
  uncertain lease 超时）。reauth 失败**永不**降级/删除 active 连接。

另有正交的 refresh 子状态机 `refresh_state ∈ {NULL, leased, requesting}`（有 CHECK）。
Connector 级状态（`ready`/`needs_config`/`degraded` 等七态）由 `core/status.Compute`
纯函数实时计算，不落库。

## 4. 关键运行时流程

### 4.1 OAuth 授权状态机（`service/oauthsvc` + `store/queries/authorization_state.sql`）

数据库是唯一并发仲裁者：所有迁移都是 SQL 级 CAS + DB 时间；**网络 I/O 绝不在
DB 事务/行锁内**。

1. **Begin**（initial）：随机数/PKCE/加密/URL 全部先于 INSERT；单条 CTE 语句同时插
   pending Connection 与授权行（任一失败不留孤儿），并对 `connector_policy_identities`
   做 `FOR SHARE` 快照绑定（与配置写者的 `FOR UPDATE` 线性化）。授权上下文
   （PKCE verifier、expected generation 等）AES-GCM 加密，AAD 绑 authorization
   ID；state 库中只存 sha256。
2. **BeginReauth**：单语句 CAS 推进 attempt+1、generation+1 并插入 reauth 授权行——
   新尝试立即取代旧尝试（旧 session 同步失效）。
3. **Callback**：非消费性读取拿 redirect_url → **Claim 是唯一允许进入网络 token
   交换的迁移**（state 命中 + 未过期 + attempt/generation 完全一致）→ token
   交换（静态 origin、RedirectDenyAll、1MB 响应
   上限、核心参数最后写防 extras 覆盖）→ **validate-then-activate**：注入的
   Provider validator 证明身份/权限，scope 取协议结果与 validator 证明的**交集**
   （只能收窄）→ `CompleteCAS` 原子写凭证、置 active、credential_version+1、
   **generation 再 +1**。失败：initial 删 pending（FK 级联），reauth 只标记失败。
4. **Janitor**：每分钟 `MaintainAuthorizations` 按 DB 时间清理过期/stale/superseded
   尝试（authorizationTTL 10min + claimed 恢复宽限 2min）。

### 4.2 OAuth token 刷新（`service/tokens`，DB lease）

执行期惰性刷新：距过期 >60s 直接用；否则进程内 singleflight + **DB lease**
（`AcquireRefreshLease`，90s，DB 时间）→ 标记 `requesting` **提交后**才发起网络请求
→ 成功 `CompleteRefreshLeaseCAS`（cv+1；授权事实变化才 gen+1 并向调用方返回
`ErrAuthorizationChanged`）。**rotating refresh token 绝不重放**：只有 httptrace 证明
请求确未写出才允许清 lease 立即重试；uncertain 一律留在 requesting，由 DB 过期直接
进入 `reauth_required`（默认不重放策略）。

### 4.3 Tool 执行管线（`service/exec/engine.go`，顺序即安全边界）

```
32MiB 协议硬上限（任何解码/DB 之前）→ grant 形状校验 → 读 Connection/Definition
→ per-tool MaxInputBytes（超限不解码不记录）→ 授权栅栏①（重读 DB：session active
未过期 + connection active + 绑定 generation == 当前 generation）→ schema 归一化
（安全整数 ±2^53-1、有限 float、应用默认值并校验）→ scope 检查 → 凭证装配
（OAuth 惰性刷新 / api_key 解密）→ 授权栅栏②
（backend 调用紧前，用可能被刷新推进过的 generation）→ Managed handler 或
Remote MCP 调用 → 结果校验（输出 schema、Failure 形状）→ Provider 明确报凭证失效
则按执行时精确版本 CAS 置 reauth_required → 审计 + 健康记录
```

- 审计**零内容**：`tool_runs` 只存字节数、schema 声明且实际出现的顶层字段名、
  结果类型、稳定错误码、上游状态——不存参数值、动态 key 或 Provider 原始响应。
  仓库从未发布，fresh baseline 从第一条记录起执行该规则，不存在历史 scrub/收据链。
- 健康只记 backend 真正被调用后的成败；上游 4xx 证明可达不计入失败；配置类拒绝
  不污染健康数据。
- Remote MCP：每次调用新建并关闭上游 session（≤32 并发）；上游 IsError 载荷视为
  不可信材料全部丢弃，只回稳定 ToolFailure；带凭证的 401 升级为凭证失效信号。
- **输出契约与 backend 类型无关**：`callManaged` 与 `callRemote` 收敛到同一个
  `finish()`，它无条件调用 `validateBackendResult(result, schemas.Output)`
  （`exec/schema_pipeline.go`）——**声明了 `OutputSchema` 就校验，没声明就跳过**，
  校验不过坍缩为 `invalid_response`。所以 Remote MCP 型 Tool **同样可以声明
  `OutputSchema` 并拿到输出契约保护，无需任何引擎改动**；当前 7 个 Provider 的
  50 个 Remote Tool 无一声明（各 `definition.go` 包注释写明原因：缺真实响应采样），
  是取证成本问题，不是架构限制（补写前提见
  [模板指引](templates/provider/README.md#给-remote-mcp-tool-补-outputschema-的安全做法)：
  必须先采真实响应，猜错 schema 会把可用 Tool 写死）。要**重塑**输出形状则另说：
  `RemoteMCPBackend` 的 `Input/OutputMapperKey` 尚未实现，非空值在 backend 触达前
  被显式拒绝。

### 4.4 `/mcp` 端点（`packages/api/mcp.go`）

Stateless Streamable HTTP：每请求 `sessions.Resolve` → 按 session 绑定与 allowlist
**动态构建一次性 MCP server**（连接已删/Definition 缺失则不暴露该工具；grant 的
Risk 必须与当前 Tool Risk 相等——Definition 升险后旧 session 自动收窄）→ 名字在
Grants 中但 Definition 已下线 → 工具级 `tool_unavailable`（而非协议 unknown tool）。
鉴权刻意先于 body 准入：4 个并发 decode slot × 36MiB 精确上限 × 25s 读 deadline，
slot 持有到 SDK 返回。

## 5. 数据库（fresh baseline）

数据库只支持空库初始化；旧数据库必须重建，不提供原地保数据升级。仓库只有一个
canonical v1 baseline 和一个 clean marker，不存在历史 upgrade/down migration。
稳定领域表如下：

| 表 | 要点 |
|---|---|
| `admin_account` | 单行（check id=1），argon2id |
| `api_tokens` | sha256 hash unique，revoked_at |
| `connector_configs` | connector_type PK；public_config jsonb + secret_config AES-GCM 密文 + secret_key_version；mcp_verified_endpoint/at |
| `connector_policy_identities` | policy identity/definition 双 sha256 digest；**删配置后行仍存活**，保证无配置 connector 也有可加锁的策略世代边界 |
| `connections` | credential 密文、profile、scopes(+scopes_known)、status、三版本号、refresh lease 三字段 |
| `oauth_authorizations` | state_hash unique、加密 context、attempt_version、expected_generation、flow_kind |
| `mcp_sessions` / `mcp_session_connections` | token sha256、versioned allowlist 信封、绑定行固化 generation；binding 对 connections **无 FK**（删连接留悬空行，三层运行时兜底） |
| `connector_health` | 连续失败计数；无后台探测，只有执行 piggyback 与 mcp:verify 写入 |
| `tool_runs` | 仅元数据审计（error_code、upstream_status、字节数、字段名） |

加密模型：AES-256-GCM 多版本 KEK keyring（`CONNECT_IT_SECRET_KEY="1:<hex>,2:<hex>"`），
**写恒用最高版本、读按行存版本**；AAD 绑行身份（connector_type / connection id /
authorization id），防密文跨行移植。

## 6. 安全模型（纵深）

1. **单一出网口**：providerkit guarded transport 是进程内 Provider egress 唯一边界
   （Managed、validator、OAuth exchange/refresh/profile、Remote MCP、公共下载五类
   路径全覆盖）；全仓 AST guard 测试禁止 `http.DefaultClient`/裸 `http.Client`。
   SSRF 防御：URL 规范化（拒 punycode/非 canonical IP/多重编码路径穿越）→ canonical
   origin 白名单 → IP 分类（loopback/私网/云 metadata/IPv6 transition 全拒，IPv6
   公网还须命中 IANA 已分配前缀白名单）→ **DNS 解析结果全组校验后 pin 到 dial**
   （消除 rebinding TOCTOU；`Proxy: nil` 忽略环境代理）。重定向每跳全套重查，
   https→http 永拒，跨 origin 后永久剥离凭证 footprint。逐条规格见
   [Provider 出站网络安全](provider-egress-security.md)。
2. **双钥匙 self-hosted opt-in**：私网访问 = Definition/Policy `SelfHostedOptIn`
   ∧ `CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK`；明文 HTTP 再加
   `CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP`。env 只接受精确小写 true/false，拼错
   拒启；零值即最严（PublicOnly）。
3. **凭证最小暴露**：Authorizer 的 `CredentialFootprint` 是硬契约（快照比对拒绝
   未声明的改动），同一 footprint 驱动重定向剥离与日志脱敏；secret 配置字段
   只回 key 列表；错误 message 有界（≤512B）且绝不含 Provider 原始 body；17 个
   稳定 FailureCode 是对外失败面的封闭词汇表，非法结构坍缩为 `internal_error`。
4. **显式优于推断**：public OAuth client 与 Remote MCP credential 绑定必须显式
   声明；403/普通授权失败**故意 state-neutral**，只有 Provider 明确报告凭证吊销
   才触发连接状态迁移。
5. **生产数据库门禁**：每条池连接 AfterConnect 校验库名/schema/系统标识符/角色
   无特权/权限矩阵**精确相等**（多一项也失败）/schema 命中当前 fresh baseline；
   期望值来自
   5 个 `CONNECT_IT_EXPECTED_DATABASE_*` 独立 env，**从不从 DATABASE_URL 推断**。
   双角色模型：`connect_it_owner`（nologin，持有对象）/ `connect_it_app`（login，
   逐表最小授权：tool_runs 仅 INSERT、session 表仅 SELECT+INSERT）。
6. **入口分层限额**：nginx 37MiB 粗 cap（413 改写为与 Go 同构的 JSON）→ Go 36MiB
   精确 + 4 slot + 25s 读 deadline → Engine 32MiB → per-tool 5MiB 默认。超时阶梯
   25s < 30s(Go Read) < 35s(nginx body)；nginx proxy read 300s < Go Write 310s。
   这些常量被 Go 测试直接读取 `docker/nginx.conf` 断言排序（跨制品契约测试）。

## 7. 构建、测试与交付

### 7.1 生成链（改路由后必跑）

```
store/queries/*.sql ─ mise run sqlc ────▶ packages/service/store/*.go
handler swag 注释 ─── mise run swagger ─▶ packages/api/docs/swagger.json
swagger.json ──────── mise run sdk ─────▶ packages/sdk/src/*.gen.ts
```

全部生成物提交入库；CI 重新生成后 `git diff --exit-code` 门禁。swagger 之上另有
`swagger_contract_test.go` 把语义契约（admin 不暴露 caller redirect、CSRF 必填、
TTL 上限、错误码全集）钉进测试——机械同步 + 语义正确双层保障。

### 7.2 测试策略

- 默认档位是**真 service 栈 + 真 Postgres**（`testutil.NewDB`：`TEST_DATABASE_URL`
  未设 skip；每测试随机 schema + fresh baseline + CASCADE 清理）。注入面极窄：整个
  api 层只有 `api.ToolExecutor` 与 `catalogsvc.MCPToolLister` 两个接口可 fake，
  其余依赖都是具体 service 类型。
- 假 OAuth provider（`testutil.NewProviderServer`）只重定向物理拨号，DNS pin/TLS/
  origin 校验按生产路径真实执行。MCP 测试用官方 go-sdk client 保证协议合规。
- 契约测试文化：目录名↔connector type、Provider 文档↔Definition（文档与 Definition
  一一对应，且每个 Tool ID 必须出现在文档里，`documentation_contract_test.go`）、
  runtime bundle 完整性（handler/validator 无遗漏无孤儿）、依赖方向、全仓 HTTP
  guard——大量架构约定以测试形式锁定。
- 真实基础设施合同：`mise run docker-ingress-test` 起真 Docker/nginx 栈验证 MCP
  大 body、流式、并发预算、日志无 secret。

### 7.3 CI（单 job）

vet → 逐 module test（core 与 connectors 额外跑一遍 `-race`，connectors 跳过
`RealSmoke$`）→ 生成物一致性 + `go mod tidy -diff` → web 测试/构建 → release Compose
静态 gate → 构建双生产镜像（revision label 绑 commit）→ 用刚构建镜像跑真实
ingress 合同。action 全部 SHA-pin。

### 7.4 数据库初始化与发布

1. **生产 server：只校验，绝不迁移**；每条池连接继续执行数据库身份、权限与
   baseline shape 门禁。
2. **dev/CI 与生产 bootstrap**：仅支持**完全空库**创建 canonical baseline；命中
   当前 baseline 时可 no-op，任何 legacy/nonempty shape 一律 fail closed 并要求
   重建。dev Compose、mise 与 ingress 共用一份固定
   `scripts/configure-dev-database.sql`，不再扫描或改写历史 ACL。
3. **实现状态**：baseline squash、fresh-only 初始化和 dev provisioning 已收敛；
   生产命令与边界以`docs/runbooks/fresh-database-bootstrap.md`为准。release
   compose 只接受`@sha256`不可变镜像 digest，并保持必填环境变量 fail closed。

## 8. Provider 形态选型：Remote MCP 优先

新增 Provider 的**第一个决定**不是写代码，是选形态。两种形态的成本差 4.5 倍
（实测：notion/stripe/linear/gitlab 四个从 Managed REST 改成 Remote MCP，
9,596 → 2,123 行，**省 7,473 行**、−78%，而 tool 数从 27 涨到 41），选错的代价在整个
Provider 生命周期里持续付。

**默认走 Remote MCP。** 依据：
- 2026-07-24 对 139 个常见 SaaS 的调研（`docs/provider-mcp-decision-table.md`）显示
  **61% 已有可用的官方 Remote MCP**；
- 走 MCP 时厂商维护 tool 定义，我们不写 client、不挑工作流、不写响应投影，
  也不必跟着对方的 REST API 变更走；
- 本服务的差异化（Connection ID 即句柄、下游自主多租户）在两种形态下**完全等价**，
  差异化在 connection 模型而非 client 实现，不受形态影响。

### 8.1 降级到 Managed REST 的条件

命中**任意一条**就降级到 Managed REST。每条都对应真实案例，不是假想：

| # | 条件 | 真实案例 |
|---|---|---|
| 1 | 厂商 MCP 仍是 preview / 明说不用于生产 | OneDrive：微软 Work IQ 明写 “aren’t meant for production use” |
| 2 | 有硬性功能天花板 | OneDrive：所有文件读写 ≤5MB，对文件连接器是绝路 |
| 3 | 需要高门槛许可证或套餐 | OneDrive：M365 Copilot 许可证 + Entra 注册 + 管理员逐 server 授权 |
| 4 | 只有 stdio 或 SSE，没有 Streamable HTTP | 见 §1「明确不做」——本服务不支持这两种传输 |
| 5 | 拿不到账号，写不出准确定义 | 无 token 时只能推导 schema，复杂 tool 会不可用（见 8.2） |
| 6 | 必须精确控制输出形状 | 需要稳定 Agent 契约、或必须防止上游字段泄漏时 |

**四条可用性标准**（全表引用为「§8.1 四条标准」）：**“厂商有官方 MCP” ≠ “我们能用”**，
必须同时满足 ① 厂商官方维护、② 厂商远程托管、③ Streamable HTTP 传输、
④ 认证可用单个 `Authorization` header 表达。只能 `npx` 跑的本地 stdio 包不算。
四条全过只说明**协议层可用**，是否真的走 Remote MCP 仍由上表六条降级条件决定
（`one_drive` 就是四条全过、却按第 1/2/3 条降级为 Managed REST 的例子）。

### 8.2 tool 定义从哪来（三条路，成本递增）

Remote backend 是**参数直通**：Definition 里的 InputSchema 会原样成为调用上游的参数。
tool 名错 → 调用失败；参数名/类型错 → 上游报错。**猜出来的 schema 比没有 schema 更糟**，
因为它看起来能用。

| 来源 | 需要 token | 准确度 | 适用 |
|---|---|---|---|
| `mise run mcp-probe` 拉 `tools/list` | **是**（一次性） | 权威 | Remote MCP，唯一准确来源 |
| 厂商官方文档 / 第三方 client 对**同一 endpoint** 的 `tools/list` 快照 | 否 | tool 名可信，参数常缺 | Remote MCP 的过渡方案（linear 走的就是这条） |
| OpenAPI 规范生成 | 否 | 准确 | **仅 Managed REST**（MCP tool 参数 ≠ REST API 参数） |

`mcp-probe` 与生产走同一个 `mcpclient`，因此**探到的 tool 名就是运行时会拿到的名字**，
不存在探测与运行时不一致。它是只读的（只问 tools/list，不调用任何 tool）。

厂商 MCP 文档普遍不完整——Linear 官方文档明确不列 tool 名与参数，Notion 的远程 server
零参数文档。这是 MCP 生态早期的普遍状态，会随时间改善，但现在必须靠 probe 补齐。

### 8.3 已知陷阱（都踩过）

- **同名不同代**：Notion 有三代 MCP，tool namespace 零重叠（本地 v1 `API-post-search`
  / 本地 v2 `query-data-source` / 远程 `notion-search`）。改 Provider 时只看厂商
  **远程 server** 文档，不要参考其开源本地版仓库。
- **tool 名可能随客户端而变**：Notion 对 OpenAI 客户端把 `notion-fetch` 暴露成裸
  `fetch`。本服务以通用客户端身份连接（`mcp.Implementation{Name: "connect-it"}`），
  应当拿到带前缀的一套，但需 probe 实测确认。
- **上游自身命名不一致要照抄**：GitLab 的 `create_workitem_note`（无下划线）与
  `link_work_items`（有下划线）并存，是上游真实存在的不一致，不要“顺手纠正”。
- **不要发会改名的 header**：GitLab 的 `X-Gitlab-Mcp-Server-Tool-Name-Prefix`
  会给所有 tool 名加前缀，硬编码的 RemoteToolName 会全部失配。
- **scope 不蕴含**：GitLab 的 `api` scope **不**蕴含 `mcp`，凭据探针也必须打
  `/oauth/token/info` 而非 `/api/v4/user`——后者会 403 掉只带 mcp scope 的合法 token。

### 8.4 配套：漂移检测

走 Remote MCP 的代价是厂商改 tool 形状我们不会知道。因此每个 Remote Provider 应定期
对 `tools/list` 做快照 diff，变化转成可评审的 CI 信号，而不是等运行时失败。
**运行时动态拉 `tools/list` 生成工具仍然禁止**（见 §1）——构建期拉取、提交入库、
经 review 后 `go:embed`，与该禁令不冲突：前者让 Agent 契约随上游静默漂移，后者不会。

## 9. 新增 Provider 清单（第 1/4/5/7 条由契约测试强制，其余由 REVIEW_CHECKLIST 把关）

1. 建目录 `packages/connectors/<type去下划线>/`；
2. `definition.go`：Definition（封闭 InputSchema；managed Tool 须 OutputSchema，
   Remote MCP Tool 的 OutputSchema 可选但**一旦声明必须来自真实响应采样**；首版
   Tool 收敛到高频工作流——Managed REST 3–8 个，Remote MCP 取上游列表里评审过的
   子集（stripe 13 选 5、notion 18 选 10）；只保存运行时行为合同）；
3. `validator.go`：每个可验证 auth method 一个 CredentialValidator（复用
   `internal/credentialvalidator`）；
4. 有 managed Tool 则加 `managed.go`（请求构造复杂时再拆 `client.go`；HandlerKey
   与 Definition 一一对应，多/少都被 `validateRuntimeBundle` 拒绝）；
5. `all.go` 注册（import、NewRuntime 装配、definitions() 各一行）；
6. 常规单测；能写出真机冒烟 harness 就加 `real_smoke_test.go`，写不出（如纯 Remote
   MCP 的 gitlab/notion）就在 Provider 文档的 smoke 表里显式记 `PENDING`；
7. `docs/providers/<type>.md`的 Source 段记录官方文档、API 版本、review date、钉死
   commit 的参考实现、实际审阅路径和许可证；同一文档的 smoke evidence 表按最新在前
   记录账号/认证类别、API 版本、结果与清理状态，未执行项写`PENDING — not run`。

真实账号冒烟只能经 `scripts/run-provider-real-smoke.sh`（clean HEAD 绑定、TSV
receipt、写操作显式 opt-in）。逐项 review 门槛见
`docs/templates/provider/REVIEW_CHECKLIST.md`，它是 Provider PR 的冻结验收清单。

## 10. 现状与路线图（2026-07-25 口径）

- **已注册 9 个 Provider**（形态选型见 §8）：

  | Provider | 形态 | Tool | 备注 |
  |---|---|---|---|
  | GitHub | Remote MCP（固定 endpoint） | 5 | 官方 `api.githubcopilot.com/mcp/` |
  | GitLab | Remote MCP（**self-hosted** endpoint） | 14 | `{instance}/api/v4/mcp`，需 `mcp` scope + Duo 开启 |
  | Linear | Remote MCP（固定 endpoint） | 12 | `mcp.linear.app/mcp`，OAuth 或 Personal API key |
  | Notion | Remote MCP（固定 endpoint） | 10 | `mcp.notion.com/mcp`，仅 OAuth（不收长期 integration token） |
  | Stripe | Remote MCP（固定 endpoint） | 5 | `mcp.stripe.com`，建议 Restricted API Key |
  | Google Ads | Remote MCP（**self-hosted** endpoint） | 2 | 用户自带 endpoint |
  | Gmail | 混合（2 Remote + 2 Managed） | 4 | `send_message` 为 destructive |
  | OneDrive | Managed REST | 2 | 微软 MCP 仍 preview + 5MB 上限，见 §8.1 |
  | Datadog | Managed REST | 5 | **尚未按 §8.1 四条标准核实是否有官方 MCP** |

  Remote MCP tool 合计 50 个、Managed tool 9 个。notion/stripe/linear/gitlab 四个
  原为 Managed REST（合计 9,596 行），2026-07-25 按 §8 改写为 Remote MCP（2,123 行）。
  **InputSchema 全部是近似值，欠校准的是 7 个 Provider 而不只是改写的这四个**：tool 名
  可信（notion/gitlab/stripe 来自厂商官方文档，linear 来自两个第三方 client 对同一
  endpoint 的 `tools/list` 快照；github/gmail 未标出处），参数由文档/开源源码推导、
  schema 一律 `additionalProperties: true`，**50 个 remote tool 无一经 `mcp-probe`
  对真实 endpoint 采样校准**（见 §8.2；逐项状态见决策表 §5.5）。
- **production_supported 数量为 0**：9 个全部处于 `implemented_acceptance_pending`；
  **真实账号 smoke 目前一个都没有**——GitLab 那次 2026-07-23 的 PASS 属于已删除的
  guarded-REST 实现，不作为当前 Remote MCP 实现的覆盖（`docs/providers/gitlab.md`
  明确不结转）。此外仍欠 immutable commit 绑定的 CI、真实 ingress 证据和目标环境
  发布 gate。
- **交付状态词汇**（只有 `production_supported` 可以对外简称 supported；注册、
  preview、Definition-only 或确定性测试通过都不能提升状态）：

  | 状态 | 含义 |
  |---|---|
  | `implemented_acceptance_pending` | 已有代码/注册和确定性测试，外部 gate 或真实账号验收未完成 |
  | `blocked_external_approval` | 代码可继续准备，生产交付被 app/client/Marketplace/权限审核阻塞 |
  | `planned` | 尚无可注册的 Connector 实现 |
  | `production_supported` | REVIEW_CHECKLIST、真实 smoke、外部 gate 和发布验收全部完成 |

  状态词汇目前只活在本文与 `REVIEW_CHECKLIST.md` 里：Provider 文档刻意不再充当
  发布状态机（见 `documentation_contract_test.go`），只以 Smoke records 表记录证据。
- tenant OAuth、签名 callback、install proof、resource binding 与非标准 token
  adapter 没有生产消费者，对应的平台扩展点已删除。新 Provider 若首次需要这类能力，
  在自己的 Provider 代码内实现最小安全版本并单独评审，出现稳定重复后才泛化。
- 未实施的 Provider 一律是候选池，不预建“进行中”Connector：先有真实需求或使用数据，
  再按第 9 节清单和 REVIEW_CHECKLIST 单独交付。

## 11. 已知缺口与债务（蓝图如实记录）

| 缺口 | 现状 |
|---|---|
| KEK 轮换无运维闭环 | 机制完备（写新读旧、逐行版本号）但**无后台 rewrap、无存量观测、无 runbook**；不活跃行无限期滞留旧 key 版本，v1 下线只能人肉 SQL 盘点。重存一遍 connector 配置是无副作用的手动 rewrap 手段（未文档化） |
| 过期 session 行永不清理 | app 角色无 DELETE 权限，`mcp_sessions`/`mcp_session_connections` 无限增长，需 owner 角色带外维护 |
| `tool_runs` retention 未接入 | 目标 90 天，清理调度未实现 |
| README 环境变量表不全 | 5 个 `CONNECT_IT_EXPECTED_DATABASE_*` 生产必填但只见于 release compose 与 main.go |
| OutputSchema 债务 | 两类：① gmail/one_drive 的 4 个 managed Tool 有 OutputSchema 但未封闭（白名单 `openManagedOutputSchemaDebt` 显式列在 `connectors/all_test.go`，正确修法是像 datadog 那样加 output projector）；② 7 个 Provider 的 **全部 50 个** Remote MCP Tool 都未声明 OutputSchema —— 引擎已支持（见 §4.3），缺的是真实响应采样，不可凭猜补 |
| AuthNone 虚拟连接生命周期 | 未实现，等第一个真实 No Auth Provider |
| mapper registry | 非空 mapper key 在 backend 触达前明确拒绝（首版只做参数直通）。**只影响「重塑」输出形状，不影响「校验」——`OutputSchema` 走的是另一条路，两种 backend 都已生效** |
| Remote MCP InputSchema 未校准 | 7 个 Provider 的 **全部 50 个** remote tool 都没对真实 `tools/list` 核过（见 §10）。参数直通意味着参数名/类型写错就是运行时上游报错；`mcp:verify` 只比对 tool **名字**，挡不住参数改名。仓库里也还没有任何已提交的 `tools/list` 快照 |
| Remote MCP 无漂移检测 | §8.4 描述的 `tools/list` 快照 diff 尚未实现；目前只能靠人工跑 `mise run mcp-probe`。与上一行是同一条命令的两件事：第一次 probe 既校准 schema，也建立漂移基线 |

## 12. 环境变量权威全表（server 二进制实际读取）

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | 是 | Postgres 连接串（应用角色） |
| `CONNECT_IT_SECRET_KEY` | 是 | KEK keyring，`1:<64hex>`，逗号分隔多版本 |
| `COOKIE_SECRET` | 是 | admin cookie HMAC 密钥 |
| `CONNECT_IT_BASE_URL` | 是 | 对外绝对地址；拼 OAuth 回调；https ⇒ Secure cookie；禁带 query |
| `CONNECT_IT_EXPECTED_DATABASE_NAME` 等 5 个 | 是（生产） | 数据库身份期望值，缺一 fail closed |
| `CONNECT_IT_ADMIN_PASSWORD` | 首次 | seed admin |
| `LISTEN_ADDR` | 否 | 默认 `:8080` |
| `CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK` | 否 | 默认 false；精确小写 true/false |
| `CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP` | 否 | 同上 |
| `TEST_DATABASE_URL` | 仅测试 | 未设时集成用例 skip |
