# connect-it

connect-it 是一个自部署的内部 Connector 网关。它集中保存第三方平台凭证，通过
REST API 管理 Connection，并通过一个聚合的 MCP Streamable HTTP 端点向可信
下游暴露工具。

## Connector 模型

每个 Connector Definition 只能选择一种实现：

- `remote_mcp`：连接代码中指定的上游 MCP Server，在签发 Session 时通过
  `tools/list` 动态发现工具，`tools/call` 原样转发参数和结果。
- `managed`：当平台没有可用的 Remote MCP 时，由 connect-it 使用 REST API
  或 SDK 定义并实现工具。

两种实现不在同一个 Connector 内混用。Remote MCP 的工具由上游 Server 定义，
Managed 的工具由 connect-it 定义。

### 为什么这样设计

旧模型同时在 Definition 中声明 Remote MCP Server、静态工具 schema、后端映射
和参数转换器。connect-it 因此需要重复描述上游已经通过 `tools/list` 提供的
信息，工具变化后两边的定义可能不一致；尚未实现的转换器也增加了无效代码。

现在按照“工具由谁定义和维护”划分职责：

- 上游有可用 MCP Server 时，connect-it 不再复制工具定义，只负责凭证注入、
  动态发现和协议透传。
- 上游没有可用 MCP Server 时，connect-it 才拥有工具定义，并通过 REST API
  或 SDK 实现它们。

不采用混合模式，是因为同一 Connector 同时拥有“上游动态工具”和“本地附加
工具”后，会重新出现工具重名、schema 不一致、权限难以判断和版本不同步等问题。
需要新增能力时，应由上游 MCP 增加工具，或者将该 Connector 明确实现为
Managed，而不是在代理层增加另一套工具。

部分 Managed Connector 使用通用的 `api_request` 工具访问官方 REST API。
启用这类工具，等同于允许下游使用该凭证访问对应 REST API 的完整能力；
`tool_allowlist` 只能允许或拒绝整个工具，不能按 HTTP method 或 path 进一步限制。

这个选择减少的是需要理解和维护的概念，不一定会减少第一次修改的代码量。
Registry 和执行层只需处理两种明确实现，新增 Provider 也不需要再增加新的
后端或转换器类型。

OAuth 也分为两类：

- Provider OAuth：使用预先配置的 OAuth App，适用于 Managed Provider，以及
  不支持 MCP 原生 OAuth 的上游。
- MCP 原生 OAuth：从 Remote MCP endpoint 发现 OAuth metadata，使用 PKCE 和
  Dynamic Client Registration；仅在上游支持时不需要管理员配置 Client ID 和
  Client Secret。

## Connection 与 MCP Session

Connection 是长期凭证句柄。第三方 access token、refresh token 或 API Key 只
保存在 connect-it；下游只持久化 `connection_id`，并自行管理它属于哪个用户或
Bot。

MCP Session 是一份短期且不可变的工具清单，可以同时包含多个 Connection：

1. 下游提交 `namespace → connection_id` 映射。
2. connect-it 并发发现所有 Connection 的工具。
3. 工具以 `namespace__tool_name` 暴露并固化到 Session。
4. 下游使用短期 Session Token 访问统一的 `/mcp`。

保存这份工具清单，是为了保证同一个 Session 的 `tools/list` 与 `tools/call`
始终对应同一批工具。即使上游在会话期间增加或删除工具，已经签发的 Session
也不会随之改变。Session 按完整快照签发；任何一个 Connection 的工具发现失败，
本次签发都会失败，不会生成只包含部分 Connection 的 Session。

省略 `tool_allowlist` 表示允许本次签发时发现到的全部工具；显式 allowlist 中的
每个工具都必须存在，否则拒绝签发。撤销签发 Session 的 API Token、删除其中的
Connection 或 Session 到期，都会使 Session 失效。

## 目录

```text
packages/
├── core/        Connector Definition、Registry、加密与公共类型
├── connectors/  Remote MCP 与 Managed Provider
├── service/     配置、Connection、OAuth、Token、执行与 MCP Session
├── api/         Echo API、程序入口和 OpenAPI
├── sdk/         管理界面使用的、根据 OpenAPI 生成的 TypeScript SDK
├── ui/          git submodule → github.com/memohai/ui
└── web/         Vue 3 管理界面
sdk/
└── go/          面向可信下游服务的手写 Go SDK
docker/          Dockerfile、nginx 和 Compose
```

仓库没有 `go.work`；Go module 通过相对 `replace` 互相引用。clone 时需带
`--recursive`，因为 `packages/ui` 是 submodule。

## 本地开发

```bash
mise install
pnpm install
mise run db-up
mise run dev
```

API 默认监听 `http://localhost:8080`，开发管理员账号为 `admin` / `admin123`。
另开终端启动前端：

```bash
pnpm --dir packages/web run dev
```

完整 Docker 环境：

```bash
mise run docker-up
open http://localhost:8080
mise run docker-down
```

项目仍处于 pre-1.0 开发阶段，migration 只维护当前空库 schema，不承诺旧开发
数据库原地升级。

## 环境变量

| 变量 | 必填 | 说明 |
|---|---|---|
| `DATABASE_URL` | 是 | PostgreSQL 连接串 |
| `CONNECT_IT_SECRET_KEY` | 是 | AES-256-GCM 密钥环，例如 `1:<64 位 hex>` |
| `COOKIE_SECRET` | 是 | 管理会话 Cookie 的 HMAC 密钥 |
| `CONNECT_IT_BASE_URL` | 是 | 对外地址，用于生成 OAuth callback |
| `CONNECT_IT_ADMIN_PASSWORD` | 首次启动 | 初始化 `admin` 账号 |
| `LISTEN_ADDR` | 否 | 默认 `:8080` |
| `TEST_DATABASE_URL` | 测试 | Go 集成测试数据库；未设置时相关测试跳过 |

## 常用任务

| 任务 | 作用 |
|---|---|
| `mise run test` | 测试所有 Go module，包括下游 Go SDK |
| `mise run vet` | vet 所有 Go module，包括下游 Go SDK |
| `mise run sqlc` | 重新生成 store |
| `mise run swagger` | 重新生成 OpenAPI |
| `mise run sdk` | 重新生成 TypeScript SDK |
| `mise run build-web` | 构建管理界面 |
| `mise run docker-build` | 构建 server 和 web 镜像 |

## 下游接入

在管理端创建 API Token，然后创建 Connection：

```bash
curl -X POST "$BASE/v1/connections/oauth" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"connector_type":"github","auth_method":"oauth"}'
```

终端用户完成授权后，connect-it 显示本地完成页；下游通过
`GET /v1/connections/{id}` 查询 Connection 状态。

将当前 Bot 启用的 Connection 聚合成一个 MCP Session：

```bash
curl -X POST "$BASE/v1/mcp-sessions" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"connections":{"github":"<github_connection_id>",
                      "notion":"<notion_connection_id>"},
       "ttl_seconds":3600}'
```

随后使用返回的 Session Token 连接 `/mcp`：

```text
Authorization: Bearer <session_token>
```

Go 下游优先使用 [`sdk/go`](sdk/README.md)；它负责控制面请求和短期 MCP Session
Token 的内存缓存，`tools/list` 与 `tools/call` 仍直接使用官方 MCP SDK。

REST API 文档在服务启动后的 `/swagger/index.html`。

## 安全边界

- API Token 是部署级权限，能够管理本实例中的全部 Connection 并签发 Session，
  不是按 Connection 隔离的用户凭证。它只保存在可信下游服务端，不能发送给浏览器。
- Remote MCP endpoint 固定在代码中的 Definition 内，并强制使用 HTTPS。
- 出站凭证请求不会跨 Origin 跟随重定向。
- `tool_runs` 只记录调用归属、耗时、错误分类和上游状态码，不保存参数、结果或
  原始错误文本。
- 注册 Remote MCP Connector 表示信任该上游返回的工具 schema 与 description。
