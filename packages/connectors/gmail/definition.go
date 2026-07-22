// Package gmail 是 Gmail Connector 的固定 Definition 与 Managed handler。
// 验证点（spec §16）：同一 Connector 混合 Remote MCP＋Managed Tool。
package gmail

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "gmail",
	Name:                "Gmail",
	Description:         "Google Gmail 邮件服务",
	Categories:          []string{"communication"},
	HomepageURL:         "https://mail.google.com",
	IconURL:             "https://cdn.simpleicons.org/gmail",
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
			Description: "启用了 Gmail API 与 Gmail MCP server 的 Google Cloud 项目 ID。",
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
				// readonly 覆盖 search_threads / list_messages，
				// compose 覆盖 create_draft / send_message（messages.send 接受 compose）。
				Scopes: []string{
					"https://www.googleapis.com/auth/gmail.readonly",
					"https://www.googleapis.com/auth/gmail.compose",
				},
				UsePKCE:           false,
				TokenEndpointAuth: connector.TokenAuthPost,
				// 没有这两个参数 Google 不发 refresh token。
				ExtraAuthParams: map[string]string{
					"access_type": "offline",
					"prompt":      "consent",
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "official",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    "https://gmailmcp.googleapis.com/mcp/v1",
			},
			AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				Publisher:        "Google",
				DocsURL:          "https://developers.google.com/workspace/gmail/api/reference/mcp",
				ReviewedAt:       "2026-07-22",
				AllowedHostnames: []string{"gmailmcp.googleapis.com"},
				// Google Workspace Developer Preview。
				Stability: connector.StabilityPreview,
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	Tools: []connector.Tool{
		// —— Remote MCP tool（schema 为宽松近似，以 mcp:verify 对照 tools/list 为准）——
		{
			ID:          "search_threads",
			Name:        "Search threads",
			Description: "按 Gmail 搜索语法检索邮件会话。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Gmail 搜索语法，如 from:alice is:unread"
    },
    "pageSize": {"type": "integer", "minimum": 1, "maximum": 100}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.readonly"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "search_threads"},
		},
		{
			ID:          "create_draft",
			Name:        "Create draft",
			Description: "创建邮件草稿（不发送）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "to": {"type": ["string", "array"], "description": "收件人"},
    "cc": {"type": ["string", "array"], "description": "抄送"},
    "bcc": {"type": ["string", "array"], "description": "密送"},
    "subject": {"type": "string"},
    "body": {"type": "string"},
    "threadId": {"type": "string", "description": "回复既有会话时填其 threadId"},
    "raw": {
      "type": "string",
      "description": "base64url 编码的完整 RFC 2822 邮件，与结构化字段二选一"
    },
    "includeBodyHtml": {"type": "boolean"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.compose"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "create_draft"},
		},
		// —— Managed tool（严格 schema，handler 在 managed.go）——
		{
			ID:          "list_messages",
			Name:        "List messages",
			Description: "经 Gmail REST API 列出邮箱中的消息（可按搜索语法过滤）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "q": {"type": "string", "description": "Gmail 搜索语法过滤条件"},
    "max_results": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20}
  },
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "messages": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "threadId": {"type": "string"}
        }
      }
    },
    "nextPageToken": {"type": "string"},
    "resultSizeEstimate": {"type": "integer"}
  }
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.readonly"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "list_messages"},
		},
		{
			ID:          "send_message",
			Name:        "Send message",
			Description: "经 Gmail REST API 发送纯文本邮件。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "to": {"type": "string", "description": "收件人邮箱"},
    "subject": {"type": "string"},
    "body": {"type": "string", "description": "纯文本正文（UTF-8）"}
  },
  "required": ["to", "subject", "body"],
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string"},
    "threadId": {"type": "string"},
    "labelIds": {"type": "array", "items": {"type": "string"}}
  }
}`),
			RequiredScopes: []string{"https://www.googleapis.com/auth/gmail.compose"},
			Risk:           connector.RiskWrite,
			Backend:        connector.ManagedBackend{HandlerKey: "send_message"},
		},
	},
}
