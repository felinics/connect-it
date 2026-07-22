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

func newRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	connectors.RegisterAll(r) // 非法 definition 会 panic，测试即失败
	return r
}

func TestRegisterAll(t *testing.T) {
	r := newRegistry(t)
	for _, typ := range []connector.Type{"github", "gmail", "one_drive", "google_ads"} {
		if _, ok := r.Get(typ); !ok {
			t.Errorf("%s 未注册", typ)
		}
	}
	if len(r.All()) != 4 {
		t.Fatalf("应注册 4 个 connector: %d", len(r.All()))
	}
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

func TestGitHubDefinition(t *testing.T) {
	r := newRegistry(t)
	def, _ := r.Get("github")

	methods := map[string]connector.AuthMethodType{}
	for _, m := range def.AuthMethods {
		methods[m.Key] = m.Type
	}
	if methods["oauth"] != connector.AuthOAuth2 || methods["pat"] != connector.AuthAPIKey {
		t.Fatalf("github 应有 oauth+pat 双 auth method: %+v", methods)
	}
	// OAuth 配置字段全部可选：PAT 路径独立可用，github 无配置也是 ready
	for _, f := range def.ConfigFields {
		if f.Required {
			t.Errorf("github 配置字段 %s 应为可选", f.Key)
		}
	}
	if len(def.RemoteMCPServers) != 1 ||
		def.RemoteMCPServers[0].Endpoint.URL != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("github 应指向官方 remote MCP: %+v", def.RemoteMCPServers)
	}
	if len(def.Tools) != 5 {
		t.Fatalf("github 应有 5 个 tool: %d", len(def.Tools))
	}
	for _, tool := range def.Tools {
		if _, ok := tool.Backend.(connector.RemoteMCPBackend); !ok {
			t.Errorf("github tool %s 应为 RemoteMCPBackend", tool.ID)
		}
	}
}

func TestGmailDefinition(t *testing.T) {
	r := newRegistry(t)
	def, _ := r.Get("gmail")

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

	// 混合 backend：至少 1 个 remote＋2 个 managed
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
		t.Fatalf("gmail 应混合 remote(%d)+managed(%d)", remote, managed)
	}

	h := connectors.AllHandlers()["gmail"]
	for _, key := range []string{"list_messages", "send_message"} {
		if h[key] == nil {
			t.Errorf("gmail managed handler %q 未接线", key)
		}
	}
	// refresh token 参数
	extra := def.AuthMethods[0].OAuth.ExtraAuthParams
	if extra["access_type"] != "offline" || extra["prompt"] != "consent" {
		t.Fatalf("google oauth 需 access_type=offline&prompt=consent: %v", extra)
	}
}

func TestOneDriveDefinition(t *testing.T) {
	r := newRegistry(t)
	def, _ := r.Get("one_drive")

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

func TestGoogleAdsDefinition(t *testing.T) {
	r := newRegistry(t)
	def, _ := r.Get("google_ads")

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
