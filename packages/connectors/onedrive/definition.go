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
				// 用管理员配置（含默认值 common）替换。
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
