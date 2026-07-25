// Package notion 是 Notion Connector 的固定 Definition，走官方托管的
// Remote MCP server（https://mcp.notion.com/mcp）。
//
// ⚠️ 三代 "Notion MCP" 并存，tool namespace 互不兼容，改这个文件前先看清楚：
//
//	(a) 开源本地版 v1（makenotion/notion-mcp-server <2.0.0，由 OpenAPI 生成）：
//	    tool 名形如 API-post-search / API-retrieve-a-page；
//	(b) 开源本地版 v2（同仓库 main）：tool 名形如 query-data-source /
//	    retrieve-page-markdown，无前缀；
//	(c) 官方托管远程版（本文件唯一目标）：18 个 tool 全部带 notion- 前缀、
//	    连字符分隔，如 notion-search / notion-fetch / notion-create-pages。
//
// 三者零重叠。开源本地版官方已声明不再积极维护，绝不要拿它的 tool 名来改这里。
// 另有一个别名陷阱：远程 server 对 OpenAI 风格的 client 会把 notion-fetch 暴露成
// 裸 fetch，connect-it 以通用 MCP client 身份接入，拿到的是 notion- 前缀名。
//
// 认证：远程 server 只接受 OAuth 签发的 access token（Authorization: Bearer），
// 不接受长期的 internal integration token（ntn_...）；文档里那句 "does not
// support bearer token authentication" 指的正是后者，长期 token 只在已废弃的
// 本地版上可用。因此这里只有 oauth 一种 auth method。
package notion

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "notion",
	Name:                "Notion",
	Description:         "Notion pages, data sources, and blocks",
	Categories:          []string{"productivity", "collaboration"},
	HomepageURL:         "https://www.notion.so",
	IconURL:             "https://cdn.simpleicons.org/notion",
	ConfigSchemaVersion: 1,

	// mcp.notion.com 既是 resource server 也是 authorization server，并支持
	// RFC 7591 动态客户端注册。运营方在 https://mcp.notion.com/register 注册一次
	// 机密客户端，把结果填进这两个字段；两者缺一则 OAuth 授权不可用。
	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "OAuth Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Notion MCP OAuth 客户端的 Client ID（动态客户端注册所得）。",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Notion MCP OAuth 客户端的 Client Secret（动态客户端注册所得）。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   notionAuthMethod,
			Type:  connector.AuthOAuth2,
			Label: "Notion OAuth",
			// endpoint 取自 mcp.notion.com 自述的 RFC 8414 元数据：
			// issuer / authorization_endpoint / token_endpoint 都在 mcp.notion.com，
			// 与 api.notion.com 的公开 integration OAuth 不是同一套。
			// code_challenge_methods_supported 含 S256；access token 约 8 小时有效，
			// refresh token 每次使用轮换。
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://mcp.notion.com/authorize",
				TokenEndpoint:         "https://mcp.notion.com/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://mcp.notion.com:443"},
					TokenOrigins:         []string{"https://mcp.notion.com:443"},
				},
				Scopes:            []string{"default"},
				UsePKCE:           true,
				TokenEndpointAuth: connector.TokenAuthBasic,
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "official",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    "https://mcp.notion.com/mcp",
			},
			// OAuth access token 以 Authorization: Bearer 呈递；没有 api_key
			// auth method，因此不需要 credential 字段绑定。
			AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				AllowedHostnames: []string{"mcp.notion.com"},
			},
			// search 与 data source 查询走 Notion AI，比普通读慢。
			RequestTimeout: 60 * time.Second,
		},
	},

	// tool 名与官方托管 Notion MCP server 一致；Notion 未公开任何参数契约
	// （文档只给描述和示例 prompt），因此 schema 一律是宽松近似：
	// additionalProperties: true、不声明 required，参数直通、以上游为准，
	// 待 mcp-probe 校准（mcp:verify 只核对 tool 名，不读 schema）。
	// properties 只写文档逐字出现过的参数。
	//
	// 上游 18 个 tool 里取 10 个：先覆盖原 Managed 版 6 个 tool 的等价能力
	// （search/retrieve_page/retrieve_data_source/retrieve_block_children 收敛成
	// notion-search + notion-fetch，create_page/update_page 对应
	// notion-create-pages/notion-update-page），再补评论与成员这类高价值读写。
	// 未暴露：notion-get-async-task（运维/内部轮询）、notion-duplicate-page（异步，
	// 依赖 async task 才能收敛）、notion-create-view/update-view/update-data-source/
	// query-database-view/query-meeting-notes/move-pages（配置 DSL 或窄场景，
	// 没有参数契约时暴露出去只会让 Agent 反复试错）。
	Tools: []connector.Tool{
		{
			ID:          "search",
			Name:        "Search",
			Description: "搜索 Notion workspace 及已连接的第三方内容（需要 Notion AI 方可跨源搜索）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "自然语言或关键词查询"}
  },
  "additionalProperties": true
}`),
			Risk:    connector.RiskRead,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-search"},
		},
		{
			ID:          "fetch",
			Name:        "Fetch",
			Description: "按 URL 或 ID 读取 page、database、data source 内容；id 传 self 返回当前 workspace 与用户身份。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {
      "type": "string",
      "description": "page/database URL 或 ID，data source 用 collection://<uuid>，特殊值 self 返回身份信息"
    }
  },
  "additionalProperties": true
}`),
			Risk:    connector.RiskRead,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-fetch"},
		},
		{
			ID:          "query_data_sources",
			Name:        "Query data sources",
			Description: "用 SQL 查询 Notion data source，或运行已有视图，返回带计数与汇总的结构化结果。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskRead,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-query-data-sources"},
		},
		{
			ID:          "get_users",
			Name:        "Get users",
			Description: "列出 workspace 成员与访客，或按 ID 取单个用户；传 self 取当前用户。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskRead,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-get-users"},
		},
		{
			ID:          "get_teams",
			Name:        "Get teams",
			Description: "列出当前 workspace 的 teamspace。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskRead,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-get-teams"},
		},
		{
			ID:          "get_comments",
			Name:        "Get comments",
			Description: "列出某个 page 上的评论与讨论，可含块级、行内讨论与已解决的线程。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskRead,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-get-comments"},
		},
		{
			ID:          "create_pages",
			Name:        "Create pages",
			Description: "创建一个或多个 Notion page，可指定属性、正文、图标与封面，可套用 database 模板。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "allow_async": {
      "type": "boolean",
      "description": "为 true 时返回异步 task ID 而非直接结果；本 Connector 未暴露轮询 tool，建议留空"
    }
  },
  "additionalProperties": true
}`),
			Risk:    connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-create-pages"},
		},
		{
			ID:          "update_page",
			Name:        "Update page",
			Description: "更新一个 Notion page 的属性、正文、图标或封面。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-update-page"},
		},
		{
			ID:          "create_database",
			Name:        "Create database",
			Description: "创建一个 Notion database 及其初始 data source 与初始视图。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-create-database"},
		},
		{
			ID:          "create_comment",
			Name:        "Create comment",
			Description: "在 page 或指定内容上添加评论，支持回复已有讨论。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk:    connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{ServerKey: "official", RemoteToolName: "notion-create-comment"},
		},
	},
}
