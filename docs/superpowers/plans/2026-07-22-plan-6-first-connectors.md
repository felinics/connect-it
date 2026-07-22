# connect-it 计划 6：首批四个 Connector 完整实装

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 GitHub、Gmail、OneDrive、Google Ads 四个 Connector 从骨架实装为完整 Definition（真实 OAuth endpoint、官方 Remote MCP 映射、Managed handler），并给 oauthsvc 补上 OAuth endpoint 的`{tenant}`模板替换。

**Architecture:** 全部 Definition 仍是纯数据（`definition.go`不做 I/O），Tool 分两种 backend：Remote MCP 用固定映射指向官方或自托管 server；Managed handler 在 provider 包内实现（`managed.go`导出`var Handlers connector.HandlerMap`），通过`connectors.AllHandlers()`交给计划 4 的执行引擎。每个 provider 一个 task：definition、handler、注册、测试一次落地，保证任务边界处全仓库测试常绿（目录映射测试要求目录与注册一一对应，不允许出现「目录已建、尚未注册」的中间态）。

**Tech Stack:** Go 1.25、标准库`net/http`＋`httptest`（managed handler 不引第三方 HTTP 库）、计划 1 的 core module（connector 类型＋registry）、计划 3 的 oauthsvc、计划 4 的 Tool 执行契约。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 5、6、13、16 节是本计划的依据，§16 首批 Connector 表是核心）

## Global Constraints

沿用计划 1 的全局约束：

- Go module 路径固定为`github.com/memohai/connect-it/packages/<name>`；`connectors`的 go.mod 用`replace`按相对路径（`../core`）引用`core`。
- 依赖方向单向：`connectors→core`；`service→core＋connectors`。禁止反向 import。
- 没有 workspace：所有`go`命令必须`cd`进对应 module 目录执行。
- `definition.go`只声明固定数据，不访问数据库或网络。
- `connector_type`用 snake_case（`one_drive`、`google_ads`）；包目录名为去掉下划线的形式（`onedrive`、`googleads`）；映射由`TestDirectoryMatchesRegisteredTypes`校验，本计划必须保持其通过。
- Secret 配置字段禁止设置默认值（Registry 校验强制）。
- MCP 仅 streamable_http；固定 endpoint 必须 https；从配置取 endpoint 仅限 self_hosted。
- 工具链版本一律经 mise 安装：go 1.25、node 22、pnpm 10。
- 提交信息用 conventional commits（feat:／test:／chore:），每个 task 至少一次提交。

本计划新增的约束：

- `RemoteMCPBackend`的`InputMapperKey`／`OutputMapperKey`第一期必须留空（计划 4 契约：非空在执行时报错）。Task 1 加统一测试锁定。
- Managed handler 的 HTTP 测试模式：每个 provider 包内用包级变量`apiBaseURL`指向真实 API 根地址，测试用`httptest.Server`覆盖后还原（`t.Cleanup`）。
- InputSchema 约定：Remote MCP tool 用宽松 schema（`"additionalProperties": true`，参数校验以上游 server 为准，参数直通不做映射）；Managed tool 用严格 schema（`"additionalProperties": false`，handler 自己解析参数）。所有 InputSchema 必须是合法 JSON 且顶层`"type": "object"`。
- API 层错误（HTTP >= 400）转成`ToolResultData{IsError: true}`返回、Go error 为 nil；传输层错误（连接失败等）才返回 Go error。
- **2026-07-22 跨计划仲裁（本条覆盖本计划后文一切与之矛盾的代码段）**：`connector.ToolResultData`定稿为`{Text string; Structured json.RawMessage; IsError bool}`——后文 handler 代码中`Content: string(...)`一律改写为`Structured: json.RawMessage(...)`（错误详情对象同样放`Structured`，`Text`可留空或放一句摘要）；`ToolCallContext.Arguments`定稿为`map[string]any`，与后文`call.Arguments["…"]`的取参写法一致，无需调整。

### 查证来源（所有 endpoint／scope／MCP 地址均已于 2026-07-22 核实）

| 事实 | 值 | 来源 |
|---|---|---|
| GitHub 官方 remote MCP endpoint | `https://api.githubcopilot.com/mcp/` | <https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md> |
| GitHub MCP tool 名与参数（get_me、search_repositories、get_file_contents、list_issues、issue_write） | 见 Task 1 | <https://github.com/github/github-mcp-server/blob/main/README.md> |
| GitHub OAuth endpoint | `https://github.com/login/oauth/authorize`、`https://github.com/login/oauth/access_token` | <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps> |
| GitHub OAuth scope（`repo`、`read:user`） | 见 Task 1 | <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps> |
| Google OAuth endpoint | `https://accounts.google.com/o/oauth2/v2/auth`、`https://oauth2.googleapis.com/token`；refresh token 需`access_type=offline`＋`prompt=consent` | <https://developers.google.com/identity/protocols/oauth2/web-server> |
| Gmail 官方 remote MCP endpoint（Developer Preview） | `https://gmailmcp.googleapis.com/mcp/v1`，tool 含`search_threads`、`create_draft`等 10 个 | <https://developers.google.com/workspace/gmail/api/reference/mcp> |
| Gmail MCP 所需 scope | `gmail.readonly`＋`gmail.compose` | <https://developers.google.com/workspace/gmail/api/guides/configure-mcp-server> |
| Gmail REST 列消息 | `GET https://gmail.googleapis.com/gmail/v1/users/{userId}/messages`（scope 含`gmail.readonly`） | <https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list> |
| Gmail REST 发信 | `POST https://gmail.googleapis.com/gmail/v1/users/{userId}/messages/send`（scope 含`gmail.compose`） | <https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/send> |
| Microsoft identity platform v2.0 endpoint | `https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize`、`…/oauth2/v2.0/token` | <https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow> |
| Graph 列目录 | `GET https://graph.microsoft.com/v1.0/me/drive/root/children`；子目录`…/root:/{path}:/children` | <https://learn.microsoft.com/en-us/graph/api/driveitem-list-children?view=graph-rest-1.0> |
| Graph 简单上传（<4MB） | `PUT https://graph.microsoft.com/v1.0/me/drive/root:/{path}:/content`，需`Files.ReadWrite` | <https://learn.microsoft.com/en-us/graph/api/driveitem-put-content?view=graph-rest-1.0> |
| Google Ads OAuth scope 与 developer token | `https://www.googleapis.com/auth/adwords`；请求需`developer-token`头 | <https://developers.google.com/google-ads/api/rest/auth> |
| Google Ads 官方 MCP server（自托管） | 源码<https://github.com/googleads/google-ads-mcp>，tool 含`list_accessible_customers`、`search` | <https://developers.google.com/google-ads/api/docs/developer-toolkit/mcp-server> |

### 偏离与澄清（相对 spec §16 与任务指令）

1. **Gmail 官方 remote MCP 确实存在**（`https://gmailmcp.googleapis.com/mcp/v1`），无需退化为全 managed。但它处于 Google Workspace Developer Preview，Provenance 的`Stability`标为`preview`；其公开文档未列出各 tool 的参数 schema（要用`tools/list`获取），因此 Gmail 两个 remote tool 的 InputSchema 是**宽松近似**（`additionalProperties: true`），最终以管理端`mcp:verify`对照上游`tools/list`为准。
2. **GitHub 的 client_id／client_secret 可选**（`Required: false`）。计划 1 的状态机只对`Required`字段产生`needs_config`，所以「OAuth 未配置」不会把 github 压到`needs_config`——PAT 路径不依赖这两个字段，github 有 Tools 后即`ready`。实际挡点在 oauthsvc：发起 OAuth 授权时若`client_id`缺失直接报错（计划 3 行为，无需本计划改动）。这是对指令中「靠 needs_config 状态挡住」的澄清：状态挡的是「整个 Connector 不可用」，而 OAuth 未配置只影响 oauth 这一个 auth method。
3. **developer_token 第一期只保存、不传输**。计划 4 的`MCPAuthBinding`仅支持 bearer（用户 access token），没有「附加自定义头」机制；自托管 google-ads-mcp server 自行持有 developer token。该字段在本期只用于验证 spec §16 的「公开／秘密扩展字段」机制。
4. **gmail 与 onedrive 各自复制约 30 行 HTTP helper**（`callGmail`／`callGraph`），不抽公共包：计划 1 的`TestDirectoryMatchesRegisteredTypes`要求`packages/connectors`下每个目录都对应一个注册的 connector，`internal/`共享目录会使该测试失败，改测试不在本计划范围。
5. **UsePKCE**：Microsoft 为`true`（官方文档明确推荐）；Google 与 GitHub 为`false`——两家面向 confidential web app 的官方文档未确认 PKCE 参数，不写未查证的值。
6. **ProfileResolverKey 全部留空**：计划 3 的 resolver key 清单不在跨计划契约内，留空表示不解析 profile，连接的`profile`字段保持空对象。
7. **GitHub token endpoint 默认返回 form-encoded**，需要`Accept: application/json`头才返回 JSON。这是 oauthsvc（计划 3）的请求行为、不是 Definition 数据；本计划假定 oauthsvc 统一发送该头（标准做法），若未发送需在计划 3 代码中补充，不属于本计划任务。

### 前置契约（计划 1–5 已落地，本计划逐字依赖）

计划 1（core module）：

- `connector`包全部类型：`Definition`、`ConfigField{Key, Label, InputType, Required, Secret, DefaultValue *string, Description, Validation}`、`AuthMethod{Key, Type, Label, OAuth *OAuthConfig, CredentialFields}`、`OAuthConfig{AuthorizationEndpoint, TokenEndpoint, Scopes, UsePKCE, TokenEndpointAuth, ExtraAuthParams, ProfileResolverKey}`、`RemoteMCPServer{Key, Endpoint, AuthBinding, Provenance, RequestTimeout}`、`Endpoint{Source, URL, ConfigFieldKey}`、`Tool{ID, Name, Description, InputSchema, OutputSchema, RequiredScopes, Risk, Backend}`、`RemoteMCPBackend{ServerKey, RemoteToolName, InputMapperKey, OutputMapperKey}`、`ManagedBackend{HandlerKey}`。
- `registry.Registry`校验规则：type snake_case、key／ID 唯一、Secret 无默认值、固定 endpoint 必须 https、config endpoint 仅 self_hosted、tool ID `^[a-z0-9_]+$`、悬空引用（server／handler）拒绝。`Register(def, managedHandlerKeys ...string)`／`MustRegister`。

计划 4（Tool 执行契约，位于`packages/core/connector`）：

```go
type ToolCallContext struct {
    ConnectorType Type
    ToolID        string
    Arguments     map[string]any
    Config        map[string]any // 合并默认值后的管理员配置
    Credential    map[string]any // api_key／custom_credential 的用户凭证字段
    AccessToken   string         // OAuth access token（api_key 时为 token 字段值）
}

type ToolResultData struct {
    Content string
    IsError bool
}

type ManagedHandler func(ctx context.Context, call ToolCallContext) (ToolResultData, error)

type HandlerMap map[string]ManagedHandler
```

- `connectors.AllHandlers() map[connector.Type]connector.HandlerMap`已存在（当前只含空表）；`RegisterAll`已改为把`AllHandlers()`中对应 type 的 HandlerMap key 传给`MustRegister`。
- 计划 3（`packages/service/oauthsvc`）：OAuth 授权发起、回调、token 惰性刷新；本计划 Task 3 给它加`ExpandEndpoint`模板替换，不改任何既有函数签名。

## 文件结构

```text
packages/connectors/
├── all.go                     # 修改：注册四个 provider＋AllHandlers 接线
├── all_test.go                # 修改：github 断言更新＋新增各 provider 断言＋通用 schema 测试
├── github/definition.go       # 修改：oauth＋pat、官方 remote MCP、5 个 remote tool
├── gmail/definition.go        # 新建：Google OAuth、官方 MCP、2 remote＋2 managed tool
├── gmail/managed.go           # 新建：list_messages、send_message handler
├── gmail/managed_test.go      # 新建：httptest 覆盖 apiBaseURL
├── onedrive/definition.go     # 新建：Microsoft OAuth（{tenant}）、2 managed tool
├── onedrive/managed.go        # 新建：list_drive_items、upload_file handler
├── onedrive/managed_test.go   # 新建
└── googleads/definition.go    # 新建：self_hosted MCP、全 remote tool

packages/service/oauthsvc/
├── template.go                # 新建：ExpandEndpoint（{tenant} 等占位符替换）
└── template_test.go           # 新建
```

---

### Task 1: GitHub Connector 实装（多 auth method＋官方 Remote MCP Tools）

**Files:**
- Modify: `packages/connectors/github/definition.go`（整文件重写）
- Modify: `packages/connectors/all_test.go`（整文件重写：删除「catalog_only」断言，新增 github 断言与通用 schema／mapper 测试）

**Interfaces:**
- Consumes: 计划 1 的`connector`包全部类型、`registry.New()`／`Register`；计划 4 改造后的`connectors.RegisterAll(r *registry.Registry)`与`connectors.AllHandlers()`
- Produces: `github.Definition`（2 个 auth method：`oauth`／`pat`；1 个 official remote MCP server `official`；5 个 RemoteMCPBackend tool：`get_me`、`search_repositories`、`get_file_contents`、`list_issues`、`issue_write`）。后续 task 依赖 all_test.go 中的`TestToolSchemasAndMapperKeys`（对所有已注册 connector 生效）

- [ ] **Step 1: 重写 all_test.go（失败测试）**

整文件替换`packages/connectors/all_test.go`（计划 1 的「github 应为 catalog_only」断言删除，`TestDirectoryMatchesRegisteredTypes`原样保留）：

```go
package connectors_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestRegisterAll(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r) // 非法 definition 会 panic，测试即失败

	def, ok := r.Get("github")
	if !ok {
		t.Fatal("github 未注册")
	}
	if def.Name != "GitHub" {
		t.Fatalf("got %q", def.Name)
	}
}

func TestGitHubDefinition(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)
	def, _ := r.Get("github")

	if len(def.AuthMethods) != 2 || def.AuthMethods[0].Key != "oauth" || def.AuthMethods[1].Key != "pat" {
		t.Fatalf("github 应有 oauth＋pat 两个 auth method: %+v", def.AuthMethods)
	}
	oauth := def.AuthMethods[0].OAuth
	if oauth.AuthorizationEndpoint != "https://github.com/login/oauth/authorize" ||
		oauth.TokenEndpoint != "https://github.com/login/oauth/access_token" {
		t.Fatalf("github oauth endpoint 不符: %+v", oauth)
	}
	pat := def.AuthMethods[1]
	if pat.Type != connector.AuthAPIKey || len(pat.CredentialFields) != 1 ||
		pat.CredentialFields[0].Key != "token" || !pat.CredentialFields[0].Secret {
		t.Fatalf("pat 应为 api_key 且含一个 Secret 的 token 字段: %+v", pat)
	}
	for _, f := range def.ConfigFields {
		if f.Required {
			t.Errorf("github 配置字段 %s 应为可选（PAT 路径不依赖 OAuth 配置）", f.Key)
		}
	}

	if len(def.RemoteMCPServers) != 1 {
		t.Fatal("github 应恰有一个 remote MCP server")
	}
	srv := def.RemoteMCPServers[0]
	if srv.Provenance.Kind != connector.ProvenanceOfficial {
		t.Fatalf("provenance 应为 official: %+v", srv.Provenance)
	}
	if srv.Endpoint.URL != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("endpoint got %q", srv.Endpoint.URL)
	}

	wantTools := []string{"get_me", "search_repositories", "get_file_contents", "list_issues", "issue_write"}
	if len(def.Tools) != len(wantTools) {
		t.Fatalf("github 应有 %d 个 tool, got %d", len(wantTools), len(def.Tools))
	}
	for i, want := range wantTools {
		if def.Tools[i].ID != want {
			t.Errorf("tool[%d] 应为 %s, got %s", i, want, def.Tools[i].ID)
		}
		if _, ok := def.Tools[i].Backend.(connector.RemoteMCPBackend); !ok {
			t.Errorf("github tool %s 应为 RemoteMCPBackend", def.Tools[i].ID)
		}
	}
}

// 对全部已注册 connector 生效：InputSchema 必须是合法 JSON object，
// 且第一期 RemoteMCPBackend 的 mapper key 必须留空（计划 4 契约）。
func TestToolSchemasAndMapperKeys(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)
	for _, def := range r.All() {
		for _, tool := range def.Tools {
			var schema map[string]any
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Errorf("%s/%s InputSchema 不是合法 JSON: %v", def.Type, tool.ID, err)
				continue
			}
			if schema["type"] != "object" {
				t.Errorf("%s/%s InputSchema 顶层 type 应为 object", def.Type, tool.ID)
			}
			if b, ok := tool.Backend.(connector.RemoteMCPBackend); ok {
				if b.InputMapperKey != "" || b.OutputMapperKey != "" {
					t.Errorf("%s/%s: 第一期 mapper key 必须留空", def.Type, tool.ID)
				}
			}
		}
	}
}

// 目录名必须等于 connector_type 去掉下划线的形式，且一一对应。
func TestDirectoryMatchesRegisteredTypes(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)

	want := map[string]bool{}
	for _, def := range r.All() {
		want[strings.ReplaceAll(string(def.Type), "_", "")] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			got[e.Name()] = true
		}
	}

	for dir := range got {
		if !want[dir] {
			t.Errorf("目录 %q 没有对应的注册 connector", dir)
		}
	}
	for typ := range want {
		if !got[typ] {
			t.Errorf("注册的 connector %q 没有对应目录", typ)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/connectors && go test ./...
```

预期：`TestGitHubDefinition`失败（`github 应有 oauth＋pat 两个 auth method`——当前 definition 还是 catalog-only）。

- [ ] **Step 3: 重写 github definition**

整文件替换`packages/connectors/github/definition.go`：

```go
// Package github 是 GitHub Connector 的固定 Definition。
// 验证点（spec §16）：多 auth method（OAuth＋PAT）；官方 Remote MCP Tools。
package github

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "github",
	Name:                "GitHub",
	Description:         "GitHub 代码托管与协作平台",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://github.com",
	ConfigSchemaVersion: 1,

	// 两个字段均可选：不配置时 OAuth 授权在发起时报错（oauthsvc 行为），
	// PAT 路径不受影响，github 仍为 ready。
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Description: "GitHub OAuth App 的 Client ID。可不填：不配置时 OAuth 授权不可用，仍可用 PAT 连接。",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Secret:      true,
			Description: "GitHub OAuth App 的 Client Secret。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "GitHub OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
				TokenEndpoint:         "https://github.com/login/oauth/access_token",
				Scopes:                []string{"repo", "read:user"},
				UsePKCE:               false,
				TokenEndpointAuth:     connector.TokenAuthPost,
			},
		},
		{
			Key:   "pat",
			Type:  connector.AuthAPIKey,
			Label: "Personal Access Token",
			CredentialFields: []connector.ConfigField{
				{
					Key:         "token",
					Label:       "Personal Access Token",
					InputType:   connector.InputText,
					Required:    true,
					Secret:      true,
					Description: "GitHub PAT（classic 或 fine-grained），需具备目标仓库的读写权限。",
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "official",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    "https://api.githubcopilot.com/mcp/",
			},
			// OAuth access token 与 PAT 都以 Authorization: Bearer 呈递。
			AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				Publisher:        "GitHub",
				DocsURL:          "https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md",
				SourceURL:        "https://github.com/github/github-mcp-server",
				ReviewedAt:       "2026-07-22",
				AllowedHostnames: []string{"api.githubcopilot.com"},
				Stability:        connector.StabilityStable,
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	// 5 个固定映射的官方 remote tool。remote tool 的 schema 宽松
	// （additionalProperties: true），参数直通、以上游为准。
	Tools: []connector.Tool{
		{
			ID:          "get_me",
			Name:        "Get my profile",
			Description: "获取当前授权用户的 GitHub 个人资料。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read:user"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "get_me"},
		},
		{
			ID:          "search_repositories",
			Name:        "Search repositories",
			Description: "按 GitHub 仓库搜索语法检索仓库。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "GitHub 仓库搜索语法，如 language:go stars:>100"
    },
    "sort": {
      "type": "string",
      "enum": ["stars", "forks", "help-wanted-issues", "updated"]
    },
    "order": {"type": "string", "enum": ["asc", "desc"]},
    "page": {"type": "integer", "minimum": 1},
    "perPage": {"type": "integer", "minimum": 1, "maximum": 100},
    "minimal_output": {"type": "boolean"}
  },
  "required": ["query"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "search_repositories"},
		},
		{
			ID:          "get_file_contents",
			Name:        "Get file contents",
			Description: "读取仓库中某文件或目录的内容。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "owner": {"type": "string"},
    "repo": {"type": "string"},
    "path": {"type": "string", "description": "文件或目录路径，目录以 / 结尾"},
    "ref": {"type": "string", "description": "分支、tag 或 commit SHA，缺省为默认分支"}
  },
  "required": ["owner", "repo"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "get_file_contents"},
		},
		{
			ID:          "list_issues",
			Name:        "List issues",
			Description: "列出仓库的 issue。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "owner": {"type": "string"},
    "repo": {"type": "string"},
    "state": {"type": "string", "description": "OPEN 或 CLOSED，缺省列出全部"},
    "labels": {"type": "array", "items": {"type": "string"}},
    "perPage": {"type": "integer", "minimum": 1, "maximum": 100}
  },
  "required": ["owner", "repo"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "list_issues"},
		},
		{
			ID:          "issue_write",
			Name:        "Create or update issue",
			Description: "创建或更新仓库 issue（写操作）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "method": {"type": "string", "enum": ["create", "update"]},
    "owner": {"type": "string"},
    "repo": {"type": "string"},
    "title": {"type": "string"},
    "body": {"type": "string"},
    "labels": {"type": "array", "items": {"type": "string"}},
    "assignees": {"type": "array", "items": {"type": "string"}},
    "issue_number": {"type": "number", "description": "method=update 时必填"}
  },
  "required": ["method", "owner", "repo"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "issue_write"},
		},
	},
}
```

`all.go`本 task 不需要改：github 没有 managed handler，计划 4 已把`AllHandlers()`的 key 传给`MustRegister`，空表对 github 传零个 key。

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/connectors && go test ./...
```

预期：全部`ok`（含`TestDirectoryMatchesRegisteredTypes`——尚只有 github 目录与注册）。

- [ ] **Step 5: 提交**

```bash
git add packages/connectors/github/definition.go packages/connectors/all_test.go
git commit -m "feat(connectors): implement github connector with oauth+pat and official remote MCP tools"
```

---

### Task 2: Gmail Connector 实装（Remote MCP＋Managed 混合）

**Files:**
- Create: `packages/connectors/gmail/managed_test.go`
- Create: `packages/connectors/gmail/managed.go`
- Create: `packages/connectors/gmail/definition.go`
- Modify: `packages/connectors/all.go`（注册 gmail、AllHandlers 接线）
- Modify: `packages/connectors/all_test.go`（追加`TestGmailDefinition`）

**Interfaces:**
- Consumes: 计划 4 的`connector.ToolCallContext`／`ToolResultData`／`HandlerMap`（签名见「前置契约」）；Task 1 的`TestToolSchemasAndMapperKeys`自动覆盖新 tool
- Produces: `gmail.Definition`（type `gmail`；1 个 oauth auth method；official remote MCP server `official`；tool：remote `search_threads`／`create_draft`＋managed `list_messages`／`send_message`）；`gmail.Handlers connector.HandlerMap`（key：`list_messages`、`send_message`）

managed handler、definition、注册必须在同一 task 落地：definition 引用 handler key，注册时校验引用；而目录一旦创建，`TestDirectoryMatchesRegisteredTypes`就要求该 type 已注册。

- [ ] **Step 1: 写 managed handler 失败测试**

`packages/connectors/gmail/managed_test.go`：

```go
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// withTestServer 用 httptest.Server 覆盖包级 apiBaseURL，测试结束还原。
func withTestServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() {
		apiBaseURL = old
		srv.Close()
	})
}

func TestListMessages(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method got %s", r.Method)
		}
		if r.URL.Path != "/gmail/v1/users/me/messages" {
			t.Errorf("path got %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "from:alice is:unread" {
			t.Errorf("q got %q", got)
		}
		if got := r.URL.Query().Get("maxResults"); got != "5" {
			t.Errorf("maxResults got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("auth got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"messages":[{"id":"m1","threadId":"t1"}],"resultSizeEstimate":1}`))
	})

	res, err := Handlers["list_messages"](context.Background(), connector.ToolCallContext{
		Arguments:   map[string]any{"q": "from:alice is:unread", "max_results": float64(5)},
		AccessToken: "tok-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, `"m1"`) {
		t.Fatalf("res: %+v", res)
	}
}

func TestListMessagesDefaults(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("maxResults"); got != "20" {
			t.Errorf("默认 maxResults 应为 20, got %q", got)
		}
		if r.URL.Query().Has("q") {
			t.Error("未传 q 时不应带 q 参数")
		}
		w.Write([]byte(`{"messages":[]}`))
	})
	if _, err := Handlers["list_messages"](context.Background(), connector.ToolCallContext{
		Arguments:   map[string]any{},
		AccessToken: "t",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSendMessage(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method got %s", r.Method)
		}
		if r.URL.Path != "/gmail/v1/users/me/messages/send" {
			t.Errorf("path got %s", r.URL.Path)
		}
		var body struct {
			Raw string `json:"raw"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		rfc822, err := base64.URLEncoding.DecodeString(body.Raw)
		if err != nil {
			t.Fatalf("raw 不是合法 base64url: %v", err)
		}
		msg := string(rfc822)
		for _, want := range []string{"To: bob@example.com", "Subject: Hi", "hello world"} {
			if !strings.Contains(msg, want) {
				t.Errorf("邮件缺少 %q:\n%s", want, msg)
			}
		}
		w.Write([]byte(`{"id":"sent-1","threadId":"t1","labelIds":["SENT"]}`))
	})

	res, err := Handlers["send_message"](context.Background(), connector.ToolCallContext{
		Arguments: map[string]any{
			"to":      "bob@example.com",
			"subject": "Hi",
			"body":    "hello world",
		},
		AccessToken: "tok-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "sent-1") {
		t.Fatalf("res: %+v", res)
	}
}

func TestSendMessageMissingArgs(t *testing.T) {
	// 参数校验在发请求前完成，无需测试 server。
	res, err := Handlers["send_message"](context.Background(), connector.ToolCallContext{
		Arguments: map[string]any{"to": "bob@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("缺参数应返回 IsError")
	}
}

func TestAPIErrorMapsToToolError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"code":401,"message":"Invalid Credentials"}}`))
	})
	res, err := Handlers["list_messages"](context.Background(), connector.ToolCallContext{
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("API 层错误不应返回 Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "401") {
		t.Fatalf("res: %+v", res)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/connectors && go test ./gmail/
```

预期：编译失败，`undefined: apiBaseURL`、`undefined: Handlers`。

- [ ] **Step 3: 实现 managed.go**

`packages/connectors/gmail/managed.go`：

```go
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/memohai/connect-it/packages/core/connector"
)

// apiBaseURL 指向 Gmail REST API 根地址；测试用 httptest.Server 覆盖后还原。
var apiBaseURL = "https://gmail.googleapis.com"

// Handlers 由 connectors.AllHandlers() 暴露给计划 4 的执行引擎。
var Handlers = connector.HandlerMap{
	"list_messages": listMessages,
	"send_message":  sendMessage,
}

// listMessages 调 GET /gmail/v1/users/me/messages。
// 参数：q（Gmail 搜索语法，可选）、max_results（1–100，默认 20）。
func listMessages(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	max := 20
	if v, ok := call.Arguments["max_results"].(float64); ok && v >= 1 && v <= 100 {
		max = int(v)
	}
	u := apiBaseURL + "/gmail/v1/users/me/messages?maxResults=" + strconv.Itoa(max)
	if q, _ := call.Arguments["q"].(string); q != "" {
		u += "&q=" + url.QueryEscape(q)
	}
	return callGmail(ctx, http.MethodGet, u, nil, call.AccessToken)
}

// sendMessage 组装 RFC 2822 纯文本邮件，base64url 编码后
// 调 POST /gmail/v1/users/me/messages/send。
func sendMessage(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	to, _ := call.Arguments["to"].(string)
	subject, _ := call.Arguments["subject"].(string)
	body, _ := call.Arguments["body"].(string)
	if to == "" || subject == "" || body == "" {
		return connector.ToolResultData{
			Content: `{"error":"to、subject、body 均为必填参数"}`,
			IsError: true,
		}, nil
	}
	rfc822 := fmt.Sprintf(
		"To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		to, subject, body)
	payload, err := json.Marshal(map[string]string{
		"raw": base64.URLEncoding.EncodeToString([]byte(rfc822)),
	})
	if err != nil {
		return connector.ToolResultData{}, err
	}
	return callGmail(ctx, http.MethodPost,
		apiBaseURL+"/gmail/v1/users/me/messages/send",
		bytes.NewReader(payload), call.AccessToken)
}

// callGmail 发送请求并把结果统一转换为 ToolResultData：
// 传输层失败返回 error；HTTP >= 400 转成 IsError 结果（error 为 nil）。
// 与 onedrive 包的 callGraph 结构相同：目录映射测试要求 packages/connectors
// 下只有 provider 目录，因此不抽公共包，接受两份小重复。
func callGmail(ctx context.Context, method, u string, body io.Reader, token string) (connector.ToolResultData, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	if resp.StatusCode >= 400 {
		detail, _ := json.Marshal(map[string]any{
			"error":  fmt.Sprintf("gmail api 返回 %d", resp.StatusCode),
			"detail": string(data),
		})
		return connector.ToolResultData{Content: string(detail), IsError: true}, nil
	}
	return connector.ToolResultData{Content: string(data)}, nil
}
```

- [ ] **Step 4: 运行 handler 测试确认通过**

```bash
cd packages/connectors && go test ./gmail/
```

预期：`ok  github.com/memohai/connect-it/packages/connectors/gmail`。注意此时**不要**运行整个 module 的测试——gmail 目录已存在但尚未注册，`TestDirectoryMatchesRegisteredTypes`会失败，这正是 Step 5–6 要补齐的。

- [ ] **Step 5: 在 all_test.go 追加 definition 失败测试**

在`packages/connectors/all_test.go`末尾追加：

```go
func TestGmailDefinition(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)
	def, ok := r.Get("gmail")
	if !ok {
		t.Fatal("gmail 未注册")
	}

	secretByKey := map[string]bool{}
	for _, f := range def.ConfigFields {
		secretByKey[f.Key] = f.Secret
	}
	for key, wantSecret := range map[string]bool{"client_id": false, "client_secret": true, "project_id": false} {
		got, ok := secretByKey[key]
		if !ok {
			t.Errorf("缺配置字段 %s", key)
			continue
		}
		if got != wantSecret {
			t.Errorf("字段 %s 的 Secret 应为 %v", key, wantSecret)
		}
	}

	if len(def.AuthMethods) != 1 || def.AuthMethods[0].Type != connector.AuthOAuth2 {
		t.Fatalf("gmail 应只有一个 oauth2 auth method: %+v", def.AuthMethods)
	}
	oauth := def.AuthMethods[0].OAuth
	if oauth.AuthorizationEndpoint != "https://accounts.google.com/o/oauth2/v2/auth" ||
		oauth.TokenEndpoint != "https://oauth2.googleapis.com/token" {
		t.Fatalf("google oauth endpoint 不符: %+v", oauth)
	}
	if oauth.ExtraAuthParams["access_type"] != "offline" || oauth.ExtraAuthParams["prompt"] != "consent" {
		t.Fatalf("缺少 refresh token 所需的 ExtraAuthParams: %+v", oauth.ExtraAuthParams)
	}

	var remote, managed int
	for _, tool := range def.Tools {
		switch tool.Backend.(type) {
		case connector.RemoteMCPBackend:
			remote++
		case connector.ManagedBackend:
			managed++
		}
	}
	if remote < 1 || managed < 2 {
		t.Fatalf("gmail 应混合 backend（remote>=1、managed>=2）: remote=%d managed=%d", remote, managed)
	}

	h := connectors.AllHandlers()["gmail"]
	for _, key := range []string{"list_messages", "send_message"} {
		if h[key] == nil {
			t.Errorf("gmail managed handler %q 未接线", key)
		}
	}
}
```

运行确认失败：

```bash
cd packages/connectors && go test -run 'TestGmailDefinition' .
```

预期：FAIL，`gmail 未注册`。

- [ ] **Step 6: 写 definition.go 并接线 all.go**

`packages/connectors/gmail/definition.go`：

```go
// Package gmail 是 Gmail Connector 的固定 Definition 与 Managed handler。
// 验证点（spec §16）：同一 Connector 混合 Remote MCP＋Managed Tool。
package gmail

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "gmail",
	Name:                "Gmail",
	Description:         "Google Gmail 邮件服务",
	Categories:          []string{"communication"},
	HomepageURL:         "https://mail.google.com",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client ID。",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client Secret。",
		},
		{
			Key:         "project_id",
			Label:       "Project ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "启用了 Gmail API 与 Gmail MCP server 的 Google Cloud 项目 ID。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Google OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
				TokenEndpoint:         "https://oauth2.googleapis.com/token",
				// readonly 覆盖 search_threads / list_messages，
				// compose 覆盖 create_draft / send_message（messages.send 接受 compose）。
				Scopes: []string{
					"https://www.googleapis.com/auth/gmail.readonly",
					"https://www.googleapis.com/auth/gmail.compose",
				},
				UsePKCE:           false,
				TokenEndpointAuth: connector.TokenAuthPost,
				// 没有这两个参数 Google 不发 refresh token。
				ExtraAuthParams: map[string]string{
					"access_type": "offline",
					"prompt":      "consent",
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "official",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    "https://gmailmcp.googleapis.com/mcp/v1",
			},
			AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				Publisher:        "Google",
				DocsURL:          "https://developers.google.com/workspace/gmail/api/reference/mcp",
				ReviewedAt:       "2026-07-22",
				AllowedHostnames: []string{"gmailmcp.googleapis.com"},
				// Google Workspace Developer Preview，见「偏离与澄清」第 1 条。
				Stability: connector.StabilityPreview,
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	Tools: []connector.Tool{
		// —— Remote MCP tool（schema 为宽松近似，以 mcp:verify 对照 tools/list 为准）——
		{
			ID:          "search_threads",
			Name:        "Search threads",
			Description: "按 Gmail 搜索语法检索邮件会话。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Gmail 搜索语法，如 from:alice is:unread"
    },
    "pageSize": {"type": "integer", "minimum": 1, "maximum": 100}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.readonly"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "search_threads"},
		},
		{
			ID:          "create_draft",
			Name:        "Create draft",
			Description: "创建邮件草稿（不发送）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "to": {"type": ["string", "array"], "description": "收件人"},
    "cc": {"type": ["string", "array"], "description": "抄送"},
    "bcc": {"type": ["string", "array"], "description": "密送"},
    "subject": {"type": "string"},
    "body": {"type": "string"},
    "threadId": {"type": "string", "description": "回复既有会话时填其 threadId"},
    "raw": {
      "type": "string",
      "description": "base64url 编码的完整 RFC 2822 邮件，与结构化字段二选一"
    },
    "includeBodyHtml": {"type": "boolean"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.compose"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "create_draft"},
		},
		// —— Managed tool（严格 schema，handler 在 managed.go）——
		{
			ID:          "list_messages",
			Name:        "List messages",
			Description: "经 Gmail REST API 列出邮箱中的消息（可按搜索语法过滤）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "q": {"type": "string", "description": "Gmail 搜索语法过滤条件"},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20}
  },
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "messages": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "threadId": {"type": "string"}
        }
      }
    },
    "nextPageToken": {"type": "string"},
    "resultSizeEstimate": {"type": "integer"}
  }
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.readonly"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "list_messages"},
		},
		{
			ID:          "send_message",
			Name:        "Send message",
			Description: "经 Gmail REST API 发送纯文本邮件。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "to": {"type": "string", "description": "收件人邮箱"},
    "subject": {"type": "string"},
    "body": {"type": "string", "description": "纯文本正文（UTF-8）"}
  },
  "required": ["to", "subject", "body"],
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string"},
    "threadId": {"type": "string"},
    "labelIds": {"type": "array", "items": {"type": "string"}}
  }
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.compose"},
			Risk:           connector.RiskWrite,
			Backend:        connector.ManagedBackend{HandlerKey: "send_message"},
		},
	},
}
```

更新`packages/connectors/all.go`为如下内容（若计划 4 把`AllHandlers`放在同包其他文件如`handlers.go`，保持其位置、把函数体改成一致即可）：

```go
// Package connectors 显式注册全部 provider 的 Definition 与 Managed handler。
// 新增 Connector：加目录、加 definition.go（及可选 managed.go）、在这里加注册行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// AllHandlers 返回各 connector type 的 Managed handler 映射（计划 4 执行引擎消费）。
func AllHandlers() map[connector.Type]connector.HandlerMap {
	return map[connector.Type]connector.HandlerMap{
		gmail.Definition.Type: gmail.Handlers,
	}
}

// RegisterAll 注册全部 Definition，并把各自的 handler key 传给 MustRegister 做引用校验。
func RegisterAll(r *registry.Registry) {
	handlers := AllHandlers()
	register := func(def connector.Definition) {
		keys := make([]string, 0, len(handlers[def.Type]))
		for k := range handlers[def.Type] {
			keys = append(keys, k)
		}
		r.MustRegister(def, keys...)
	}
	register(github.Definition)
	register(gmail.Definition)
}
```

- [ ] **Step 7: 运行整个 module 确认通过**

```bash
cd packages/connectors && go test ./...
```

预期：根包与 gmail 包全部`ok`（目录映射测试恢复通过：github、gmail 两个目录各对应注册）。

- [ ] **Step 8: 提交**

```bash
git add packages/connectors/gmail packages/connectors/all.go packages/connectors/all_test.go
git commit -m "feat(connectors): implement gmail connector with mixed remote MCP and managed tools"
```

---

### Task 3: oauthsvc 增加 OAuth endpoint 模板替换

**Files:**
- Create: `packages/service/oauthsvc/template.go`
- Test: `packages/service/oauthsvc/template_test.go`
- Modify: `packages/service/oauthsvc`内读取`OAuthConfig.AuthorizationEndpoint`／`TokenEndpoint`的既有调用点（计划 3 落地的授权发起、code 换 token、token 刷新处）

**Interfaces:**
- Consumes: 计划 3 的 oauthsvc（既有函数签名一律不变）；`connector.OAuthConfig`
- Produces: `oauthsvc.ExpandEndpoint(endpoint string, config map[string]any) (string, error)`——Task 4 的 OneDrive definition 依赖此机制在运行时把`{tenant}`替换为管理员配置值

背景：`OAuthConfig`是纯数据，OneDrive 的 endpoint 含`{tenant}`占位符（Microsoft identity platform 的 endpoint 按租户区分）。计划 3 契约没有模板替换机制，本 task 补上：占位符替换是 oauthsvc 的运行时职责，Definition 不变、oauthsvc 既有签名不变。对不含占位符的 endpoint（GitHub、Google）替换是恒等变换，可无条件套用。

- [ ] **Step 1: 写失败测试**

`packages/service/oauthsvc/template_test.go`：

```go
package oauthsvc

import (
	"strings"
	"testing"
)

func TestExpandEndpointNoPlaceholder(t *testing.T) {
	got, err := ExpandEndpoint(
		"https://accounts.google.com/o/oauth2/v2/auth",
		map[string]any{"tenant": "common"},
	)
	if err != nil || got != "https://accounts.google.com/o/oauth2/v2/auth" {
		t.Fatalf("无占位符应恒等: got %q err %v", got, err)
	}
}

func TestExpandEndpointReplacesTenant(t *testing.T) {
	got, err := ExpandEndpoint(
		"https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
		map[string]any{"tenant": "contoso.onmicrosoft.com"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/authorize"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestExpandEndpointMissingValue(t *testing.T) {
	_, err := ExpandEndpoint(
		"https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
		map[string]any{},
	)
	if err == nil || !strings.Contains(err.Error(), "tenant") {
		t.Fatalf("缺配置值应报含字段名的错误, got %v", err)
	}
}

func TestExpandEndpointNonStringValue(t *testing.T) {
	_, err := ExpandEndpoint("https://x.example.com/{tenant}/y", map[string]any{"tenant": 42})
	if err == nil {
		t.Fatal("非字符串配置值应报错")
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/service && go test ./oauthsvc/
```

预期：编译失败，`undefined: ExpandEndpoint`。

- [ ] **Step 3: 写实现**

`packages/service/oauthsvc/template.go`：

```go
package oauthsvc

import (
	"fmt"
	"regexp"
)

var endpointPlaceholder = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// ExpandEndpoint 把 OAuth endpoint 中的 {config_key} 占位符替换为管理员配置值
// （config 是运行时装配、合并默认值之后的公开配置）。
// OAuthConfig 是纯数据，模板替换是 oauthsvc 的运行时职责；
// 不含占位符的 endpoint 原样返回。占位符缺少对应的非空字符串值时报错。
func ExpandEndpoint(endpoint string, config map[string]any) (string, error) {
	var firstErr error
	out := endpointPlaceholder.ReplaceAllStringFunc(endpoint, func(m string) string {
		key := m[1 : len(m)-1]
		v, ok := config[key].(string)
		if !ok || v == "" {
			if firstErr == nil {
				firstErr = fmt.Errorf("oauth endpoint 占位符 {%s} 缺少对应配置值", key)
			}
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/service && go test ./oauthsvc/
```

预期：`ok  github.com/memohai/connect-it/packages/service/oauthsvc`。

- [ ] **Step 5: 接入既有调用点**

先定位计划 3 代码里读取两个 endpoint 的位置：

```bash
grep -rn "AuthorizationEndpoint\|TokenEndpoint" packages/service --include="*.go" | grep -v _test
```

预期命中三类调用点：授权发起（构造跳转 URL）、回调中 code 换 token、惰性刷新中调 token endpoint。每一处把直接使用`oauth.AuthorizationEndpoint`／`oauth.TokenEndpoint`改为先展开（`cfg`是该函数已持有的、合并默认值后的管理员公开配置）：

```go
endpoint, err := ExpandEndpoint(oauth.AuthorizationEndpoint, cfg)
if err != nil {
	return err // 按所在函数既有的错误返回形态传播（含错误包装惯例）
}
```

token 端同理：

```go
tokenEndpoint, err := ExpandEndpoint(oauth.TokenEndpoint, cfg)
if err != nil {
	return err
}
```

注意刷新路径也必须替换——OneDrive 刷新 token 同样打`{tenant}`endpoint。不改任何函数签名。

- [ ] **Step 6: 运行 service module 全部测试**

```bash
cd packages/service && go test ./...
```

预期：全部`ok`（既有 oauth 测试不受影响：它们的 endpoint 不含占位符，替换为恒等变换）。

- [ ] **Step 7: 提交**

```bash
git add packages/service/oauthsvc
git commit -m "feat(service): expand {tenant}-style placeholders in oauth endpoints"
```

---

### Task 4: OneDrive Connector 实装（Managed Tools＋非 Secret 默认值）

**Files:**
- Create: `packages/connectors/onedrive/managed_test.go`
- Create: `packages/connectors/onedrive/managed.go`
- Create: `packages/connectors/onedrive/definition.go`
- Modify: `packages/connectors/all.go`（注册 onedrive、AllHandlers 接线）
- Modify: `packages/connectors/all_test.go`（追加`TestOneDriveDefinition`）

**Interfaces:**
- Consumes: 计划 4 的`connector.ToolCallContext`／`ToolResultData`／`HandlerMap`；Task 3 的`oauthsvc.ExpandEndpoint`（运行时替换`{tenant}`，本 task 只在 definition 中写占位符）
- Produces: `onedrive.Definition`（type `one_drive`、目录名`onedrive`；tenant 字段默认值`common`；全 ManagedBackend tool：`list_drive_items`、`upload_file`）；`onedrive.Handlers connector.HandlerMap`（key：`list_drive_items`、`upload_file`）

- [ ] **Step 1: 写 managed handler 失败测试**

`packages/connectors/onedrive/managed_test.go`：

```go
package onedrive

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// withTestServer 用 httptest.Server 覆盖包级 apiBaseURL，测试结束还原。
func withTestServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() {
		apiBaseURL = old
		srv.Close()
	})
}

func TestListDriveItemsRoot(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method got %s", r.Method)
		}
		if r.URL.Path != "/v1.0/me/drive/root/children" {
			t.Errorf("path got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("auth got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":[{"id":"i1","name":"Documents","folder":{"childCount":2}}]}`))
	})

	res, err := Handlers["list_drive_items"](context.Background(), connector.ToolCallContext{
		Arguments:   map[string]any{},
		AccessToken: "tok-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, "Documents") {
		t.Fatalf("res: %+v", res)
	}
}

func TestListDriveItemsSubfolder(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path 是解码后的路径：路径段（含中文）已被 handler 逐段 PathEscape。
		if r.URL.Path != "/v1.0/me/drive/root:/Documents/月报:/children" {
			t.Errorf("path got %s", r.URL.Path)
		}
		w.Write([]byte(`{"value":[]}`))
	})
	if _, err := Handlers["list_drive_items"](context.Background(), connector.ToolCallContext{
		Arguments:   map[string]any{"path": "Documents/月报"},
		AccessToken: "t",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUploadFile(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method got %s", r.Method)
		}
		if r.URL.Path != "/v1.0/me/drive/root:/notes/hello.txt:/content" {
			t.Errorf("path got %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("content-type got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "hello world" {
			t.Errorf("body got %q", body)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"f1","name":"hello.txt","size":11}`))
	})

	res, err := Handlers["upload_file"](context.Background(), connector.ToolCallContext{
		Arguments: map[string]any{
			"path":    "notes/hello.txt",
			"content": "hello world",
		},
		AccessToken: "tok-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content, `"f1"`) {
		t.Fatalf("res: %+v", res)
	}
}

func TestUploadFileMissingArgs(t *testing.T) {
	// 参数校验在发请求前完成，无需测试 server。
	res, err := Handlers["upload_file"](context.Background(), connector.ToolCallContext{
		Arguments: map[string]any{"path": "a.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("缺参数应返回 IsError")
	}
}

func TestGraphErrorMapsToToolError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":"accessDenied","message":"Access denied"}}`))
	})
	res, err := Handlers["list_drive_items"](context.Background(), connector.ToolCallContext{
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("API 层错误不应返回 Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "403") {
		t.Fatalf("res: %+v", res)
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/connectors && go test ./onedrive/
```

预期：编译失败，`undefined: apiBaseURL`、`undefined: Handlers`。

- [ ] **Step 3: 实现 managed.go**

`packages/connectors/onedrive/managed.go`：

```go
package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
)

// apiBaseURL 指向 Microsoft Graph 根地址；测试用 httptest.Server 覆盖后还原。
var apiBaseURL = "https://graph.microsoft.com"

// Handlers 由 connectors.AllHandlers() 暴露给计划 4 的执行引擎。
var Handlers = connector.HandlerMap{
	"list_drive_items": listDriveItems,
	"upload_file":      uploadFile,
}

// listDriveItems 列出根目录（GET /v1.0/me/drive/root/children）
// 或指定文件夹（GET /v1.0/me/drive/root:/{path}:/children）。
func listDriveItems(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	u := apiBaseURL + "/v1.0/me/drive/root/children"
	if path, _ := call.Arguments["path"].(string); path != "" {
		u = apiBaseURL + "/v1.0/me/drive/root:/" + escapePath(path) + ":/children"
	}
	return callGraph(ctx, http.MethodGet, u, nil, "", call.AccessToken)
}

// uploadFile 简单上传文本文件（PUT /v1.0/me/drive/root:/{path}:/content，<4MB）。
func uploadFile(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	path, _ := call.Arguments["path"].(string)
	content, _ := call.Arguments["content"].(string)
	if path == "" || content == "" {
		return connector.ToolResultData{
			Content: `{"error":"path 与 content 均为必填参数"}`,
			IsError: true,
		}, nil
	}
	u := apiBaseURL + "/v1.0/me/drive/root:/" + escapePath(path) + ":/content"
	return callGraph(ctx, http.MethodPut, u,
		strings.NewReader(content), "application/octet-stream", call.AccessToken)
}

// escapePath 对路径逐段 PathEscape，保留段间的 /。
func escapePath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// callGraph 发送请求并把结果统一转换为 ToolResultData：
// 传输层失败返回 error；HTTP >= 400 转成 IsError 结果（error 为 nil）。
// 与 gmail 包的 callGmail 结构相同：目录映射测试要求 packages/connectors
// 下只有 provider 目录，因此不抽公共包，接受两份小重复。
func callGraph(ctx context.Context, method, u string, body io.Reader, contentType, token string) (connector.ToolResultData, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	if resp.StatusCode >= 400 {
		detail, _ := json.Marshal(map[string]any{
			"error":  fmt.Sprintf("microsoft graph 返回 %d", resp.StatusCode),
			"detail": string(data),
		})
		return connector.ToolResultData{Content: string(detail), IsError: true}, nil
	}
	return connector.ToolResultData{Content: string(data)}, nil
}
```

- [ ] **Step 4: 运行 handler 测试确认通过**

```bash
cd packages/connectors && go test ./onedrive/
```

预期：`ok  github.com/memohai/connect-it/packages/connectors/onedrive`。同 Task 2：此时不要跑整个 module，目录映射测试要到 Step 6 接线后才恢复。

- [ ] **Step 5: 在 all_test.go 追加 definition 失败测试**

在`packages/connectors/all_test.go`末尾追加：

```go
func TestOneDriveDefinition(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)
	def, ok := r.Get("one_drive")
	if !ok {
		t.Fatal("one_drive 未注册")
	}

	var tenant *connector.ConfigField
	for i := range def.ConfigFields {
		if def.ConfigFields[i].Key == "tenant" {
			tenant = &def.ConfigFields[i]
		}
	}
	if tenant == nil {
		t.Fatal("缺 tenant 字段")
	}
	if tenant.Secret || tenant.DefaultValue == nil || *tenant.DefaultValue != "common" {
		t.Fatalf("tenant 应为非 Secret 且默认 common: %+v", tenant)
	}

	if len(def.AuthMethods) != 1 || def.AuthMethods[0].Type != connector.AuthOAuth2 {
		t.Fatalf("one_drive 应只有一个 oauth2 auth method: %+v", def.AuthMethods)
	}
	oauth := def.AuthMethods[0].OAuth
	if !strings.Contains(oauth.AuthorizationEndpoint, "{tenant}") ||
		!strings.Contains(oauth.TokenEndpoint, "{tenant}") {
		t.Fatalf("microsoft endpoint 应含 {tenant} 占位符: %+v", oauth)
	}
	if !oauth.UsePKCE {
		t.Fatal("microsoft oauth 应启用 PKCE")
	}

	if len(def.RemoteMCPServers) != 0 {
		t.Fatal("one_drive 不应有 remote MCP server")
	}
	if len(def.Tools) == 0 {
		t.Fatal("one_drive 应至少有一个 tool")
	}
	for _, tool := range def.Tools {
		if _, ok := tool.Backend.(connector.ManagedBackend); !ok {
			t.Errorf("one_drive tool %s 应为 ManagedBackend", tool.ID)
		}
	}

	h := connectors.AllHandlers()["one_drive"]
	for _, key := range []string{"list_drive_items", "upload_file"} {
		if h[key] == nil {
			t.Errorf("one_drive managed handler %q 未接线", key)
		}
	}
}
```

运行确认失败：

```bash
cd packages/connectors && go test -run 'TestOneDriveDefinition' .
```

预期：FAIL，`one_drive 未注册`。

- [ ] **Step 6: 写 definition.go 并接线 all.go**

`packages/connectors/onedrive/definition.go`：

```go
// Package onedrive 是 OneDrive Connector 的固定 Definition 与 Managed handler。
// 验证点（spec §16）：Managed Tools；非 Secret 字段默认值（tenant=common）。
package onedrive

import (
	"encoding/json"

	"github.com/memohai/connect-it/packages/core/connector"
)

func strPtr(s string) *string { return &s }

var Definition = connector.Definition{
	Type:                "one_drive",
	Name:                "OneDrive",
	Description:         "Microsoft OneDrive 云存储",
	Categories:          []string{"storage"},
	HomepageURL:         "https://onedrive.live.com",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Microsoft Entra 应用注册的 Application (client) ID。",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Microsoft Entra 应用注册的 client secret。",
		},
		{
			Key:          "tenant",
			Label:        "Tenant",
			InputType:    connector.InputText,
			Required:     true,
			DefaultValue: strPtr("common"),
			Description:  "Microsoft 租户：common、organizations、consumers 或具体 tenant ID。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Microsoft OAuth",
			OAuth: &connector.OAuthConfig{
				// {tenant} 占位符由 oauthsvc.ExpandEndpoint 在运行时
				// 用管理员配置（含默认值 common）替换，见计划 6 Task 3。
				AuthorizationEndpoint: "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
				TokenEndpoint:         "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
				// offline_access 换取 refresh token；Files.ReadWrite 覆盖两个 tool。
				Scopes:            []string{"offline_access", "User.Read", "Files.ReadWrite"},
				UsePKCE:           true,
				TokenEndpointAuth: connector.TokenAuthPost,
			},
		},
	},

	Tools: []connector.Tool{
		{
			ID:          "list_drive_items",
			Name:        "List drive items",
			Description: "列出 OneDrive 指定文件夹（默认根目录）下的文件与子文件夹。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "相对 OneDrive 根目录的文件夹路径，留空列出根目录"
    }
  },
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "value": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "name": {"type": "string"},
          "size": {"type": "integer"},
          "folder": {"type": "object"},
          "file": {"type": "object"}
        }
      }
    }
  }
}`),
			RequiredScopes: []string{"Files.ReadWrite"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "list_drive_items"},
		},
		{
			ID:          "upload_file",
			Name:        "Upload file",
			Description: "上传文本文件到 OneDrive 指定路径（简单上传，上限 4MB）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "目标文件路径（含文件名），相对 OneDrive 根目录"
    },
    "content": {
      "type": "string",
      "description": "文件文本内容（UTF-8）"
    }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string"},
    "name": {"type": "string"},
    "size": {"type": "integer"},
    "webUrl": {"type": "string"}
  }
}`),
			RequiredScopes: []string{"Files.ReadWrite"},
			Risk:           connector.RiskWrite,
			Backend:        connector.ManagedBackend{HandlerKey: "upload_file"},
		},
	},
}
```

更新`packages/connectors/all.go`：

```go
// Package connectors 显式注册全部 provider 的 Definition 与 Managed handler。
// 新增 Connector：加目录、加 definition.go（及可选 managed.go）、在这里加注册行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/connectors/onedrive"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// AllHandlers 返回各 connector type 的 Managed handler 映射（计划 4 执行引擎消费）。
func AllHandlers() map[connector.Type]connector.HandlerMap {
	return map[connector.Type]connector.HandlerMap{
		gmail.Definition.Type:    gmail.Handlers,
		onedrive.Definition.Type: onedrive.Handlers,
	}
}

// RegisterAll 注册全部 Definition，并把各自的 handler key 传给 MustRegister 做引用校验。
func RegisterAll(r *registry.Registry) {
	handlers := AllHandlers()
	register := func(def connector.Definition) {
		keys := make([]string, 0, len(handlers[def.Type]))
		for k := range handlers[def.Type] {
			keys = append(keys, k)
		}
		r.MustRegister(def, keys...)
	}
	register(github.Definition)
	register(gmail.Definition)
	register(onedrive.Definition)
}
```

- [ ] **Step 7: 运行整个 module 确认通过**

```bash
cd packages/connectors && go test ./...
```

预期：全部`ok`（目录映射：github、gmail、onedrive 三目录对应 github、gmail、one_drive 三个 type）。

- [ ] **Step 8: 提交**

```bash
git add packages/connectors/onedrive packages/connectors/all.go packages/connectors/all_test.go
git commit -m "feat(connectors): implement onedrive connector with managed graph tools"
```

---

### Task 5: Google Ads Connector 实装（self_hosted MCP＋公开／秘密扩展字段）

**Files:**
- Create: `packages/connectors/googleads/definition.go`
- Modify: `packages/connectors/all.go`（注册 googleads）
- Modify: `packages/connectors/all_test.go`（追加`TestGoogleAdsDefinition`）

**Interfaces:**
- Consumes: 计划 1 registry 的 self_hosted 校验规则（config endpoint 仅 self_hosted、引用字段必须存在）；计划 2／管理端的`mcp:verify`流程（运行时行为，本 task 只声明数据）
- Produces: `googleads.Definition`（type `google_ads`、目录名`googleads`；6 个配置字段含 2 个 Secret；self_hosted RemoteMCPServer `self_hosted`，endpoint 取自`mcp_url`配置字段；全 RemoteMCPBackend tool：`list_accessible_customers`、`search`）

Google Ads 没有官方托管的 remote MCP endpoint——官方实现`googleads/google-ads-mcp`需要自行部署，因此 endpoint 由管理员填写（`mcp_url`），Provenance 为`self_hosted`，未通过`mcp:verify`前状态停在`needs_config`（spec §13）。

- [ ] **Step 1: 在 all_test.go 追加失败测试**

在`packages/connectors/all_test.go`末尾追加：

```go
func TestGoogleAdsDefinition(t *testing.T) {
	r := registry.New()
	connectors.RegisterAll(r)
	def, ok := r.Get("google_ads")
	if !ok {
		t.Fatal("google_ads 未注册")
	}

	secretByKey := map[string]bool{}
	for _, f := range def.ConfigFields {
		secretByKey[f.Key] = f.Secret
	}
	for key, wantSecret := range map[string]bool{
		"client_id":       false,
		"client_secret":   true,
		"project_id":      false,
		"developer_token": true,
		"customer_id":     false,
		"mcp_url":         false,
	} {
		got, ok := secretByKey[key]
		if !ok {
			t.Errorf("缺配置字段 %s", key)
			continue
		}
		if got != wantSecret {
			t.Errorf("字段 %s 的 Secret 应为 %v", key, wantSecret)
		}
	}

	if len(def.AuthMethods) != 1 || def.AuthMethods[0].Type != connector.AuthOAuth2 {
		t.Fatalf("google_ads 应只有一个 oauth2 auth method: %+v", def.AuthMethods)
	}
	scopes := def.AuthMethods[0].OAuth.Scopes
	if len(scopes) != 1 || scopes[0] != "https://www.googleapis.com/auth/adwords" {
		t.Fatalf("scope 应为 adwords: %v", scopes)
	}

	if len(def.RemoteMCPServers) != 1 {
		t.Fatal("google_ads 应恰有一个 remote MCP server")
	}
	srv := def.RemoteMCPServers[0]
	if srv.Endpoint.Source != connector.EndpointConfigField || srv.Endpoint.ConfigFieldKey != "mcp_url" {
		t.Fatalf("endpoint 应取自 mcp_url 配置字段: %+v", srv.Endpoint)
	}
	if srv.Provenance.Kind != connector.ProvenanceSelfHosted {
		t.Fatalf("provenance 应为 self_hosted: %+v", srv.Provenance)
	}

	if len(def.Tools) == 0 {
		t.Fatal("google_ads 应至少有一个 tool")
	}
	for _, tool := range def.Tools {
		b, ok := tool.Backend.(connector.RemoteMCPBackend)
		if !ok {
			t.Errorf("google_ads tool %s 应为 RemoteMCPBackend", tool.ID)
			continue
		}
		if b.ServerKey != "self_hosted" {
			t.Errorf("tool %s 应指向 self_hosted server", tool.ID)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/connectors && go test -run 'TestGoogleAdsDefinition' .
```

预期：FAIL，`google_ads 未注册`。

- [ ] **Step 3: 写 definition.go 并接线 all.go**

`packages/connectors/googleads/definition.go`：

```go
// Package googleads 是 Google Ads Connector 的固定 Definition。
// 验证点（spec §16）：公开／秘密扩展字段；self_hosted Streamable HTTP MCP。
package googleads

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_ads",
	Name:                "Google Ads",
	Description:         "Google Ads 广告投放平台（经自托管官方 MCP server）",
	Categories:          []string{"advertising"},
	HomepageURL:         "https://ads.google.com",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client ID。",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client Secret。",
		},
		{
			Key:         "project_id",
			Label:       "Project ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "启用了 Google Ads API 的 Google Cloud 项目 ID。",
		},
		{
			Key:       "developer_token",
			Label:     "Developer Token",
			InputType: connector.InputText,
			Required:  true,
			Secret:    true,
			Description: "Google Ads API developer token。第一期仅保存不随请求传输，" +
				"自托管 MCP server 侧需配置同一 token。",
		},
		{
			Key:         "customer_id",
			Label:       "Customer ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "默认操作的 Google Ads customer ID，10 位数字，不含连字符。",
			Validation:  connector.FieldValidation{Pattern: `^[0-9]{10}$`},
		},
		{
			Key:       "mcp_url",
			Label:     "MCP URL",
			InputType: connector.InputURL,
			Required:  true,
			Description: "自托管 googleads/google-ads-mcp server 的 Streamable HTTP endpoint，" +
				"填写后须通过 mcp:verify 方可用。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Google OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
				TokenEndpoint:         "https://oauth2.googleapis.com/token",
				Scopes:                []string{"https://www.googleapis.com/auth/adwords"},
				UsePKCE:               false,
				TokenEndpointAuth:     connector.TokenAuthPost,
				ExtraAuthParams: map[string]string{
					"access_type": "offline",
					"prompt":      "consent",
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "self_hosted",
			Endpoint: connector.Endpoint{
				Source:         connector.EndpointConfigField,
				ConfigFieldKey: "mcp_url",
			},
			AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
			Provenance: connector.Provenance{
				Kind:       connector.ProvenanceSelfHosted,
				Publisher:  "self-hosted（官方 googleads/google-ads-mcp）",
				DocsURL:    "https://developers.google.com/google-ads/api/docs/developer-toolkit/mcp-server",
				SourceURL:  "https://github.com/googleads/google-ads-mcp",
				ReviewedAt: "2026-07-22",
				Stability:  connector.StabilityPreview,
			},
			// GAQL 查询可能较慢，给足超时。
			RequestTimeout: 60 * time.Second,
		},
	},

	// tool 名与官方 googleads/google-ads-mcp 一致；schema 为宽松近似，
	// 以 mcp:verify 对照 tools/list 为准。
	Tools: []connector.Tool{
		{
			ID:          "list_accessible_customers",
			Name:        "List accessible customers",
			Description: "列出当前授权用户可访问的 Google Ads customer。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/adwords"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "self_hosted",
				RemoteToolName: "list_accessible_customers",
			},
		},
		{
			ID:          "search",
			Name:        "Search (GAQL)",
			Description: "对指定 customer 执行 GAQL 查询，返回广告系列、预算、指标等数据。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "customer_id": {
      "type": "string",
      "description": "查询的 Google Ads customer ID（10 位数字，不含连字符）"
    },
    "query": {
      "type": "string",
      "description": "GAQL 查询，如 SELECT campaign.id, campaign.name FROM campaign"
    }
  },
  "required": ["customer_id", "query"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/adwords"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "self_hosted",
				RemoteToolName: "search",
			},
		},
	},
}
```

更新`packages/connectors/all.go`为最终形态：

```go
// Package connectors 显式注册全部 provider 的 Definition 与 Managed handler。
// 新增 Connector：加目录、加 definition.go（及可选 managed.go）、在这里加注册行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/connectors/googleads"
	"github.com/memohai/connect-it/packages/connectors/onedrive"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// AllHandlers 返回各 connector type 的 Managed handler 映射（计划 4 执行引擎消费）。
func AllHandlers() map[connector.Type]connector.HandlerMap {
	return map[connector.Type]connector.HandlerMap{
		gmail.Definition.Type:    gmail.Handlers,
		onedrive.Definition.Type: onedrive.Handlers,
	}
}

// RegisterAll 注册全部 Definition，并把各自的 handler key 传给 MustRegister 做引用校验。
func RegisterAll(r *registry.Registry) {
	handlers := AllHandlers()
	register := func(def connector.Definition) {
		keys := make([]string, 0, len(handlers[def.Type]))
		for k := range handlers[def.Type] {
			keys = append(keys, k)
		}
		r.MustRegister(def, keys...)
	}
	register(github.Definition)
	register(gmail.Definition)
	register(onedrive.Definition)
	register(googleads.Definition)
}
```

- [ ] **Step 4: 运行整个 module 确认通过**

```bash
cd packages/connectors && go test ./...
```

预期：全部`ok`（目录映射：github、gmail、onedrive、googleads 四目录对应 github、gmail、one_drive、google_ads 四个 type）。

- [ ] **Step 5: 全仓库回归**

```bash
mise run test
mise run vet
```

预期：core、connectors、service、api 各 module 全绿——service 与 api 的启动路径调用`RegisterAll`，四个 definition 的注册校验在它们的测试中再次执行。

- [ ] **Step 6: 提交**

```bash
git add packages/connectors/googleads packages/connectors/all.go packages/connectors/all_test.go
git commit -m "feat(connectors): implement google ads connector with self-hosted MCP"
```

---

## 完成标准（对照 spec §16／§17）

- [ ] GitHub：oauth＋pat 双 auth method；官方 remote MCP（`https://api.githubcopilot.com/mcp/`）5 个固定映射 tool；OAuth 配置字段可选、PAT 路径独立可用（spec §16「多 auth method；Remote MCP Tools」）；
- [ ] Gmail：同一 Connector 混合 2 个 Remote MCP tool（官方`gmailmcp.googleapis.com`）＋2 个 Managed tool（Gmail REST），`gmail.Handlers`经`AllHandlers()`接线（spec §16「混合 Remote MCP＋Managed Tool」）；
- [ ] OneDrive：全 Managed tool（Microsoft Graph）；tenant 字段非 Secret 默认值`common`；OAuth endpoint 含`{tenant}`占位符且`oauthsvc.ExpandEndpoint`完成运行时替换（spec §16「Managed Tools；非 Secret 默认值」）；
- [ ] Google Ads：6 个配置字段（2 个 Secret）；self_hosted RemoteMCPServer 的 endpoint 取自`mcp_url`配置字段；全部 tool 走 RemoteMCPBackend（spec §16「公开／秘密扩展字段；self_hosted Streamable HTTP」）；
- [ ] 所有 RemoteMCPBackend 的`InputMapperKey`／`OutputMapperKey`为空（计划 4 契约），由`TestToolSchemasAndMapperKeys`锁定；
- [ ] 所有 InputSchema 为合法 JSON object；managed handler 用`httptest`覆盖`apiBaseURL`验证请求路径、`Authorization: Bearer`头、参数与错误映射；
- [ ] `TestDirectoryMatchesRegisteredTypes`通过：四个目录（github、gmail、onedrive、googleads）与四个 type（github、gmail、one_drive、google_ads）一一对应；
- [ ] `mise run test`与`mise run vet`全绿；新增 Connector 未触碰任何数据库 migration（spec §17）。

