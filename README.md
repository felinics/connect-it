# connect-it

connect-it 是一个自部署的内部 Connector 网关。它集中保存第三方平台凭证，通过
REST API 管理连接，并用一个 MCP Streamable HTTP 端点把工具暴露给可信下游。

当前包含 GitHub、Gmail、OneDrive 和 Google Ads，以及 Vue 3 管理界面。

每个 Connector 只有一种实现模式：

- `remote_mcp`：使用用户 OAuth token 连接上游 MCP，`tools/list` 动态发现工具，
  Session 签发时固化工具快照，`tools/call` 原样转发参数和结果。
- `managed`：仅在平台没有可用 MCP server 时，由 connect-it 定义并实现固定工具。

GitHub 和 Gmail 是 `remote_mcp`；OneDrive 和 Google Ads 是 `managed`。两种模式
不在同一个 Connector 内混用。

## 目录

```text
packages/
├── core/        Connector Definition、Registry、加密与公共类型
├── connectors/  GitHub、Gmail、OneDrive、Google Ads 实现
├── service/     配置、连接、OAuth、token、执行与 MCP session
├── api/         Echo API、程序入口和 OpenAPI
├── sdk/         生成的 TypeScript SDK
└── web/         Vue 管理界面
sdk/
└── go/          面向可信下游服务的手写 Go SDK
docker/          Dockerfile、nginx 和本地 Compose
```

仓库没有 `go.work`；Go module 通过相对 `replace` 互相引用。`packages/ui` 是
submodule，clone 时请使用 `--recursive`。

## 本地开发

```bash
mise install
pnpm install
mise run db-up
mise run dev
pnpm --dir packages/web run dev
```

API 默认监听 `http://localhost:8080`。`mise run dev` 的管理员账号为
`admin` / `admin123`。

也可以直接启动完整 Compose：

```bash
mise run docker-up
open http://localhost:8080
mise run docker-down
```

Compose 中的初始管理员密码是 `change-me-admin-password`；部署前必须修改其中的
密码和密钥。服务启动时会在空数据库中创建当前 schema。

项目当前处于 pre-1.0 开发阶段，数据库 migration 只维护最新的空库 schema，
升级时可能需要重建开发数据库，不承诺旧 schema 的原地升级。

## 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | 是 | PostgreSQL 连接串 |
| `CONNECT_IT_SECRET_KEY` | 是 | AES-256-GCM 密钥环，例如 `1:<64 位 hex>` |
| `COOKIE_SECRET` | 是 | 管理会话 Cookie 的 HMAC 密钥 |
| `CONNECT_IT_BASE_URL` | 是 | 对外 HTTP(S) 地址，用于 OAuth callback |
| `CONNECT_IT_ADMIN_PASSWORD` | 首次启动 | 初始化 `admin` 账号 |
| `LISTEN_ADDR` | 否 | 默认 `:8080` |
| `TEST_DATABASE_URL` | 测试 | Go 集成测试数据库；未设置时相关测试跳过 |

## 常用任务

| 任务 | 作用 |
|---|---|
| `mise run test` | 测试所有 Go module |
| `mise run vet` | vet 所有 Go module |
| `mise run sqlc` | 重新生成 store |
| `mise run swagger` | 重新生成 OpenAPI |
| `mise run sdk` | 重新生成 TypeScript SDK |
| `mise run build-web` | 构建管理界面 |
| `mise run docker-build` | 构建 server 和 web 镜像 |

## GitHub 配置

GitHub OAuth 需要在管理端填写 OAuth App 的 `client_id` 和 `client_secret`，
callback URL 是：

```text
<CONNECT_IT_BASE_URL>/v1/oauth/callback
```

GitHub MCP 的握手和工具调用使用用户的 OAuth token。

## Gmail 配置

Gmail MCP 当前属于 Google Workspace Developer Preview。使用前必须在 OAuth
客户端所属的 Google Cloud project 中同时启用 `gmail.googleapis.com` 和
`gmailmcp.googleapis.com`，并配置 `gmail.readonly`、`gmail.compose` 两个 scope。

## YouTube 配置

YouTube 使用 Google OAuth 和 YouTube Data API v3。启用
`youtube.googleapis.com`，创建 Web application 类型的 OAuth Client，并将
callback URL 配置为：

```text
<CONNECT_IT_BASE_URL>/v1/oauth/callback
```

## 接入流程

1. 在管理端创建 API Token。
2. 创建 OAuth 或 API-key Connection，保存返回的 `connection_id`。
3. 用 Connection 签发短期 MCP session。
4. 使用 session token 访问 `/mcp`。

MCP session 绑定一个 Connection，是一份短期、不可变的能力快照。签发时会从
`remote_mcp` 上游发现工具；显式 `tool_allowlist` 必须全部匹配，否则拒绝签发。
省略 allowlist 表示允许“本次签发时发现到的全部工具”，同一个 session 不会自动
获得上游之后新增的工具。撤销签发它的 API Token 会立即让该 session 失效。

Remote MCP endpoint 固定在代码内的 Connector Definition 中，必须使用 HTTPS，
并且出站 Bearer 请求不跟随重定向。注册 Connector 即表示信任该上游返回的工具
schema 和 description。

`tool_runs` 只保存调用归属、耗时、`error_kind` 和可取得时的上游 HTTP 状态码；
不保存参数、结果或原始错误文本。

创建 OAuth Connection：

```bash
curl -X POST "$BASE/v1/connections/oauth" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"connector_type":"github","auth_method":"oauth",
       "redirect_url":"https://app.example/oauth/done"}'
```

签发 MCP session：

```bash
curl -X POST "$BASE/v1/mcp-sessions" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"connection_id":"<connection_id>",
       "tool_allowlist":["list_issues"],
       "ttl_seconds":3600}'
```

连接 MCP 时，每个请求都带：

```text
Authorization: Bearer <session_token>
```

REST API 文档在服务启动后的 `/swagger/index.html`。
