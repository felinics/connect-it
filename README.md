# connect-it

connect-it 是一个内部使用的有状态 Connector 服务（Go）。它集中管理第三方平台的
授权凭证与 Tool 执行，通过一个聚合 MCP 端点（Streamable HTTP）暴露给内部应用，
并内置 Vue 3 管理界面用于连接器配置、OAuth 授权与健康监控。

当前代码已注册 GitHub、GitLab、Gmail、OneDrive、Google Ads、Datadog、Linear、
Notion 和 Stripe。代码注册不等于生产支持；真实账号、外部 app/client 和发布验收
状态以各 Provider 的 [`docs/providers/<type>.md`](docs/providers/) smoke 记录为准
（状态词汇见[项目蓝图](docs/blueprint.md) §9）。其余 Provider 仍是 planned。

核心特性：

- **代码即事实源**：Connector Definition 编译进二进制，数据库只存管理员配置与
  连接状态；新增／修改 Connector 不需要数据库 migration。
- **独立容器部署**：web（nginx 托管界面并反代 API）、server（Go 单二进制，
  启动时只接受精确 v1/clean schema）、postgres 三容器。
- **单租户**：一个部署一套配置；Connection ID 是持久句柄，连接 alias 只是可选、
  非唯一的展示标签。
- **聚合 /mcp**：短期 session token 固化 alias→connection、Tool grant 与
  `authorization_generation`；工具名为 `{alias}__{tool_id}`，授权事实变化后旧
  session 自动失效。
- **统一执行边界**：输入硬上限、JSON Schema/default、安全数值、scope、risk 与
  session grant 在进入 Provider 前校验，backend 调用前再次检查持久授权。
- **凭证先验证后激活**：OAuth、API key 与自定义凭证都先解析 profile/scopes；
  整组换 key 使用版本 CAS，OAuth 刷新使用 DB lease，网络请求不占数据库事务。
- **Swagger + SDK**：swag 注释生成 OpenAPI，`packages/sdk` 由 Hey API 生成
  TypeScript 客户端供前端使用。

## 仓库结构

```text
packages/
├── core/        Go：Definition 类型、Registry、加密、状态机（纯库），
│                    含 core/providerkit：Provider 公共 HTTP、安全、认证与错误运行时
├── connectors/  Go：github / gitlab / gmail / onedrive / googleads / datadog / linear / notion / stripe 各 Provider
├── service/     Go：sqlc store、migrations、OAuth、token 刷新、Tool 执行、MCP
├── api/         Go：Echo 路由、swagger、程序入口
├── sdk/         TypeScript SDK（openapi-ts 生成，产物提交仓库）
├── ui/          git submodule → github.com/memohai/ui（设计系统）
└── web/         Vite + Vue 3 管理界面
docker/          server.Dockerfile、web.Dockerfile、nginx.conf、docker-compose.yml
```

无 go.work：Go module 间用 go.mod `replace` 相对路径互引，clone 即可构建。
**clone 必须带 `--recursive`**（packages/ui 是 submodule）。

## 快速开始

### 方式一：本地开发（mise）

```bash
mise install                 # go 1.25 / node 22 / pnpm 10 / sqlc
pnpm install                 # 前端 workspace 依赖
mise run db-up               # 启动测试 postgres（localhost:5433）
mise run dev                 # 显式初始化本地 schema 后起 API（http://localhost:8080）
pnpm --dir packages/web run dev   # 另开终端起前端（Vite 代理 API 到 8080）
```

也可单独运行 `mise run dev-db-init` 初始化 `db-up` 创建的固定 localhost:5433
开发库；`mise run dev` 随后通过唯一的
`scripts/configure-dev-database.sql`配置开发双角色和最小权限。两者都不会构建进
生产 server image。

### 方式二：Docker Compose（web + server + postgres 三容器）

```bash
mise run docker-up           # 构建两个镜像并启动三个服务，等待健康
open http://localhost:8080   # 管理界面（nginx；默认密码见 docker/docker-compose.yml）
mise run docker-down
```

`docker-up` 会先为**完全空的开发 Postgres**执行唯一的 v1 baseline，再通过
`scripts/configure-dev-database.sql`配置固定 owner/app 双角色和逐表权限，然后启动
server。已有且 shape 与 marker 都是 v1/clean 时 baseline no-op；任何部分建表、
脏版本或不完整 shape 都会 fail closed。配置 SQL 不扫描或修复 legacy ACL，偏离
固定权限矩阵的库由运行时门禁拒绝。这个 bootstrap 仅供本地 dev 和隔离的 CI
ingress 使用；生产 server 只校验 schema，release compose 不会执行 bootstrap。
生产部署当前只支持新建空数据库，见
[Fresh database bootstrap](docs/runbooks/fresh-database-bootstrap.md)。
发布前用`mise run check-release-compose`检查合并后的生产模型；实际部署的
Postgres、Server 和 Web 镜像都必须通过各自的`CONNECT_IT_*_IMAGE`提供不可变
`@sha256`引用。

server 容器不对外发布端口，全部流量（含内部应用的 `/v1`、`/mcp` 机器调用）
经 web 容器的 nginx 反代进入。

部署到任何会活过当前终端会话的环境前，把 compose 文件里所有标着
`CHANGE ME` 的值全部换掉。

## 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | 是 | Postgres 连接串（`postgres://…`） |
| `CONNECT_IT_SECRET_KEY` | 是 | AES-256-GCM 密钥环，格式 `1:<64位hex>`，可逗号分隔多版本轮换 |
| `COOKIE_SECRET` | 是 | 管理会话 cookie 的 HMAC 密钥 |
| `CONNECT_IT_BASE_URL` | 是 | 对外可达的绝对 HTTP(S) 地址；OAuth 回调由它拼出，且 `https` 会强制管理会话 Cookie 使用 `Secure`（不信任代理请求头） |
| `CONNECT_IT_ADMIN_PASSWORD` | 首次 | 首次启动 seed admin 账号（用户名 `admin`） |
| `LISTEN_ADDR` | 否 | 监听地址，默认 `:8080` |
| `CONNECT_IT_{POSTGRES,SERVER,WEB}_IMAGE` | 发布 | Release Compose 必填；三个互不相同的不可变 `@sha256` 镜像引用 |
| `CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK` | 否 | 默认 `false`；仅为明确支持 self-hosted 的 Provider 开放私网，localhost/link-local/metadata 仍永久禁止 |
| `CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP` | 否 | 默认 `false`；仅与 self-hosted Policy 的显式配置共同生效，固定 SaaS 仍强制 HTTPS |
| `TEST_DATABASE_URL` | 测试 | 集成测试用库；未设置时相关用例 skip |

经 `providerkit` 发出的 Provider HTTP 默认忽略 `HTTP_PROXY`／`HTTPS_PROXY`，
避免代理重新解析 host 绕过 DNS pin。Managed、credential validator、OAuth
exchange/refresh/profile、Remote MCP 和公共下载路径统一使用这条安全边界。
两个 self-hosted opt-in 必须使用精确小写 `true`／`false`；拼写错误会阻止服务启动。
完整边界和部署检查见
[Provider 出站网络安全](docs/provider-egress-security.md)。

## mise 任务

| 任务 | 作用 |
|---|---|
| `test` / `vet` | 逐 Go module 测试 / vet |
| `db-up` / `db-down` / `dev-db-init` | 起停本地测试 postgres（5433）/ 显式初始化其 schema |
| `sqlc` | 重新生成 store 层 |
| `swagger` | 由 swag 注释生成 `packages/api/docs`（改路由后必跑） |
| `sdk` | 由 swagger.json 重新生成 TypeScript SDK（改路由后必跑） |
| `build-web` | 构建前端（产物 `packages/web/dist`） |
| `check-release-compose` | 静态校验仅使用 digest 镜像、私有网络、固定入口与健康依赖的生产 Compose |
| `dev` | 本地起 API |
| `docker-build` / `docker-up` / `docker-down` | 镜像构建与 compose 起停 |
| `docker-ingress-test` | 真实 Docker/Nginx MCP 大 body、流式转发与并发预算合同 |

## 接入指南（内部应用 / SaaS 后端）

完整链路四步。第 1 步在管理台做一次，其余全部是机器 API。

### 1. 拿 API Token

管理台「API Token」页创建，明文只显示一次（`cit_` 前缀）。它是你们后端调
`/v1/*` 的凭证。

### 2. 为终端用户创建连接（拿到 connection_id）

```bash
# OAuth 类：立即返回持久 connection_id（pending）＋授权 URL
curl -X POST $BASE/v1/connections/oauth \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"connector_type":"github","auth_method":"oauth",
       "redirect_url":"https://your-app.example/oauth/done"}'
# => {"connection_id":"…","authorization_url":"…"}
```

把 `authorization_url` 跳给终端用户；用户同意后回调把连接置 `active`，并
302 到你的 `redirect_url?status=connected&connection_id=…`（也可轮询
`GET /v1/connections/{id}`）。**connection_id 就是句柄**——它属于哪个用户，
由你们自己的库来记。api_key 类走 `POST /v1/connections/api-key`，同样直接
返回 connection_id。

若某个 Definition 声明受约束的 tenant OAuth，OAuth begin 还会接受该 auth method
公开的`tenant_fields`；字段 schema 从
`GET /admin/connectors/{type}/auth-methods`取得，不能提交任意 origin。声明
`install`认证的 Provider 走`POST /v1/connections/install`，可选提交
`expected_resource_id`作为加密绑定约束；回调固定为`/v1/install/callback`。当前
这些是已完成的平台扩展点，不代表路线图中的 Shopify、Zendesk 或 Discord 已交付。

API key／自定义凭证需要轮换时，用
`PUT /v1/connections/{connection_id}/credential` 提交完整 `fields` 组。服务先向
Provider 验证，再以 credential/authorization 双版本 CAS 原子替换；它不是局部
PATCH。

### 3. 签发 MCP session

```bash
curl -X POST $BASE/v1/mcp-sessions \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"connections":{"gh":"<connection_id>"},
       "tool_allowlist":["gh__list_issues"],
       "ttl_seconds":3600}'
# => {"token":"<session_token>","expires_at":"…"}
```

`connections` 的 key 是你临时起的绑定名，决定这个 session 里工具名的前缀
（`gh__list_issues`）。session 是短期的（默认 1h，上限 24h），给一次对话/任务
签一个。省略 `tool_allowlist` 时只展开签发当时已有的 read Tool；write 和
destructive Tool 必须显式列出。显式 `[]` 表示零 Tool，`null` 会被拒绝。grant
以带版本的不可变快照保存；连接重授权、换 key、scope/token scheme 或影响安全策略
的 Connector 配置／Definition 变化都会推进授权代际，旧 session 随即 fail closed。

### 4. 用 session token 连接 /mcp

`/mcp` 是标准 **MCP Streamable HTTP** 端点。任何支持该传输的 MCP 客户端都能
连，唯一要求：**每个请求带 `Authorization: Bearer <session_token>`**。

TypeScript（官方 `@modelcontextprotocol/sdk`）：

```ts
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js'

const transport = new StreamableHTTPClientTransport(new URL(`${BASE}/mcp`), {
  requestInit: { headers: { Authorization: `Bearer ${sessionToken}` } },
})
const client = new Client({ name: 'your-app', version: '1.0.0' })
await client.connect(transport)

const { tools } = await client.listTools()          // [{ name: "gh__list_issues", … }]
const result = await client.callTool({
  name: 'gh__list_issues',
  arguments: { owner: 'memohai', repo: 'connect-it' },
})
```

Claude Code 等支持 HTTP MCP 的客户端：

```bash
claude mcp add --transport http connect-it $BASE/mcp \
  --header "Authorization: Bearer $SESSION_TOKEN"
```

裸 JSON-RPC（调试用）：

```bash
curl -X POST $BASE/mcp \
  -H "Authorization: Bearer $SESSION_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
        "protocolVersion":"2025-06-18","capabilities":{},
        "clientInfo":{"name":"debug","version":"0"}}}'
# 然后同样方式发 {"method":"tools/list"} / {"method":"tools/call", …}
```

错误约定：session 缺失/过期/吊销 → HTTP 401（`invalid_session`），重新签发即可；
工具已被 Definition 下线 → 工具级错误 `tool_unavailable`；连接凭证失效 →
执行结果报错，同时 `GET /v1/connections/{id}` 会显示 `reauth_required`，用
`POST /v1/connections/{id}/reauth` 生成新授权链接给用户。

REST 错误统一返回稳定的 `error` 与安全 `message`；Provider 验证错误还可能返回
`temporary`、`retry_after_seconds`，并在可用时附 `Retry-After`。Tool 审计只保存
schema 声明字段名／数量、字节数、结果类型、稳定错误码和上游状态，不保存参数值或
Provider 原始响应。

## 文档

- [项目蓝图](docs/blueprint.md)：架构、领域模型、关键流程、安全边界与交付现状
- [Provider 出站网络安全](docs/provider-egress-security.md)
- [生产空库初始化约束](docs/runbooks/fresh-database-bootstrap.md)
- [Provider 冻结模板与 review checklist](docs/templates/provider/)
- 现有 Provider 运维/烟测：
  [GitHub](docs/providers/github.md)、
  [GitLab](docs/providers/gitlab.md)、
  [Gmail](docs/providers/gmail.md)、
  [OneDrive](docs/providers/one_drive.md)、
  [Google Ads](docs/providers/google_ads.md)、
  [Datadog](docs/providers/datadog.md)、
  [Linear](docs/providers/linear.md)、
  [Notion](docs/providers/notion.md)、
  [Stripe](docs/providers/stripe.md)
- REST API 文档：服务启动后访问 `/swagger/index.html`（`/mcp` 是 MCP 协议
  端点，不在 swagger 内，见上方接入指南）
