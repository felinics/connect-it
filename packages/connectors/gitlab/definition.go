// Package gitlab 是 GitLab Connector 的固定 Definition。
// GitLab 的 MCP server 不是独立服务，而是实例自身的一个 REST 命名空间
// （POST {instance}/api/v4/mcp），因此 endpoint 由运营方按实例填写：
// SaaS 填 https://gitlab.com/api/v4/mcp，self-managed / Dedicated 填自己的域名。
// 验证点：self_hosted Streamable HTTP MCP + api_key 绑定的 bearer 凭据。
package gitlab

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

func gitlabStringPtr(value string) *string { return &value }

var Definition = connector.Definition{
	Type:                "gitlab",
	Name:                "GitLab",
	Description:         "GitLab 代码托管、Issue 与 Merge Request 协作平台（经实例自带的官方 MCP server）",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://gitlab.com",
	IconURL:             "https://cdn.simpleicons.org/gitlab",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:       "mcp_url",
			Label:     "MCP URL",
			InputType: connector.InputURL,
			Required:  true,
			Description: "GitLab 实例的 MCP endpoint，形如 https://gitlab.com/api/v4/mcp；" +
				"self-managed / Dedicated 换成自己的实例域名。故意不给默认值：" +
				"填错实例等于把 access token 发给别人。实例侧需开启 GitLab Duo 与 MCP server。",
		},
		{
			Key:          "allow_insecure_http",
			Label:        "Allow insecure HTTP",
			InputType:    connector.InputSelect,
			DefaultValue: gitlabStringPtr("false"),
			Description:  "仅供显式批准的 self-managed 开发环境使用；实际生效还需要部署级开关。",
			Validation: connector.FieldValidation{
				Options: []string{"false", "true"},
			},
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "access_token",
			Type:  connector.AuthAPIKey,
			Label: "OAuth access token (mcp scope)",
			CredentialFields: []connector.ConfigField{
				{
					Key:       "token",
					Label:     "Access Token",
					InputType: connector.InputText,
					Required:  true,
					Secret:    true,
					Description: "GitLab OAuth 2.0 access token，scope 必须包含 mcp（api scope 不够，" +
						"见 lib/api/mcp/base.rb 的 include_any_scope? 守卫），用户还需 execute_mcp_tool 权限。" +
						"Personal / project / group access token 不被 /api/v4/mcp 接受。" +
						"实例的 OAuth endpoint 随域名而变，无法写进 Definition，因此 token 由运营方带入。",
					Validation: connector.FieldValidation{
						// Go's RE2 implementation caps each counted repeat at
						// 1000, so the concatenated groups express 1..4096.
						Pattern: `^\S{1,1000}\S{0,1000}\S{0,1000}\S{0,1000}\S{0,96}$`,
					},
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "instance",
			Endpoint: connector.Endpoint{
				Source:         connector.EndpointConfigField,
				ConfigFieldKey: "mcp_url",
			},
			AuthBinding: connector.MCPAuthBinding{
				Scheme: "bearer",
				CredentialFieldByAuthMethod: map[string]string{
					"access_token": "token",
				},
			},
			Provenance: connector.Provenance{
				Kind: connector.ProvenanceSelfHosted,
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	// tool 名与 GitLab 上游一致（app/services/mcp/tools/manager.rb 与
	// doc/user/model_context_protocol/mcp_server_tools.md 互相印证），schema 为宽松近似，
	// tool 名可由 mcp:verify 兜底核对；schema 只有 `mise run mcp-probe` 能校准
	// （mcp:verify 只比对名字集合，不读 schema）。remote tool 参数直通，
	// 一律 additionalProperties: true。
	//
	// 上游共 23 个 tool，这里只暴露 14 个 —— 每个 tool 都是长期契约：
	//   - 覆盖旧 Managed 版 7 个 tool 的等价能力：create_issue 直接对应；
	//     get_merge_request 直接对应；list_projects / list_issues /
	//     list_merge_requests 由 search（scope=projects|issues|merge_requests）覆盖。
	//     get_current_user、get_project 上游没有对应 tool，能力下降，等上游补齐。
	//   - 补上高价值低风险的 MR review / CI 排障 / work item 协作能力。
	// 刻意舍弃：get_mcp_server_version（运维工具，不该给 Agent）；
	// manage_pipeline（一个 tool 里塞了 list/create/delete/retry/cancel，
	// 删除 pipeline 属破坏性且靠参数组合分派，语义太糊）；
	// semantic_code_search、attach_scan_profile、get_saved_view_work_items（EE 专属，
	// CE 实例的 tools/list 里根本不会出现）；get_merge_request_conflicts、
	// get_work_item_types（只在 master 源码里注册，官方 tool 文档尚未列出，老实例没有）。
	//
	// tool 名的大小写不一致是上游真实存在的，照抄不要"纠正"：
	// create_workitem_note / get_workitem_notes 无下划线，link_work_items 有下划线。
	Tools: []connector.Tool{
		{
			ID:          "search",
			Name:        "Search",
			Description: "用 GitLab Search API 在实例 / group / project 范围内搜索 issues、merge requests、projects、代码等。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "scope": {"type": "string", "description": "搜索范围，如 issues、merge_requests、projects、blobs"},
    "search": {"type": "string", "description": "搜索词"},
    "group_id": {"type": "string", "description": "限定 group 的 ID 或 URL-encoded 路径"},
    "project_id": {"type": "string", "description": "限定 project 的 ID 或 URL-encoded 路径"},
    "state": {"type": "string", "description": "issues / merge_requests 的状态过滤"},
    "confidential": {"type": "boolean"},
    "fields": {"type": "array", "items": {"type": "string"}},
    "order_by": {"type": "string"},
    "sort": {"type": "string"},
    "page": {"type": "integer"},
    "per_page": {"type": "integer"}
  },
  "required": ["scope", "search"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "search"},
		},
		{
			ID:          "search_labels",
			Name:        "Search labels",
			Description: "在 project 或 group 中按标题搜索 label。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "full_path": {"type": "string", "description": "project 或 group 的完整路径，如 group/project"},
    "is_project": {"type": "boolean", "description": "true 搜 project，false 搜 group"},
    "search": {"type": "string", "description": "按 label 标题过滤的搜索词"}
  },
  "required": ["full_path", "is_project"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "search_labels"},
		},
		{
			ID:          "get_issue",
			Name:        "Get issue",
			Description: "获取一个 GitLab issue 的详情。",
			// schema 由 Grape params 白名单在运行时生成，这里是推导结果，待 mcp-probe 校准。
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "issue_iid": {"type": "integer", "description": "issue 在 project 内的 IID"}
  },
  "required": ["id", "issue_iid"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_issue"},
		},
		{
			ID:          "create_issue",
			Name:        "Create issue",
			Description: "在一个 GitLab project 中创建 issue。",
			// 只声明文档与 CE 源码都有的参数：epic_id（EE）与 milestone（CE）随版型变动，
			// 参数直通仍可透传，待 mcp-probe 校准。
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "title": {"type": "string", "description": "Issue 标题"},
    "description": {"type": "string", "description": "Issue 正文"},
    "assignee_ids": {"type": "array", "items": {"type": "integer"}},
    "milestone_id": {"type": "integer"},
    "labels": {"type": "array", "items": {"type": "string"}},
    "confidential": {"type": "boolean"}
  },
  "required": ["id", "title"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "create_issue"},
		},
		{
			ID:          "get_merge_request",
			Name:        "Get merge request",
			Description: "获取一个 merge request 的详情。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "merge_request_iid": {"type": "integer", "description": "merge request 在 project 内的 IID"}
  },
  "required": ["id", "merge_request_iid"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_merge_request"},
		},
		{
			ID:          "create_merge_request",
			Name:        "Create merge request",
			Description: "在一个 GitLab project 中创建 merge request。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "title": {"type": "string"},
    "source_branch": {"type": "string"},
    "target_branch": {"type": "string"},
    "target_project_id": {"type": "integer"},
    "description": {"type": "string"},
    "assignee_ids": {"type": "array", "items": {"type": "integer"}},
    "reviewer_ids": {"type": "array", "items": {"type": "integer"}},
    "labels": {"type": "array", "items": {"type": "string"}},
    "milestone_id": {"type": "integer"}
  },
  "required": ["id", "title", "source_branch", "target_branch"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "create_merge_request"},
		},
		{
			ID:          "get_merge_request_diffs",
			Name:        "Get merge request diffs",
			Description: "分页获取一个 merge request 的 diff。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "merge_request_iid": {"type": "integer"},
    "page": {"type": "integer"},
    "per_page": {"type": "integer"}
  },
  "required": ["id", "merge_request_iid"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_merge_request_diffs"},
		},
		{
			ID:          "get_merge_request_notes",
			Name:        "Get merge request notes",
			Description: "游标分页获取一个 merge request 的 notes（评论与系统消息）。",
			// url 与 project_id + merge_request_iid 二选一，上游没有把它写进 required。
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "merge request 的 GitLab URL；不填则必须给 project_id 与 merge_request_iid"},
    "project_id": {"type": "string"},
    "merge_request_iid": {"type": "integer"},
    "after": {"type": "string", "description": "向后翻页游标，用上一次响应的 endCursor"},
    "before": {"type": "string", "description": "向前翻页游标，用上一次响应的 startCursor"},
    "first": {"type": "integer", "minimum": 1, "maximum": 100},
    "last": {"type": "integer", "minimum": 1, "maximum": 100}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_merge_request_notes"},
		},
		{
			ID:          "create_merge_request_note",
			Name:        "Create merge request note",
			Description: "以当前授权用户身份在 merge request 上评论，或回复已有 discussion。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "merge request 的 GitLab URL；不填则必须给 project_id 与 merge_request_iid"},
    "project_id": {"type": "string"},
    "merge_request_iid": {"type": "integer"},
    "body": {"type": "string", "maxLength": 1048576, "description": "评论正文；上游拒绝以 / 开头的行，避免触发 /merge 之类 quick action"},
    "discussion_id": {"type": "string", "description": "要回复的 discussion 全局 ID，格式 gid://gitlab/Discussion/<id>；不填则新建顶层评论"}
  },
  "required": ["body"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "create_merge_request_note"},
		},
		{
			ID:          "get_workitem_notes",
			Name:        "Get work item notes",
			Description: "游标分页获取一个 work item 的 notes（评论）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "work item 的 GitLab URL；不填则必须给 group_id 或 project_id 加 work_item_iid"},
    "group_id": {"type": "string"},
    "project_id": {"type": "string"},
    "work_item_iid": {"type": "integer"},
    "after": {"type": "string"},
    "before": {"type": "string"},
    "first": {"type": "integer", "minimum": 1, "maximum": 100},
    "last": {"type": "integer", "minimum": 1, "maximum": 100}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_workitem_notes"},
		},
		{
			ID:          "create_workitem_note",
			Name:        "Create work item note",
			Description: "在一个 work item 上创建评论。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "work item 的 GitLab URL；不填则必须给 group_id 或 project_id 加 work_item_iid"},
    "group_id": {"type": "string"},
    "project_id": {"type": "string"},
    "work_item_iid": {"type": "integer"},
    "body": {"type": "string", "maxLength": 1048576, "description": "评论正文"},
    "internal": {"type": "boolean", "description": "标记为内部评论，仅 Reporter 及以上可见"},
    "discussion_id": {"type": "string", "description": "要回复的 discussion 全局 ID，格式 gid://gitlab/Discussion/<id>"}
  },
  "required": ["body"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "create_workitem_note"},
		},
		{
			ID:          "link_work_items",
			Name:        "Link work items",
			Description: "把一个 work item 关联到其它 work item，并指定关系类型。",
			// link_type 故意不写 enum：CE 源码只允许 relates_to，官方（EE）文档写
			// relates_to|blocks|blocked_by，取值随实例版型变化，交给上游判定。
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "源 work item 的 GitLab URL；不填则必须给 group_id 或 project_id 加 work_item_iid"},
    "group_id": {"type": "string"},
    "project_id": {"type": "string"},
    "work_item_iid": {"type": "integer", "description": "源 work item 的 IID"},
    "work_items_ids": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "maxItems": 10,
      "description": "被关联 work item 的全局 ID，格式 gid://gitlab/WorkItem/<id>"
    },
    "link_type": {"type": "string", "description": "关系类型，如 relates_to；blocks/blocked_by 需 Premium 或 Ultimate"}
  },
  "required": ["work_items_ids"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskWrite,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "link_work_items"},
		},
		{
			ID:          "get_pipeline_jobs",
			Name:        "Get pipeline jobs",
			Description: "获取一条 CI/CD pipeline 的 job 列表。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "pipeline_id": {"type": "integer"},
    "page": {"type": "integer"},
    "per_page": {"type": "integer"}
  },
  "required": ["id", "pipeline_id"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_pipeline_jobs"},
		},
		{
			ID:          "get_job_log",
			Name:        "Get job log",
			Description: "获取一个 CI/CD job 的 trace（日志输出）。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "project 的 ID 或 URL-encoded 路径"},
    "job_id": {"type": "integer"}
  },
  "required": ["id", "job_id"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"mcp"},
			Risk:           connector.RiskRead,
			Backend:        connector.RemoteMCPBackend{ServerKey: "instance", RemoteToolName: "get_job_log"},
		},
	},
}
