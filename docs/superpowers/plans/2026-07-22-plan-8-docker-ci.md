# connect-it计划8：Docker与CI

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付生产可用的多阶段Docker镜像、docker-compose部署栈、GitHub Actions持续集成流水线，并补齐mise任务与根README。

**Architecture:** 三阶段Dockerfile（node:22构建前端→golang:1.25静态编译单二进制→alpine:3.24非root运行）；compose栈由postgres:17与connect-it两个服务组成，靠healthcheck串联启动顺序；CI在同一job里经mise-action安装与本地一致的工具链，用postgres:17服务容器提供集成测试数据库，最后做docker build冒烟。本计划只产出配置与文档，不写任何Go／TS代码。

**Tech Stack:** Docker（BuildKit）＋docker compose v2、GitHub Actions（actions/checkout@v7、jdx/mise-action@v4）、postgres:17、alpine:3.24、mise。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第3、4、15节是本计划的依据）

## Global Constraints

- 仓库布局：`packages/{ui(submodule),web,core,connectors,service,api}`；无go.work；各Go module的go.mod用相对路径`replace`（`api`replace `core`、`connectors`、`service`）。
- 工具链：Go 1.25、node 22、pnpm 10，全部由mise管理；CI与本地走同一份`mise.toml`。
- 已有mise任务：`test`、`vet`、`sqlc`、`db-up`、`db-down`、`dev`、`build-web`（`build-web`把`packages/web`的dist拷入`packages/api/webdist`，Go侧用go:embed服务）。
- 服务形态：单二进制`packages/api/cmd/connect-it`；启动时自动跑golang-migrate；监听`LISTEN_ADDR`（默认`:8080`）；`GET /healthz`无鉴权返回200。
- 环境变量：`DATABASE_URL`（postgres://…）、`CONNECT_IT_SECRET_KEY`（格式`1:<64位hex>`）、`COOKIE_SECRET`、`CONNECT_IT_ADMIN_PASSWORD`、`CONNECT_IT_BASE_URL`、`LISTEN_ADDR`。
- 集成测试约定：`TEST_DATABASE_URL`未设置则`t.Skip`。
- Docker构建上下文为仓库根，需`packages/ui`submodule已检出；Go编译`CGO_ENABLED=0`。
- 提交信息用conventional commits，每个产出文件的task至少一次提交；本计划不做`git push`（Task 4的远端验证步骤除外，视权限执行）。

## 前置条件

- 计划1–7已全部落地：根目录存在`pnpm-lock.yaml`；`pnpm --dir packages/web run build`可产出`packages/web/dist`；`packages/api/cmd/connect-it`可编译；上述mise任务全部可用。
- 本机安装Docker≥24（BuildKit默认启用）与docker compose v2插件，`curl`可用。
- 仓库以`--recursive`克隆，`git submodule status`能列出`packages/ui`。

## 版本查证（2026-07-22）

| 组件 | 采用版本 | 查证结果与来源 |
|---|---|---|
| actions/checkout | `@v7` | 最新release为v7.0.1（2026-07-20）；来源：https://github.com/actions/checkout/releases |
| jdx/mise-action | `@v4` | 最新release为v4.2.1，项目README推荐`jdx/mise-action@v4`；来源：https://github.com/jdx/mise-action/releases 与 https://github.com/jdx/mise-action ；跨计划契约草案写`@v2`，按「以查证的当前稳定版为准」改用v4，见文末偏离点 |
| alpine | `3.24` | 当前stable分支v3.24（2026-06-09发布，支持到2028-06）；来源：https://alpinelinux.org/releases/ |
| pnpm | `10.34.5` | npm registry dist-tag `latest-10`＝10.34.5（`corepack prepare`需要精确版本）；来源：https://registry.npmjs.org/-/package/pnpm/dist-tags |
| postgres | `17` | spec第15节钉定，不另查证 |
| node／golang基础镜像 | `node:22`／`golang:1.25` | 跨计划契约钉定，与mise.toml的`[tools]`一致 |

## 运行镜像选型说明

第三阶段用`alpine:3.24`而非`gcr.io/distroless/static`，理由：

- alpine自带busybox `wget`，`HEALTHCHECK`可以内置在镜像里，裸`docker run`与compose、K8s等任何编排下都有健康状态；distroless无shell，healthcheck只能下沉到compose层，脱离compose就失效。方案取「镜像内HEALTHCHECK」并全计划保持一致：compose侧不再重复定义healthcheck，直接继承镜像的。
- connect-it是内部工具，出问题时能`docker exec`进容器排障的价值大于distroless省掉shell带来的攻击面收益；风险已由非root用户＋`CGO_ENABLED=0`静态二进制＋`--no-cache`最小安装缓解。
- 体积差异可忽略：alpine基底约8MB，最终镜像仍在几十MB量级。

## 文件结构

```text
.dockerignore                    # 新建：构建上下文裁剪（Task 1）
docker/Dockerfile                # 新建：三阶段构建（Task 1）
docker/docker-compose.yml        # 新建：postgres + connect-it（Task 2）
.github/workflows/ci.yml         # 新建：push/PR流水线（Task 4）
mise.toml                        # 修改：追加docker-build/docker-up/docker-down（Task 1、2）
README.md                        # 重写：简介＋快速开始＋env表＋任务表（Task 5）
```

---

### Task 1: 多阶段Dockerfile与.dockerignore

**Files:**
- Create: `.dockerignore`
- Create: `docker/Dockerfile`
- Modify: `mise.toml`（末尾追加`docker-build`任务）

**Interfaces:**
- Consumes: 计划1–7的约定——`pnpm-lock.yaml`与workspace配置、`packages/web`的`build`脚本产出`packages/web/dist`、`packages/api/webdist`为go:embed目录、入口`packages/api/cmd/connect-it`、go.mod相对`replace`。
- Produces: 镜像`connect-it:dev`（非root、EXPOSE 8080、内置HEALTHCHECK、ENTRYPOINT为二进制）；`mise run docker-build`；Task 2的compose与Task 4的CI都以`docker/Dockerfile`＋仓库根上下文为准。

- [ ] **Step 1: 写入.dockerignore（仓库根）**

`.dockerignore`：

```text
# Keep the build context small; the image never needs these.
.git
**/.git
.github
.gitignore
.gitmodules
docs
docker
LICENSE
README.md
.env
.env.*
node_modules
**/node_modules
# Local build artifacts; the image rebuilds these itself.
packages/web/dist
packages/api/webdist
**/.DS_Store
```

注意：`packages/ui`（submodule内容）**不排除**，前端构建依赖它；`docker/`可排除是因为`-f docker/Dockerfile`下BuildKit单独传送Dockerfile，不走上下文。

- [ ] **Step 2: 写入docker/Dockerfile**

`docker/Dockerfile`：

```dockerfile
# syntax=docker/dockerfile:1
# Build context MUST be the repository root with the packages/ui submodule
# checked out:  docker build -f docker/Dockerfile -t connect-it:dev .

# ---------- Stage 1: build the Vue admin UI ----------
FROM node:22 AS web-builder
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
# corepack prepare needs an exact version; keep the major in sync with
# [tools].pnpm in mise.toml (latest-10 as of 2026-07-22).
RUN corepack enable && corepack prepare pnpm@10.34.5 --activate
WORKDIR /src
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY packages/ui packages/ui
COPY packages/sdk packages/sdk
COPY packages/web packages/web
RUN pnpm install --frozen-lockfile
RUN pnpm --dir packages/web run build

# ---------- Stage 2: build the Go binary ----------
FROM golang:1.25 AS go-builder
WORKDIR /src
# All four modules must be present: go.mod replace directives use ../ paths.
COPY packages/core packages/core
COPY packages/connectors packages/connectors
COPY packages/service packages/service
COPY packages/api packages/api
# Same destination the `mise run build-web` task uses; served via go:embed.
COPY --from=web-builder /src/packages/web/dist /src/packages/api/webdist
WORKDIR /src/packages/api
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/connect-it ./cmd/connect-it

# ---------- Stage 3: runtime ----------
# alpine (not distroless/static): busybox wget keeps the HEALTHCHECK inside
# the image so bare `docker run` gets health status too, and a shell remains
# available for debugging this internal tool. Mitigations: non-root user,
# static binary, minimal package set.
FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 connectit \
    && adduser -D -H -u 10001 -G connectit connectit
COPY --from=go-builder /out/connect-it /usr/local/bin/connect-it
USER connectit:connectit
# The binary defaults to :8080 anyway; set it explicitly so EXPOSE and
# HEALTHCHECK below stay truthful if the default ever changes.
ENV LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=5 \
    CMD wget -qO /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/connect-it"]
```

- [ ] **Step 3: mise.toml追加docker-build任务**

在`mise.toml`末尾追加：

```toml
[tasks.docker-build]
description = "Build the production image (context = repo root)"
run = "docker build -f docker/Dockerfile -t connect-it:dev ."
```

- [ ] **Step 4: 构建验证**

```bash
mise run docker-build
```

预期：退出码0；首次构建数分钟（拉取基础镜像＋pnpm install＋go build），BuildKit输出依次出现`web-builder`、`go-builder`两阶段，最后一行含`naming to docker.io/library/connect-it:dev`。若失败于`pnpm install --frozen-lockfile`，先确认`pnpm-lock.yaml`已提交且`packages/ui`submodule已检出。

- [ ] **Step 5: 镜像元数据验证**

```bash
docker inspect -f 'user={{.Config.User}} ports={{.Config.ExposedPorts}} entry={{.Config.Entrypoint}}' connect-it:dev
docker image ls connect-it:dev --format '{{.Size}}'
```

预期：第一条输出`user=connectit:connectit ports=map[8080/tcp:{}] entry=[/usr/local/bin/connect-it]`；第二条输出几十MB量级（如`45MB`上下，明显小于500MB即正常）。

- [ ] **Step 6: 二进制可执行性冒烟（无数据库，预期报配置错误而非无法执行）**

```bash
docker run --rm connect-it:dev; echo "exit=$?"
```

预期：容器内二进制正常启动并因缺少`DATABASE_URL`／密钥配置立即退出，stderr输出配置类错误信息，`exit=`为非零值。**不允许**出现`exec format error`或`no such file or directory`（那说明静态编译或COPY路径有误）。

- [ ] **Step 7: 提交**

```bash
git add .dockerignore docker/Dockerfile mise.toml
git commit -m "feat(docker): add multi-stage Dockerfile and docker-build task"
```

---

### Task 2: docker-compose栈与compose起停任务

**Files:**
- Create: `docker/docker-compose.yml`
- Modify: `mise.toml`（末尾追加`docker-up`、`docker-down`任务）

**Interfaces:**
- Consumes: Task 1的`docker/Dockerfile`（build指向仓库根上下文）与镜像内HEALTHCHECK（compose的`--wait`直接消费它，不再自建connect-it侧healthcheck）。
- Produces: compose项目`connect-it`（服务名`postgres`、`connect-it`，卷`postgres-data`，宿主端口8080）；`mise run docker-up`／`mise run docker-down`；Task 3的冒烟与README的Docker快速开始都依赖这两个任务。

- [ ] **Step 1: 写入docker/docker-compose.yml**

`docker/docker-compose.yml`：

```yaml
# Local / single-host deployment stack: postgres 17 + the connect-it binary.
# Usage: mise run docker-up / mise run docker-down
#
# Values marked "CHANGE ME" are development-only defaults. Replace ALL of
# them before any deployment that outlives your terminal session.
name: connect-it

services:
  postgres:
    image: postgres:17
    environment:
      POSTGRES_USER: connect_it
      # CHANGE ME together with DATABASE_URL below.
      POSTGRES_PASSWORD: connect_it_dev_password
      POSTGRES_DB: connect_it
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U connect_it -d connect_it"]
      interval: 5s
      timeout: 3s
      retries: 12

  connect-it:
    build:
      # Context is the repository root: the Dockerfile copies packages/*
      # including the checked-out packages/ui submodule.
      context: ..
      dockerfile: docker/Dockerfile
    depends_on:
      postgres:
        condition: service_healthy
    ports:
      - "8080:8080"
    # Health status comes from the HEALTHCHECK baked into the image
    # (wget /healthz); `docker compose up --wait` relies on it.
    environment:
      # Must match the postgres service credentials above.
      DATABASE_URL: "postgres://connect_it:connect_it_dev_password@postgres:5432/connect_it?sslmode=disable"
      # CHANGE ME: AES-256-GCM keyring, format "1:<64-char-hex>".
      # Generate: echo "1:$(openssl rand -hex 32)"
      CONNECT_IT_SECRET_KEY: "1:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
      # CHANGE ME: HMAC secret for admin session cookies.
      # Generate: openssl rand -hex 32
      COOKIE_SECRET: "change-me-cookie-secret"
      # CHANGE ME: initial password for POST /admin/login.
      CONNECT_IT_ADMIN_PASSWORD: "change-me-admin-password"
      # Externally reachable base URL; OAuth callbacks are built from it.
      # Change when deploying behind a domain / reverse proxy.
      CONNECT_IT_BASE_URL: "http://localhost:8080"
      # Listen address inside the container. Keep in sync with `ports`
      # above and the HEALTHCHECK port baked into the image.
      LISTEN_ADDR: ":8080"

volumes:
  postgres-data:
```

- [ ] **Step 2: mise.toml追加docker-up／docker-down任务**

在`mise.toml`末尾追加：

```toml
[tasks.docker-up]
description = "Build and start the docker compose stack, wait until healthy"
run = "docker compose -f docker/docker-compose.yml up --build --wait"

[tasks.docker-down]
description = "Stop the docker compose stack (postgres volume is kept)"
run = "docker compose -f docker/docker-compose.yml down"
```

- [ ] **Step 3: 静态校验compose文件**

```bash
docker compose -f docker/docker-compose.yml config --quiet && echo CONFIG_OK
docker compose -f docker/docker-compose.yml config --services
```

预期：第一条输出`CONFIG_OK`（语法与schema合法）；第二条恰好输出两行：`postgres`与`connect-it`（顺序不限）。

- [ ] **Step 4: 提交**

```bash
git add docker/docker-compose.yml mise.toml
git commit -m "feat(docker): add postgres + connect-it compose stack with up/down tasks"
```

---

### Task 3: 端到端冒烟（compose起栈→healthz→错误密码登录→清理）

**Files:**
- 无新增／修改文件（纯验证task；任一步骤失败即本计划前两个task有缺陷，回到对应task修复后重跑本task）

**Interfaces:**
- Consumes: Task 2的`mise run docker-up`／`docker-down`、compose内`CONNECT_IT_ADMIN_PASSWORD=change-me-admin-password`；服务契约`GET /healthz`→200、`POST /admin/login`错误密码→401、go:embed的管理界面。
- Produces: 「镜像＋compose栈可用」的最终验收结论，README的Docker快速开始据此成立。

- [ ] **Step 1: 起栈并等待健康**

```bash
mise run docker-up
```

预期：退出码0；输出中postgres与connect-it两容器先后出现`Started`／`Healthy`；`--wait`会阻塞到镜像内HEALTHCHECK通过（含首次migration，约10–40秒）。若超时失败，用`docker compose -f docker/docker-compose.yml logs connect-it`查启动日志（常见原因：`DATABASE_URL`与postgres服务凭证不一致）。

- [ ] **Step 2: healthz冒烟**

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/healthz
```

预期：输出`200`。

- [ ] **Step 3: 内嵌管理界面冒烟**

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/
```

预期：输出`200`（go:embed的web dist正常服务；若为404，说明Dockerfile第二阶段`webdist`拷贝路径与api module的embed路径不一致）。

- [ ] **Step 4: 错误密码登录冒烟**

```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"definitely-wrong-password"}' \
  http://localhost:8080/admin/login
```

预期：输出`401`（`definitely-wrong-password`≠compose中设置的`change-me-admin-password`）。若得到`400`，是请求体字段名与计划2实现的登录handler不一致——查阅`packages/api`中login handler的绑定结构体，改用其实际字段名重试，仍须得到`401`。

- [ ] **Step 5: 容器健康状态复核**

```bash
docker compose -f docker/docker-compose.yml ps --format '{{.Name}} {{.Status}}'
```

预期：两行输出，均含`Up`，connect-it一行含`(healthy)`。

- [ ] **Step 6: 清理**

```bash
mise run docker-down
docker compose -f docker/docker-compose.yml ps --format '{{.Name}}'
```

预期：`down`正常移除两容器与网络（卷`postgres-data`保留，需要彻底清库时手动执行`docker compose -f docker/docker-compose.yml down -v`）；第二条命令无输出。本task无文件变更，不产生提交。

---

### Task 4: GitHub Actions CI工作流

**Files:**
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: 根`mise.toml`的`[tools]`与`vet`／`test`／`build-web`任务（jdx/mise-action读取它安装工具链）；`TEST_DATABASE_URL`未设置则跳过集成测试的约定（CI设置它以真正执行）；Task 1的`docker/Dockerfile`。
- Produces: 工作流`CI`（单job `ci`），push到main与全部PR触发；后续贡献流程以其绿灯为合并门禁。

- [ ] **Step 1: 写入.github/workflows/ci.yml**

`.github/workflows/ci.yml`：

```yaml
# Versions verified 2026-07-22:
#   actions/checkout v7 (latest v7.0.1)  https://github.com/actions/checkout/releases
#   jdx/mise-action  v4 (latest v4.2.1)  https://github.com/jdx/mise-action/releases
name: CI

on:
  push:
    branches: [main]
  pull_request:

jobs:
  ci:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_USER: connect_it
          POSTGRES_PASSWORD: connect_it
          POSTGRES_DB: connect_it_test
        ports:
          - "5432:5432"
        options: >-
          --health-cmd "pg_isready -U connect_it -d connect_it_test"
          --health-interval 5s
          --health-timeout 3s
          --health-retries 20
    steps:
      # NOTE: if the memohai/ui submodule is (or becomes) private, the default
      # GITHUB_TOKEN cannot fetch it; provide a deploy key via the `ssh-key`
      # input or a PAT via the `token` input of actions/checkout.
      - name: Check out repository (with ui submodule)
        uses: actions/checkout@v7
        with:
          submodules: recursive

      - name: Install toolchain (go/node/pnpm) via mise
        uses: jdx/mise-action@v4

      - name: Vet Go modules
        run: mise run vet

      - name: Test Go modules (unit + DB integration)
        run: mise run test
        env:
          TEST_DATABASE_URL: postgres://connect_it:connect_it@localhost:5432/connect_it_test?sslmode=disable

      - name: Install frontend dependencies
        run: pnpm install --frozen-lockfile

      - name: Build web UI (dist -> packages/api/webdist)
        run: mise run build-web

      - name: Docker build smoke test
        run: docker build -f docker/Dockerfile -t connect-it:ci .
```

- [ ] **Step 2: 本地静态校验**

```bash
mise exec actionlint@latest -- actionlint .github/workflows/ci.yml
```

预期：无输出，退出码0（actionlint零输出即通过；mise会临时安装actionlint，无需改动`mise.toml`）。

- [ ] **Step 3: 提交**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add vet/test/build-web/docker-build workflow"
```

- [ ] **Step 4: 远端真实运行验证（视权限，可选）**

若当前流程允许推送（通常在feature分支上）：

```bash
git push -u origin HEAD
gh run watch --exit-status
```

预期：`CI`工作流全部step绿：vet与test（日志中集成测试**不再**出现skip字样，说明`TEST_DATABASE_URL`生效）、`pnpm install --frozen-lockfile`、`build-web`、docker build依次通过。若本环节不允许push，以Step 2的actionlint通过作为本task验收，真实运行在分支推送后由执行者确认。

---

### Task 5: 根README重写

**Files:**
- Modify: `README.md`（现仅有`# connect-it`一行，整文件重写）

**Interfaces:**
- Consumes: Task 1、2的mise任务名（`docker-build`／`docker-up`／`docker-down`）、compose文件中的`CHANGE ME`约定、Global Constraints中的全部环境变量与既有任务。
- Produces: 面向新成员与部署者的入口文档；无代码消费者。

- [ ] **Step 1: 写入README.md全文**

`README.md`：

````markdown
# connect-it

connect-it is an internal, stateful connector service written in Go. It
centralizes third-party platform credentials (GitHub, Gmail, OneDrive,
Google Ads) and tool execution behind a single aggregated MCP endpoint
(Streamable HTTP), and ships a built-in Vue 3 admin UI for connector
configuration, OAuth authorization and health monitoring.

Key properties:

- Connector definitions live in code and are compiled into the binary; the
  database stores only admin configuration and connection state.
- Single binary: the web UI is embedded via go:embed and database migrations
  (golang-migrate) run automatically at startup.
- Single tenant by design: one deployment carries one set of connector
  configs; multiple connections of the same type are distinguished by a
  unique alias.

## Repository layout

```text
packages/
  ui/          # git submodule -> github.com/memohai/ui (component library)
  web/         # Vite + Vue 3 + Vue Router admin UI
  core/        # Go module: definition types, registry, crypto, status machine
  connectors/  # Go module: provider definitions + explicit registration
  service/     # Go module: sqlc store, migrations, OAuth, tool execution, MCP
  api/         # Go module: Echo handlers, cmd/connect-it, embedded web dist
docker/        # Dockerfile + docker-compose.yml
```

Go modules reference each other through relative `replace` directives in each
go.mod; there is no go.work. The ui submodule is required by the web build,
so always clone with submodules:

```bash
git clone --recursive https://github.com/memohai/connect-it.git
cd connect-it
```

## Quick start: local development

Prerequisites: [mise](https://mise.jdx.dev/) and Docker (for the dev
database).

```bash
mise install                     # go 1.25, node 22, pnpm 10
pnpm install                     # frontend workspace dependencies
mise run db-up                   # start the local dev postgres

# Minimal configuration (see the environment table below).
# Adjust DATABASE_URL if your dev postgres uses different credentials
# (see the db-up task definition in mise.toml).
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/connect_it?sslmode=disable"
export CONNECT_IT_SECRET_KEY="1:$(openssl rand -hex 32)"
export COOKIE_SECRET="$(openssl rand -hex 32)"
export CONNECT_IT_ADMIN_PASSWORD="dev-password"
export CONNECT_IT_BASE_URL="http://localhost:8080"

mise run dev                     # API server + Vite dev server
```

Stop the dev database with `mise run db-down`.

## Quick start: Docker

```bash
mise run docker-up               # build the image, start postgres + connect-it
curl http://localhost:8080/healthz   # -> 200
open http://localhost:8080           # admin UI
mise run docker-down             # stop; the postgres data volume is kept
```

`docker/docker-compose.yml` ships with development-only defaults. Every value
marked `CHANGE ME` (secret key, cookie secret, admin password, postgres
password) must be replaced before any deployment that outlives your terminal
session.

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `DATABASE_URL` | yes | — | Postgres DSN, e.g. `postgres://user:pass@host:5432/db?sslmode=disable`. Migrations run automatically at startup. |
| `CONNECT_IT_SECRET_KEY` | yes | — | AES-256-GCM keyring, format `1:<64-char-hex>`. Multiple keys for rotation: `1:<hex>,2:<hex>` (highest version encrypts). Generate one: `echo "1:$(openssl rand -hex 32)"`. |
| `COOKIE_SECRET` | yes | — | HMAC secret signing admin session cookies. Generate: `openssl rand -hex 32`. |
| `CONNECT_IT_ADMIN_PASSWORD` | yes | — | Initial admin password for `POST /admin/login`; can be changed in the admin UI afterwards. |
| `CONNECT_IT_BASE_URL` | yes | — | Externally reachable base URL; OAuth callback URLs (`<base>/v1/oauth/callback`) are built from it. |
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address. |
| `TEST_DATABASE_URL` | tests only | — | Enables Go integration tests; when unset those tests `t.Skip`. Point it at a disposable database only. |

## mise tasks

| Task | Description |
|---|---|
| `mise run dev` | Run the API server and the Vite dev server for local development |
| `mise run test` | Run Go tests module by module (integration tests need `TEST_DATABASE_URL`) |
| `mise run vet` | Run `go vet` module by module |
| `mise run sqlc` | Regenerate sqlc query code in `packages/service` |
| `mise run db-up` / `db-down` | Start / stop the local dev postgres |
| `mise run build-web` | Build `packages/web` and copy dist into `packages/api/webdist` (go:embed) |
| `mise run docker-build` | Build the production image as `connect-it:dev` |
| `mise run docker-up` / `docker-down` | Start / stop the docker compose stack in `docker/` |

## License

See [LICENSE](LICENSE).
````

- [ ] **Step 2: 结构验证**

```bash
grep '^## ' README.md
```

预期输出恰好六行：

```text
## Repository layout
## Quick start: local development
## Quick start: Docker
## Environment variables
## mise tasks
## License
```

- [ ] **Step 3: 提交**

```bash
git add README.md
git commit -m "docs: rewrite README with quick start, env vars and task tables"
```

---

## 完成标准（对照spec与契约）

- [ ] `mise run docker-build`成功产出`connect-it:dev`：三阶段（node:22→golang:1.25→alpine:3.24）、`CGO_ENABLED=0`、非root用户`connectit`、`EXPOSE 8080`、镜像内HEALTHCHECK打`/healthz`（spec §15、契约第1条）；
- [ ] `.dockerignore`排除`.git`、`node_modules`、本地`dist`／`webdist`，保留`packages/ui`；
- [ ] `mise run docker-up`后：`/healthz`返回200、`/`返回内嵌管理界面、错误密码`POST /admin/login`返回401；`mise run docker-down`干净落栈且保留数据卷（契约第2、5条）；
- [ ] compose栈：postgres:17带卷与`pg_isready`healthcheck，connect-it经`depends_on.condition: service_healthy`串联，env六项全列且`CHANGE ME`注释齐全，端口`8080:8080`（spec §15、契约第2条）；
- [ ] CI：push（main）与PR触发，`actions/checkout@v7`带`submodules: recursive`，`jdx/mise-action@v4`装工具链，postgres:17服务容器供`TEST_DATABASE_URL`，依次跑`mise run vet`→`mise run test`→`pnpm install --frozen-lockfile`→`mise run build-web`→docker build冒烟；actionlint通过（契约第3条）；
- [ ] `mise.toml`新增`docker-build`／`docker-up`／`docker-down`三个任务（契约第4条）；
- [ ] README覆盖：项目简介、mise与Docker双路径快速开始、七个环境变量的表格、十个mise任务的表格（契约第6条）。

## 偏离点

- jdx/mise-action：跨计划契约草案写`@v2`，但查证（2026-07-22，github.com/jdx/mise-action/releases）当前稳定主线为v4（v4.2.1），按「action版本以查证的当前稳定版为准」的要求采用`@v4`。
- 运行镜像在契约给出的两个选项中选`alpine:3.24`（非distroless），HEALTHCHECK内置于镜像、compose层不重复定义；理由见「运行镜像选型说明」。
- CI的push触发限定`main`分支：契约仅要求「push/PR触发」，不限分支会导致PR分支上每次push双跑（push＋pull_request各一次），故取`branches: [main]`＋全部PR的惯例组合。
- 端到端冒烟按契约「写成可执行步骤」落成本计划的Task 3（纯验证task，无文件产出、无提交），未额外增加mise任务。
