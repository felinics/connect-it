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
