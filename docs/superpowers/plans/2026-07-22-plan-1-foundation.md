# connect-it 计划 1：仓库脚手架与 core／connectors 模块

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 搭起 MonoRepo 脚手架（mise、pnpm workspace、ui submodule），完整实现`packages/core`（Definition 类型、Registry 校验、AES-GCM 加密、状态机）和`packages/connectors`骨架（catalog-only 的 GitHub definition＋显式注册）。

**Architecture:** 多 Go module MonoRepo，无 go.work，module 间用 go.mod `replace`按相对路径互引。core 是纯库（不依赖 Echo／pgx），connectors 只含纯数据 definition 与注册代码。所有校验双层执行：注册时校验（启动 panic）＋单元测试。

**Tech Stack:** Go 1.25、mise（工具链＋任务）、pnpm 10 workspace、node 22。本计划不涉及 Echo、sqlc、前端。

**Spec:** `docs/superpowers/specs/2026-07-22-connect-it-design.md`（第 3–6、8、9 节是本计划的依据）

## Global Constraints

- Go module 路径固定为`github.com/memohai/connect-it/packages/<name>`。
- 依赖方向单向：`connectors→core`。禁止反向 import。
- 每个 module 的 go.mod 必须用`replace`列出其全部本地依赖（含间接），按相对路径。
- 没有 workspace：所有`go`命令必须`cd`进对应 module 目录执行。
- `definition.go`只声明固定数据，不访问数据库或网络。
- `connector_type`用 snake_case；包目录名为去掉下划线的形式；由测试校验映射。
- Secret 配置字段禁止设置默认值（Registry 校验强制）。
- MCP 仅 streamable_http；固定 endpoint 必须 https；从配置取 endpoint 仅限 self_hosted。
- 工具链版本一律经 mise 安装：go 1.25、node 22、pnpm 10。
- 提交信息用 conventional commits（feat:／test:／chore:），每个 task 至少一次提交。

## 后续计划路线图（本计划不含，仅供了解全貌）

- 计划 2：service module 数据库层（sqlc、migrations、connector_configs）＋api module 骨架（Echo、门禁、catalog／config API）
- 计划 3：OAuth 授权流程、connections、token 惰性刷新
- 计划 4：Tool 执行引擎（Managed＋Remote MCP client）、connector_health、tool_runs
- 计划 5：聚合 /mcp 与 mcp_sessions
- 计划 6：四个首批 Connector 完整实现
- 计划 7：前端管理界面（packages/web）
- 计划 8：Docker 与 CI

---

### Task 1: MonoRepo 脚手架

**Files:**
- Create: `mise.toml`
- Create: `package.json`
- Create: `pnpm-workspace.yaml`
- Modify: `.gitignore`（追加条目）
- Create: `packages/ui`（git submodule）

**Interfaces:**
- Consumes: 无
- Produces: `mise run test`／`mise run vet`任务（逐 module 循环，后续所有 task 用它跑测试）；`packages/`目录结构

- [ ] **Step 1: 写入 mise.toml**

```toml
[tools]
go = "1.25"
node = "22"
pnpm = "10"

[tasks.test]
description = "逐 Go module 运行测试"
run = '''
set -e
for d in packages/*/; do
  if [ -f "$d/go.mod" ]; then
    echo "==> $d"
    (cd "$d" && go test ./...)
  fi
done
'''

[tasks.vet]
description = "逐 Go module 运行 go vet"
run = '''
set -e
for d in packages/*/; do
  if [ -f "$d/go.mod" ]; then
    echo "==> $d"
    (cd "$d" && go vet ./...)
  fi
done
'''
```

- [ ] **Step 2: 写入 pnpm workspace 配置**

`package.json`：

```json
{
  "name": "connect-it",
  "private": true
}
```

`pnpm-workspace.yaml`：

```yaml
packages:
  - packages/ui
  - packages/sdk
  - packages/web
```

（`packages/sdk`是计划 7 建立的 TypeScript SDK 包，workspace 先行声明，目录不存在时 pnpm 忽略。）

- [ ] **Step 3: 追加 .gitignore**

先读现有`.gitignore`内容，把其中**尚未包含**的以下条目追加到末尾：

```text
node_modules/
dist/
.env
```

- [ ] **Step 4: 添加 ui submodule**

```bash
git submodule add https://github.com/memohai/ui.git packages/ui
```

需要网络与仓库访问权限。若失败，报告错误并停止，不要用空目录蒙混。

- [ ] **Step 5: 验证**

```bash
mise install
mise run test
git submodule status
```

预期：`mise install`装齐 go 1.25／node 22／pnpm 10；`mise run test`无输出直接成功退出（还没有 go.mod）；`git submodule status`列出`packages/ui`及其 commit。

- [ ] **Step 6: 提交**

```bash
git add mise.toml package.json pnpm-workspace.yaml .gitignore .gitmodules packages/ui
git commit -m "chore: scaffold monorepo with mise, pnpm workspace and ui submodule"
```

---

### Task 2: core module 与 connector 类型包

**Files:**
- Create: `packages/core/go.mod`
- Create: `packages/core/connector/types.go`
- Test: `packages/core/connector/types_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `package connector`的全部类型（后续所有 task 依赖），关键的有：
  - `connector.Type`（string）、`connector.Definition`
  - `connector.ConfigField{Key, Label, InputType, Required, Secret, DefaultValue *string, Description, Validation}`
  - `connector.AuthMethod{Key, Type, Label, OAuth *OAuthConfig, CredentialFields}`，`AuthMethodType`常量`AuthNone/AuthOAuth2/AuthAPIKey/AuthCustomCredential`
  - `connector.RemoteMCPServer{Key, Endpoint, AuthBinding, Provenance, RequestTimeout}`，`Endpoint{Source, URL, ConfigFieldKey}`，`EndpointFixed/EndpointConfigField`
  - `connector.Tool{ID, Name, Description, InputSchema, OutputSchema, RequiredScopes, Risk, Backend}`
  - tagged union：`connector.ToolBackend`接口，实现为`RemoteMCPBackend{ServerKey, RemoteToolName, InputMapperKey, OutputMapperKey}`与`ManagedBackend{HandlerKey}`
  - `connector.ConfigUpgrader{FromVersion int, Upgrade func(public, secret map[string]any) (map[string]any, map[string]any, error)}`

- [ ] **Step 1: 初始化 module**

```bash
mkdir -p packages/core/connector
cd packages/core && go mod init github.com/memohai/connect-it/packages/core
```

预期：生成`go.mod`，内容为 module 行＋`go 1.25`。

- [ ] **Step 2: 写失败测试**

`packages/core/connector/types_test.go`：

```go
package connector_test

import (
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// 类型包无行为，测试锁定两件事：tagged union 可被 type switch 区分；零值可用。
func TestToolBackendTaggedUnion(t *testing.T) {
	tools := []connector.Tool{
		{ID: "a", Backend: connector.RemoteMCPBackend{ServerKey: "s", RemoteToolName: "r"}},
		{ID: "b", Backend: connector.ManagedBackend{HandlerKey: "h"}},
	}
	var kinds []string
	for _, tool := range tools {
		switch tool.Backend.(type) {
		case connector.RemoteMCPBackend:
			kinds = append(kinds, "remote")
		case connector.ManagedBackend:
			kinds = append(kinds, "managed")
		default:
			t.Fatalf("tool %s: 未知 backend", tool.ID)
		}
	}
	if kinds[0] != "remote" || kinds[1] != "managed" {
		t.Fatalf("got %v", kinds)
	}
}

func TestDefinitionZeroValue(t *testing.T) {
	var def connector.Definition
	if def.Deprecated || len(def.Tools) != 0 {
		t.Fatal("零值 Definition 应为空且未弃用")
	}
}
```

- [ ] **Step 3: 运行确认失败**

```bash
cd packages/core && go test ./...
```

预期：编译失败，`undefined: connector.Tool`等。

- [ ] **Step 4: 写类型实现**

`packages/core/connector/types.go`：

```go
// Package connector 定义 Connector 的固定 Definition 类型。
// 本包只有纯数据类型，不做任何 I/O。
package connector

import (
	"encoding/json"
	"time"
)

// Type 是稳定的平台标识（snake_case），如 "github"、"one_drive"。
type Type string

// Definition 是代码内固定的 Connector 模板。
type Definition struct {
	Type                Type
	Name                string
	Description         string
	Categories          []string
	HomepageURL         string
	IconURL             string
	ConfigSchemaVersion int

	ConfigFields     []ConfigField
	AuthMethods      []AuthMethod
	RemoteMCPServers []RemoteMCPServer
	Tools            []Tool

	ConfigUpgraders []ConfigUpgrader

	Deprecated bool
}

type ConfigInputType string

const (
	InputText   ConfigInputType = "text"
	InputURL    ConfigInputType = "url"
	InputSelect ConfigInputType = "select"
)

type FieldValidation struct {
	Pattern string   // 正则，空串表示不校验
	Options []string // InputSelect 的可选值
}

type ConfigField struct {
	Key          string
	Label        string
	InputType    ConfigInputType
	Required     bool
	Secret       bool
	DefaultValue *string // Secret 字段禁止设置
	Description  string
	Validation   FieldValidation
}

type AuthMethodType string

const (
	AuthNone             AuthMethodType = "none"
	AuthOAuth2           AuthMethodType = "oauth2"
	AuthAPIKey           AuthMethodType = "api_key"
	AuthCustomCredential AuthMethodType = "custom_credential"
)

type TokenEndpointAuth string

const (
	TokenAuthBasic TokenEndpointAuth = "client_secret_basic"
	TokenAuthPost  TokenEndpointAuth = "client_secret_post"
)

type OAuthConfig struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
	Scopes                []string
	UsePKCE               bool
	TokenEndpointAuth     TokenEndpointAuth
	ExtraAuthParams       map[string]string
	ProfileResolverKey    string
}

type AuthMethod struct {
	Key   string // Connector 内唯一，如 "oauth"、"pat"
	Type  AuthMethodType
	Label string
	// OAuth 仅 Type == AuthOAuth2 时必填，其他类型必须为 nil。
	OAuth *OAuthConfig
	// CredentialFields 是 api_key / custom_credential 需要用户填写的字段。
	CredentialFields []ConfigField
}

type EndpointSource string

const (
	EndpointFixed       EndpointSource = "fixed"
	EndpointConfigField EndpointSource = "config_field"
)

type Endpoint struct {
	Source         EndpointSource
	URL            string // Source == EndpointFixed 时使用，必须为 https
	ConfigFieldKey string // Source == EndpointConfigField 时引用的 ConfigField
}

type MCPAuthBinding struct {
	Scheme string // 目前仅 "bearer"
}

type ProvenanceKind string

const (
	ProvenanceOfficial   ProvenanceKind = "official"
	ProvenanceThirdParty ProvenanceKind = "third_party"
	ProvenanceSelfHosted ProvenanceKind = "self_hosted"
)

type Stability string

const (
	StabilityStable       Stability = "stable"
	StabilityPreview      Stability = "preview"
	StabilityExperimental Stability = "experimental"
)

type Provenance struct {
	Kind             ProvenanceKind
	Publisher        string
	DocsURL          string
	SourceURL        string
	ReviewedAt       string // YYYY-MM-DD
	AllowedHostnames []string
	Stability        Stability
}

type RemoteMCPServer struct {
	Key            string
	Endpoint       Endpoint
	AuthBinding    MCPAuthBinding
	Provenance     Provenance
	RequestTimeout time.Duration
}

type ToolRisk string

const (
	RiskRead        ToolRisk = "read"
	RiskWrite       ToolRisk = "write"
	RiskDestructive ToolRisk = "destructive"
)

// ToolBackend 是 tagged union：RemoteMCPBackend 或 ManagedBackend。
type ToolBackend interface{ isToolBackend() }

type RemoteMCPBackend struct {
	ServerKey       string
	RemoteToolName  string
	InputMapperKey  string
	OutputMapperKey string
}

func (RemoteMCPBackend) isToolBackend() {}

type ManagedBackend struct {
	HandlerKey string
}

func (ManagedBackend) isToolBackend() {}

type Tool struct {
	ID             string // ^[a-z0-9_]+$
	Name           string
	Description    string
	InputSchema    json.RawMessage
	OutputSchema   json.RawMessage
	RequiredScopes []string
	Risk           ToolRisk
	Backend        ToolBackend
}

// ConfigUpgrader 把管理员配置从 FromVersion 升级到 FromVersion+1。
type ConfigUpgrader struct {
	FromVersion int
	Upgrade     func(public, secret map[string]any) (map[string]any, map[string]any, error)
}
```

- [ ] **Step 5: 运行确认通过**

```bash
cd packages/core && go test ./...
```

预期：`ok  github.com/memohai/connect-it/packages/core/connector`。

- [ ] **Step 6: 提交**

```bash
git add packages/core
git commit -m "feat(core): add connector definition types"
```

---

### Task 3: Registry 注册与校验

**Files:**
- Create: `packages/core/registry/registry.go`
- Test: `packages/core/registry/registry_test.go`

**Interfaces:**
- Consumes: Task 2 的`package connector`全部类型
- Produces:
  - `registry.New() *Registry`
  - `(*Registry).Register(def connector.Definition, managedHandlerKeys ...string) error`
  - `(*Registry).MustRegister(def connector.Definition, managedHandlerKeys ...string)`（失败 panic，启动时用）
  - `(*Registry).Get(t connector.Type) (connector.Definition, bool)`
  - `(*Registry).All() []connector.Definition`（按 Type 升序）

- [ ] **Step 1: 写失败测试**

`packages/core/registry/registry_test.go`：

```go
package registry_test

import (
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func strPtr(s string) *string { return &s }

// makeValid 返回一个覆盖全部特性的合法 Definition。
// 各用例在它基础上做单点破坏。
func makeValid() connector.Definition {
	return connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
			{Key: "mcp_url", Label: "MCP URL", InputType: connector.InputURL},
		},
		AuthMethods: []connector.AuthMethod{
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				UsePKCE:               true,
			}},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{
			{
				Key:        "main",
				Endpoint:   connector.Endpoint{Source: connector.EndpointFixed, URL: "https://mcp.example.com/mcp"},
				Provenance: connector.Provenance{Kind: connector.ProvenanceOfficial},
			},
			{
				Key:        "self",
				Endpoint:   connector.Endpoint{Source: connector.EndpointConfigField, ConfigFieldKey: "mcp_url"},
				Provenance: connector.Provenance{Kind: connector.ProvenanceSelfHosted},
			},
		},
		Tools: []connector.Tool{
			{ID: "list_items", Name: "List items", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "main", RemoteToolName: "list-items"}},
			{ID: "create_item", Name: "Create item", Risk: connector.RiskWrite,
				Backend: connector.ManagedBackend{HandlerKey: "create_item"}},
		},
	}
}

func TestRegisterValid(t *testing.T) {
	r := registry.New()
	if err := r.Register(makeValid(), "create_item"); err != nil {
		t.Fatalf("合法 definition 注册失败: %v", err)
	}
	got, ok := r.Get("example_app")
	if !ok || got.Name != "Example" {
		t.Fatalf("Get 失败: ok=%v got=%+v", ok, got)
	}
}

func TestRegisterDuplicateType(t *testing.T) {
	r := registry.New()
	if err := r.Register(makeValid(), "create_item"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(makeValid(), "create_item"); err == nil {
		t.Fatal("重复注册同一 type 应报错")
	}
}

func TestAllSorted(t *testing.T) {
	r := registry.New()
	b := makeValid()
	b.Type = "bbb"
	a := makeValid()
	a.Type = "aaa"
	if err := r.Register(b, "create_item"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(a, "create_item"); err != nil {
		t.Fatal(err)
	}
	all := r.All()
	if len(all) != 2 || all[0].Type != "aaa" || all[1].Type != "bbb" {
		t.Fatalf("All 应按 Type 升序: %+v", all)
	}
}

func TestValidationRules(t *testing.T) {
	cases := []struct {
		name        string
		mutate      func(*connector.Definition)
		handlerKeys []string
		wantErr     string
	}{
		{"type 非法字符", func(d *connector.Definition) { d.Type = "OneDrive" }, []string{"create_item"}, "type"},
		{"type 为空", func(d *connector.Definition) { d.Type = "" }, []string{"create_item"}, "type"},
		{"Name 为空", func(d *connector.Definition) { d.Name = "" }, []string{"create_item"}, "Name"},
		{"ConfigSchemaVersion 为 0", func(d *connector.Definition) { d.ConfigSchemaVersion = 0 }, []string{"create_item"}, "ConfigSchemaVersion"},
		{"配置字段 key 重复", func(d *connector.Definition) {
			d.ConfigFields = append(d.ConfigFields, connector.ConfigField{Key: "client_id", Label: "X", InputType: connector.InputText})
		}, []string{"create_item"}, "重复"},
		{"Secret 字段带默认值", func(d *connector.Definition) {
			d.ConfigFields[1].DefaultValue = strPtr("boom")
		}, []string{"create_item"}, "默认值"},
		{"auth method key 重复", func(d *connector.Definition) {
			d.AuthMethods = append(d.AuthMethods, d.AuthMethods[0])
		}, []string{"create_item"}, "重复"},
		{"oauth2 缺 OAuth 配置", func(d *connector.Definition) {
			d.AuthMethods[0].OAuth = nil
		}, []string{"create_item"}, "OAuth"},
		{"非 oauth2 带 OAuth 配置", func(d *connector.Definition) {
			d.AuthMethods[0].Type = connector.AuthAPIKey
		}, []string{"create_item"}, "OAuth"},
		{"MCP server key 重复", func(d *connector.Definition) {
			d.RemoteMCPServers = append(d.RemoteMCPServers, d.RemoteMCPServers[0])
		}, []string{"create_item"}, "重复"},
		{"固定 endpoint 非 https", func(d *connector.Definition) {
			d.RemoteMCPServers[0].Endpoint.URL = "http://mcp.example.com/mcp"
		}, []string{"create_item"}, "https"},
		{"配置 endpoint 非 self_hosted", func(d *connector.Definition) {
			d.RemoteMCPServers[1].Provenance.Kind = connector.ProvenanceOfficial
		}, []string{"create_item"}, "self_hosted"},
		{"配置 endpoint 引用不存在字段", func(d *connector.Definition) {
			d.RemoteMCPServers[1].Endpoint.ConfigFieldKey = "nope"
		}, []string{"create_item"}, "不存在"},
		{"tool ID 非法", func(d *connector.Definition) { d.Tools[0].ID = "List-Items" }, []string{"create_item"}, "tool"},
		{"tool ID 重复", func(d *connector.Definition) { d.Tools[1].ID = "list_items" }, []string{"create_item"}, "重复"},
		{"tool 引用不存在 server", func(d *connector.Definition) {
			d.Tools[0].Backend = connector.RemoteMCPBackend{ServerKey: "nope", RemoteToolName: "x"}
		}, []string{"create_item"}, "不存在"},
		{"remote tool 缺 RemoteToolName", func(d *connector.Definition) {
			d.Tools[0].Backend = connector.RemoteMCPBackend{ServerKey: "main"}
		}, []string{"create_item"}, "RemoteToolName"},
		{"managed handler 未注册", func(d *connector.Definition) {}, nil, "handler"},
		{"tool 缺 Backend", func(d *connector.Definition) { d.Tools[0].Backend = nil }, []string{"create_item"}, "Backend"},
		{"upgrader 版本越界", func(d *connector.Definition) {
			d.ConfigUpgraders = []connector.ConfigUpgrader{{FromVersion: 1}}
		}, []string{"create_item"}, "upgrader"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValid()
			tc.mutate(&def)
			err := registry.New().Register(def, tc.handlerKeys...)
			if err == nil {
				t.Fatal("应报错但通过了")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息 %q 不含 %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestMustRegisterPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister 对非法 definition 应 panic")
		}
	}()
	def := makeValid()
	def.Name = ""
	registry.New().MustRegister(def, "create_item")
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/core && go test ./registry/
```

预期：编译失败，`undefined: registry.New`。

- [ ] **Step 3: 写实现**

`packages/core/registry/registry.go`：

```go
// Package registry 保存全部 ConnectorDefinition 并在注册时校验。
package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"

	"github.com/memohai/connect-it/packages/core/connector"
)

var (
	typePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	toolIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

type Registry struct {
	defs        map[connector.Type]connector.Definition
	handlerKeys map[connector.Type]map[string]bool
}

func New() *Registry {
	return &Registry{
		defs:        map[connector.Type]connector.Definition{},
		handlerKeys: map[connector.Type]map[string]bool{},
	}
}

// Register 校验并登记一个 Definition。
// managedHandlerKeys 是该 Connector 在 managed.go 中注册的 handler key 集合，
// 用于校验 ManagedBackend 引用的 handler 确实存在。
func (r *Registry) Register(def connector.Definition, managedHandlerKeys ...string) error {
	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("connector %q: type 重复注册", def.Type)
	}
	keys := map[string]bool{}
	for _, k := range managedHandlerKeys {
		keys[k] = true
	}
	if err := validate(def, keys); err != nil {
		return err
	}
	r.defs[def.Type] = def
	r.handlerKeys[def.Type] = keys
	return nil
}

func (r *Registry) MustRegister(def connector.Definition, managedHandlerKeys ...string) {
	if err := r.Register(def, managedHandlerKeys...); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(t connector.Type) (connector.Definition, bool) {
	def, ok := r.defs[t]
	return def, ok
}

func (r *Registry) All() []connector.Definition {
	out := make([]connector.Definition, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func validate(def connector.Definition, handlerKeys map[string]bool) error {
	if !typePattern.MatchString(string(def.Type)) {
		return fmt.Errorf("connector %q: type 必须匹配 %s", def.Type, typePattern)
	}
	if def.Name == "" {
		return fmt.Errorf("connector %q: Name 不能为空", def.Type)
	}
	if def.ConfigSchemaVersion < 1 {
		return fmt.Errorf("connector %q: ConfigSchemaVersion 必须 >= 1", def.Type)
	}

	fieldKeys := map[string]bool{}
	for _, f := range def.ConfigFields {
		if f.Key == "" {
			return fmt.Errorf("connector %q: 配置字段 key 不能为空", def.Type)
		}
		if fieldKeys[f.Key] {
			return fmt.Errorf("connector %q: 配置字段 %q 重复", def.Type, f.Key)
		}
		fieldKeys[f.Key] = true
		if f.Secret && f.DefaultValue != nil {
			return fmt.Errorf("connector %q: Secret 字段 %q 不允许默认值", def.Type, f.Key)
		}
	}

	authKeys := map[string]bool{}
	for _, a := range def.AuthMethods {
		if a.Key == "" {
			return fmt.Errorf("connector %q: auth method key 不能为空", def.Type)
		}
		if authKeys[a.Key] {
			return fmt.Errorf("connector %q: auth method %q 重复", def.Type, a.Key)
		}
		authKeys[a.Key] = true
		if a.Type == connector.AuthOAuth2 && a.OAuth == nil {
			return fmt.Errorf("connector %q: auth method %q 是 oauth2 但缺少 OAuth 配置", def.Type, a.Key)
		}
		if a.Type != connector.AuthOAuth2 && a.OAuth != nil {
			return fmt.Errorf("connector %q: auth method %q 不是 oauth2 不允许携带 OAuth 配置", def.Type, a.Key)
		}
	}

	serverKeys := map[string]bool{}
	for _, s := range def.RemoteMCPServers {
		if s.Key == "" {
			return fmt.Errorf("connector %q: MCP server key 不能为空", def.Type)
		}
		if serverKeys[s.Key] {
			return fmt.Errorf("connector %q: MCP server %q 重复", def.Type, s.Key)
		}
		serverKeys[s.Key] = true
		switch s.Endpoint.Source {
		case connector.EndpointFixed:
			u, err := url.Parse(s.Endpoint.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("connector %q: MCP server %q 的固定 endpoint 必须是 https URL", def.Type, s.Key)
			}
		case connector.EndpointConfigField:
			if s.Provenance.Kind != connector.ProvenanceSelfHosted {
				return fmt.Errorf("connector %q: MCP server %q 从配置取 endpoint 仅允许 self_hosted", def.Type, s.Key)
			}
			if !fieldKeys[s.Endpoint.ConfigFieldKey] {
				return fmt.Errorf("connector %q: MCP server %q 引用的配置字段 %q 不存在", def.Type, s.Key, s.Endpoint.ConfigFieldKey)
			}
		default:
			return fmt.Errorf("connector %q: MCP server %q 的 Endpoint.Source 非法", def.Type, s.Key)
		}
	}

	toolIDs := map[string]bool{}
	for _, tl := range def.Tools {
		if !toolIDPattern.MatchString(tl.ID) {
			return fmt.Errorf("connector %q: tool ID %q 必须匹配 %s", def.Type, tl.ID, toolIDPattern)
		}
		if toolIDs[tl.ID] {
			return fmt.Errorf("connector %q: tool %q 重复", def.Type, tl.ID)
		}
		toolIDs[tl.ID] = true
		switch b := tl.Backend.(type) {
		case connector.RemoteMCPBackend:
			if !serverKeys[b.ServerKey] {
				return fmt.Errorf("connector %q: tool %q 引用的 MCP server %q 不存在", def.Type, tl.ID, b.ServerKey)
			}
			if b.RemoteToolName == "" {
				return fmt.Errorf("connector %q: tool %q 缺少 RemoteToolName", def.Type, tl.ID)
			}
		case connector.ManagedBackend:
			if !handlerKeys[b.HandlerKey] {
				return fmt.Errorf("connector %q: tool %q 引用的 managed handler %q 未注册", def.Type, tl.ID, b.HandlerKey)
			}
		case nil:
			return fmt.Errorf("connector %q: tool %q 缺少 Backend", def.Type, tl.ID)
		default:
			return fmt.Errorf("connector %q: tool %q 的 Backend 类型未知", def.Type, tl.ID)
		}
	}

	seenFrom := map[int]bool{}
	for _, up := range def.ConfigUpgraders {
		if up.FromVersion < 1 || up.FromVersion >= def.ConfigSchemaVersion {
			return fmt.Errorf("connector %q: upgrader FromVersion %d 越界", def.Type, up.FromVersion)
		}
		if seenFrom[up.FromVersion] {
			return fmt.Errorf("connector %q: upgrader FromVersion %d 重复", def.Type, up.FromVersion)
		}
		seenFrom[up.FromVersion] = true
	}
	return nil
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/core && go test ./...
```

预期：connector 与 registry 两个包都`ok`。

- [ ] **Step 5: 提交**

```bash
git add packages/core/registry
git commit -m "feat(core): add registry with definition validation"
```

---

### Task 4: crypto Keyring（AES-256-GCM＋版本轮换）

**Files:**
- Create: `packages/core/crypto/keyring.go`
- Test: `packages/core/crypto/keyring_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `crypto.EnvSecretKey = "CONNECT_IT_SECRET_KEY"`（环境变量名常量；值格式`"1:<64位hex>,2:<64位hex>"`，最大版本为当前写入 key）
  - `crypto.ParseKeyring(spec string) (*Keyring, error)`
  - `crypto.NewKeyring(keys map[int][]byte) (*Keyring, error)`
  - `(*Keyring).CurrentVersion() int`
  - `(*Keyring).Encrypt(plaintext, aad []byte) (ciphertext []byte, version int, err error)`（nonce 前置于密文）
  - `(*Keyring).Decrypt(ciphertext []byte, version int, aad []byte) ([]byte, error)`

- [ ] **Step 1: 写失败测试**

`packages/core/crypto/keyring_test.go`：

```go
package crypto_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/crypto"
)

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.ParseKeyring(
		"1:" + strings.Repeat("11", 32) + ",2:" + strings.Repeat("22", 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	k := testKeyring(t)
	aad := []byte("gmail")
	ct, ver, err := k.Encrypt([]byte(`{"client_secret":"x"}`), aad)
	if err != nil {
		t.Fatal(err)
	}
	if ver != 2 {
		t.Fatalf("应使用最大版本 2 加密, got %d", ver)
	}
	pt, err := k.Decrypt(ct, ver, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pt, []byte(`{"client_secret":"x"}`)) {
		t.Fatalf("roundtrip 失败: %s", pt)
	}
}

func TestDecryptWrongAADFails(t *testing.T) {
	k := testKeyring(t)
	ct, ver, _ := k.Encrypt([]byte("data"), []byte("gmail"))
	if _, err := k.Decrypt(ct, ver, []byte("github")); err == nil {
		t.Fatal("错误 AAD 应解密失败")
	}
}

func TestDecryptOldVersion(t *testing.T) {
	// 只有版本 1 的旧 keyring 加密的数据，新 keyring（1+2）仍能按版本 1 解密。
	old, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	ct, ver, _ := old.Encrypt([]byte("data"), []byte("a"))
	if ver != 1 {
		t.Fatalf("got %d", ver)
	}
	pt, err := testKeyring(t).Decrypt(ct, 1, []byte("a"))
	if err != nil || string(pt) != "data" {
		t.Fatalf("旧版本解密失败: %v %q", err, pt)
	}
}

func TestDecryptUnknownVersionFails(t *testing.T) {
	k := testKeyring(t)
	ct, _, _ := k.Encrypt([]byte("data"), nil)
	if _, err := k.Decrypt(ct, 9, nil); err == nil {
		t.Fatal("未知版本应报错")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	k := testKeyring(t)
	ct, ver, _ := k.Encrypt([]byte("data"), nil)
	ct[len(ct)-1] ^= 0xff
	if _, err := k.Decrypt(ct, ver, nil); err == nil {
		t.Fatal("被篡改的密文应解密失败")
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	// 无 Secret 字段的 Connector 存空配置，允许空明文。
	k := testKeyring(t)
	ct, ver, err := k.Encrypt(nil, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := k.Decrypt(ct, ver, []byte("x"))
	if err != nil || len(pt) != 0 {
		t.Fatalf("空明文 roundtrip 失败: %v %q", err, pt)
	}
}

func TestParseKeyringRejectsBadInput(t *testing.T) {
	for _, spec := range []string{
		"",                                  // 空
		"abc",                               // 无版本
		"1:zz",                              // 非 hex
		"1:" + strings.Repeat("11", 16),     // 长度不足 32 字节
		"0:" + strings.Repeat("11", 32),     // 版本 < 1
		"1:" + strings.Repeat("11", 32) + ",1:" + strings.Repeat("22", 32), // 版本重复
	} {
		if _, err := crypto.ParseKeyring(spec); err == nil {
			t.Fatalf("spec %q 应被拒绝", spec)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/core && go test ./crypto/
```

预期：编译失败，`undefined: crypto.ParseKeyring`。

- [ ] **Step 3: 写实现**

`packages/core/crypto/keyring.go`：

```go
// Package crypto 提供 Secret 配置与 credential 的 AES-256-GCM 加解密，
// 支持多版本 KEK：写入用最大版本，读取按存储的版本，实现平滑轮换。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// EnvSecretKey 的值格式："1:<64位hex>,2:<64位hex>"，最大版本为当前写入 key。
const EnvSecretKey = "CONNECT_IT_SECRET_KEY"

const keySize = 32

type Keyring struct {
	keys    map[int][]byte
	current int
}

func NewKeyring(keys map[int][]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("keyring: 至少需要一把 key")
	}
	current := 0
	for v, k := range keys {
		if v < 1 {
			return nil, fmt.Errorf("keyring: 非法版本 %d", v)
		}
		if len(k) != keySize {
			return nil, fmt.Errorf("keyring: 版本 %d 的 key 必须是 %d 字节", v, keySize)
		}
		if v > current {
			current = v
		}
	}
	return &Keyring{keys: keys, current: current}, nil
}

func ParseKeyring(spec string) (*Keyring, error) {
	keys := map[int][]byte{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		verStr, hexKey, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("keyring: 片段 %q 不是 version:hex 形式", part)
		}
		v, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("keyring: 版本 %q 不是整数", verStr)
		}
		raw, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("keyring: 版本 %d 的 key 不是合法 hex", v)
		}
		if _, dup := keys[v]; dup {
			return nil, fmt.Errorf("keyring: 版本 %d 重复", v)
		}
		keys[v] = raw
	}
	return NewKeyring(keys)
}

func (k *Keyring) CurrentVersion() int { return k.current }

func (k *Keyring) Encrypt(plaintext, aad []byte) ([]byte, int, error) {
	gcm, err := k.gcm(k.current)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), k.current, nil
}

func (k *Keyring) Decrypt(ciphertext []byte, version int, aad []byte) ([]byte, error) {
	gcm, err := k.gcm(version)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("keyring: 密文过短")
	}
	return gcm.Open(nil, ciphertext[:ns], ciphertext[ns:], aad)
}

func (k *Keyring) gcm(version int) (cipher.AEAD, error) {
	key, ok := k.keys[version]
	if !ok {
		return nil, fmt.Errorf("keyring: 未知 key 版本 %d", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/core && go test ./...
```

预期：三个包全部`ok`。

- [ ] **Step 5: 提交**

```bash
git add packages/core/crypto
git commit -m "feat(core): add AES-GCM keyring with key rotation"
```

---

### Task 5: status 状态机

**Files:**
- Create: `packages/core/status/status.go`
- Test: `packages/core/status/status_test.go`

**Interfaces:**
- Consumes: Task 2 的`connector.Definition`
- Produces:
  - `status.Status`常量：`CatalogOnly/NeedsConfig/ConfigIncompatible/Ready/Degraded/Deprecated/DefinitionMissing`（值为 spec 中的 snake_case 字符串）
  - `status.ConfigState{Exists bool, SchemaVersion int, PublicValues map[string]any, SecretKeysSet map[string]bool, MCPVerified bool, MCPVerifiedEndpoint string}`
  - `status.Health{ConsecutiveFailures int, LastErrorAt time.Time}`
  - `status.Compute(def *connector.Definition, cfg ConfigState, h Health, now time.Time) Status`

判定优先级（高到低）：`definition_missing`（def 为 nil）→`deprecated`→`catalog_only`→`config_incompatible`→`needs_config`→`degraded`→`ready`。

- [ ] **Step 1: 写失败测试**

`packages/core/status/status_test.go`：

```go
package status_test

import (
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/status"
)

var now = time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

func strPtr(s string) *string { return &s }

// makeDef 返回一个有 Tool、有必填字段（含 secret）、含 self_hosted MCP 的 definition。
func makeDef() connector.Definition {
	return connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 2,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Required: true, InputType: connector.InputText},
			{Key: "client_secret", Required: true, Secret: true, InputType: connector.InputText},
			{Key: "tenant", Required: true, InputType: connector.InputText, DefaultValue: strPtr("common")},
			{Key: "mcp_url", InputType: connector.InputURL},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{
			{Key: "self",
				Endpoint:   connector.Endpoint{Source: connector.EndpointConfigField, ConfigFieldKey: "mcp_url"},
				Provenance: connector.Provenance{Kind: connector.ProvenanceSelfHosted}},
		},
		Tools: []connector.Tool{
			{ID: "t", Backend: connector.ManagedBackend{HandlerKey: "t"}},
		},
	}
}

// fullConfig 返回满足 makeDef 全部要求的配置状态。
func fullConfig() status.ConfigState {
	return status.ConfigState{
		Exists:        true,
		SchemaVersion: 2,
		PublicValues:  map[string]any{"client_id": "abc", "mcp_url": "https://mcp.internal/mcp"},
		SecretKeysSet: map[string]bool{"client_secret": true},
		MCPVerified:         true,
		MCPVerifiedEndpoint: "https://mcp.internal/mcp",
	}
}

func TestCompute(t *testing.T) {
	cases := []struct {
		name string
		def  func() *connector.Definition
		cfg  func() status.ConfigState
		h    status.Health
		want status.Status
	}{
		{"definition_missing", func() *connector.Definition { return nil },
			fullConfig, status.Health{}, status.DefinitionMissing},
		{"deprecated 优先于其他", func() *connector.Definition {
			d := makeDef()
			d.Deprecated = true
			return &d
		}, fullConfig, status.Health{}, status.Deprecated},
		{"catalog_only", func() *connector.Definition {
			d := makeDef()
			d.Tools = nil
			return &d
		}, fullConfig, status.Health{}, status.CatalogOnly},
		{"config 比代码新", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.SchemaVersion = 3
			return c
		}, status.Health{}, status.ConfigIncompatible},
		{"缺必填公开字段", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			delete(c.PublicValues, "client_id")
			return c
		}, status.Health{}, status.NeedsConfig},
		{"缺必填 secret 字段", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.SecretKeysSet = nil
			return c
		}, status.Health{}, status.NeedsConfig},
		{"必填但有默认值不算缺", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig, status.Health{}, status.Ready}, // tenant 靠默认值补齐
		{"self_hosted endpoint 未验证", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.MCPVerified = false
			return c
		}, status.Health{}, status.NeedsConfig},
		{"验证后 endpoint 又被改动", func() *connector.Definition {
			d := makeDef()
			return &d
		}, func() status.ConfigState {
			c := fullConfig()
			c.PublicValues["mcp_url"] = "https://other.internal/mcp"
			return c
		}, status.Health{}, status.NeedsConfig},
		{"连续失败进入 degraded", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig,
			status.Health{ConsecutiveFailures: 3, LastErrorAt: now.Add(-5 * time.Minute)},
			status.Degraded},
		{"失败已过期回到 ready", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig,
			status.Health{ConsecutiveFailures: 5, LastErrorAt: now.Add(-16 * time.Minute)},
			status.Ready},
		{"ready", func() *connector.Definition {
			d := makeDef()
			return &d
		}, fullConfig, status.Health{}, status.Ready},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := status.Compute(tc.def(), tc.cfg(), tc.h, now)
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行确认失败**

```bash
cd packages/core && go test ./status/
```

预期：编译失败，`undefined: status.Compute`。

- [ ] **Step 3: 写实现**

`packages/core/status/status.go`：

```go
// Package status 由 Definition＋配置状态＋健康数据实时计算 Connector 运行状态。
// 纯函数，无 I/O。
package status

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

type Status string

const (
	CatalogOnly        Status = "catalog_only"
	NeedsConfig        Status = "needs_config"
	ConfigIncompatible Status = "config_incompatible"
	Ready              Status = "ready"
	Degraded           Status = "degraded"
	Deprecated         Status = "deprecated"
	DefinitionMissing  Status = "definition_missing"
)

const (
	degradedFailureThreshold = 3
	degradedWindow           = 15 * time.Minute
)

// ConfigState 是 connector_configs 行的内存视图，不含 secret 明文。
type ConfigState struct {
	Exists        bool
	SchemaVersion int
	PublicValues  map[string]any
	// SecretKeysSet 记录哪些 Secret 字段已被设置（值不出库）。
	SecretKeysSet map[string]bool
	// MCPVerified / MCPVerifiedEndpoint 对应 mcp:verify 的结果。
	MCPVerified         bool
	MCPVerifiedEndpoint string
}

// Health 是 connector_health 行的内存视图。
type Health struct {
	ConsecutiveFailures int
	LastErrorAt         time.Time
}

func Compute(def *connector.Definition, cfg ConfigState, h Health, now time.Time) Status {
	if def == nil {
		return DefinitionMissing
	}
	if def.Deprecated {
		return Deprecated
	}
	if len(def.Tools) == 0 {
		return CatalogOnly
	}
	if cfg.Exists && cfg.SchemaVersion > def.ConfigSchemaVersion {
		return ConfigIncompatible
	}
	if missingRequired(def, cfg) || selfHostedUnverified(def, cfg) {
		return NeedsConfig
	}
	if h.ConsecutiveFailures >= degradedFailureThreshold &&
		now.Sub(h.LastErrorAt) <= degradedWindow {
		return Degraded
	}
	return Ready
}

func missingRequired(def *connector.Definition, cfg ConfigState) bool {
	for _, f := range def.ConfigFields {
		if !f.Required {
			continue
		}
		if f.Secret {
			if !cfg.SecretKeysSet[f.Key] {
				return true
			}
			continue
		}
		if f.DefaultValue != nil {
			continue // 默认值兜底，永不缺失
		}
		v, ok := cfg.PublicValues[f.Key]
		if !ok {
			return true
		}
		if s, isStr := v.(string); isStr && s == "" {
			return true
		}
	}
	return false
}

func selfHostedUnverified(def *connector.Definition, cfg ConfigState) bool {
	for _, s := range def.RemoteMCPServers {
		if s.Endpoint.Source != connector.EndpointConfigField {
			continue
		}
		current, _ := cfg.PublicValues[s.Endpoint.ConfigFieldKey].(string)
		if current == "" {
			return true // endpoint 未填也算未就绪
		}
		if !cfg.MCPVerified || cfg.MCPVerifiedEndpoint != current {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 运行确认通过**

```bash
cd packages/core && go test ./...
```

预期：四个包全部`ok`。

- [ ] **Step 5: 全模块验证＋提交**

```bash
mise run test
mise run vet
git add packages/core/status
git commit -m "feat(core): add connector status computation"
```

预期：`mise run test`对`packages/core`输出各包`ok`。

---

### Task 6: connectors module 与 catalog-only GitHub 注册

**Files:**
- Create: `packages/connectors/go.mod`
- Create: `packages/connectors/github/definition.go`
- Create: `packages/connectors/all.go`
- Test: `packages/connectors/all_test.go`

**Interfaces:**
- Consumes: Task 2 的`connector.Definition`、Task 3 的`registry.Registry`
- Produces:
  - `connectors.RegisterAll(r *registry.Registry)`——api module（计划 2）启动时调用的唯一入口
  - `github.Definition`（catalog-only，后续计划 6 扩充 Tools／AuthMethods）

- [ ] **Step 1: 初始化 module（含 replace）**

```bash
mkdir -p packages/connectors/github
```

写入`packages/connectors/go.mod`：

```text
module github.com/memohai/connect-it/packages/connectors

go 1.25

require github.com/memohai/connect-it/packages/core v0.0.0

replace github.com/memohai/connect-it/packages/core => ../core
```

- [ ] **Step 2: 写失败测试**

`packages/connectors/all_test.go`：

```go
package connectors_test

import (
	"os"
	"strings"
	"testing"

	connectors "github.com/memohai/connect-it/packages/connectors"
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
	if len(def.Tools) != 0 {
		t.Fatal("计划 1 阶段 github 应为 catalog_only（无 Tools）")
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

- [ ] **Step 3: 运行确认失败**

```bash
cd packages/connectors && go mod tidy && go test ./...
```

预期：编译失败，`undefined: connectors.RegisterAll`。

- [ ] **Step 4: 写实现**

`packages/connectors/github/definition.go`：

```go
// Package github 是 GitHub Connector 的固定 Definition。
// 计划 1 阶段为 catalog-only：无 AuthMethods、无 Tools，仅进入 catalog。
package github

import "github.com/memohai/connect-it/packages/core/connector"

var Definition = connector.Definition{
	Type:                "github",
	Name:                "GitHub",
	Description:         "GitHub 代码托管与协作平台",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://github.com",
	ConfigSchemaVersion: 1,
}
```

`packages/connectors/all.go`：

```go
// Package connectors 显式注册全部 provider 的 Definition。
// 新增 Connector：加目录、加 definition.go、在这里加一行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/core/registry"
)

func RegisterAll(r *registry.Registry) {
	r.MustRegister(github.Definition)
}
```

- [ ] **Step 5: 运行确认通过**

```bash
cd packages/connectors && go test ./...
```

预期：`ok  github.com/memohai/connect-it/packages/connectors`。

- [ ] **Step 6: 全仓库验证＋提交**

```bash
mise run test
mise run vet
git add packages/connectors
git commit -m "feat(connectors): add module with catalog-only github definition"
```

预期：`mise run test`依次进入`packages/connectors`、`packages/core`且全部`ok`（注意循环按目录字母序，connectors 在 core 之前）。

---

## 完成标准（对照 spec）

- [ ] `mise install`后，`mise run test`与`mise run vet`全绿；
- [ ] Registry 拒绝：重复 type／key／ID、Secret 默认值、非 https 固定 endpoint、非 self_hosted 的配置 endpoint、悬空的 server／handler 引用（spec §5 校验清单）；
- [ ] Keyring 满足 AAD 绑定与多版本轮换（spec §8）；
- [ ] `status.Compute`覆盖七个状态与优先级（spec §9）；
- [ ] `packages/ui`submodule 就位，pnpm workspace 配置存在（spec §4）；
- [ ] 无 go.work；connectors 通过 replace 引用 core，目录映射测试通过。
