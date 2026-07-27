// Package googleads 是 Google Ads Connector 的固定 Definition 与 Managed handler。
package googleads

import (
	"encoding/json"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var Definition = connector.Definition{
	Type:                "google_ads",
	Name:                "Google Ads",
	Description:         "Google Ads 广告投放平台",
	Categories:          []string{"advertising"},
	HomepageURL:         "https://ads.google.com",
	IconURL:             "https://cdn.simpleicons.org/googleads",
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
			Key:         "developer_token",
			Label:       "Developer Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Google Ads API Developer Token。",
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

	Implementation: connector.Managed{Tools: []connector.ManagedTool{
		{
			Tool: mcp.Tool{
				Name:        "list_accessible_customers",
				Title:       "List accessible customers",
				Description: "列出当前 Google 账号可直接访问的 Google Ads customer。",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`),
			},
			Handler: listAccessibleCustomers,
		},
		{
			Tool: mcp.Tool{
				Name:        "search",
				Title:       "Search Google Ads",
				Description: "对指定 customer 执行 GAQL 查询，返回一页结果。",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "customer_id": {
      "type": "string",
      "description": "Google Ads customer ID，10 位数字，可包含连字符"
    },
    "query": {
      "type": "string",
      "description": "Google Ads Query Language 查询"
    },
    "page_token": {
      "type": "string",
      "description": "上一页返回的 nextPageToken"
    },
    "login_customer_id": {
      "type": "string",
      "description": "通过 Manager Account 访问时填写其 customer ID"
    }
  },
  "required": ["customer_id", "query"],
  "additionalProperties": false
}`),
			},
			Handler: search,
		},
	}},
}
