package configsvc

import (
	"net/url"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Fixture 是 configsvc 全部测试共用的唯一 Connector fixture 构造器。它按
// export_test 惯例导出：内部测试文件里的导出符号对外部 configsvc_test 包同样
// 可见，两个测试包因此不必各留一份拷贝。
//
// 基础形态覆盖校验与读写需要的全部字段类型：必填 public、必填 secret、带默认
// 值、可选 secret、枚举、正则。policyTokenEndpoint 非空时再叠加 policy
// identity 用例需要的形态：带 {tenant} 占位符的 OAuth、自托管 Remote MCP
// endpoint、网络开关，以及一个 Secret 的 PolicyIdentity 字段。
func Fixture(typ connector.Type, policyTokenEndpoint string) connector.Definition {
	defaultTenant := "common"
	falseValue := "false"
	def := connector.Definition{
		Type:                typ,
		Name:                "Example",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID",
				InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret",
				InputType: connector.InputText, Required: true, Secret: true},
			{Key: "tenant", Label: "Tenant",
				InputType: connector.InputText, Required: true,
				DefaultValue: &defaultTenant},
			{Key: "region", Label: "Region", InputType: connector.InputSelect,
				Validation: connector.FieldValidation{Options: []string{"us", "eu"}}},
			{Key: "project_id", Label: "Project ID", InputType: connector.InputText,
				Validation: connector.FieldValidation{Pattern: `^[0-9]+$`}},
			{Key: "api_key", Label: "API Key",
				InputType: connector.InputText, Secret: true},
		},
	}
	if policyTokenEndpoint == "" {
		return def
	}

	tokenURL, err := url.Parse(policyTokenEndpoint)
	if err != nil {
		panic(err)
	}
	def.ConfigFields = append(def.ConfigFields,
		connector.ConfigField{Key: "mcp_url", Label: "MCP URL",
			InputType: connector.InputURL},
		connector.ConfigField{Key: "allow_insecure_http", Label: "Allow HTTP",
			InputType:    connector.InputSelect,
			DefaultValue: &falseValue,
			Validation: connector.FieldValidation{
				Options: []string{"false", "true"},
			}},
		connector.ConfigField{Key: "private_partition", Label: "Private partition",
			InputType: connector.InputText, Secret: true, PolicyIdentity: true},
	)
	def.AuthMethods = []connector.AuthMethod{{
		Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth",
		OAuth: &connector.OAuthConfig{
			AuthorizationEndpoint: "https://login.example/{tenant}/authorize",
			TokenEndpoint:         policyTokenEndpoint,
			Egress: connector.OAuthEgressConfig{
				AuthorizationOrigins: []string{"https://login.example:443"},
				TokenOrigins: []string{
					"https://" + tokenURL.Hostname() + ":443",
				},
			},
		},
	}}
	def.RemoteMCPServers = []connector.RemoteMCPServer{{
		Key: "self",
		Endpoint: connector.Endpoint{
			Source:         connector.EndpointConfigField,
			ConfigFieldKey: "mcp_url",
		},
		AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
		Provenance:  connector.Provenance{Kind: connector.ProvenanceSelfHosted},
	}}
	return def
}
