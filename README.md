# connect-it

connect-it 是一个内部使用的有状态 Connector 服务（Go）。它集中管理第三方平台
（GitHub、Gmail、OneDrive、Google Ads）的授权凭证与 Tool 执行，通过一个聚合
MCP 端点（Streamable HTTP）暴露给内部应用，并内置 Vue 3 管理界面用于连接器
配置、OAuth 授权与健康监控。

核心特性：

- **代码即事实源**：Connector Definition 编译进二进制，数据库只存管理员配置与
  连接状态；新增／修改 Connector 不需要数据库 migration。
- **独立容器部署**：web（nginx 托管界面并反代 API）、server（Go 单二进制，
  migration 启动时自动执行）、postgres 三容器。
- **单租户**：一个部署一套配置；同类型多连接靠唯一 alias 区分。
- **聚合 /mcp**：短期 session token 限定可见连接与 Tool allowlist，工具名
  `{alias}__{tool_id}`。
- **Swagger + SDK**：swag 注释生成 OpenAPI，`packages/sdk` 由 Hey API 生成
  TypeScript 客户端供前端使用。

## 仓库结构

```text
packages/
├── core/        Go：Definition 类型、Registry、加密、状态机（纯库）
├── connectors/  Go：github / gmail / onedrive / googleads 各 provider
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
mise run dev                 # 起 API（http://localhost:8080，admin/admin123）
pnpm --dir packages/web run dev   # 另开终端起前端（Vite 代理 API 到 8080）
```

### 方式二：Docker Compose（web + server + postgres 三容器）

```bash
mise run docker-up           # 构建两个镜像并启动三个服务，等待健康
open http://localhost:8080   # 管理界面（nginx；默认密码见 docker/docker-compose.yml）
mise run docker-down
```

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
| `CONNECT_IT_BASE_URL` | 是 | 对外可达地址，OAuth 回调由它拼出 |
| `CONNECT_IT_ADMIN_PASSWORD` | 首次 | 首次启动 seed admin 账号（用户名 `admin`） |
| `LISTEN_ADDR` | 否 | 监听地址，默认 `:8080` |
| `TEST_DATABASE_URL` | 测试 | 集成测试用库；未设置时相关用例 skip |

## mise 任务

| 任务 | 作用 |
|---|---|
| `test` / `vet` | 逐 Go module 测试 / vet |
| `db-up` / `db-down` | 起停本地测试 postgres（5433） |
| `sqlc` | 重新生成 store 层 |
| `swagger` | 由 swag 注释生成 `packages/api/docs`（改路由后必跑） |
| `sdk` | 由 swagger.json 重新生成 TypeScript SDK（改路由后必跑） |
| `build-web` | 构建前端（产物 `packages/web/dist`） |
| `dev` | 本地起 API |
| `docker-build` / `docker-up` / `docker-down` | 镜像构建与 compose 起停 |

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

### 3. 签发 MCP session

```bash
curl -X POST $BASE/v1/mcp-sessions \
  -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
  -d '{"connections":{"gh":"<connection_id>"},
       "tool_allowlist":["gh__list_issues"],   # 可省略＝放行全部
       "ttl_seconds":3600}'
# => {"token":"<session_token>","expires_at":"…"}
```

`connections` 的 key 是你临时起的绑定名，决定这个 session 里工具名的前缀
（`gh__list_issues`）。session 是短期的（默认 1h，上限 24h），给一次对话/任务
签一个。

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

## 文档

- 设计 spec：`docs/superpowers/specs/2026-07-22-connect-it-design.md`
- 实施计划：`docs/superpowers/plans/`
- REST API 文档：服务启动后访问 `/swagger/index.html`（`/mcp` 是 MCP 协议
  端点，不在 swagger 内，见上方接入指南）
