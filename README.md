# connect-it

connect-it 是一个内部使用的有状态 Connector 服务（Go）。它集中管理第三方平台
（GitHub、Gmail、OneDrive、Google Ads）的授权凭证与 Tool 执行，通过一个聚合
MCP 端点（Streamable HTTP）暴露给内部应用，并内置 Vue 3 管理界面用于连接器
配置、OAuth 授权与健康监控。

核心特性：

- **代码即事实源**：Connector Definition 编译进二进制，数据库只存管理员配置与
  连接状态；新增／修改 Connector 不需要数据库 migration。
- **单二进制**：管理界面经 go:embed 内嵌，migration（golang-migrate）启动时自动执行。
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
├── api/         Go：Echo 路由、swagger、程序入口、web 产物 embed
├── sdk/         TypeScript SDK（openapi-ts 生成，产物提交仓库）
├── ui/          git submodule → github.com/memohai/ui（设计系统）
└── web/         Vite + Vue 3 管理界面
docker/          Dockerfile 与 docker-compose.yml
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
# 另开终端起前端热更新（可选，直接访问 8080 用的是上次 build 的产物）：
pnpm --dir packages/web run dev
```

### 方式二：Docker Compose

```bash
mise run docker-up           # 构建镜像并启动 postgres + connect-it，等待健康
open http://localhost:8080   # 管理界面（默认密码见 docker/docker-compose.yml）
mise run docker-down
```

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
| `build-web` | 构建前端并拷入 `packages/api/webdist` |
| `dev` | 本地起 API |
| `docker-build` / `docker-up` / `docker-down` | 镜像构建与 compose 起停 |

## 文档

- 设计 spec：`docs/superpowers/specs/2026-07-22-connect-it-design.md`
- 实施计划：`docs/superpowers/plans/`
- API 文档：服务启动后访问 `/swagger/index.html`
