package connectors_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/registry"
)

func newRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	runtime, err := connectors.NewRuntime(providerkit.NewFactory())
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	r := registry.New()
	connectors.RegisterAll(r, runtime) // 非法 definition 会 panic，测试即失败
	return r
}

func TestRegisterAll(t *testing.T) {
	r := newRegistry(t)
	for _, typ := range []connector.Type{
		"github",
		"gitlab",
		"gmail",
		"one_drive",
		"google_ads",
		"datadog",
		"linear",
		"notion",
		"stripe",
	} {
		if _, ok := r.Get(typ); !ok {
			t.Errorf("%s 未注册", typ)
		}
	}
	if len(r.All()) != 9 {
		t.Fatalf("应注册 9 个 connector: %d", len(r.All()))
	}
}

func TestOAuthTokenScopeSeparators(t *testing.T) {
	r := newRegistry(t)
	for _, def := range r.All() {
		for _, method := range def.AuthMethods {
			if method.Type != connector.AuthOAuth2 {
				continue
			}
			want := connector.OAuthScopeSpace
			if def.Type == "github" {
				want = connector.OAuthScopeComma
			}
			if got := method.OAuth.EffectiveTokenScopeSeparator(); got != want {
				t.Errorf(
					"%s/%s token scope separator = %q, want %q",
					def.Type,
					method.Key,
					got,
					want,
				)
			}
		}
	}
}

func TestNewRuntime(t *testing.T) {
	if _, err := connectors.NewRuntime(nil); err == nil {
		t.Fatal("nil providerkit factory 应失败")
	}
	runtime, err := connectors.NewRuntime(providerkit.NewFactory())
	if err != nil {
		t.Fatal(err)
	}
	// 只剩 gmail / one_drive / datadog 还有 managed handler；gitlab、linear、
	// notion、stripe 已整体改写为 Remote MCP，不再装配任何 handler。
	if runtime.Handlers["gmail"] == nil ||
		runtime.Handlers["one_drive"] == nil ||
		runtime.Handlers["datadog"] == nil {
		t.Fatalf("现有 managed handlers 未装配: %+v", runtime.Handlers)
	}
	for _, typ := range []connector.Type{"gitlab", "linear", "notion", "stripe"} {
		if runtime.Handlers[typ] != nil {
			t.Errorf("%s 已是纯 Remote MCP，不应装配 managed handler", typ)
		}
	}
	if runtime.Authorization.CredentialValidators == nil ||
		runtime.Authorization.ScopeMatchers == nil {
		t.Fatal("已装配的 runtime maps 不能为空")
	}
	for typ, keys := range map[connector.Type][]string{
		"github":     {"oauth", "pat"},
		"gitlab":     {"access_token"},
		"gmail":      {"oauth"},
		"one_drive":  {"oauth"},
		"google_ads": {"oauth"},
		"datadog":    {"api_keys"},
		"linear":     {"oauth", "api_key"},
		"notion":     {"oauth"},
		"stripe":     {"api_key"},
	} {
		for _, key := range keys {
			if runtime.Authorization.CredentialValidators[typ][key] == nil {
				t.Errorf(
					"%s/%s credential validator 未装配",
					typ,
					key,
				)
			}
		}
	}
	if runtime.Authorization.ScopeMatchers["github"]["oauth"] == nil ||
		runtime.Authorization.ScopeMatchers["github"]["pat"] == nil {
		t.Fatal("GitHub scope matcher 未装配")
	}

	t.Run("orphan handler type fails closed", func(t *testing.T) {
		bad := runtime
		bad.Handlers = map[connector.Type]connector.HandlerMap{
			"unknown": {"orphan": func(
				context.Context,
				connector.ToolCallContext,
			) (connector.ToolResultData, error) {
				return connector.ToolResultData{}, nil
			}},
		}
		defer func() {
			if recover() == nil {
				t.Fatal("没有 Definition 的 runtime handler 应 panic")
			}
		}()
		connectors.RegisterAll(registry.New(), bad)
	})

	t.Run("orphan scope matcher key fails closed", func(t *testing.T) {
		bad := runtime
		bad.Authorization.ScopeMatchers = map[connector.Type]map[string]connector.ScopeMatcher{
			"github": {"missing": func([]string, string) bool { return true }},
		}
		defer func() {
			if recover() == nil {
				t.Fatal("没有对应 AuthMethod 的 scope matcher 应 panic")
			}
		}()
		connectors.RegisterAll(registry.New(), bad)
	})

	t.Run("missing validator fails before registration", func(t *testing.T) {
		bad := runtime
		bad.Authorization.CredentialValidators = cloneValidators(
			runtime.Authorization.CredentialValidators,
		)
		delete(bad.Authorization.CredentialValidators["github"], "oauth")
		reg := registry.New()
		panicked := false
		func() {
			defer func() {
				panicked = recover() != nil
			}()
			connectors.RegisterAll(reg, bad)
		}()
		if !panicked {
			t.Fatal("缺少 validator 应 panic")
		}
		if len(reg.All()) != 0 {
			t.Fatal("runtime completeness 失败后不应部分注册 Definition")
		}
	})
}

func cloneValidators(
	input connector.CredentialValidatorMap,
) connector.CredentialValidatorMap {
	out := make(connector.CredentialValidatorMap, len(input))
	for typ, validators := range input {
		out[typ] = make(map[string]connector.CredentialValidator, len(validators))
		for key, validator := range validators {
			out[typ][key] = validator
		}
	}
	return out
}

// 目录名必须等于 connector_type 去掉下划线的形式，且一一对应。
func TestDirectoryMatchesRegisteredTypes(t *testing.T) {
	r := newRegistry(t)

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
		if e.IsDir() && e.Name() != "internal" {
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

// 全部 tool：InputSchema 合法 JSON object；RemoteMCPBackend 的 mapper key 留空（第一期契约）。
func TestToolSchemasAndMapperKeys(t *testing.T) {
	r := newRegistry(t)
	for _, def := range r.All() {
		for _, tool := range def.Tools {
			var schema map[string]any
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Errorf("%s/%s InputSchema 不是合法 JSON: %v", def.Type, tool.ID, err)
				continue
			}
			if schema["type"] != "object" {
				t.Errorf("%s/%s InputSchema 顶层应为 object", def.Type, tool.ID)
			}
			if b, ok := tool.Backend.(connector.RemoteMCPBackend); ok {
				if b.InputMapperKey != "" || b.OutputMapperKey != "" {
					t.Errorf("%s/%s 的 mapper key 第一期必须留空", def.Type, tool.ID)
				}
			}
		}
	}
}

// remoteMCPContract 是一台 Remote MCP server 的评审身份：凭据被发往哪里、
// 由谁提供、以什么方式绑定、暴露哪些上游 tool。Remote MCP 是主路径，这些
// 结论必须钉在仓库里；新增 remote provider 只需在下表加一行，provider 目录
// 下不必再写一份 definition_test.go。
//
// EndpointFixed（厂商官方 endpoint）与 EndpointConfigField + ProvenanceSelfHosted
// （endpoint 由运营方填写，如 Google Ads / Jenkins / Terraform 这类自托管产品）
// 是两条并列的路径，同一张表都要能表达。
type remoteMCPContract struct {
	source           connector.EndpointSource
	url              string // source == EndpointFixed
	configFieldKey   string // source == EndpointConfigField
	provenance       connector.ProvenanceKind
	allowedHostnames []string
	authScheme       string
	credentialFields map[string]string // auth method key -> credential 字段名
	requestTimeout   time.Duration
	tools            map[string]string // tool ID -> 上游 tool 名
}

var remoteMCPContracts = map[connector.Type]map[string]remoteMCPContract{
	"github": {
		"official": {
			source:           connector.EndpointFixed,
			url:              "https://api.githubcopilot.com/mcp/",
			provenance:       connector.ProvenanceOfficial,
			allowedHostnames: []string{"api.githubcopilot.com"},
			authScheme:       "bearer",
			// OAuth 用 typed access token，只有 PAT 需要显式字段绑定。
			credentialFields: map[string]string{"pat": "token"},
			requestTimeout:   30 * time.Second,
			tools: map[string]string{
				"get_me":              "get_me",
				"search_repositories": "search_repositories",
				"get_file_contents":   "get_file_contents",
				"list_issues":         "list_issues",
				"issue_write":         "issue_write",
			},
		},
	},
	"gitlab": {
		// GitLab 自托管：endpoint 由运营方填 mcp_url，凭据是带 mcp scope 的
		// OAuth access token。
		"instance": {
			source:           connector.EndpointConfigField,
			configFieldKey:   "mcp_url",
			provenance:       connector.ProvenanceSelfHosted,
			authScheme:       "bearer",
			credentialFields: map[string]string{"access_token": "token"},
			requestTimeout:   30 * time.Second,
			tools: map[string]string{
				"search":                    "search",
				"search_labels":             "search_labels",
				"get_issue":                 "get_issue",
				"create_issue":              "create_issue",
				"get_merge_request":         "get_merge_request",
				"create_merge_request":      "create_merge_request",
				"get_merge_request_diffs":   "get_merge_request_diffs",
				"get_merge_request_notes":   "get_merge_request_notes",
				"create_merge_request_note": "create_merge_request_note",
				"get_workitem_notes":        "get_workitem_notes",
				"create_workitem_note":      "create_workitem_note",
				"link_work_items":           "link_work_items",
				"get_pipeline_jobs":         "get_pipeline_jobs",
				"get_job_log":               "get_job_log",
			},
		},
	},
	"gmail": {
		"official": {
			source:           connector.EndpointFixed,
			url:              "https://gmailmcp.googleapis.com/mcp/v1",
			provenance:       connector.ProvenanceOfficial,
			allowedHostnames: []string{"gmailmcp.googleapis.com"},
			authScheme:       "bearer",
			requestTimeout:   30 * time.Second,
			tools: map[string]string{
				"search_threads": "search_threads",
				"create_draft":   "create_draft",
			},
		},
	},
	"linear": {
		"official": {
			source:           connector.EndpointFixed,
			url:              "https://mcp.linear.app/mcp",
			provenance:       connector.ProvenanceOfficial,
			allowedHostnames: []string{"mcp.linear.app"},
			authScheme:       "bearer",
			// OAuth 用 typed access token，只有 personal API key 需要字段绑定。
			credentialFields: map[string]string{"api_key": "api_key"},
			requestTimeout:   30 * time.Second,
			tools: map[string]string{
				"list_issues":         "list_issues",
				"get_issue":           "get_issue",
				"create_issue":        "create_issue",
				"update_issue":        "update_issue",
				"list_comments":       "list_comments",
				"create_comment":      "create_comment",
				"list_projects":       "list_projects",
				"list_teams":          "list_teams",
				"list_users":          "list_users",
				"list_issue_labels":   "list_issue_labels",
				"list_issue_statuses": "list_issue_statuses",
				"list_cycles":         "list_cycles",
			},
		},
	},
	"notion": {
		"official": {
			source:           connector.EndpointFixed,
			url:              "https://mcp.notion.com/mcp",
			provenance:       connector.ProvenanceOfficial,
			allowedHostnames: []string{"mcp.notion.com"},
			authScheme:       "bearer",
			// 只有 OAuth 一种 auth method，access token 直接作为 bearer。
			requestTimeout: 60 * time.Second,
			tools: map[string]string{
				"search":             "notion-search",
				"fetch":              "notion-fetch",
				"query_data_sources": "notion-query-data-sources",
				"get_users":          "notion-get-users",
				"get_teams":          "notion-get-teams",
				"get_comments":       "notion-get-comments",
				"create_pages":       "notion-create-pages",
				"update_page":        "notion-update-page",
				"create_database":    "notion-create-database",
				"create_comment":     "notion-create-comment",
			},
		},
	},
	"stripe": {
		"official": {
			source:           connector.EndpointFixed,
			url:              "https://mcp.stripe.com",
			provenance:       connector.ProvenanceOfficial,
			allowedHostnames: []string{"mcp.stripe.com"},
			authScheme:       "bearer",
			credentialFields: map[string]string{"api_key": "api_key"},
			requestTimeout:   30 * time.Second,
			tools: map[string]string{
				"get_stripe_account_info": "get_stripe_account_info",
				"get_balance_summary":     "get_balance_summary",
				"search_stripe_resources": "search_stripe_resources",
				"fetch_stripe_resources":  "fetch_stripe_resources",
				"create_refund":           "create_refund",
			},
		},
	},
	"google_ads": {
		"self_hosted": {
			source:         connector.EndpointConfigField,
			configFieldKey: "mcp_url",
			provenance:     connector.ProvenanceSelfHosted,
			authScheme:     "bearer",
			// GAQL 查询可能较慢，超时比官方 endpoint 长。
			requestTimeout: 60 * time.Second,
			tools: map[string]string{
				"list_accessible_customers": "list_accessible_customers",
				"search":                    "search",
			},
		},
	},
}

func TestRemoteMCPContracts(t *testing.T) {
	seen := map[connector.Type]map[string]bool{}
	for _, def := range newRegistry(t).All() {
		// serverKey -> tool ID -> 上游 tool 名
		byServer := map[string]map[string]string{}
		for _, tool := range def.Tools {
			backend, ok := tool.Backend.(connector.RemoteMCPBackend)
			if !ok {
				continue
			}
			if backend.RemoteToolName == "" {
				t.Errorf("%s/%s 缺少 RemoteToolName", def.Type, tool.ID)
			}
			if byServer[backend.ServerKey] == nil {
				byServer[backend.ServerKey] = map[string]string{}
			}
			byServer[backend.ServerKey][tool.ID] = backend.RemoteToolName
		}

		for _, server := range def.RemoteMCPServers {
			want, ok := remoteMCPContracts[def.Type][server.Key]
			if !ok {
				t.Errorf(
					"%s 的 remote MCP server %q 未在期望表登记",
					def.Type,
					server.Key,
				)
				continue
			}
			if seen[def.Type] == nil {
				seen[def.Type] = map[string]bool{}
			}
			seen[def.Type][server.Key] = true
			got := remoteMCPContract{
				source:           server.Endpoint.Source,
				url:              server.Endpoint.URL,
				configFieldKey:   server.Endpoint.ConfigFieldKey,
				provenance:       server.Provenance.Kind,
				allowedHostnames: server.Provenance.AllowedHostnames,
				authScheme:       server.AuthBinding.Scheme,
				credentialFields: server.AuthBinding.CredentialFieldByAuthMethod,
				requestTimeout:   server.RequestTimeout,
				tools:            byServer[server.Key],
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf(
					"%s/%s remote MCP 契约 = %+v, want %+v",
					def.Type,
					server.Key,
					got,
					want,
				)
			}
			delete(byServer, server.Key)
		}
		// Registry 注册时已挡住悬空 ServerKey，这里兜底并给出定位信息。
		for key := range byServer {
			t.Errorf("%s: tool 引用了不存在的 MCP server %q", def.Type, key)
		}
	}

	for typ, servers := range remoteMCPContracts {
		for key := range servers {
			if !seen[typ][key] {
				t.Errorf("期望表里的 %s/%s 已不存在，请删除该行", typ, key)
			}
		}
	}
}

// 每个固定 endpoint 都必须能构造出 providerkit 的静态出网策略：https、主机名
// 在 Provenance 允许列表内、origin 可规范化且非私网。启动时 mcpclient 的预检
// 会 fatal，但那是运行时；这里在测试期就给出同样的信号。
//
// EndpointConfigField 的 endpoint 由运营方填写，只能在连接配置落库时校验。
func TestRemoteMCPFixedEndpointsSatisfyEgressPolicy(t *testing.T) {
	for _, def := range newRegistry(t).All() {
		for _, server := range def.RemoteMCPServers {
			if server.Endpoint.Source != connector.EndpointFixed {
				continue
			}
			label := string(def.Type) + "/" + server.Key
			parsed, err := providerkit.ParseAndValidateURL(server.Endpoint.URL)
			if err != nil || parsed.Scheme != "https" {
				t.Errorf("%s endpoint 不是合法 https URL: %v", label, err)
				continue
			}
			if !slices.ContainsFunc(
				server.Provenance.AllowedHostnames,
				func(hostname string) bool {
					return strings.EqualFold(hostname, parsed.Hostname())
				},
			) {
				t.Errorf("%s endpoint 主机名不在 Provenance.AllowedHostnames 内", label)
				continue
			}
			origin, err := providerkit.CanonicalOrigin(parsed)
			if err != nil {
				t.Errorf("%s endpoint origin 无法规范化: %v", label, err)
				continue
			}
			if _, err := providerkit.NormalizePolicy(providerkit.Policy{
				Provider:       string(def.Type),
				BaseURL:        server.Endpoint.URL,
				AllowedOrigins: []string{origin},
				NetworkMode:    providerkit.PublicOnly,
				RequestTimeout: server.RequestTimeout,
			}); err != nil {
				t.Errorf("%s endpoint 不满足 providerkit 出网策略: %v", label, err)
			}
		}
	}
}

// Managed tool 的 input/output schema 是本服务自己的契约（参数由 handler 消费，
// 结构化输出由 handler 产出），必须封闭：显式约束值类型、object 拒绝未声明属性、
// array 声明 items。规则实现在 registry.LintClosedSchema，这里对全部已注册
// Provider 生效，新增 Provider 自动纳入。
//
// Remote MCP tool 不在此列：它的 schema 以上游 server 为准，参数直通。
//
// gmail / one_drive 的 managed tool 目前把上游响应体原样透传，其 output schema
// 只描述了部分字段；封闭它们会让真实调用被判成 invalid response，正确修法是像
// datadog 那样加 output projector。技术债显式列在这里，新增 tool 默认必须封闭。
var openManagedOutputSchemaDebt = map[string]bool{
	"gmail.list_messages":        true,
	"gmail.send_message":         true,
	"one_drive.list_drive_items": true,
	"one_drive.upload_file":      true,
}

func TestManagedToolSchemasAreClosed(t *testing.T) {
	for _, def := range newRegistry(t).All() {
		for _, tool := range def.Tools {
			if _, managed := tool.Backend.(connector.ManagedBackend); !managed {
				continue
			}
			label := string(def.Type) + "." + tool.ID
			if err := registry.LintClosedSchema(
				tool.InputSchema,
				label+".input",
			); err != nil {
				t.Errorf("input schema 不是封闭契约: %v", err)
			}
			if len(tool.OutputSchema) == 0 {
				t.Errorf("%s: managed tool 缺少 output schema", label)
				continue
			}
			err := registry.LintClosedSchema(tool.OutputSchema, label+".output")
			if openManagedOutputSchemaDebt[label] {
				if err == nil {
					t.Errorf("%s: output schema 已封闭，请移出技术债列表", label)
				}
				continue
			}
			if err != nil {
				t.Errorf("output schema 不是封闭契约: %v", err)
			}
		}
	}
}

func TestManagedRuntimeBundles(t *testing.T) {
	r := newRegistry(t)
	runtime, err := connectors.NewRuntime(providerkit.NewFactory())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		typ             connector.Type
		toolCount       int
		authKey         string
		authType        connector.AuthMethodType
		hasScopeMatcher bool
	}{
		{"datadog", 5, "api_keys", connector.AuthCustomCredential, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.typ), func(t *testing.T) {
			def, ok := r.Get(tc.typ)
			if !ok {
				t.Fatalf("%s Definition 未注册", tc.typ)
			}
			if len(def.Tools) != tc.toolCount ||
				len(def.AuthMethods) != 1 ||
				def.AuthMethods[0].Key != tc.authKey ||
				def.AuthMethods[0].Type != tc.authType {
				t.Fatalf(
					"Definition 合同不完整: tools=%d auth=%+v",
					len(def.Tools),
					def.AuthMethods,
				)
			}
			handlers := runtime.Handlers[tc.typ]
			for _, tool := range def.Tools {
				backend, ok := tool.Backend.(connector.ManagedBackend)
				if !ok || handlers[backend.HandlerKey] == nil {
					t.Fatalf("tool %q 未装配 managed handler", tool.ID)
				}
			}
			if len(handlers) != tc.toolCount {
				t.Fatalf(
					"managed handlers = %d, want %d",
					len(handlers),
					tc.toolCount,
				)
			}
			if runtime.Authorization.CredentialValidators[tc.typ][tc.authKey] == nil {
				t.Fatalf("%s credential validator 未装配", tc.authKey)
			}
			matcher := runtime.Authorization.ScopeMatchers[tc.typ][tc.authKey]
			if (matcher != nil) != tc.hasScopeMatcher {
				t.Fatalf(
					"scope matcher present = %t, want %t",
					matcher != nil,
					tc.hasScopeMatcher,
				)
			}
		})
	}
}
