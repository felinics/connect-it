package registry_test

import (
	"encoding/json"
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
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://example.com:443"},
					TokenOrigins:         []string{"https://example.com:443"},
				},
				UsePKCE: true,
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
				InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
				Backend:     connector.RemoteMCPBackend{ServerKey: "main", RemoteToolName: "list-items"}},
			{ID: "create_item", Name: "Create item", Risk: connector.RiskWrite,
				InputSchema:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`),
				OutputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
				Backend:      connector.ManagedBackend{HandlerKey: "create_item"}},
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

func TestRegisterRejectsInvalidToolPolicy(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*connector.Tool)
		wantErr string
	}{
		{
			name: "missing risk",
			mutate: func(tool *connector.Tool) {
				tool.Risk = ""
			},
			wantErr: "Risk",
		},
		{
			name: "unknown risk",
			mutate: func(tool *connector.Tool) {
				tool.Risk = "admin"
			},
			wantErr: "Risk",
		},
		{
			name: "empty scope",
			mutate: func(tool *connector.Tool) {
				tool.RequiredScopes = []string{""}
			},
			wantErr: "RequiredScope",
		},
		{
			name: "whitespace scope",
			mutate: func(tool *connector.Tool) {
				tool.RequiredScopes = []string{" read "}
			},
			wantErr: "RequiredScope",
		},
		{
			name: "control character scope",
			mutate: func(tool *connector.Tool) {
				tool.RequiredScopes = []string{"read\nwrite"}
			},
			wantErr: "RequiredScope",
		},
		{
			name: "invalid UTF-8 scope",
			mutate: func(tool *connector.Tool) {
				tool.RequiredScopes = []string{string([]byte{0xff})}
			},
			wantErr: "RequiredScope",
		},
		{
			name: "oversized scope",
			mutate: func(tool *connector.Tool) {
				tool.RequiredScopes = []string{strings.Repeat("s", 4097)}
			},
			wantErr: "RequiredScope",
		},
		{
			name: "duplicate scope",
			mutate: func(tool *connector.Tool) {
				tool.RequiredScopes = []string{"read", "read"}
			},
			wantErr: "重复",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			def := makeValid()
			test.mutate(&def.Tools[0])
			err := registry.New().Register(def, "create_item")
			if err == nil {
				t.Fatal("非法 Tool policy 应注册失败")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("错误信息 %q 不含 %q", err.Error(), test.wantErr)
			}
		})
	}

	for _, risk := range []connector.ToolRisk{
		connector.RiskRead,
		connector.RiskWrite,
		connector.RiskDestructive,
	} {
		t.Run("valid risk/"+string(risk), func(t *testing.T) {
			def := makeValid()
			def.Tools[0].Risk = risk
			def.Tools[0].RequiredScopes = []string{
				"https://provider.example/scope",
				"resource:read",
			}
			if err := registry.New().Register(
				def,
				"create_item",
			); err != nil {
				t.Fatalf("合法 Tool policy 注册失败: %v", err)
			}
		})
	}
}

func TestRegisterOAuthTokenScopeSeparator(t *testing.T) {
	for _, separator := range []connector.OAuthScopeSeparator{
		"",
		connector.OAuthScopeSpace,
		connector.OAuthScopeComma,
	} {
		t.Run(string(separator), func(t *testing.T) {
			def := makeValid()
			def.AuthMethods[0].OAuth.TokenScopeSeparator = separator
			if err := registry.New().Register(def, "create_item"); err != nil {
				t.Fatalf("合法 TokenScopeSeparator %q 注册失败: %v", separator, err)
			}
		})
	}

	def := makeValid()
	def.AuthMethods[0].OAuth.TokenScopeSeparator = "semicolon"
	err := registry.New().Register(def, "create_item")
	if err == nil || !strings.Contains(err.Error(), "TokenScopeSeparator") {
		t.Fatalf("非法 TokenScopeSeparator 应注册失败，got %v", err)
	}
}

func TestRegisterNormalizesOAuthDefaultsAndRejectsUnknownEnums(t *testing.T) {
	def := makeValid()
	r := registry.New()
	if err := r.Register(def, "create_item"); err != nil {
		t.Fatal(err)
	}
	if def.AuthMethods[0].OAuth.AuthorizationScopeSeparator != "" ||
		def.AuthMethods[0].OAuth.TokenScopeSeparator != "" ||
		def.AuthMethods[0].OAuth.TokenEndpointAuth != "" ||
		def.AuthMethods[0].OAuth.TokenRequestFormat != "" {
		t.Fatal("Register mutated caller-owned Definition while normalizing")
	}
	got, _ := r.Get(def.Type)
	oauth := got.AuthMethods[0].OAuth
	if oauth.AuthorizationScopeSeparator != connector.OAuthScopeSpace ||
		oauth.TokenScopeSeparator != connector.OAuthScopeSpace ||
		oauth.TokenEndpointAuth != connector.TokenAuthBasic ||
		oauth.TokenRequestFormat != connector.TokenRequestForm {
		t.Fatalf("OAuth defaults were not normalized: %+v", oauth)
	}

	for _, tc := range []struct {
		name    string
		mutate  func(*connector.OAuthConfig)
		wantErr string
	}{
		{
			name: "authorization scope separator",
			mutate: func(oauth *connector.OAuthConfig) {
				oauth.AuthorizationScopeSeparator = "semicolon"
			},
			wantErr: "AuthorizationScopeSeparator",
		},
		{
			name: "token request format",
			mutate: func(oauth *connector.OAuthConfig) {
				oauth.TokenRequestFormat = "xml"
			},
			wantErr: "TokenRequestFormat",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invalid := makeValid()
			tc.mutate(invalid.AuthMethods[0].OAuth)
			err := registry.New().Register(invalid, "create_item")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("registration error = %v", err)
			}
		})
	}

	for _, format := range []connector.TokenRequestFormat{
		connector.TokenRequestForm,
		connector.TokenRequestJSON,
	} {
		valid := makeValid()
		valid.AuthMethods[0].OAuth.TokenRequestFormat = format
		if err := registry.New().Register(valid, "create_item"); err != nil {
			t.Fatalf("合法 TokenRequestFormat %q 注册失败: %v", format, err)
		}
	}
}

func TestRegisterRejectsReservedOAuthParametersASCIICaseInsensitive(
	t *testing.T,
) {
	for _, tc := range []struct {
		name   string
		mutate func(*connector.OAuthConfig)
	}{
		{
			name: "authorization",
			mutate: func(oauth *connector.OAuthConfig) {
				oauth.ExtraAuthParams = map[string]string{"CLIENT_ID": "override"}
			},
		},
		{
			name: "token",
			mutate: func(oauth *connector.OAuthConfig) {
				oauth.ExtraTokenParams = map[string]string{"Refresh_Token": "override"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValid()
			tc.mutate(def.AuthMethods[0].OAuth)
			err := registry.New().Register(def, "create_item")
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("reserved param registration error = %v", err)
			}
		})
	}

	def := makeValid()
	def.AuthMethods[0].OAuth.ExtraTokenParams = map[string]string{
		"audience": "https://api.example.com",
		"AUDIENCE": "duplicate",
	}
	err := registry.New().Register(def, "create_item")
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("case-folded duplicate registration error = %v", err)
	}
}

func TestRegisterOAuthEgressPolicy(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*connector.OAuthConfig)
		wantErr string
	}{
		{
			name: "missing authorization origins",
			mutate: func(config *connector.OAuthConfig) {
				config.Egress.AuthorizationOrigins = nil
			},
			wantErr: "AuthorizationOrigins",
		},
		{
			name: "missing token origins",
			mutate: func(config *connector.OAuthConfig) {
				config.Egress.TokenOrigins = nil
			},
			wantErr: "TokenOrigins",
		},
		{
			name: "origin must include canonical port",
			mutate: func(config *connector.OAuthConfig) {
				config.Egress.TokenOrigins = []string{"https://example.com"}
			},
			wantErr: "canonical HTTPS origin",
		},
		{
			name: "token origin escape",
			mutate: func(config *connector.OAuthConfig) {
				config.TokenEndpoint = "https://tokens.example.com/token"
			},
			wantErr: "origin 未列入",
		},
		{
			name: "refresh origin escape",
			mutate: func(config *connector.OAuthConfig) {
				config.RefreshTokenEndpoint =
					"https://refresh.example.com/token"
			},
			wantErr: "origin 未列入",
		},
		{
			name: "host placeholder forbidden",
			mutate: func(config *connector.OAuthConfig) {
				config.AuthorizationEndpoint =
					"https://{tenant}.example.com/authorize"
			},
			wantErr: "placeholder",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := makeValid()
			if test.name == "host placeholder forbidden" {
				definition.ConfigFields = append(
					definition.ConfigFields,
					connector.ConfigField{
						Key:       "tenant",
						Label:     "Tenant",
						InputType: connector.InputText,
					},
				)
			}
			test.mutate(definition.AuthMethods[0].OAuth)
			err := registry.New().Register(definition, "create_item")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("OAuth egress validation error = %v", err)
			}
		})
	}
}

func TestRegisteredOAuthEgressPolicyIsImmutable(t *testing.T) {
	definition := makeValid()
	definition.AuthMethods[0].OAuth.RefreshTokenEndpoint =
		"https://example.com/refresh"
	definition.AuthMethods[0].OAuth.ExtraTokenParams =
		map[string]string{"audience": "example"}
	registryInstance := registry.New()
	if err := registryInstance.Register(definition, "create_item"); err != nil {
		t.Fatal(err)
	}

	definition.AuthMethods[0].OAuth.TokenEndpoint =
		"https://attacker.example/token"
	definition.AuthMethods[0].OAuth.Egress.TokenOrigins[0] =
		"https://attacker.example:443"
	definition.AuthMethods[0].OAuth.ExtraTokenParams["audience"] = "attacker"
	first, ok := registryInstance.Get("example_app")
	if !ok {
		t.Fatal("registered definition missing")
	}
	if first.AuthMethods[0].OAuth.TokenEndpoint !=
		"https://example.com/token" ||
		first.AuthMethods[0].OAuth.Egress.TokenOrigins[0] !=
			"https://example.com:443" ||
		first.AuthMethods[0].OAuth.ExtraTokenParams["audience"] != "example" {
		t.Fatalf("registration retained caller-owned OAuth policy: %+v", first)
	}

	first.AuthMethods[0].OAuth.TokenEndpoint =
		"https://second-attacker.example/token"
	first.AuthMethods[0].OAuth.Egress.TokenOrigins[0] =
		"https://second-attacker.example:443"
	first.AuthMethods[0].OAuth.ExtraTokenParams["audience"] = "second-attacker"
	second, _ := registryInstance.Get("example_app")
	if second.AuthMethods[0].OAuth.TokenEndpoint !=
		"https://example.com/token" ||
		second.AuthMethods[0].OAuth.Egress.TokenOrigins[0] !=
			"https://example.com:443" ||
		second.AuthMethods[0].OAuth.ExtraTokenParams["audience"] != "example" {
		t.Fatalf("Get returned mutable OAuth policy: %+v", second)
	}
}

func TestFieldDeclarationValidationIsShared(t *testing.T) {
	type declarationCase struct {
		name    string
		mutate  func(*[]connector.ConfigField)
		wantErr string
	}
	cases := []declarationCase{
		{
			name: "empty key",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].Key = ""
			},
			wantErr: "key",
		},
		{
			name: "duplicate key",
			mutate: func(fields *[]connector.ConfigField) {
				*fields = append(*fields, (*fields)[0])
			},
			wantErr: "重复",
		},
		{
			name: "invalid input type",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].InputType = "password"
			},
			wantErr: "InputType",
		},
		{
			name: "invalid pattern",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].Validation.Pattern = `[`
			},
			wantErr: "Pattern",
		},
		{
			name: "select without options",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].InputType = connector.InputSelect
			},
			wantErr: "Options",
		},
		{
			name: "non select with options",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].Validation.Options = []string{"one"}
			},
			wantErr: "Options",
		},
		{
			name: "empty option",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].InputType = connector.InputSelect
				(*fields)[0].Validation.Options = []string{"one", ""}
			},
			wantErr: "空值",
		},
		{
			name: "duplicate option",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].InputType = connector.InputSelect
				(*fields)[0].Validation.Options = []string{"one", "one"}
			},
			wantErr: "重复值",
		},
		{
			name: "secret default",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].Secret = true
				(*fields)[0].DefaultValue = strPtr("value")
			},
			wantErr: "DefaultValue",
		},
		{
			name: "default fails pattern",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].Validation.Pattern = `^allowed$`
				(*fields)[0].DefaultValue = strPtr("denied")
			},
			wantErr: "DefaultValue",
		},
		{
			name: "default outside options",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].InputType = connector.InputSelect
				(*fields)[0].Validation.Options = []string{"one", "two"}
				(*fields)[0].DefaultValue = strPtr("three")
			},
			wantErr: "DefaultValue",
		},
		{
			name: "required empty default",
			mutate: func(fields *[]connector.ConfigField) {
				(*fields)[0].DefaultValue = strPtr("")
			},
			wantErr: "DefaultValue",
		},
	}

	locations := []struct {
		name  string
		apply func(*connector.Definition, []connector.ConfigField)
	}{
		{
			name: "ConfigFields",
			apply: func(def *connector.Definition, fields []connector.ConfigField) {
				def.ConfigFields = fields
			},
		},
		{
			name: "CredentialFields",
			apply: func(def *connector.Definition, fields []connector.ConfigField) {
				def.AuthMethods = []connector.AuthMethod{{
					Key:              "credential",
					Type:             connector.AuthCustomCredential,
					Label:            "Credential",
					CredentialFields: fields,
				}}
			},
		},
	}

	for _, location := range locations {
		for _, tc := range cases {
			t.Run(location.name+"/"+tc.name, func(t *testing.T) {
				fields := []connector.ConfigField{{
					Key:       "value",
					Label:     "Value",
					InputType: connector.InputText,
					Required:  true,
				}}
				tc.mutate(&fields)
				def := connector.Definition{
					Type:                "declaration_test",
					Name:                "Declaration Test",
					ConfigSchemaVersion: 1,
				}
				location.apply(&def, fields)
				err := registry.New().Register(def)
				if err == nil {
					t.Fatal("非法字段声明应注册失败")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("错误信息 %q 不含 %q", err.Error(), tc.wantErr)
				}
			})
		}
	}
}

func TestRegisterValidSelectDefaultsInConfigAndCredentialFields(t *testing.T) {
	defaultRegion := "us"
	def := connector.Definition{
		Type:                "declaration_test",
		Name:                "Declaration Test",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{{
			Key:          "region",
			Label:        "Region",
			InputType:    connector.InputSelect,
			Required:     true,
			DefaultValue: &defaultRegion,
			Validation: connector.FieldValidation{
				Pattern: `^(us|eu)$`,
				Options: []string{"us", "eu"},
			},
		}},
		AuthMethods: []connector.AuthMethod{{
			Key:   "custom",
			Type:  connector.AuthCustomCredential,
			Label: "Custom",
			CredentialFields: []connector.ConfigField{
				{
					Key:        "token",
					Label:      "Token",
					InputType:  connector.InputText,
					Required:   true,
					Secret:     true,
					Validation: connector.FieldValidation{Pattern: `^tok_`},
				},
				{
					Key:          "region",
					Label:        "Region",
					InputType:    connector.InputSelect,
					DefaultValue: &defaultRegion,
					Validation: connector.FieldValidation{
						Options: []string{"us", "eu"},
					},
				},
			},
		}},
	}
	if err := registry.New().Register(def); err != nil {
		t.Fatalf("合法字段声明注册失败: %v", err)
	}
}

func TestAuthMethodDeclarationValidation(t *testing.T) {
	validCredentialField := connector.ConfigField{
		Key:       "token",
		Label:     "Token",
		InputType: connector.InputText,
		Required:  true,
		Secret:    true,
	}
	cases := []struct {
		name    string
		mutate  func(*connector.Definition)
		wantErr string
	}{
		{
			name: "unknown auth type",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].Type = "magic"
			},
			wantErr: "Type",
		},
		{
			name: "oauth missing config",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].OAuth = nil
			},
			wantErr: "OAuth",
		},
		{
			name: "oauth with credential fields",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].CredentialFields = []connector.ConfigField{
					validCredentialField,
				}
			},
			wantErr: "CredentialFields",
		},
		{
			name: "none with oauth config",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].Type = connector.AuthNone
			},
			wantErr: "OAuth",
		},
		{
			name: "none with credential fields",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].Type = connector.AuthNone
				def.AuthMethods[0].OAuth = nil
				def.AuthMethods[0].CredentialFields = []connector.ConfigField{
					validCredentialField,
				}
			},
			wantErr: "CredentialFields",
		},
		{
			name: "api key with oauth config",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].Type = connector.AuthAPIKey
				def.AuthMethods[0].CredentialFields = []connector.ConfigField{
					validCredentialField,
				}
			},
			wantErr: "OAuth",
		},
		{
			name: "api key without credential fields",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].Type = connector.AuthAPIKey
				def.AuthMethods[0].OAuth = nil
			},
			wantErr: "CredentialFields",
		},
		{
			name: "custom without credential fields",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].Type = connector.AuthCustomCredential
				def.AuthMethods[0].OAuth = nil
			},
			wantErr: "CredentialFields",
		},
		{
			name: "invalid token endpoint auth",
			mutate: func(def *connector.Definition) {
				def.AuthMethods[0].OAuth.TokenEndpointAuth = "private_key_jwt"
			},
			wantErr: "TokenEndpointAuth",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValid()
			tc.mutate(&def)
			err := registry.New().Register(def, "create_item")
			if err == nil {
				t.Fatal("非法 auth method 声明应注册失败")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息 %q 不含 %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestRegisterLegalTokenEndpointAuth(t *testing.T) {
	tests := []struct {
		name  string
		value connector.TokenEndpointAuth
	}{
		{name: "default basic", value: ""},
		{name: "explicit basic", value: connector.TokenAuthBasic},
		{name: "post", value: connector.TokenAuthPost},
		{name: "none", value: connector.TokenAuthNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def := makeValid()
			def.AuthMethods[0].OAuth.TokenEndpointAuth = tc.value
			if err := registry.New().Register(def, "create_item"); err != nil {
				t.Fatalf("合法 TokenEndpointAuth %q 注册失败: %v", tc.value, err)
			}
		})
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

func TestDefinitionWithoutToolsDoesNotRequireSource(t *testing.T) {
	def := connector.Definition{
		Type:                "placeholder",
		Name:                "Placeholder",
		ConfigSchemaVersion: 1,
	}
	if err := registry.New().Register(def); err != nil {
		t.Fatalf("没有 Tool 的占位 Definition 不应强制来源元数据: %v", err)
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
