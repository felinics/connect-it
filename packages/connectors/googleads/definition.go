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
