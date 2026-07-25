// Package github 是 GitHub Connector 的固定 Definition。
// 验证点（spec §16）：多 auth method（OAuth＋PAT）；官方 Remote MCP Tools。
package github

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

const restAPIVersion = "2026-03-10"

var Definition = connector.Definition{
	Type:                "github",
	Name:                "GitHub",
	Description:         "GitHub 代码托管与协作平台",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://github.com",
	IconURL:             "https://cdn.simpleicons.org/github",
	ConfigSchemaVersion: 1,

	// 两个字段均可选：不配置时 OAuth 授权在发起时报错（oauthsvc 行为），
	// PAT 路径不受影响，github 仍为 ready。
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Description: "GitHub OAuth App 的 Client ID。可不填：不配置时 OAuth 授权不可用，仍可用 PAT 连接。",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Secret:      true,
			Description: "GitHub OAuth App 的 Client Secret。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "GitHub OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
				TokenEndpoint:         "https://github.com/login/oauth/access_token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://github.com:443"},
					TokenOrigins:         []string{"https://github.com:443"},
				},
				Scopes:              []string{"repo", "read:user"},
				TokenScopeSeparator: connector.OAuthScopeComma,
				UsePKCE:             false,
				TokenEndpointAuth:   connector.TokenAuthPost,
			},
		},
		{
			Key:   "pat",
			Type:  connector.AuthAPIKey,
			Label: "Personal Access Token",
			CredentialFields: []connector.ConfigField{
				{
					Key:         "token",
					Label:       "Personal Access Token",
					InputType:   connector.InputText,
					Required:    true,
					Secret:      true,
					Description: "GitHub PAT（classic 或 fine-grained），需具备目标仓库的读写权限。",
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "official",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    "https://api.githubcopilot.com/mcp/",
			},
			// OAuth access token 与 PAT 都以 Authorization: Bearer 呈递。
			AuthBinding: connector.MCPAuthBinding{
				Scheme: "bearer",
				CredentialFieldByAuthMethod: map[string]string{
					"pat": "token",
				},
			},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				AllowedHostnames: []string{"api.githubcopilot.com"},
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	// 5 个固定映射的官方 remote tool。remote tool 的 schema 宽松
	// （additionalProperties: true），参数直通、以上游为准。
	Tools: []connector.Tool{
		{
			ID:          "get_me",
			Name:        "Get my profile",
			Description: "获取当前授权用户的 GitHub 个人资料。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read:user"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "get_me"},
		},
		{
			ID:          "search_repositories",
			Name:        "Search repositories",
			Description: "按 GitHub 仓库搜索语法检索仓库。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "GitHub 仓库搜索语法，如 language:go stars:>100"
    },
    "sort": {
      "type": "string",
      "enum": ["stars", "forks", "help-wanted-issues", "updated"]
    },
    "order": {"type": "string", "enum": ["asc", "desc"]},
    "page": {"type": "integer", "minimum": 1},
    "perPage": {"type": "integer", "minimum": 1, "maximum": 100},
    "minimal_output": {"type": "boolean"}
  },
  "required": ["query"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "search_repositories"},
		},
		{
			ID:          "get_file_contents",
			Name:        "Get file contents",
			Description: "读取仓库中某文件或目录的内容。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "owner": {"type": "string"},
    "repo": {"type": "string"},
    "path": {"type": "string", "description": "文件或目录路径，目录以 / 结尾"},
    "ref": {"type": "string", "description": "分支、tag 或 commit SHA，缺省为默认分支"}
  },
  "required": ["owner", "repo"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "get_file_contents"},
		},
		{
			ID:          "list_issues",
			Name:        "List issues",
			Description: "列出仓库的 issue。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "owner": {"type": "string"},
    "repo": {"type": "string"},
    "state": {"type": "string", "description": "OPEN 或 CLOSED，缺省列出全部"},
    "labels": {"type": "array", "items": {"type": "string"}},
    "perPage": {"type": "integer", "minimum": 1, "maximum": 100}
  },
  "required": ["owner", "repo"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "list_issues"},
		},
		{
			ID:          "issue_write",
			Name:        "Create or update issue",
			Description: "创建或更新仓库 issue（写操作）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "method": {"type": "string", "enum": ["create", "update"]},
    "owner": {"type": "string"},
    "repo": {"type": "string"},
    "title": {"type": "string"},
    "body": {"type": "string"},
    "labels": {"type": "array", "items": {"type": "string"}},
    "assignees": {"type": "array", "items": {"type": "string"}},
    "issue_number": {"type": "number", "description": "method=update 时必填"}
  },
  "required": ["method", "owner", "repo"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"repo"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "issue_write"},
		},
	},
}
