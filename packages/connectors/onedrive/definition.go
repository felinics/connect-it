// Package onedrive 是 OneDrive Connector 的固定 Definition 与 Managed handler。
//
// 为什么是 Managed 而不是 Remote MCP（2026-07-24 复核，见
// docs/provider-mcp-decision-table.md §3.10 与 §5.1）：
// 微软**确实**有第一方的 Work IQ `mcp_OneDriveRemoteServer`，所以理由不是
// 「没有官方 MCP」。真实原因是三条门槛：
//  1. 仍是 preview，微软明写 "aren't meant for production use"，tool 名与参数可能变；
//  2. 所有文件读写硬性限制 ≤5MB —— 对文件连接器是功能天花板；
//  3. 需要 M365 Copilot 许可证 + Entra 应用注册 + 管理员逐 server 授权。
//
// 这三条任意一条解除（尤其是 GA 与 5MB 上限），就应该重新评估迁移到 Remote MCP。
// 记录真实理由而非假前提，是为了让复查有触发条件。
// 注意本连接器现有实现只有约 440 行，迁移的行数收益本来就接近零。
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
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://login.microsoftonline.com:443"},
					TokenOrigins:         []string{"https://login.microsoftonline.com:443"},
				},
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
			ID:            "upload_file",
			Name:          "Upload file",
			Description:   "上传文本文件到 OneDrive 指定路径（简单上传，上限 4 MiB）。",
			MaxInputBytes: connector.AbsoluteMaxInputBytes,
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
