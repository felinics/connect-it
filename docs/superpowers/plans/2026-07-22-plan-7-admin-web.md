# connect-it 计划 7：前端管理界面（packages/web）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付`packages/web`管理界面（登录、连接器列表、配置动态表单、连接管理、API Token、修改密码六个页面），并给`packages/api`加三处配套改动：`GET /admin/connectors`薄路由、`GET /admin/connectors/:type/config-schema`端点、web 构建产物的`go:embed`静态服务与 SPA 回退。

**Architecture:** Vite＋Vue 3＋Vue Router 的单页应用，组件与 design token 全部来自`packages/ui`（`@felinic/ui`，git submodule，Tailwind v4 直接扫描其源码，不 build 不 publish）。API 访问经统一 fetch 封装（cookie 鉴权、错误归一、401 跳登录）。构建产物拷入`packages/api/webdist/dist`并 embed 进服务二进制；开发模式 Vite proxy 到本地`:8080`。

**Tech Stack:** Vue ^3.5.26、Vue Router ^4.6、Vite ^8、Tailwind CSS v4（`@tailwindcss/vite`）、`@felinic/ui`（workspace 路径依赖）、vitest＋`@vue/test-utils`＋jsdom、TypeScript；Go 侧 Echo＋`go:embed`。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 4、12、14 节）＋本计划开头的跨计划接口契约。计划 1–6 的代码假定已全部落地。

## Global Constraints

### 项目级（spec＋跨计划契约，逐字生效）

- 前端包：`packages/web`，Vite＋Vue 3＋Vue Router＋Tailwind；pnpm workspace 成员；`packages/ui`以路径依赖使用（不 build 不 publish，Tailwind 扫描`../ui/src`）；Vue 版本由根 lockfile 钉住。
- 后端 JSON 一律 snake_case；错误响应`{"error":"machine_code","message":"…"}`。
- API 用 fetch 封装，必须`credentials: 'include'`。
- OAuth 回调成功后 302 到`/connections?connected=alias`，前端路由必须处理该 query 并提示成功。
- 健康状态直接用 connectors 列表的 status 徽标呈现，不单独设页（覆盖 spec §14 的「健康与验证」独立页表述）。
- Secret 字段只写不读：配置读取只回`secret_keys_set`，表单值永远从空开始，已设置的显示占位「已设置」。
- Go module 路径固定`github.com/memohai/connect-it/packages/<name>`；无 go.work，`go`命令必须`cd`进对应 module 执行；api module 已 replace core／connectors／service。
- 界面文案为简体中文（全角标点）；代码、命令、标识符用英文。
- 工具链经 mise：go 1.25、node 22、pnpm 10。提交用 conventional commits，每个 task 至少一次提交。
- 依赖版本对齐 ui 库要求：`vue ^3.5.26`（ui 的 peerDependency）、`tailwindcss`／`@tailwindcss/vite ^4.2.2`、`vite ^8.0.1`、`@vitejs/plugin-vue ^6.0.5`。安装时若 pnpm 报 peer 冲突，允许在同一大版本内调小版本。
- 范围外：Docker／CI（计划 8）。

### 从 ui 库提炼的规则（每条页面代码都必须遵守；出处标注在句末）

- R1：ui 以 git submodule 挂载于`packages/ui`（挂载点不可变），包名`@felinic/ui`，导出`.`→`src/index.ts`、`./style.css`→`src/style.css`；组件一律从`@felinic/ui`顶层导入。（README、package.json）
- R2：一切样式值都是 token——禁止裸色值（hex／rgb／`bg-white`／`text-gray-*`等）、任意半径、自造 shadow；只用语义类`bg-background`／`bg-card`／`bg-muted`／`text-foreground`／`text-muted-foreground`／`text-destructive`／`border-border`等，深色模式因此自动成立。（AGENTS.md「Everything is a token」；skills/web/SKILL.md「一个 bg-white 就会默默打破 dark」）
- R3：className 红线——ui 组件上只允许布局类`w-full`、`flex-1`、`gap-*`、外边距、`max-w-*`；禁止在组件上手写`bg-*`、`hover:*`、`border-*`、`shadow-*`、`ring-*`、`h-[Npx]`。组件通过 API 表达意图（`variant="destructive"`），不发明 chrome。（AGENTS.md「Never invent chrome」；SKILL.md「The className red line」）
- R4：字号只用`--text-*`阶梯（`text-caption`／`text-body`／`text-label`／`text-control`／`text-title`／`text-heading`／`text-display`），禁止`text-[Npx]`；文字相关尺寸用 rem；px 仅限 1–4px hairline、`border-*`／`ring-*`、图标`size-*`。（AGENTS.md「Scale with Font — rem Only」）
- R5：半径 role-map——badge／tag`rounded-sm`(6)、控件`rounded-md`(8)、菜单壳`rounded-menu-shell`(12)、卡片／Dialog`rounded-xl`(14)，不得偏离。（AGENTS.md「Radius Scale」）
- R6：z-index 只用语义梯`z-raised`／`z-sticky`／`z-panel`／`z-overlay`／`z-top`，禁止裸`z-10/20/50`。（AGENTS.md「Z-Index Ladder」）
- R7：图标一律 lucide-vue-next 组件（默认`size-4`），禁止字符图形；icon 按钮用`<Button variant="ghost" size="icon">`。（AGENTS.md「Icons are always components」；SKILL.md）
- R8：组件优先级——复用现有→组合原子→升级组件→（绝不）手写样式；菜单必须用`DropdownMenu`／`ContextMenu`系列，不得手搓按钮行。（SKILL.md「Component Discipline」）
- R9：页面壳`mx-auto max-w-3xl px-6 pt-10 pb-12`；节间`space-y-8`；节标题是卡片上方的 quiet 文本；行分隔用卡内 inset 分隔线`mx-4 border-b border-border py-3 last:border-b-0`；标签→描述`mt-0.5`；Label＋控件成组`space-y-1.5`、字段栈`space-y-4`。（SKILL.md「Shell & spacing」；reference.md）
- R10：禁止 card-in-card（卡里再套带边框盒）；空态用`Empty`组件且保持占位框架；加载状态必须预留高度不跳版。（reference.md「Dirty ↔ Clean Diagnostic」；SKILL.md）
- R11：表单遵循 New Task 范式——可选字段在 Label 上标注「（可选）」而非 placeholder；提交时才校验，不在 blur 时飘红；Dialog footer 按钮用默认高度（`h-9`），不用`sm`。（SKILL.md「UX Principles」；reference.md「Forms & Disclosure」）
- R12：保存模型不发明——手动保存：按钮 spinner＋一次成功 toast，失败 error toast；禁用态一律组件内建的`opacity-40`，页面不额外处理。（SKILL.md「One save model」；AGENTS.md「Disabled & Pointer」）
- R13：hover／选中 tint 只用中性 overlay 阶梯与语义类；蓝=选中、紫保留；高强调按钮用默认 variant（charcoal），不造品牌色按钮。（AGENTS.md「Color」）
- R14：`SettingsSection`／`SettingsRow`／`PageShell`等 owner 组件驻留宿主仓库（ui 库尚未收编）——本计划在`packages/web`自建这三个组件，且只用 token 与语义类。（README「Owner components remain in host repos pending promotion」）
- R15：提交前复核——grep 裸色（`bg-white|bg-black|text-white|text-black|-gray-|-zinc-|#[0-9a-fA-F]{3,6}`）必须零命中；窄宽度不破版；dark 模式靠 token 自动成立。（SKILL.md「Review Ritual」）
- R16：宿主 CSS 入口先`@import "tailwindcss"`再`@import "@felinic/ui/style.css"`（ui 的 style.css 自身不引 tailwind），并用`@source`把`../ui/src`纳入扫描（pnpm 链接位于 node_modules，Tailwind v4 默认不扫）。（README；style.css 结构；spec §14）

### 接口假设（计划 2–5 已实装但未在契约中钉死的部分）

以下形状按最常见约定假设。**若与实际不符，只允许调整`src/api/endpoints.ts`／`types.ts`的映射层与 Go 接线处，不改页面组件接口**：

- Echo 路由注册位于`packages/api/internal/server/routes.go`；存在`e *echo.Echo`、挂 cookie 鉴权中间件的`admin`Group（前缀`/admin`，login 除外）、服务`/v1/connectors`的 handler（下称`catalogHandler.List`／`catalogHandler.Get`）、启动时构建的`reg *registry.Registry`。执行时先 grep 确认实名，按实名替换（映射不变）。
- 列表响应包裹：`{"connectors":[…]}`、`{"connections":[…]}`、`{"api_tokens":[…]}`。
- `GET …/config`在尚未配置时返回 404`{"error":"config_not_found"}`；`PUT`响应与`GET`同形；乐观并发冲突返回 409。
- `config:validate`请求`{"public":{…},"secrets":{…}}`，响应`{"valid":bool,"field_errors":{key:msg}}`。
- `mcp:verify`成功 200；失败为 4xx／5xx 错误 JSON。
- `POST /admin/connections/oauth`请求`{"connector_type","alias"}`；`…/:id/reauth`响应同样是`{"authorization_url":"…"}`。
- `POST /admin/connections/api-key`请求`{"connector_type","alias","api_key"}`。
- `POST /admin/api-tokens`请求`{"name"}`，响应`{"id","name","token","created_at"}`（token 为一次性明文）。
- `PUT /admin/account/password`请求`{"current_password","new_password"}`。
- `POST /admin/login`成功返回 204＋`Set-Cookie: connect_it_admin`。

## File Structure

```text
packages/web/
├── package.json  index.html  tsconfig.json  vite.config.ts  vitest.config.ts
└── src/
    ├── main.ts  App.vue  style.css  env.d.ts
    ├── router/index.ts（＋index.test.ts）
    ├── api/client.ts  types.ts  endpoints.ts（＋client.test.ts）
    ├── lib/navigation.ts
    ├── layouts/AppLayout.vue
    ├── components/PageShell.vue  SettingsSection.vue  SettingsRow.vue（＋shell.test.ts）
    │             StatusBadge.vue（＋test） ConfigForm.vue（＋test） ConfirmDialog.vue
    └── pages/LoginPage.vue  ConnectorsPage.vue  ConnectorConfigPage.vue
              ConnectionsPage.vue  TokensPage.vue  SettingsPage.vue（各自＋test）
packages/api/
├── internal/configschema/configschema.go（＋test）   # 新
├── internal/webui/webui.go（＋test）                  # 新
├── webdist/webdist.go  webdist/dist/.gitkeep         # 新
└── internal/server/routes.go                          # 接线（假设路径）
mise.toml（新增 test-web／dev-web／build-web 任务）  .gitignore（追加）
```

路由与页面（spec §14＋契约）：`/login`；`/connectors`（列表＋状态徽标）；`/connectors/:type`（schema 驱动动态表单＋validate／verify）；`/connections`（列表、OAuth、API key、reauth、删除、`?connected=`提示）；`/tokens`；`/settings`。

---

### Task 1: api——config-schema 端点 handler

**Files:**
- Create: `packages/api/internal/configschema/configschema.go`
- Test: `packages/api/internal/configschema/configschema_test.go`

**Interfaces:**
- Consumes: 计划 1 的`connector.Definition`／`connector.ConfigField`／`registry.Registry`（`reg.Get(connector.Type) (connector.Definition, bool)`）；api module 已依赖 echo v4 与 core。
- Produces: `configschema.Handler(reg *registry.Registry) echo.HandlerFunc`——Task 2 把它挂到`GET /admin/connectors/:type/config-schema`；响应 JSON 形状即前端`ConfigSchema`类型（Task 4）。

- [ ] **Step 1: 写失败测试**

`packages/api/internal/configschema/configschema_test.go`：

```go
package configschema_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/memohai/connect-it/packages/api/internal/configschema"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func strPtr(s string) *string { return &s }

func newRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	r.MustRegister(connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 2,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText,
				Required: true, Description: "OAuth App 的 Client ID"},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText,
				Required: true, Secret: true},
			{Key: "tenant", Label: "Tenant", InputType: connector.InputSelect,
				DefaultValue: strPtr("common"),
				Validation:   connector.FieldValidation{Options: []string{"common", "organizations"}}},
		},
	})
	return r
}

func doRequest(t *testing.T, typ string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.GET("/admin/connectors/:type/config-schema", configschema.Handler(newRegistry(t)))
	req := httptest.NewRequest(http.MethodGet, "/admin/connectors/"+typ+"/config-schema", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestConfigSchemaOK(t *testing.T) {
	rec := doRequest(t, "example_app")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		ConnectorType       string `json:"connector_type"`
		ConfigSchemaVersion int    `json:"config_schema_version"`
		Fields              []struct {
			Key          string  `json:"key"`
			Label        string  `json:"label"`
			InputType    string  `json:"input_type"`
			Required     bool    `json:"required"`
			Secret       bool    `json:"secret"`
			DefaultValue *string `json:"default_value"`
			Description  string  `json:"description"`
			Validation   struct {
				Pattern string   `json:"pattern"`
				Options []string `json:"options"`
			} `json:"validation"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ConnectorType != "example_app" || body.ConfigSchemaVersion != 2 {
		t.Fatalf("头部字段不对: %+v", body)
	}
	if len(body.Fields) != 3 {
		t.Fatalf("fields 数量 = %d, want 3", len(body.Fields))
	}
	f0, f1, f2 := body.Fields[0], body.Fields[1], body.Fields[2]
	if f0.Key != "client_id" || !f0.Required || f0.Secret || f0.InputType != "text" {
		t.Fatalf("client_id 字段不对: %+v", f0)
	}
	if f1.Key != "client_secret" || !f1.Secret || f1.DefaultValue != nil {
		t.Fatalf("client_secret 字段不对: %+v", f1)
	}
	if f2.Key != "tenant" || f2.InputType != "select" ||
		f2.DefaultValue == nil || *f2.DefaultValue != "common" {
		t.Fatalf("tenant 字段不对: %+v", f2)
	}
	if len(f2.Validation.Options) != 2 || f2.Validation.Options[0] != "common" {
		t.Fatalf("tenant options 不对: %+v", f2.Validation)
	}
	// snake_case 键名必须原样出现在响应里
	raw := rec.Body.String()
	for _, key := range []string{`"connector_type"`, `"config_schema_version"`,
		`"input_type"`, `"default_value"`, `"secret"`, `"validation"`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("响应缺少 %s: %s", key, raw)
		}
	}
}

func TestConfigSchemaNotFound(t *testing.T) {
	rec := doRequest(t, "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "connector_not_found" || body["message"] == "" {
		t.Fatalf("错误响应不对: %+v", body)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/api && go test ./internal/configschema/
```

预期：编译失败，`undefined: configschema.Handler`。

- [ ] **Step 3: 写实现**

`packages/api/internal/configschema/configschema.go`：

```go
// Package configschema 把代码 Registry 里的 ConfigFields 以 JSON 暴露给管理界面，
// 用于驱动动态配置表单。只读 Definition，不访问数据库。
package configschema

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

type fieldValidation struct {
	Pattern string   `json:"pattern"`
	Options []string `json:"options"`
}

type field struct {
	Key          string          `json:"key"`
	Label        string          `json:"label"`
	InputType    string          `json:"input_type"`
	Required     bool            `json:"required"`
	Secret       bool            `json:"secret"`
	DefaultValue *string         `json:"default_value"`
	Description  string          `json:"description"`
	Validation   fieldValidation `json:"validation"`
}

type response struct {
	ConnectorType       string  `json:"connector_type"`
	ConfigSchemaVersion int     `json:"config_schema_version"`
	Fields              []field `json:"fields"`
}

// Handler 处理 GET /admin/connectors/:type/config-schema。
func Handler(reg *registry.Registry) echo.HandlerFunc {
	return func(c echo.Context) error {
		def, ok := reg.Get(connector.Type(c.Param("type")))
		if !ok {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "connector_not_found",
				"message": "未知的 connector type",
			})
		}
		fields := make([]field, 0, len(def.ConfigFields))
		for _, f := range def.ConfigFields {
			options := f.Validation.Options
			if options == nil {
				options = []string{}
			}
			fields = append(fields, field{
				Key:          f.Key,
				Label:        f.Label,
				InputType:    string(f.InputType),
				Required:     f.Required,
				Secret:       f.Secret,
				DefaultValue: f.DefaultValue,
				Description:  f.Description,
				Validation:   fieldValidation{Pattern: f.Validation.Pattern, Options: options},
			})
		}
		return c.JSON(http.StatusOK, response{
			ConnectorType:       string(def.Type),
			ConfigSchemaVersion: def.ConfigSchemaVersion,
			Fields:              fields,
		})
	}
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/api && go test ./internal/configschema/
```

预期：`ok  github.com/memohai/connect-it/packages/api/internal/configschema`。

- [ ] **Step 5: 提交**

```bash
git add packages/api/internal/configschema
git commit -m "feat(api): add config-schema endpoint handler"
```

---

### Task 2: api——/admin/connectors 薄路由与 config-schema 接线

**Files:**
- Modify: `packages/api/internal/server/routes.go`（假设路径，Step 1 用 grep 确认实际文件）

**Interfaces:**
- Consumes: Task 1 的`configschema.Handler`；计划 2 的 admin Group（cookie 鉴权）、`/v1/connectors`的 catalog handler、`reg *registry.Registry`。
- Produces: `GET /admin/connectors`、`GET /admin/connectors/:type`（与`/v1`同 handler、cookie 鉴权）、`GET /admin/connectors/:type/config-schema`——前端 Task 4 的`listConnectors`／`getConnector`／`getConfigSchema`依赖这三条路由。

- [ ] **Step 1: 定位路由注册处**

```bash
grep -rn "v1/connectors\|Group(\"/admin\"\|Group(\"/v1\"" packages/api --include="*.go"
```

预期：找到注册`/v1/connectors`与`/admin`Group 的文件（假设为`internal/server/routes.go`）。记下 catalog handler 与 admin Group 的实际变量名，下一步按实名替换。

- [ ] **Step 2: 添加路由**

在 admin Group 的路由注册处（login 之外、其余`/admin`路由同级）加入，`admin`／`catalogHandler`／`reg`按 Step 1 实名替换：

```go
// 管理端 catalog：与 /v1/connectors 完全同一 handler（永不含 Secret），
// 差别只在鉴权——Bearer api_token 换成 cookie，由 admin Group 中间件承担。
admin.GET("/connectors", catalogHandler.List)
admin.GET("/connectors/:type", catalogHandler.Get)
// 配置表单元数据（本计划 Task 1）。
admin.GET("/connectors/:type/config-schema", configschema.Handler(reg))
```

import 块加入：

```go
"github.com/memohai/connect-it/packages/api/internal/configschema"
```

- [ ] **Step 3: 构建与全量测试**

```bash
cd packages/api && go build ./... && go test ./...
```

预期：全部通过。若计划 2 有路由表测试，把三条新路由补进对应断言。

- [ ] **Step 4: 手工验证（可选，需本地 dev 环境）**

```bash
curl -s -c /tmp/ci.txt -X POST http://localhost:8080/admin/login \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"<本地密码>"}'
curl -s -b /tmp/ci.txt http://localhost:8080/admin/connectors
curl -s -b /tmp/ci.txt http://localhost:8080/admin/connectors/github/config-schema
```

预期：第二条返回`{"connectors":[…]}`且每项含`type/name/status`；第三条返回`connector_type/config_schema_version/fields`。

- [ ] **Step 5: 提交**

```bash
git add packages/api
git commit -m "feat(api): expose admin catalog routes and config-schema wiring"
```

---

### Task 3: web 脚手架与应用骨架

**Files:**
- Create: `packages/web/package.json`、`index.html`、`tsconfig.json`、`vite.config.ts`、`vitest.config.ts`
- Create: `packages/web/src/style.css`、`env.d.ts`、`main.ts`、`App.vue`
- Create: `packages/web/src/router/index.ts`、`src/layouts/AppLayout.vue`
- Create: `packages/web/src/components/PageShell.vue`、`SettingsSection.vue`、`SettingsRow.vue`
- Create: `packages/web/src/pages/`下六个占位页面
- Modify: `mise.toml`（新增 test-web／dev-web 任务）
- Test: `packages/web/src/router/index.test.ts`、`src/components/shell.test.ts`

**Interfaces:**
- Consumes: 计划 1 的 pnpm workspace（`pnpm-workspace.yaml`已含`packages/web`）与`packages/ui`submodule。
- Produces: 可构建的 SPA 骨架；`PageShell{ title, slot, #actions }`、`SettingsSection{ title?, slot, #actions }`、`SettingsRow{ slot, #control }`、`router`（`/login`＋五个业务路由）——后续所有页面 task 依赖这些组件与路由名。

- [ ] **Step 0: 读 ui 规范（spec §14 硬性要求）**

读`packages/ui/AGENTS.md`与`packages/ui/skills/web/`（SKILL.md、reference.md），核对本计划用到的组件 API（`Input`的 v-model 与属性透传、`NativeSelect`／`NativeSelectOption`、`Dialog`的`v-model:open`、`Badge`的 variant、`Button`的 variant／size、`toast.success/error`、`Toaster`、`Empty`系列、`Spinner`、`Label`）。若与本计划代码不符，以 ui 源码为准做最小调整并保持组件对外接口不变。

- [ ] **Step 1: 写配置与入口文件**

`packages/web/package.json`：

```json
{
  "name": "web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest",
    "type-check": "vue-tsc --noEmit"
  },
  "dependencies": {
    "@felinic/ui": "workspace:*",
    "vue": "^3.5.26",
    "vue-router": "^4.6.0"
  },
  "devDependencies": {
    "@tailwindcss/vite": "^4.2.2",
    "@types/node": "^24.10.4",
    "@vitejs/plugin-vue": "^6.0.5",
    "@vue/test-utils": "^2.4.6",
    "@vue/tsconfig": "^0.8.1",
    "jsdom": "^26.0.0",
    "tailwindcss": "^4.2.2",
    "typescript": "~5.9.3",
    "vite": "^8.0.1",
    "vitest": "^4.0.0",
    "vue-tsc": "^3.2.1"
  }
}
```

`packages/web/index.html`：

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>connect-it</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

`packages/web/tsconfig.json`：

```json
{
  "extends": "@vue/tsconfig/tsconfig.dom.json",
  "compilerOptions": {
    "noEmit": true,
    "baseUrl": ".",
    "paths": { "@/*": ["./src/*"] },
    "types": ["vite/client"]
  },
  "include": ["src/**/*.ts", "src/**/*.vue", "vite.config.ts", "vitest.config.ts"]
}
```

`packages/web/vite.config.ts`（契约：dev proxy `/v1`、`/admin`、`/healthz`到`http://localhost:8080`）：

```ts
import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    proxy: {
      '/v1': 'http://localhost:8080',
      '/admin': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
    },
  },
})
```

`packages/web/vitest.config.ts`（独立于 vite.config，避免 vitest 与 vite 8 的 peer 纠缠）：

```ts
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  test: {
    environment: 'jsdom',
  },
})
```

`packages/web/src/style.css`（R16）：

```css
@import "tailwindcss";
@import "@felinic/ui/style.css";

/* Tailwind v4 不扫描 node_modules（pnpm 链接的 packages/ui 也算），
   必须显式把 ui 源码纳入扫描（spec §14：Tailwind 扫描 ../ui/src）。 */
@source "../../ui/src";
```

`packages/web/src/env.d.ts`：

```ts
/// <reference types="vite/client" />
```

- [ ] **Step 2: 安装依赖**

```bash
mise install && pnpm install
ls packages/web/node_modules/@felinic
```

预期：安装成功；`@felinic/ui`是指向`../../ui`的 workspace 链接。

- [ ] **Step 3: 写失败测试**

`packages/web/src/router/index.test.ts`：

```ts
import { describe, expect, it } from 'vitest'
import { router } from './index'

describe('router', () => {
  it('注册 spec §14 要求的全部页面路由', () => {
    const paths = router.getRoutes().map((r) => r.path)
    for (const p of ['/login', '/connectors', '/connectors/:type', '/connections', '/tokens', '/settings']) {
      expect(paths).toContain(p)
    }
  })
})
```

`packages/web/src/components/shell.test.ts`：

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import PageShell from './PageShell.vue'
import SettingsRow from './SettingsRow.vue'
import SettingsSection from './SettingsSection.vue'

describe('页面骨架组件', () => {
  it('PageShell 渲染标题与内容槽，页面壳类名符合 R9', () => {
    const w = mount(PageShell, { props: { title: '连接器' }, slots: { default: '<p>content</p>' } })
    expect(w.find('h1').text()).toBe('连接器')
    expect(w.html()).toContain('content')
    expect(w.find('main').classes()).toEqual(
      expect.arrayContaining(['mx-auto', 'max-w-3xl', 'px-6', 'pt-10', 'pb-12']),
    )
  })

  it('SettingsSection 标题在卡片外、卡片为 bg-card 白卡（R9/R10）', () => {
    const w = mount(SettingsSection, { props: { title: '配置' }, slots: { default: '<p>inner</p>' } })
    expect(w.find('h2').text()).toBe('配置')
    const card = w.find('div.rounded-xl')
    expect(card.classes()).toEqual(expect.arrayContaining(['border', 'border-border', 'bg-card']))
    expect(card.html()).toContain('inner')
  })

  it('SettingsRow 使用卡内 inset 分隔线（R9）', () => {
    const w = mount(SettingsRow, { slots: { default: '<span>左</span>', control: '<button>右</button>' } })
    expect(w.classes()).toEqual(
      expect.arrayContaining(['mx-4', 'border-b', 'border-border', 'py-3', 'last:border-b-0']),
    )
    expect(w.text()).toContain('左')
    expect(w.find('button').exists()).toBe(true)
  })
})
```

- [ ] **Step 4: 运行确认失败**

```bash
pnpm --filter web test
```

预期：FAIL，`Cannot find module './index'`／`'./PageShell.vue'`等。

- [ ] **Step 5: 写实现**

`packages/web/src/main.ts`：

```ts
import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './style.css'

createApp(App).use(router).mount('#app')
```

`packages/web/src/App.vue`：

```vue
<script setup lang="ts">
import { Toaster } from '@felinic/ui'
</script>

<template>
  <RouterView />
  <Toaster />
</template>
```

`packages/web/src/router/index.ts`：

```ts
import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('@/pages/LoginPage.vue') },
    {
      path: '/',
      component: () => import('@/layouts/AppLayout.vue'),
      children: [
        { path: '', redirect: '/connectors' },
        { path: 'connectors', component: () => import('@/pages/ConnectorsPage.vue') },
        { path: 'connectors/:type', component: () => import('@/pages/ConnectorConfigPage.vue') },
        { path: 'connections', component: () => import('@/pages/ConnectionsPage.vue') },
        { path: 'tokens', component: () => import('@/pages/TokensPage.vue') },
        { path: 'settings', component: () => import('@/pages/SettingsPage.vue') },
      ],
    },
  ],
})
```

`packages/web/src/layouts/AppLayout.vue`（导航链接为裸`<a>`元素而非 ui 组件，ui 库无 Link 组件；只用语义 token，样式对齐 Button 的 quiet 阶梯，见偏离点 2）：

```vue
<script setup lang="ts">
const links = [
  { to: '/connectors', label: '连接器' },
  { to: '/connections', label: '连接' },
  { to: '/tokens', label: 'API Token' },
  { to: '/settings', label: '设置' },
]
</script>

<template>
  <div class="min-h-dvh bg-background text-foreground">
    <header class="sticky top-0 z-sticky border-b border-border bg-background">
      <nav class="mx-auto flex h-12 max-w-3xl items-center gap-1 px-6">
        <span class="mr-4 text-label font-semibold">connect-it</span>
        <RouterLink
          v-for="link in links"
          :key="link.to"
          :to="link.to"
          class="rounded-md px-2.5 py-1.5 text-control text-muted-foreground transition-colors hover:text-foreground"
          active-class="text-foreground"
        >
          {{ link.label }}
        </RouterLink>
      </nav>
    </header>
    <RouterView />
  </div>
</template>
```

`packages/web/src/components/PageShell.vue`：

```vue
<script setup lang="ts">
defineProps<{ title: string }>()
</script>

<template>
  <main class="mx-auto max-w-3xl px-6 pt-10 pb-12">
    <div class="mb-6 flex items-center justify-between gap-4">
      <h1 class="text-heading font-semibold text-foreground">{{ title }}</h1>
      <div class="flex items-center gap-2"><slot name="actions" /></div>
    </div>
    <div class="space-y-8"><slot /></div>
  </main>
</template>
```

`packages/web/src/components/SettingsSection.vue`：

```vue
<script setup lang="ts">
defineProps<{ title?: string }>()
</script>

<template>
  <section>
    <div v-if="title || $slots.actions" class="mb-2 flex items-center justify-between gap-4">
      <h2 class="text-label font-medium text-muted-foreground">{{ title }}</h2>
      <div class="flex items-center gap-2"><slot name="actions" /></div>
    </div>
    <div class="rounded-xl border border-border bg-card">
      <slot />
    </div>
  </section>
</template>
```

`packages/web/src/components/SettingsRow.vue`：

```vue
<template>
  <div class="mx-4 flex items-center justify-between gap-4 border-b border-border py-3 last:border-b-0">
    <div class="min-w-0 flex-1">
      <slot />
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <slot name="control" />
    </div>
  </div>
</template>
```

六个占位页面（后续 task 逐个替换为完整实现）。`packages/web/src/pages/LoginPage.vue`：

```vue
<template>
  <main class="flex min-h-dvh items-center justify-center bg-background px-6" />
</template>
```

`ConnectorsPage.vue`／`ConnectorConfigPage.vue`／`ConnectionsPage.vue`／`TokensPage.vue`／`SettingsPage.vue`统一为（title 依次为「连接器」「连接器配置」「连接」「API Token」「设置」）：

```vue
<script setup lang="ts">
import PageShell from '@/components/PageShell.vue'
</script>

<template>
  <PageShell title="连接器" />
</template>
```

- [ ] **Step 6: 运行确认通过＋构建**

```bash
pnpm --filter web test
pnpm --filter web build
```

预期：4 个测试全过；`vite build`产出`packages/web/dist/index.html`与`assets/`。

- [ ] **Step 7: mise 任务**

`mise.toml`追加：

```toml
[tasks.test-web]
description = "运行前端 vitest 测试"
run = "pnpm --filter web test"

[tasks.dev-web]
description = "启动前端开发服务器（proxy 到 :8080）"
run = "pnpm --filter web dev"
```

运行`mise run test-web`预期与 Step 6 相同。

- [ ] **Step 8: 提交**

```bash
git add packages/web mise.toml pnpm-lock.yaml
git commit -m "feat(web): scaffold admin SPA shell with ui integration"
```

---

### Task 4: packages/sdk 生成与 web 接入层（2026-07-22 修订：Hey API SDK 方案）

> **修订说明**：本 task 原方案是手写 fetch client。按新决策改为：API 调用一律经`packages/sdk`（`@hey-api/openapi-ts`从`packages/api/docs/swagger.json`生成，产物提交进仓库）；web 内**禁止手写 fetch 端点**。原 Step 中`client.ts`的手写`api()`fetch 实现**作废**，`endpoints.ts`改为委托生成 SDK 的薄适配层——它导出的函数名、参数与返回类型**保持原契约不变**，因此 Task 5–11 的页面代码与测试无需任何改动。`ApiError`归一、`setUnauthorizedHandler`、404→null、`navigation.ts`语义全部保留。

**Files:**
- Create: `packages/sdk/package.json`、`packages/sdk/openapi-ts.config.ts`、`packages/sdk/src/`（openapi-ts 生成，提交）、`packages/sdk/index.ts`
- Create: `packages/web/src/api/client.ts`（配置生成客户端＋拦截器，不再手写 fetch）、`src/api/types.ts`（re-export 生成模型＋补充别名）、`src/api/endpoints.ts`（委托 SDK）、`src/lib/navigation.ts`
- Modify: `packages/web/package.json`（依赖`"@connect-it/sdk": "workspace:*"`）、`packages/web/src/main.ts`（接 401 处理）
- Modify: `mise.toml`（sdk 任务）
- Test: `packages/web/src/api/client.test.ts`（断言改为经拦截器归一：ApiError、401 回调、404→null）

**Step A（新增）: 建立 packages/sdk**

`packages/sdk/package.json`：

```json
{
  "name": "@connect-it/sdk",
  "private": true,
  "type": "module",
  "main": "./index.ts",
  "types": "./index.ts",
  "scripts": {
    "generate": "openapi-ts"
  },
  "dependencies": {
    "@hey-api/client-fetch": "^0.13.1"
  },
  "devDependencies": {
    "@hey-api/openapi-ts": "^0.84.4"
  }
}
```

`packages/sdk/openapi-ts.config.ts`：

```ts
import { defineConfig } from '@hey-api/openapi-ts'

export default defineConfig({
  input: '../api/docs/swagger.json',
  output: { path: 'src', format: 'prettier' },
  plugins: ['@hey-api/client-fetch', '@hey-api/typescript', '@hey-api/sdk'],
})
```

`packages/sdk/index.ts`：`export * from './src'`（生成目录的桶文件；以生成产物实际入口为准）。

`mise.toml`追加：

```toml
[tasks.sdk]
description = "由 packages/api/docs/swagger.json 重新生成 TypeScript SDK"
run = "pnpm --dir packages/sdk run generate"
```

执行`pnpm install && mise run swagger && mise run sdk`，把生成的`packages/sdk/src/`**提交进仓库**。注意：SDK 函数名取自 OpenAPI operationId——计划 2／3 的 swag 注释须带`@ID`（如`@ID listConnectors`、`@ID putConfig`），保证生成函数名可读；若已落地的 handler 缺`@ID`，补注释后重跑`mise run swagger && mise run sdk`。上述依赖版本以实现时`pnpm add`拉到的当前版本为准（记录进 lockfile 即可），配置字段名以所装版本文档为准，出入只改`openapi-ts.config.ts`。

**Step B（改造）: web 接入层**

- `client.ts`：`import { client } from '@connect-it/sdk'`→`client.setConfig({ baseUrl: '', fetch, credentials: 'include' })`；注册响应拦截器：非 2xx→解析`{"error","message"}`抛`ApiError{status, code, message}`（非 JSON 回退`unknown_error`）；401 且非登录请求→调`setUnauthorizedHandler`注册的回调。导出`ApiError`与`setUnauthorizedHandler`（签名与原契约一致）。
- `endpoints.ts`：逐函数委托 SDK（例：`login`→SDK 的`postAdminLogin`；`getConfig`捕获 404 ApiError 返回 null）。导出函数名与返回类型**保持本 task 原契约**。
- `types.ts`：从`@connect-it/sdk`re-export 生成模型并起原契约的类型别名（`CatalogItem`等）。

**Interfaces:**
- Consumes: Task 3 的 router；Task 1／2 与计划 2–5 的后端路由；`packages/api/docs/swagger.json`
- Produces（与原契约一致，页面代码不感知 SDK）:
  - `client.ts`：`class ApiError{status, code, message}`、`setUnauthorizedHandler(fn|null)`
  - `types.ts`：`ConnectorStatus`、`CatalogItem`、`ConfigField`、`ConfigSchema`、`ConnectorConfig`、`ConfigPayload`、`ValidateResult`、`ConnectionStatus`、`Connection`、`ApiToken`、`CreatedApiToken`
  - `endpoints.ts`：`login`、`listConnectors`、`getConnector`、`getConfigSchema`、`getConfig`（404→null）、`putConfig`、`deleteConfig`、`validateConfig`、`verifyMcp`、`listConnections`、`startOAuth`、`createApiKeyConnection`、`deleteConnection`、`reauthConnection`、`listApiTokens`、`createApiToken`、`deleteApiToken`、`changePassword`
  - `navigation.ts`：`redirectTo(url)`（`window.location.assign`薄封装，测试 mock 用）

> 以下原 Step 1–N 中：`client.test.ts`的用例语义保留（ApiError 归一、401 回调、空响应、错误回退），但改为对「生成客户端＋拦截器」断言——stub `fetch`的方式不变；`endpoints.ts`代码段中直接调`api()`处改为委托 SDK 函数。其余不变。

- [ ] **Step 1: 写失败测试**

`packages/web/src/api/client.test.ts`：

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, setUnauthorizedHandler } from './client'

function stubFetch(status: number, body: string, contentType = 'application/json') {
  const fetchMock = vi.fn(async () => new Response(body, {
    status,
    headers: { 'Content-Type': contentType },
  }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(null)
})

describe('api', () => {
  it('携带 credentials include 与 JSON body', async () => {
    const fetchMock = stubFetch(200, '{"ok":true}')
    await api('/admin/x', { method: 'POST', body: { a: 1 } })
    expect(fetchMock).toHaveBeenCalledWith('/admin/x', expect.objectContaining({
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: '{"a":1}',
    }))
  })

  it('解析成功响应的 JSON', async () => {
    stubFetch(200, '{"connectors":[]}')
    await expect(api('/admin/connectors')).resolves.toEqual({ connectors: [] })
  })

  it('空响应体返回 undefined', async () => {
    stubFetch(204, '')
    await expect(api('/admin/x', { method: 'DELETE' })).resolves.toBeUndefined()
  })

  it('错误响应归一为 ApiError（machine code＋message）', async () => {
    stubFetch(409, '{"error":"config_conflict","message":"配置已被修改"}')
    const err = await api('/admin/x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(409)
    expect((err as ApiError).code).toBe('config_conflict')
    expect((err as ApiError).message).toBe('配置已被修改')
  })

  it('非 JSON 错误响应回退 unknown_error', async () => {
    stubFetch(502, '<html>bad gateway</html>', 'text/html')
    const err = await api('/admin/x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe('unknown_error')
    expect((err as ApiError).message).toBe('HTTP 502')
  })

  it('401 触发 unauthorized 回调', async () => {
    stubFetch(401, '{"error":"unauthorized","message":"未登录"}')
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    await expect(api('/admin/connectors')).rejects.toBeInstanceOf(ApiError)
    expect(handler).toHaveBeenCalledOnce()
  })

  it('登录接口的 401 不触发回调（错误就地展示）', async () => {
    stubFetch(401, '{"error":"invalid_credentials","message":"用户名或密码错误"}')
    const handler = vi.fn()
    setUnauthorizedHandler(handler)
    await expect(api('/admin/login', { method: 'POST', body: {} })).rejects.toBeInstanceOf(ApiError)
    expect(handler).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：client.test 全部 FAIL，`Cannot find module './client'`。

- [ ] **Step 3: 写实现**

`packages/web/src/api/client.ts`：

```ts
// 统一的 fetch 封装：cookie 鉴权（credentials: include）、JSON 编解码、
// 错误响应 {"error","message"} 归一为 ApiError、401 触发全局回调（跳登录页）。

export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

type UnauthorizedHandler = () => void

let onUnauthorized: UnauthorizedHandler | null = null

export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  onUnauthorized = handler
}

export interface RequestOptions {
  method?: string
  body?: unknown
}

export async function api<T = void>(path: string, options: RequestOptions = {}): Promise<T> {
  const init: RequestInit = {
    method: options.method ?? 'GET',
    credentials: 'include',
  }
  if (options.body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(options.body)
  }

  const res = await fetch(path, init)

  let data: unknown = null
  const text = await res.text()
  if (text !== '') {
    try {
      data = JSON.parse(text)
    } catch {
      data = null // 非 JSON 响应（网关错误页等），按 unknown_error 处理
    }
  }

  if (!res.ok) {
    if (res.status === 401 && path !== '/admin/login') {
      onUnauthorized?.()
    }
    const err = (data ?? {}) as { error?: string; message?: string }
    throw new ApiError(res.status, err.error ?? 'unknown_error', err.message ?? `HTTP ${res.status}`)
  }
  return data as T
}
```

`packages/web/src/api/types.ts`：

```ts
// 与后端 JSON（snake_case）一一对应的类型定义。
export type ConnectorStatus =
  | 'catalog_only'
  | 'needs_config'
  | 'config_incompatible'
  | 'ready'
  | 'degraded'
  | 'deprecated'
  | 'definition_missing'

export interface CatalogItem {
  type: string
  name: string
  description: string
  categories: string[]
  homepage_url: string
  icon_url: string
  status: ConnectorStatus
}

export interface FieldValidation {
  pattern: string
  options: string[]
}

export interface ConfigField {
  key: string
  label: string
  input_type: 'text' | 'url' | 'select'
  required: boolean
  secret: boolean
  default_value: string | null
  description: string
  validation: FieldValidation
}

export interface ConfigSchema {
  connector_type: string
  config_schema_version: number
  fields: ConfigField[]
}

export interface ConnectorConfig {
  connector_type: string
  schema_version: number
  public: Record<string, unknown>
  secret_keys_set: string[]
  updated_at: string
}

export interface ConfigPayload {
  public: Record<string, unknown>
  secrets: Record<string, string>
}

export interface ValidateResult {
  valid: boolean
  field_errors: Record<string, string>
}

export type ConnectionStatus = 'active' | 'reauth_required' | 'disabled'

export interface Connection {
  id: string
  connector_type: string
  alias: string
  auth_method: string
  status: ConnectionStatus
  scopes: string[]
  access_token_expires_at: string | null
  created_at: string
  updated_at: string
}

export interface ApiToken {
  id: string
  name: string
  created_at: string
  revoked_at: string | null
}

export interface CreatedApiToken {
  id: string
  name: string
  token: string
  created_at: string
}
```

`packages/web/src/api/endpoints.ts`：

```ts
import { api, ApiError } from './client'
import type {
  ApiToken, CatalogItem, ConfigPayload, ConfigSchema, Connection,
  ConnectorConfig, CreatedApiToken, ValidateResult,
} from './types'

export function login(username: string, password: string): Promise<void> {
  return api('/admin/login', { method: 'POST', body: { username, password } })
}

export async function listConnectors(): Promise<CatalogItem[]> {
  const res = await api<{ connectors: CatalogItem[] }>('/admin/connectors')
  return res.connectors
}

export function getConnector(type: string): Promise<CatalogItem> {
  return api(`/admin/connectors/${type}`)
}

export function getConfigSchema(type: string): Promise<ConfigSchema> {
  return api(`/admin/connectors/${type}/config-schema`)
}

export async function getConfig(type: string): Promise<ConnectorConfig | null> {
  try {
    return await api<ConnectorConfig>(`/admin/connectors/${type}/config`)
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null // 尚未配置
    throw e
  }
}

export function putConfig(type: string, body: ConfigPayload & { if_match: string }): Promise<ConnectorConfig> {
  return api(`/admin/connectors/${type}/config`, { method: 'PUT', body })
}

export function deleteConfig(type: string): Promise<void> {
  return api(`/admin/connectors/${type}/config`, { method: 'DELETE' })
}

export function validateConfig(type: string, body: ConfigPayload): Promise<ValidateResult> {
  return api(`/admin/connectors/${type}/config:validate`, { method: 'POST', body })
}

export function verifyMcp(type: string): Promise<void> {
  return api(`/admin/connectors/${type}/mcp:verify`, { method: 'POST' })
}

export async function listConnections(): Promise<Connection[]> {
  const res = await api<{ connections: Connection[] }>('/admin/connections')
  return res.connections
}

export function startOAuth(connectorType: string, alias: string): Promise<{ authorization_url: string }> {
  return api('/admin/connections/oauth', {
    method: 'POST',
    body: { connector_type: connectorType, alias },
  })
}

export function createApiKeyConnection(connectorType: string, alias: string, apiKey: string): Promise<Connection> {
  return api('/admin/connections/api-key', {
    method: 'POST',
    body: { connector_type: connectorType, alias, api_key: apiKey },
  })
}

export function deleteConnection(id: string): Promise<void> {
  return api(`/admin/connections/${id}`, { method: 'DELETE' })
}

export function reauthConnection(id: string): Promise<{ authorization_url: string }> {
  return api(`/admin/connections/${id}/reauth`, { method: 'POST' })
}

export async function listApiTokens(): Promise<ApiToken[]> {
  const res = await api<{ api_tokens: ApiToken[] }>('/admin/api-tokens')
  return res.api_tokens
}

export function createApiToken(name: string): Promise<CreatedApiToken> {
  return api('/admin/api-tokens', { method: 'POST', body: { name } })
}

export function deleteApiToken(id: string): Promise<void> {
  return api(`/admin/api-tokens/${id}`, { method: 'DELETE' })
}

export function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  return api('/admin/account/password', {
    method: 'PUT',
    body: { current_password: currentPassword, new_password: newPassword },
  })
}
```

`packages/web/src/lib/navigation.ts`：

```ts
// window.location 的薄封装：OAuth 跳转走这里，测试 mock 本模块即可断言跳转。
export function redirectTo(url: string): void {
  window.location.assign(url)
}
```

`packages/web/src/main.ts`整体替换为：

```ts
import { createApp } from 'vue'
import { setUnauthorizedHandler } from '@/api/client'
import App from './App.vue'
import { router } from './router'
import './style.css'

// 任何 API 返回 401（登录接口除外）→ 回登录页。
setUnauthorizedHandler(() => {
  router.push('/login')
})

createApp(App).use(router).mount('#app')
```

- [ ] **Step 4: 运行确认通过**

```bash
pnpm --filter web test && pnpm --filter web build
```

预期：11 个测试全过，构建成功。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src
git commit -m "feat(web): add typed api client with unified error handling"
```

---

### Task 5: 登录页 /login

**Files:**
- Modify: `packages/web/src/pages/LoginPage.vue`（整体替换占位）
- Test: `packages/web/src/pages/LoginPage.test.ts`

**Interfaces:**
- Consumes: Task 4 的`login`、`ApiError`；ui 的`Button`／`Input`／`Label`。
- Produces: `/login`页面；登录成功跳`/connectors`。

- [ ] **Step 1: 写失败测试**

`packages/web/src/pages/LoginPage.test.ts`：

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { ApiError } from '@/api/client'
import { login } from '@/api/endpoints'
import LoginPage from './LoginPage.vue'

vi.mock('@/api/endpoints', () => ({
  login: vi.fn(),
}))

function makeRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/login', component: LoginPage },
      { path: '/connectors', component: { template: '<div />' } },
    ],
  })
}

async function mountPage(router: Router) {
  router.push('/login')
  await router.isReady()
  return mount(LoginPage, { global: { plugins: [router] } })
}

describe('LoginPage', () => {
  beforeEach(() => {
    vi.mocked(login).mockReset()
  })

  it('登录失败展示后端 message 且不跳转', async () => {
    vi.mocked(login).mockRejectedValue(new ApiError(401, 'invalid_credentials', '用户名或密码错误'))
    const router = makeRouter()
    const w = await mountPage(router)
    await w.find('input[name="username"]').setValue('admin')
    await w.find('input[name="password"]').setValue('wrong')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).toContain('用户名或密码错误')
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('登录成功跳转 /connectors', async () => {
    vi.mocked(login).mockResolvedValue(undefined)
    const router = makeRouter()
    const w = await mountPage(router)
    await w.find('input[name="username"]').setValue('admin')
    await w.find('input[name="password"]').setValue('secret')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(login)).toHaveBeenCalledWith('admin', 'secret')
    expect(router.currentRoute.value.path).toBe('/connectors')
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：LoginPage 两条 FAIL（占位页没有 input／form）。

- [ ] **Step 3: 写实现**

`packages/web/src/pages/LoginPage.vue`整体替换为：

```vue
<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { Button, Input, Label } from '@felinic/ui'
import { ApiError } from '@/api/client'
import { login } from '@/api/endpoints'

const router = useRouter()
const username = ref('')
const password = ref('')
const error = ref('')
const submitting = ref(false)

async function onSubmit() {
  error.value = ''
  submitting.value = true
  try {
    await login(username.value, password.value)
    router.push('/connectors')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '登录失败，请稍后重试'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <main class="flex min-h-dvh items-center justify-center bg-background px-6">
    <form class="w-full max-w-sm rounded-xl border border-border bg-card p-6" novalidate @submit.prevent="onSubmit">
      <h1 class="text-title font-semibold text-foreground">connect-it</h1>
      <p class="mt-0.5 text-body text-muted-foreground">登录管理界面</p>
      <div class="mt-6 space-y-4">
        <div class="space-y-1.5">
          <Label for="login-username">用户名</Label>
          <Input id="login-username" v-model="username" name="username" autocomplete="username" />
        </div>
        <div class="space-y-1.5">
          <Label for="login-password">密码</Label>
          <Input id="login-password" v-model="password" name="password" type="password" autocomplete="current-password" />
        </div>
        <p v-if="error" class="text-caption text-destructive">{{ error }}</p>
        <Button type="submit" class="w-full" :disabled="submitting">登录</Button>
      </div>
    </form>
  </main>
</template>
```

- [ ] **Step 4: 运行确认通过**

```bash
pnpm --filter web test
```

预期：全部通过（13 个）。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src/pages
git commit -m "feat(web): add login page"
```

---

### Task 6: StatusBadge 与连接器列表页 /connectors

**Files:**
- Create: `packages/web/src/components/StatusBadge.vue`
- Modify: `packages/web/src/pages/ConnectorsPage.vue`（整体替换占位）
- Test: `packages/web/src/components/StatusBadge.test.ts`、`src/pages/ConnectorsPage.test.ts`

**Interfaces:**
- Consumes: Task 4 的`listConnectors`／`CatalogItem`／`ConnectorStatus`；Task 3 的骨架组件；ui 的`Badge`／`Button`／`Spinner`。
- Produces: `StatusBadge{ status: ConnectorStatus }`（Task 8 配置页复用）；`/connectors`页面。

- [ ] **Step 1: 写失败测试**

`packages/web/src/components/StatusBadge.test.ts`：

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { ConnectorStatus } from '@/api/types'
import StatusBadge from './StatusBadge.vue'

describe('StatusBadge', () => {
  it.each<[ConnectorStatus, string]>([
    ['catalog_only', '仅目录'],
    ['needs_config', '待配置'],
    ['config_incompatible', '配置不兼容'],
    ['ready', '就绪'],
    ['degraded', '降级'],
    ['deprecated', '已弃用'],
    ['definition_missing', '定义缺失'],
  ])('%s 显示「%s」', (status, label) => {
    const w = mount(StatusBadge, { props: { status } })
    expect(w.text()).toBe(label)
  })
})
```

`packages/web/src/pages/ConnectorsPage.test.ts`：

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { listConnectors } from '@/api/endpoints'
import type { CatalogItem } from '@/api/types'
import ConnectorsPage from './ConnectorsPage.vue'

vi.mock('@/api/endpoints', () => ({
  listConnectors: vi.fn(),
}))

const items: CatalogItem[] = [
  { type: 'github', name: 'GitHub', description: 'GitHub 代码托管与协作平台',
    categories: [], homepage_url: '', icon_url: '', status: 'needs_config' },
  { type: 'gmail', name: 'Gmail', description: 'Google 邮件服务',
    categories: [], homepage_url: '', icon_url: '', status: 'ready' },
]

function makeRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/connectors', component: ConnectorsPage },
      { path: '/connectors/:type', component: { template: '<div />' } },
    ],
  })
}

describe('ConnectorsPage', () => {
  beforeEach(() => {
    vi.mocked(listConnectors).mockResolvedValue(items)
  })

  it('渲染 catalog 列表与状态徽标', async () => {
    const router = makeRouter()
    router.push('/connectors')
    await router.isReady()
    const w = mount(ConnectorsPage, { global: { plugins: [router] } })
    await flushPromises()
    expect(w.text()).toContain('GitHub')
    expect(w.text()).toContain('待配置')
    expect(w.text()).toContain('Gmail')
    expect(w.text()).toContain('就绪')
  })

  it('「配置」按钮跳转 /connectors/:type', async () => {
    const router = makeRouter()
    router.push('/connectors')
    await router.isReady()
    const w = mount(ConnectorsPage, { global: { plugins: [router] } })
    await flushPromises()
    const btn = w.findAll('button').find((b) => b.text() === '配置')
    await btn!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/connectors/github')
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：StatusBadge FAIL（模块不存在）、ConnectorsPage FAIL（占位页为空）。

- [ ] **Step 3: 写实现**

`packages/web/src/components/StatusBadge.vue`：

```vue
<script setup lang="ts">
import { computed } from 'vue'
import { Badge } from '@felinic/ui'
import type { ConnectorStatus } from '@/api/types'

type BadgeVariant = 'default' | 'secondary' | 'destructive' | 'success' | 'warning' | 'info' | 'outline'

const props = defineProps<{ status: ConnectorStatus }>()

const meta = computed(() => {
  const map: Record<ConnectorStatus, { label: string; variant: BadgeVariant }> = {
    catalog_only: { label: '仅目录', variant: 'secondary' },
    needs_config: { label: '待配置', variant: 'info' },
    config_incompatible: { label: '配置不兼容', variant: 'destructive' },
    ready: { label: '就绪', variant: 'success' },
    degraded: { label: '降级', variant: 'warning' },
    deprecated: { label: '已弃用', variant: 'outline' },
    definition_missing: { label: '定义缺失', variant: 'destructive' },
  }
  return map[props.status]
})
</script>

<template>
  <Badge :variant="meta.variant">{{ meta.label }}</Badge>
</template>
```

`packages/web/src/pages/ConnectorsPage.vue`整体替换为：

```vue
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Button, Spinner, toast } from '@felinic/ui'
import { listConnectors } from '@/api/endpoints'
import type { CatalogItem } from '@/api/types'
import PageShell from '@/components/PageShell.vue'
import SettingsRow from '@/components/SettingsRow.vue'
import SettingsSection from '@/components/SettingsSection.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const router = useRouter()
const loading = ref(true)
const connectors = ref<CatalogItem[]>([])

onMounted(async () => {
  try {
    connectors.value = await listConnectors()
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载失败')
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <PageShell title="连接器">
    <SettingsSection title="全部连接器">
      <div v-if="loading" class="flex min-h-24 items-center justify-center"><Spinner /></div>
      <template v-else>
        <SettingsRow v-for="item in connectors" :key="item.type">
          <div class="flex items-center gap-2">
            <span class="truncate text-body font-medium text-foreground">{{ item.name }}</span>
            <StatusBadge :status="item.status" />
          </div>
          <p class="mt-0.5 truncate text-caption text-muted-foreground">{{ item.description }}</p>
          <template #control>
            <Button variant="outline" size="sm" @click="router.push(`/connectors/${item.type}`)">配置</Button>
          </template>
        </SettingsRow>
      </template>
    </SettingsSection>
  </PageShell>
</template>
```

- [ ] **Step 4: 运行确认通过**

```bash
pnpm --filter web test
```

预期：全部通过（22 个）。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src
git commit -m "feat(web): add connectors list with status badges"
```

---

### Task 7: ConfigForm 动态表单组件

**Files:**
- Create: `packages/web/src/components/ConfigForm.vue`
- Test: `packages/web/src/components/ConfigForm.test.ts`

**Interfaces:**
- Consumes: Task 4 的`ConfigField`／`ConfigPayload`；ui 的`Input`／`Label`／`NativeSelect`／`NativeSelectOption`。
- Produces: `ConfigForm`——props`{ fields, publicValues, secretKeysSet, serverErrors? }`；提交校验通过时 emit `submit(payload: ConfigPayload)`；`defineExpose({ collect(enforceRequired?: boolean): ConfigPayload | null })`；slot `#actions`放按钮（按钮`type="submit"`即触发表单提交）。Task 8 配置页依赖以上全部。

- [ ] **Step 1: 写失败测试**

`packages/web/src/components/ConfigForm.test.ts`：

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { ConfigField } from '@/api/types'
import ConfigForm from './ConfigForm.vue'

function field(overrides: Partial<ConfigField> & { key: string }): ConfigField {
  return {
    label: overrides.key,
    input_type: 'text',
    required: false,
    secret: false,
    default_value: null,
    description: '',
    validation: { pattern: '', options: [] },
    ...overrides,
  } as ConfigField
}

const fields: ConfigField[] = [
  field({ key: 'client_id', label: 'Client ID', required: true }),
  field({ key: 'client_secret', label: 'Client Secret', required: true, secret: true }),
  field({ key: 'tenant', input_type: 'select', default_value: 'common',
    validation: { pattern: '', options: ['common', 'organizations'] } }),
  field({ key: 'mcp_url', input_type: 'url',
    validation: { pattern: '^https://.*$', options: [] } }),
]

function mountForm(publicValues: Record<string, unknown> = {}, secretKeysSet: string[] = []) {
  return mount(ConfigForm, { props: { fields, publicValues, secretKeysSet } })
}

describe('ConfigForm', () => {
  it('按 schema 渲染控件：text／password／select／url', () => {
    const w = mountForm()
    expect(w.find('input[name="client_id"]').exists()).toBe(true)
    expect(w.find('input[name="client_secret"]').attributes('type')).toBe('password')
    expect(w.find('select[name="tenant"]').exists()).toBe(true)
    expect(w.find('input[name="mcp_url"]').attributes('type')).toBe('url')
  })

  it('select 用 default_value 预填', () => {
    const w = mountForm()
    const select = w.find('select[name="tenant"]').element as HTMLSelectElement
    expect(select.value).toBe('common')
  })

  it('必填校验在提交时生效且不发出 submit 事件', async () => {
    const w = mountForm()
    await w.find('form').trigger('submit')
    expect(w.emitted('submit')).toBeUndefined()
    expect(w.text()).toContain('必填')
  })

  it('校验通过时发出 payload：public 与 secrets 分开', async () => {
    const w = mountForm()
    await w.find('input[name="client_id"]').setValue('abc')
    await w.find('input[name="client_secret"]').setValue('s3cret')
    await w.find('form').trigger('submit')
    const emitted = w.emitted('submit')
    expect(emitted).toHaveLength(1)
    expect(emitted![0][0]).toEqual({
      public: { client_id: 'abc', tenant: 'common' },
      secrets: { client_secret: 's3cret' },
    })
  })

  it('secret 只写不读：已设置字段值为空、placeholder 提示已设置', () => {
    const w = mountForm({ client_id: 'abc' }, ['client_secret'])
    const secret = w.find('input[name="client_secret"]')
    expect((secret.element as HTMLInputElement).value).toBe('')
    expect(secret.attributes('placeholder')).toBe('已设置')
  })

  it('已设置且未改动的 secret 不进入 secrets，也不算缺必填', async () => {
    const w = mountForm({ client_id: 'abc' }, ['client_secret'])
    await w.find('form').trigger('submit')
    const emitted = w.emitted('submit')
    expect(emitted).toHaveLength(1)
    expect(emitted![0][0]).toEqual({
      public: { client_id: 'abc', tenant: 'common' },
      secrets: {},
    })
  })

  it('pattern 不匹配时报格式错误', async () => {
    const w = mountForm({ client_id: 'abc' }, ['client_secret'])
    await w.find('input[name="mcp_url"]').setValue('ftp://x')
    await w.find('form').trigger('submit')
    expect(w.emitted('submit')).toBeUndefined()
    expect(w.text()).toContain('格式不正确')
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：ConfigForm 七条全 FAIL（模块不存在）。

- [ ] **Step 3: 写实现**

`packages/web/src/components/ConfigForm.vue`：

```vue
<script setup lang="ts">
import { reactive, ref } from 'vue'
import { Input, Label, NativeSelect, NativeSelectOption } from '@felinic/ui'
import type { ConfigField, ConfigPayload } from '@/api/types'

const props = defineProps<{
  fields: ConfigField[]
  publicValues: Record<string, unknown>
  secretKeysSet: string[]
  serverErrors?: Record<string, string>
}>()

const emit = defineEmits<{ submit: [payload: ConfigPayload] }>()

// 表单值：非 Secret 字段用已保存值或默认值初始化；
// Secret 字段永远从空开始（只写不读，spec §12／§17）。
// 父组件在 fields 变化时用 :key 重挂本组件，因此初始化只需做一次。
const values = reactive<Record<string, string>>({})
for (const f of props.fields) {
  if (f.secret) {
    values[f.key] = ''
    continue
  }
  const saved = props.publicValues[f.key]
  if (typeof saved === 'string') {
    values[f.key] = saved
  } else if (saved != null) {
    values[f.key] = String(saved)
  } else {
    values[f.key] = f.default_value ?? ''
  }
}

const errors = ref<Record<string, string>>({})

function secretIsSet(f: ConfigField): boolean {
  return props.secretKeysSet.includes(f.key)
}

// collect 汇总并校验表单：
// - 通过：返回 { public, secrets }，secrets 只含本次真正输入的 Secret；
// - 不通过：置 errors 并返回 null。
// enforceRequired=false 用于「校验」按钮：把当前草稿原样交给后端。
function collect(enforceRequired = true): ConfigPayload | null {
  const nextErrors: Record<string, string> = {}
  const pub: Record<string, unknown> = {}
  const secrets: Record<string, string> = {}
  for (const f of props.fields) {
    const raw = values[f.key].trim()
    if (f.secret) {
      if (raw !== '') secrets[f.key] = raw
      if (enforceRequired && f.required && raw === '' && !secretIsSet(f)) {
        nextErrors[f.key] = '必填'
      }
      continue
    }
    if (raw === '') {
      if (enforceRequired && f.required && f.default_value == null) {
        nextErrors[f.key] = '必填'
      }
      continue
    }
    if (f.validation.pattern !== '' && !new RegExp(f.validation.pattern).test(raw)) {
      nextErrors[f.key] = '格式不正确'
      continue
    }
    pub[f.key] = raw
  }
  errors.value = nextErrors
  if (Object.keys(nextErrors).length > 0) return null
  return { public: pub, secrets }
}

function onSubmit() {
  const payload = collect(true)
  if (payload) emit('submit', payload)
}

function errorFor(key: string): string {
  return errors.value[key] ?? props.serverErrors?.[key] ?? ''
}

defineExpose({ collect })
</script>

<template>
  <form class="space-y-4" novalidate @submit.prevent="onSubmit">
    <div v-for="f in fields" :key="f.key" class="space-y-1.5">
      <Label :for="`field-${f.key}`">
        {{ f.label }}<span v-if="!f.required" class="text-muted-foreground">（可选）</span>
      </Label>
      <NativeSelect
        v-if="f.input_type === 'select'"
        :id="`field-${f.key}`"
        v-model="values[f.key]"
        :name="f.key"
      >
        <NativeSelectOption v-for="opt in f.validation.options" :key="opt" :value="opt">
          {{ opt }}
        </NativeSelectOption>
      </NativeSelect>
      <Input
        v-else
        :id="`field-${f.key}`"
        v-model="values[f.key]"
        :name="f.key"
        :type="f.secret ? 'password' : f.input_type === 'url' ? 'url' : 'text'"
        :placeholder="f.secret && secretIsSet(f) ? '已设置' : ''"
        :autocomplete="f.secret ? 'new-password' : 'off'"
      />
      <p v-if="f.description" class="text-caption text-muted-foreground">{{ f.description }}</p>
      <p v-if="errorFor(f.key)" class="text-caption text-destructive">{{ errorFor(f.key) }}</p>
    </div>
    <slot name="actions" />
  </form>
</template>
```

- [ ] **Step 4: 运行确认通过**

```bash
pnpm --filter web test
```

预期：全部通过（29 个）。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src/components
git commit -m "feat(web): add schema-driven config form"
```

---

### Task 8: 连接器配置页 /connectors/:type

**Files:**
- Create: `packages/web/src/components/ConfirmDialog.vue`
- Modify: `packages/web/src/pages/ConnectorConfigPage.vue`（整体替换占位）
- Test: `packages/web/src/pages/ConnectorConfigPage.test.ts`

**Interfaces:**
- Consumes: Task 4 的七个配置相关 endpoint；Task 6 的`StatusBadge`；Task 7 的`ConfigForm`（`collect`／`submit`／`#actions`）；ui 的`Dialog`系列／`Button`／`Spinner`。
- Produces: `/connectors/:type`页面；`ConfirmDialog{ v-model:open, title, description, confirmLabel?, @confirm }`（Task 9／10 复用）。

- [ ] **Step 1: 写失败测试**

`packages/web/src/pages/ConnectorConfigPage.test.ts`：

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import {
  deleteConfig, getConfig, getConfigSchema, getConnector, putConfig, validateConfig, verifyMcp,
} from '@/api/endpoints'
import type { CatalogItem, ConfigSchema, ConnectorConfig } from '@/api/types'
import ConnectorConfigPage from './ConnectorConfigPage.vue'

vi.mock('@/api/endpoints', () => ({
  deleteConfig: vi.fn(),
  getConfig: vi.fn(),
  getConfigSchema: vi.fn(),
  getConnector: vi.fn(),
  putConfig: vi.fn(),
  validateConfig: vi.fn(),
  verifyMcp: vi.fn(),
}))

const item: CatalogItem = {
  type: 'github', name: 'GitHub', description: '', categories: [],
  homepage_url: '', icon_url: '', status: 'needs_config',
}

const schema: ConfigSchema = {
  connector_type: 'github',
  config_schema_version: 1,
  fields: [
    { key: 'client_id', label: 'Client ID', input_type: 'text', required: true,
      secret: false, default_value: null, description: '', validation: { pattern: '', options: [] } },
    { key: 'client_secret', label: 'Client Secret', input_type: 'text', required: true,
      secret: true, default_value: null, description: '', validation: { pattern: '', options: [] } },
  ],
}

const config: ConnectorConfig = {
  connector_type: 'github',
  schema_version: 1,
  public: { client_id: 'abc' },
  secret_keys_set: ['client_secret'],
  updated_at: '2026-07-22T10:00:00Z',
}

async function mountPage(): Promise<{ router: Router; w: ReturnType<typeof mount> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/connectors/:type', component: ConnectorConfigPage }],
  })
  router.push('/connectors/github')
  await router.isReady()
  const w = mount(ConnectorConfigPage, { global: { plugins: [router] } })
  await flushPromises()
  return { router, w }
}

describe('ConnectorConfigPage', () => {
  beforeEach(() => {
    vi.mocked(getConnector).mockResolvedValue(item)
    vi.mocked(getConfigSchema).mockResolvedValue(schema)
    vi.mocked(getConfig).mockResolvedValue(config)
    vi.mocked(putConfig).mockResolvedValue(config)
    vi.mocked(validateConfig).mockResolvedValue({ valid: true, field_errors: {} })
    vi.mocked(verifyMcp).mockResolvedValue(undefined)
    vi.mocked(deleteConfig).mockResolvedValue(undefined)
  })

  it('按 schema 渲染表单并预填已保存的 public 值', async () => {
    const { w } = await mountPage()
    const input = w.find('input[name="client_id"]')
    expect((input.element as HTMLInputElement).value).toBe('abc')
    expect(w.find('input[name="client_secret"]').attributes('placeholder')).toBe('已设置')
  })

  it('保存时携带 if_match=updated_at，未改动的 secret 不上传', async () => {
    const { w } = await mountPage()
    await w.find('input[name="client_id"]').setValue('new-id')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(putConfig)).toHaveBeenCalledWith('github', {
      public: { client_id: 'new-id' },
      secrets: {},
      if_match: '2026-07-22T10:00:00Z',
    })
  })

  it('校验按钮把当前草稿交给 config:validate', async () => {
    const { w } = await mountPage()
    const btn = w.findAll('button').find((b) => b.text() === '校验')
    await btn!.trigger('click')
    await flushPromises()
    expect(vi.mocked(validateConfig)).toHaveBeenCalledWith('github', {
      public: { client_id: 'abc' },
      secrets: {},
    })
  })

  it('验证 MCP 按钮调用 mcp:verify', async () => {
    const { w } = await mountPage()
    const btn = w.findAll('button').find((b) => b.text() === '验证 MCP')
    await btn!.trigger('click')
    await flushPromises()
    expect(vi.mocked(verifyMcp)).toHaveBeenCalledWith('github')
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：四条 FAIL（占位页为空）。

- [ ] **Step 3: 写 ConfirmDialog**

`packages/web/src/components/ConfirmDialog.vue`：

```vue
<script setup lang="ts">
import {
  Button, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@felinic/ui'

defineProps<{ title: string; description: string; confirmLabel?: string }>()
const open = defineModel<boolean>('open', { required: true })
const emit = defineEmits<{ confirm: [] }>()

function onConfirm() {
  emit('confirm')
  open.value = false
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent>
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription>{{ description }}</DialogDescription>
      </DialogHeader>
      <DialogFooter>
        <Button variant="outline" @click="open = false">取消</Button>
        <Button variant="destructive" @click="onConfirm">{{ confirmLabel ?? '删除' }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
```

- [ ] **Step 4: 写配置页**

`packages/web/src/pages/ConnectorConfigPage.vue`整体替换为：

```vue
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { Button, Spinner, toast } from '@felinic/ui'
import { ApiError } from '@/api/client'
import {
  deleteConfig, getConfig, getConfigSchema, getConnector, putConfig, validateConfig, verifyMcp,
} from '@/api/endpoints'
import type { CatalogItem, ConfigPayload, ConfigSchema, ConnectorConfig } from '@/api/types'
import ConfigForm from '@/components/ConfigForm.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import PageShell from '@/components/PageShell.vue'
import SettingsSection from '@/components/SettingsSection.vue'
import StatusBadge from '@/components/StatusBadge.vue'

const route = useRoute()
const type = route.params.type as string

const loading = ref(true)
const item = ref<CatalogItem | null>(null)
const schema = ref<ConfigSchema | null>(null)
const config = ref<ConnectorConfig | null>(null)
const serverErrors = ref<Record<string, string>>({})
const saving = ref(false)
const confirmOpen = ref(false)
// fields／已保存值变化时重挂表单（ConfigForm 只在挂载时初始化）。
const formKey = ref(0)
const formRef = ref<{ collect: (enforceRequired?: boolean) => ConfigPayload | null } | null>(null)

async function reload() {
  const [i, s, c] = await Promise.all([getConnector(type), getConfigSchema(type), getConfig(type)])
  item.value = i
  schema.value = s
  config.value = c
  formKey.value += 1
}

onMounted(async () => {
  try {
    await reload()
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载失败')
  } finally {
    loading.value = false
  }
})

async function onSave(payload: ConfigPayload) {
  saving.value = true
  serverErrors.value = {}
  try {
    await putConfig(type, { ...payload, if_match: config.value?.updated_at ?? '' })
    await reload()
    toast.success('配置已保存')
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '保存失败')
  } finally {
    saving.value = false
  }
}

async function onValidate() {
  const payload = formRef.value?.collect(false)
  if (!payload) return
  try {
    const result = await validateConfig(type, payload)
    if (result.valid) {
      serverErrors.value = {}
      toast.success('校验通过')
    } else {
      serverErrors.value = result.field_errors
      toast.error('校验未通过')
    }
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '校验失败')
  }
}

async function onVerify() {
  try {
    await verifyMcp(type)
    toast.success('MCP 验证通过')
    await reload()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : 'MCP 验证失败')
  }
}

async function onDelete() {
  try {
    await deleteConfig(type)
    await reload()
    toast.success('配置已删除')
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '删除失败')
  }
}
</script>

<template>
  <PageShell :title="item?.name ?? type">
    <template #actions>
      <StatusBadge v-if="item" :status="item.status" />
    </template>
    <div v-if="loading" class="flex min-h-24 items-center justify-center"><Spinner /></div>
    <SettingsSection v-else-if="schema" title="配置">
      <div class="p-4">
        <p v-if="schema.fields.length === 0" class="text-body text-muted-foreground">
          该连接器没有需要填写的配置。
        </p>
        <ConfigForm
          v-else
          ref="formRef"
          :key="formKey"
          :fields="schema.fields"
          :public-values="config?.public ?? {}"
          :secret-keys-set="config?.secret_keys_set ?? []"
          :server-errors="serverErrors"
          @submit="onSave"
        >
          <template #actions>
            <div class="flex items-center gap-2 pt-2">
              <Button type="submit" :disabled="saving">
                <Spinner v-if="saving" />保存
              </Button>
              <Button type="button" variant="outline" @click="onValidate">校验</Button>
              <Button type="button" variant="outline" @click="onVerify">验证 MCP</Button>
              <Button
                v-if="config"
                type="button"
                variant="destructive"
                class="ml-auto"
                @click="confirmOpen = true"
              >删除配置</Button>
            </div>
          </template>
        </ConfigForm>
      </div>
    </SettingsSection>
    <ConfirmDialog
      v-model:open="confirmOpen"
      title="删除配置"
      description="将删除该连接器的全部管理员配置（含 Secret）。已建立的连接会因缺少配置而不可用。"
      @confirm="onDelete"
    />
  </PageShell>
</template>
```

- [ ] **Step 5: 运行确认通过**

```bash
pnpm --filter web test
```

预期：全部通过（33 个）。

- [ ] **Step 6: 提交**

```bash
git add packages/web/src
git commit -m "feat(web): add connector config page"
```

---

### Task 9: 连接管理页 /connections

**Files:**
- Modify: `packages/web/src/pages/ConnectionsPage.vue`（整体替换占位）
- Test: `packages/web/src/pages/ConnectionsPage.test.ts`

**Interfaces:**
- Consumes: Task 4 的连接相关 endpoint 与`redirectTo`；Task 8 的`ConfirmDialog`；ui 的`Badge`／`Dialog`系列／`Empty`系列／`NativeSelect`。
- Produces: `/connections`页面（列表、OAuth 发起、API key 提交、reauth、删除、`?connected=`成功提示）。

- [ ] **Step 1: 写失败测试**

`packages/web/src/pages/ConnectionsPage.test.ts`：

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'
import { toast } from '@felinic/ui'
import { listConnections, listConnectors } from '@/api/endpoints'
import type { Connection } from '@/api/types'
import ConnectionsPage from './ConnectionsPage.vue'

vi.mock('@/api/endpoints', () => ({
  createApiKeyConnection: vi.fn(),
  deleteConnection: vi.fn(),
  listConnections: vi.fn(),
  listConnectors: vi.fn(),
  reauthConnection: vi.fn(),
  startOAuth: vi.fn(),
}))

vi.mock('@felinic/ui', async (importOriginal) => {
  const mod = await importOriginal<typeof import('@felinic/ui')>()
  return {
    ...mod,
    toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
  }
})

const sample: Connection[] = [
  { id: 'c1', connector_type: 'github', alias: 'my-github', auth_method: 'oauth',
    status: 'active', scopes: [], access_token_expires_at: null,
    created_at: '2026-07-22T00:00:00Z', updated_at: '2026-07-22T00:00:00Z' },
  { id: 'c2', connector_type: 'gmail', alias: 'ops-gmail', auth_method: 'oauth',
    status: 'reauth_required', scopes: [], access_token_expires_at: null,
    created_at: '2026-07-22T00:00:00Z', updated_at: '2026-07-22T00:00:00Z' },
]

function makeRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/connections', component: ConnectionsPage }],
  })
}

describe('ConnectionsPage', () => {
  beforeEach(() => {
    vi.mocked(listConnections).mockResolvedValue(sample)
    vi.mocked(listConnectors).mockResolvedValue([])
    vi.mocked(toast.success).mockClear()
  })

  it('渲染连接列表与状态', async () => {
    const router = makeRouter()
    router.push('/connections')
    await router.isReady()
    const w = mount(ConnectionsPage, { global: { plugins: [router] } })
    await flushPromises()
    expect(w.text()).toContain('my-github')
    expect(w.text()).toContain('已连接')
    expect(w.text()).toContain('ops-gmail')
    expect(w.text()).toContain('需重新授权')
  })

  it('OAuth 回调 query connected=alias 弹成功提示并清掉 query', async () => {
    const router = makeRouter()
    router.push('/connections?connected=my-github')
    await router.isReady()
    mount(ConnectionsPage, { global: { plugins: [router] } })
    await flushPromises()
    expect(toast.success).toHaveBeenCalledWith('连接 my-github 已建立')
    expect(router.currentRoute.value.query.connected).toBeUndefined()
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：两条 FAIL（占位页为空）。

- [ ] **Step 3: 写实现**

`packages/web/src/pages/ConnectionsPage.vue`整体替换为：

```vue
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Badge, Button, Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
  Empty, EmptyDescription, EmptyHeader, EmptyTitle,
  Input, Label, NativeSelect, NativeSelectOption, Spinner, toast,
} from '@felinic/ui'
import { ApiError } from '@/api/client'
import {
  createApiKeyConnection, deleteConnection, listConnections, listConnectors,
  reauthConnection, startOAuth,
} from '@/api/endpoints'
import type { CatalogItem, Connection, ConnectionStatus } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import PageShell from '@/components/PageShell.vue'
import SettingsRow from '@/components/SettingsRow.vue'
import SettingsSection from '@/components/SettingsSection.vue'
import { redirectTo } from '@/lib/navigation'

const route = useRoute()
const router = useRouter()

const loading = ref(true)
const connections = ref<Connection[]>([])
const connectors = ref<CatalogItem[]>([])

const statusMeta: Record<ConnectionStatus, { label: string; variant: 'success' | 'warning' | 'secondary' }> = {
  active: { label: '已连接', variant: 'success' },
  reauth_required: { label: '需重新授权', variant: 'warning' },
  disabled: { label: '已禁用', variant: 'secondary' },
}

// 新建连接对话框：mode 决定提交到 OAuth 还是 API Key 端点。
const createOpen = ref(false)
const createMode = ref<'oauth' | 'api_key'>('oauth')
const createType = ref('')
const createAlias = ref('')
const createApiKey = ref('')
const createError = ref('')
const submitting = ref(false)

// spec §7 connections.alias 的约束
const aliasPattern = /^[a-z0-9][a-z0-9-]{0,31}$/

const confirmOpen = ref(false)
const pendingDelete = ref<Connection | null>(null)

async function refresh() {
  connections.value = await listConnections()
}

onMounted(async () => {
  // OAuth 回调成功后 302 到 /connections?connected=alias（跨计划契约）。
  const connected = route.query.connected
  if (typeof connected === 'string' && connected !== '') {
    toast.success(`连接 ${connected} 已建立`)
    router.replace({ query: {} })
  }
  try {
    const [conns, cats] = await Promise.all([listConnections(), listConnectors()])
    connections.value = conns
    connectors.value = cats
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载失败')
  } finally {
    loading.value = false
  }
})

function openCreate(mode: 'oauth' | 'api_key') {
  createMode.value = mode
  createType.value = connectors.value[0]?.type ?? ''
  createAlias.value = ''
  createApiKey.value = ''
  createError.value = ''
  createOpen.value = true
}

async function onCreateSubmit() {
  createError.value = ''
  if (createType.value === '') {
    createError.value = '请选择连接器'
    return
  }
  if (!aliasPattern.test(createAlias.value)) {
    createError.value = 'alias 只能是小写字母、数字与连字符，以字母或数字开头，最长 32 字符'
    return
  }
  if (createMode.value === 'api_key' && createApiKey.value === '') {
    createError.value = '请填写 API Key'
    return
  }
  submitting.value = true
  try {
    if (createMode.value === 'oauth') {
      const { authorization_url } = await startOAuth(createType.value, createAlias.value)
      redirectTo(authorization_url)
      return
    }
    await createApiKeyConnection(createType.value, createAlias.value, createApiKey.value)
    createOpen.value = false
    toast.success(`连接 ${createAlias.value} 已建立`)
    await refresh()
  } catch (e) {
    createError.value = e instanceof ApiError ? e.message : '操作失败'
  } finally {
    submitting.value = false
  }
}

async function onReauth(conn: Connection) {
  try {
    const { authorization_url } = await reauthConnection(conn.id)
    redirectTo(authorization_url)
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '重新授权失败')
  }
}

function askDelete(conn: Connection) {
  pendingDelete.value = conn
  confirmOpen.value = true
}

async function onDelete() {
  const conn = pendingDelete.value
  if (!conn) return
  try {
    await deleteConnection(conn.id)
    toast.success(`连接 ${conn.alias} 已删除`)
    await refresh()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '删除失败')
  }
}
</script>

<template>
  <PageShell title="连接">
    <SettingsSection title="全部连接">
      <template #actions>
        <Button variant="outline" size="sm" @click="openCreate('api_key')">API Key 连接</Button>
        <Button size="sm" @click="openCreate('oauth')">发起 OAuth</Button>
      </template>
      <div v-if="loading" class="flex min-h-24 items-center justify-center"><Spinner /></div>
      <Empty v-else-if="connections.length === 0">
        <EmptyHeader>
          <EmptyTitle>还没有连接</EmptyTitle>
          <EmptyDescription>先在「连接器」页完成配置，再回来发起 OAuth 或提交 API Key。</EmptyDescription>
        </EmptyHeader>
      </Empty>
      <template v-else>
        <SettingsRow v-for="conn in connections" :key="conn.id">
          <div class="flex items-center gap-2">
            <span class="truncate text-body font-medium text-foreground">{{ conn.alias }}</span>
            <Badge :variant="statusMeta[conn.status].variant">{{ statusMeta[conn.status].label }}</Badge>
          </div>
          <p class="mt-0.5 truncate text-caption text-muted-foreground">
            {{ conn.connector_type }} · {{ conn.auth_method }}
          </p>
          <template #control>
            <Button
              v-if="conn.status === 'reauth_required'"
              variant="outline" size="sm" @click="onReauth(conn)"
            >重新授权</Button>
            <Button variant="ghost" size="sm" @click="askDelete(conn)">删除</Button>
          </template>
        </SettingsRow>
      </template>
    </SettingsSection>

    <Dialog v-model:open="createOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ createMode === 'oauth' ? '发起 OAuth 授权' : '提交 API Key' }}</DialogTitle>
        </DialogHeader>
        <form id="create-connection-form" class="space-y-4" novalidate @submit.prevent="onCreateSubmit">
          <div class="space-y-1.5">
            <Label for="create-type">连接器</Label>
            <NativeSelect id="create-type" v-model="createType" name="connector_type">
              <NativeSelectOption v-for="c in connectors" :key="c.type" :value="c.type">
                {{ c.name }}
              </NativeSelectOption>
            </NativeSelect>
          </div>
          <div class="space-y-1.5">
            <Label for="create-alias">alias</Label>
            <Input id="create-alias" v-model="createAlias" name="alias" placeholder="my-github" />
            <p class="text-caption text-muted-foreground">部署内唯一，用作 MCP 工具名前缀。</p>
          </div>
          <div v-if="createMode === 'api_key'" class="space-y-1.5">
            <Label for="create-api-key">API Key</Label>
            <Input id="create-api-key" v-model="createApiKey" name="api_key" type="password" autocomplete="off" />
          </div>
          <p v-if="createError" class="text-caption text-destructive">{{ createError }}</p>
        </form>
        <DialogFooter>
          <Button variant="outline" @click="createOpen = false">取消</Button>
          <Button type="submit" form="create-connection-form" :disabled="submitting">
            {{ createMode === 'oauth' ? '前往授权' : '提交' }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <ConfirmDialog
      v-model:open="confirmOpen"
      title="删除连接"
      :description="`将删除连接 ${pendingDelete?.alias ?? ''} 及其凭证，正在使用它的 MCP Session 会立即失效。`"
      @confirm="onDelete"
    />
  </PageShell>
</template>
```

- [ ] **Step 4: 运行确认通过**

```bash
pnpm --filter web test
```

预期：全部通过（35 个）。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src/pages
git commit -m "feat(web): add connections management page"
```

---

### Task 10: API Token 页 /tokens

**Files:**
- Modify: `packages/web/src/pages/TokensPage.vue`（整体替换占位）
- Test: `packages/web/src/pages/TokensPage.test.ts`

**Interfaces:**
- Consumes: Task 4 的`listApiTokens`／`createApiToken`／`deleteApiToken`；Task 8 的`ConfirmDialog`。
- Produces: `/tokens`页面（创建后一次性展示明文、吊销）。

- [ ] **Step 1: 写失败测试**

`packages/web/src/pages/TokensPage.test.ts`：

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiToken, listApiTokens } from '@/api/endpoints'
import type { ApiToken } from '@/api/types'
import TokensPage from './TokensPage.vue'

vi.mock('@/api/endpoints', () => ({
  createApiToken: vi.fn(),
  deleteApiToken: vi.fn(),
  listApiTokens: vi.fn(),
}))

const sample: ApiToken[] = [
  { id: 't0', name: 'chatbot', created_at: '2026-07-22T00:00:00Z', revoked_at: null },
]

// Dialog 内容经 Teleport 渲染，测试里把 teleport stub 掉以便在 wrapper 内断言。
function mountPage() {
  return mount(TokensPage, { global: { stubs: { teleport: true } } })
}

describe('TokensPage', () => {
  beforeEach(() => {
    vi.mocked(listApiTokens).mockResolvedValue(sample)
    vi.mocked(createApiToken).mockReset()
  })

  it('渲染 token 列表', async () => {
    const w = mountPage()
    await flushPromises()
    expect(w.text()).toContain('chatbot')
  })

  it('创建后一次性展示明文 token', async () => {
    vi.mocked(createApiToken).mockResolvedValue({
      id: 't1', name: 'new-app', token: 'cit_plaintext_abc', created_at: '2026-07-22T00:00:00Z',
    })
    const w = mountPage()
    await flushPromises()
    await w.findAll('button').find((b) => b.text() === '创建令牌')!.trigger('click')
    await w.find('#token-name').setValue('new-app')
    await w.find('form#create-token-form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(createApiToken)).toHaveBeenCalledWith('new-app')
    expect(w.find('[data-testid="plaintext-token"]').text()).toBe('cit_plaintext_abc')
    expect(w.text()).toContain('明文只显示这一次')
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：两条 FAIL（占位页为空）。

- [ ] **Step 3: 写实现**

`packages/web/src/pages/TokensPage.vue`整体替换为：

```vue
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import {
  Badge, Button, Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
  Empty, EmptyDescription, EmptyHeader, EmptyTitle, Input, Label, Spinner, toast,
} from '@felinic/ui'
import { ApiError } from '@/api/client'
import { createApiToken, deleteApiToken, listApiTokens } from '@/api/endpoints'
import type { ApiToken } from '@/api/types'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import PageShell from '@/components/PageShell.vue'
import SettingsRow from '@/components/SettingsRow.vue'
import SettingsSection from '@/components/SettingsSection.vue'

const loading = ref(true)
const tokens = ref<ApiToken[]>([])

const createOpen = ref(false)
const createName = ref('')
const createError = ref('')
const submitting = ref(false)
// 创建成功后一次性展示的明文；关闭对话框即丢弃，后端只存 hash。
const createdToken = ref('')

const confirmOpen = ref(false)
const pendingRevoke = ref<ApiToken | null>(null)

async function refresh() {
  tokens.value = await listApiTokens()
}

onMounted(async () => {
  try {
    await refresh()
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载失败')
  } finally {
    loading.value = false
  }
})

function openCreate() {
  createName.value = ''
  createError.value = ''
  createdToken.value = ''
  createOpen.value = true
}

async function onCreate() {
  createError.value = ''
  if (createName.value.trim() === '') {
    createError.value = '请填写名称'
    return
  }
  submitting.value = true
  try {
    const created = await createApiToken(createName.value.trim())
    createdToken.value = created.token
    await refresh()
  } catch (e) {
    createError.value = e instanceof ApiError ? e.message : '创建失败'
  } finally {
    submitting.value = false
  }
}

async function copyToken() {
  await navigator.clipboard.writeText(createdToken.value)
  toast.success('已复制到剪贴板')
}

function askRevoke(tk: ApiToken) {
  pendingRevoke.value = tk
  confirmOpen.value = true
}

async function onRevoke() {
  const tk = pendingRevoke.value
  if (!tk) return
  try {
    await deleteApiToken(tk.id)
    toast.success(`令牌 ${tk.name} 已吊销`)
    await refresh()
  } catch (e) {
    toast.error(e instanceof ApiError ? e.message : '吊销失败')
  }
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleString('zh-CN')
}
</script>

<template>
  <PageShell title="API Token">
    <SettingsSection title="内部应用令牌">
      <template #actions>
        <Button size="sm" @click="openCreate">创建令牌</Button>
      </template>
      <div v-if="loading" class="flex min-h-24 items-center justify-center"><Spinner /></div>
      <Empty v-else-if="tokens.length === 0">
        <EmptyHeader>
          <EmptyTitle>还没有令牌</EmptyTitle>
          <EmptyDescription>内部应用调用 /v1 接口需要 Bearer 令牌。</EmptyDescription>
        </EmptyHeader>
      </Empty>
      <template v-else>
        <SettingsRow v-for="tk in tokens" :key="tk.id">
          <div class="flex items-center gap-2">
            <span class="truncate text-body font-medium text-foreground">{{ tk.name }}</span>
            <Badge v-if="tk.revoked_at" variant="secondary">已吊销</Badge>
          </div>
          <p class="mt-0.5 text-caption text-muted-foreground">创建于 {{ formatDate(tk.created_at) }}</p>
          <template #control>
            <Button v-if="!tk.revoked_at" variant="ghost" size="sm" @click="askRevoke(tk)">吊销</Button>
          </template>
        </SettingsRow>
      </template>
    </SettingsSection>

    <Dialog v-model:open="createOpen">
      <DialogContent>
        <template v-if="createdToken === ''">
          <DialogHeader>
            <DialogTitle>创建令牌</DialogTitle>
          </DialogHeader>
          <form id="create-token-form" class="space-y-4" novalidate @submit.prevent="onCreate">
            <div class="space-y-1.5">
              <Label for="token-name">名称</Label>
              <Input id="token-name" v-model="createName" name="name" placeholder="chatbot" />
            </div>
            <p v-if="createError" class="text-caption text-destructive">{{ createError }}</p>
          </form>
          <DialogFooter>
            <Button variant="outline" @click="createOpen = false">取消</Button>
            <Button type="submit" form="create-token-form" :disabled="submitting">创建</Button>
          </DialogFooter>
        </template>
        <template v-else>
          <DialogHeader>
            <DialogTitle>令牌已创建</DialogTitle>
            <DialogDescription>明文只显示这一次，请立即复制保存。</DialogDescription>
          </DialogHeader>
          <p
            data-testid="plaintext-token"
            class="break-all rounded-md bg-muted px-3 py-2 font-mono text-body text-foreground"
          >{{ createdToken }}</p>
          <DialogFooter>
            <Button variant="outline" @click="copyToken">复制</Button>
            <Button @click="createOpen = false">完成</Button>
          </DialogFooter>
        </template>
      </DialogContent>
    </Dialog>

    <ConfirmDialog
      v-model:open="confirmOpen"
      title="吊销令牌"
      :description="`吊销后使用 ${pendingRevoke?.name ?? ''} 的内部应用会立即失去访问权限。`"
      confirm-label="吊销"
      @confirm="onRevoke"
    />
  </PageShell>
</template>
```

- [ ] **Step 4: 运行确认通过**

```bash
pnpm --filter web test
```

预期：全部通过（37 个）。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src/pages
git commit -m "feat(web): add api token management page"
```

---

### Task 11: 设置页 /settings（修改密码）

**Files:**
- Modify: `packages/web/src/pages/SettingsPage.vue`（整体替换占位）
- Test: `packages/web/src/pages/SettingsPage.test.ts`

**Interfaces:**
- Consumes: Task 4 的`changePassword`。
- Produces: `/settings`页面。

- [ ] **Step 1: 写失败测试**

`packages/web/src/pages/SettingsPage.test.ts`：

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { changePassword } from '@/api/endpoints'
import SettingsPage from './SettingsPage.vue'

vi.mock('@/api/endpoints', () => ({
  changePassword: vi.fn(),
}))

describe('SettingsPage', () => {
  beforeEach(() => {
    vi.mocked(changePassword).mockReset()
    vi.mocked(changePassword).mockResolvedValue(undefined)
  })

  it('两次新密码不一致时就地报错、不调接口', async () => {
    const w = mount(SettingsPage)
    await w.find('input[name="current_password"]').setValue('old')
    await w.find('input[name="new_password"]').setValue('new-1')
    await w.find('input[name="confirm_password"]').setValue('new-2')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).toContain('两次输入的新密码不一致')
    expect(vi.mocked(changePassword)).not.toHaveBeenCalled()
  })

  it('提交成功调用接口并清空表单', async () => {
    const w = mount(SettingsPage)
    await w.find('input[name="current_password"]').setValue('old')
    await w.find('input[name="new_password"]').setValue('new-pass')
    await w.find('input[name="confirm_password"]').setValue('new-pass')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(vi.mocked(changePassword)).toHaveBeenCalledWith('old', 'new-pass')
    expect((w.find('input[name="new_password"]').element as HTMLInputElement).value).toBe('')
  })
})
```

- [ ] **Step 2: 运行确认失败**

```bash
pnpm --filter web test
```

预期：两条 FAIL（占位页为空）。

- [ ] **Step 3: 写实现**

`packages/web/src/pages/SettingsPage.vue`整体替换为：

```vue
<script setup lang="ts">
import { ref } from 'vue'
import { Button, Input, Label, toast } from '@felinic/ui'
import { ApiError } from '@/api/client'
import { changePassword } from '@/api/endpoints'
import PageShell from '@/components/PageShell.vue'
import SettingsSection from '@/components/SettingsSection.vue'

const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const error = ref('')
const submitting = ref(false)

async function onSubmit() {
  error.value = ''
  if (currentPassword.value === '' || newPassword.value === '') {
    error.value = '请填写当前密码与新密码'
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    error.value = '两次输入的新密码不一致'
    return
  }
  submitting.value = true
  try {
    await changePassword(currentPassword.value, newPassword.value)
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
    toast.success('密码已修改')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : '修改失败'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <PageShell title="设置">
    <SettingsSection title="修改密码">
      <form class="space-y-4 p-4" novalidate @submit.prevent="onSubmit">
        <div class="space-y-1.5">
          <Label for="current-password">当前密码</Label>
          <Input
            id="current-password" v-model="currentPassword" name="current_password"
            type="password" autocomplete="current-password"
          />
        </div>
        <div class="space-y-1.5">
          <Label for="new-password">新密码</Label>
          <Input
            id="new-password" v-model="newPassword" name="new_password"
            type="password" autocomplete="new-password"
          />
        </div>
        <div class="space-y-1.5">
          <Label for="confirm-password">确认新密码</Label>
          <Input
            id="confirm-password" v-model="confirmPassword" name="confirm_password"
            type="password" autocomplete="new-password"
          />
        </div>
        <p v-if="error" class="text-caption text-destructive">{{ error }}</p>
        <Button type="submit" :disabled="submitting">保存</Button>
      </form>
    </SettingsSection>
  </PageShell>
</template>
```

- [ ] **Step 4: 运行确认通过＋全量检查**

```bash
pnpm --filter web test
pnpm --filter web build
pnpm --filter web type-check
grep -rnE "bg-white|bg-black|text-white|text-black|-gray-|-zinc-|#[0-9a-fA-F]{6}" packages/web/src || echo "clean"
```

预期：39 个测试全过；构建与类型检查通过；grep 输出`clean`（R15）。

- [ ] **Step 5: 提交**

```bash
git add packages/web/src
git commit -m "feat(web): add password settings page"
```

---

### Task 12: api——webdist embed、SPA 回退与 build-web

**Files:**
- Create: `packages/api/webdist/webdist.go`、`packages/api/webdist/dist/.gitkeep`
- Create: `packages/api/internal/webui/webui.go`
- Test: `packages/api/internal/webui/webui_test.go`
- Modify: `packages/api/internal/server/routes.go`（同 Task 2 的实际文件）、`mise.toml`、`.gitignore`

**Interfaces:**
- Consumes: Task 3 的`pnpm --filter web build`产出`packages/web/dist`；计划 2 的 Echo 实例。
- Produces: `webdist.Dist() fs.FS`；`webui.Register(e *echo.Echo, dist fs.FS)`（`GET /*`：静态文件直出、非`/v1`／`/admin`／`/mcp`／`/healthz`的 GET 回退`index.html`）；mise 任务`build-web`。计划 8 的 Dockerfile 依赖`mise run build-web`。

- [ ] **Step 1: 建 embed 目录与包**

```bash
mkdir -p packages/api/webdist/dist && touch packages/api/webdist/dist/.gitkeep
```

`packages/api/webdist/webdist.go`：

```go
// Package webdist 承载前端构建产物。dist/ 由 mise run build-web 填充，
// 仓库只提交 .gitkeep 占位，保证未构建前端时 go:embed 也能编译。
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist 返回以 dist/ 为根的静态文件系统。
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // 仅当 embed 声明与目录名不一致时发生
	}
	return sub
}
```

- [ ] **Step 2: 写失败测试**

`packages/api/internal/webui/webui_test.go`：

```go
package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v4"
	"github.com/memohai/connect-it/packages/api/internal/webui"
)

func builtFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":    {Data: []byte("<html>connect-it spa</html>")},
		"assets/app.js": {Data: []byte("console.log('app')")},
	}
}

func newServer(dist fstest.MapFS) *echo.Echo {
	e := echo.New()
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	webui.Register(e, dist)
	return e
}

func get(e *echo.Echo, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestServeStaticAsset(t *testing.T) {
	rec := get(newServer(builtFS()), "/assets/app.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('app')" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestSPAFallbackForClientRoutes(t *testing.T) {
	e := newServer(builtFS())
	for _, path := range []string{
		"/", "/login", "/connectors", "/connectors/github", "/connections", "/tokens", "/settings",
	} {
		rec := get(e, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "connect-it spa") {
			t.Fatalf("%s: code=%d body=%q", path, rec.Code, rec.Body.String())
		}
	}
}

func TestReservedPrefixesNeverFallback(t *testing.T) {
	e := newServer(builtFS())
	for _, path := range []string{"/v1/nope", "/admin/nope", "/mcp", "/mcp/sub", "/v1", "/admin"} {
		rec := get(e, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: code=%d, want 404", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Fatalf("%s: 应返回错误 JSON: %q", path, rec.Body.String())
		}
	}
}

func TestExplicitRouteWins(t *testing.T) {
	rec := get(newServer(builtFS()), "/healthz")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestNotBuiltReturnsJSON(t *testing.T) {
	rec := get(newServer(fstest.MapFS{".gitkeep": {Data: []byte{}}}), "/login")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "web_ui_not_built") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}
```

运行确认失败：

```bash
cd packages/api && go test ./internal/webui/
```

预期：编译失败，`undefined: webui.Register`。

- [ ] **Step 3: 写实现**

`packages/api/internal/webui/webui.go`：

```go
// Package webui 通过 Echo 提供 SPA 静态资源与 history 路由回退。
package webui

import (
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

// Register 挂载 SPA：静态文件直出，非保留前缀的 GET 回退 index.html。
// 必须在全部 API 路由注册之后调用（Echo 的精确路由优先于 /*）。
func Register(e *echo.Echo, dist fs.FS) {
	e.GET("/*", handler(dist))
}

// reserved 判断路径是否属于 API 命名空间：这些路径未命中具体路由时
// 返回 404 JSON，绝不回退 index.html（避免 API 调用方拿到 HTML）。
func reserved(path string) bool {
	return path == "/healthz" || path == "/mcp" || path == "/v1" || path == "/admin" ||
		strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/admin/") ||
		strings.HasPrefix(path, "/mcp/")
}

func handler(dist fs.FS) echo.HandlerFunc {
	return func(c echo.Context) error {
		path := c.Request().URL.Path
		if reserved(path) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "not_found",
				"message": "unknown API path: " + path,
			})
		}
		name := strings.TrimPrefix(path, "/")
		if name != "" && name != "index.html" && fs.ValidPath(name) {
			if body, err := fs.ReadFile(dist, name); err == nil {
				return c.Blob(http.StatusOK, contentTypeFor(name), body)
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "web_ui_not_built",
				"message": "前端未构建：先执行 mise run build-web",
			})
		}
		return c.HTMLBlob(http.StatusOK, index)
	}
}

func contentTypeFor(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return echo.MIMEOctetStream
}
```

运行确认通过：

```bash
cd packages/api && go test ./internal/webui/
```

预期：`ok  github.com/memohai/connect-it/packages/api/internal/webui`。

- [ ] **Step 4: 接线**

在 Task 2 定位的路由注册文件里，**所有 API 路由注册完成之后**加入（import 同步补充）：

```go
// SPA 静态服务与 history 回退（本计划 Task 12）。
webui.Register(e, webdist.Dist())
```

```go
"github.com/memohai/connect-it/packages/api/internal/webui"
"github.com/memohai/connect-it/packages/api/webdist"
```

- [ ] **Step 5: mise build-web 与 .gitignore**

`mise.toml`追加：

```toml
[tasks.build-web]
description = "构建前端并把产物拷入 api module 的 embed 目录"
run = '''
set -e
pnpm --filter web build
rm -rf packages/api/webdist/dist
mkdir -p packages/api/webdist/dist
cp -R packages/web/dist/. packages/api/webdist/dist/
touch packages/api/webdist/dist/.gitkeep
'''
```

`.gitignore`末尾追加（计划 1 的`dist/`规则会整目录忽略`webdist/dist`，须先把目录本身重新纳入，再排除内容、只保留`.gitkeep`；gitignore 后写的规则优先）：

```text
!packages/api/webdist/dist/
packages/api/webdist/dist/*
!packages/api/webdist/dist/.gitkeep
```

验证：

```bash
git check-ignore -v packages/api/webdist/dist/.gitkeep || echo "not ignored"
```

预期：输出`not ignored`。

- [ ] **Step 6: 全链路验证**

```bash
mise run build-web
ls packages/api/webdist/dist/index.html
cd packages/api && go build ./... && go test ./...
cd ../.. && mise run test && mise run test-web
```

预期：`build-web`产出`index.html`与`assets/`；api module 构建、全部 Go 测试、全部前端测试通过。可选：启动 dev server 后`curl -s http://localhost:8080/connectors | head -1`应返回`<!doctype html>`开头的 SPA 页面。

- [ ] **Step 7: 提交**

```bash
git add packages/api mise.toml .gitignore
git commit -m "feat(api): embed web dist and add spa fallback"
```

---

## 偏离点及理由

1. `PageShell`／`SettingsSection`／`SettingsRow`在`packages/web`自建：ui 库 README 明示 owner 组件驻留宿主仓库（pending promotion），实现严格按 skills/web 的 spacing 词汇（R9）。
2. 顶部导航链接是手写样式的`RouterLink`：ui 库没有 Link 组件，样式只用语义 token 并对齐 Button quiet 阶梯；这是全计划唯一一处对裸元素写交互色。
3. 动态表单与对话框的下拉用`NativeSelect`（仍是 ui 库组件）而非浮层`Select`：schema 驱动表单以原生控件保证 jsdom 可测与可访问性。
4. 无「退出登录」入口：跨计划契约没有 logout 端点。
5. 计划 2–5 未钉死的 JSON 形状集中列在「接口假设」；差异只允许在`endpoints.ts`／`types.ts`与 Go 接线处吸收。
6. Task 2／12 修改的路由注册文件路径是假设值（计划 2–5 文档不在仓库），以 grep 实际结果为准，添加的代码不变。
7. vitest 用独立`vitest.config.ts`而非复用`vite.config.ts`：vitest 自带兼容的 vite，避免与应用的 vite 8 产生 peer 纠缠。

## 完成标准（对照 spec §14 与跨计划契约）

- [ ] 六个路由齐备：`/login`、`/connectors`、`/connectors/:type`、`/connections`、`/tokens`、`/settings`；健康状态由 connectors 列表徽标呈现，不设独立页；
- [ ] 配置表单由`config-schema`元数据驱动：text／url／select 渲染、必填与 pattern 校验、Secret 只写不读＋「已设置」占位、`if_match`乐观并发、validate／verify 按钮；
- [ ] OAuth：发起后`window.location`跳转`authorization_url`；回调`/connections?connected=alias`弹成功提示并清 query；reauth 同路径；
- [ ] API Token 创建后一次性展示明文；改密码走`PUT /admin/account/password`；
- [ ] API client：`credentials:'include'`、错误归一`ApiError{status,code,message}`、401 全局跳登录（登录接口除外）；
- [ ] 契约要求的最少测试全部存在：动态表单渲染与必填校验、Secret 只写不读、登录失败提示、client 错误统一处理；
- [ ] Go 侧：`GET /admin/connectors`（复用 catalogsvc handler、cookie 鉴权）、`GET /admin/connectors/:type/config-schema`、`go:embed`SPA 服务（保留前缀不回退）、`mise run build-web`全部落地且有测试；
- [ ] ui 规范：全部页面只用`@felinic/ui`组件与语义 token，R15 的裸色 grep 零命中；`pnpm --filter web build`／`type-check`／`mise run test`／`mise run test-web`全绿。
