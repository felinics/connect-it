// Package linear is the fixed Definition of the Linear Connector. Linear
// capabilities are served by Linear's own hosted Remote MCP server
// (https://mcp.linear.app/mcp); this Connector owns no Managed handler and
// keeps only the credential validators.
//
// Tool names match the official hosted server: they were taken from two
// independent clients that talk to that very endpoint (fprochazka/linear-mcp-cli
// generates its commands from the live tools/list, openclaw/mcporter published a
// real `mcporter list linear` snapshot). The input schemas, however, are a
// permissive reconstruction, not verbatim tools/list output — property names are
// reliable, types and required sets are inferred. Every schema is therefore
// declared with "additionalProperties": true (arguments pass through and the
// upstream server is the authority) and must be checked against tools/list with
// mcp-probe (mcp:verify only reconciles tool names, never schemas).
// No OutputSchema is declared for the same reason: guessing one
// would make a Tool permanently fail validation at runtime.
//
// Linear also publishes a read-only variant of the same server at
// https://mcp.linear.app/mcp/readonly; it is not declared here because a
// read-scoped credential already cannot reach the write API.
package linear

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

const (
	linearOAuthMethod  = "oauth"
	linearAuthMethod   = "api_key"
	linearAPIKeyField  = "api_key"
	linearMCPServerKey = "official"
)

var Definition = connector.Definition{
	Type:                "linear",
	Name:                "Linear",
	Description:         "Linear issue tracking and product development platform",
	Categories:          []string{"productivity", "developer_tools"},
	HomepageURL:         "https://linear.app",
	IconURL:             "https://cdn.simpleicons.org/linear",
	ConfigSchemaVersion: 1,

	// Both fields are optional: without them OAuth authorization fails when it
	// is started (oauthsvc behaviour), while the personal API key path — and
	// therefore the Connector's readiness — is unaffected.
	ConfigFields: []connector.ConfigField{
		{
			Key:       "client_id",
			Label:     "OAuth Client ID",
			InputType: connector.InputText,
			Description: "Client ID of the Linear OAuth application. Optional: " +
				"without it only the personal API key path can connect.",
		},
		{
			Key:         "client_secret",
			Label:       "OAuth Client Secret",
			InputType:   connector.InputText,
			Secret:      true,
			Description: "Client secret of the Linear OAuth application.",
		},
	},

	// Linear accepts either credential on the same Bearer header, so both auth
	// methods reach the same Remote MCP server.
	AuthMethods: []connector.AuthMethod{
		{
			Key:   linearOAuthMethod,
			Type:  connector.AuthOAuth2,
			Label: "Linear OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://linear.app/oauth/authorize",
				TokenEndpoint:         "https://api.linear.app/oauth/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://linear.app:443"},
					TokenOrigins:         []string{"https://api.linear.app:443"},
				},
				// Linear access tokens are long-lived and are issued without a
				// refresh token, so no refresh endpoint is declared.
				Scopes: []string{
					"read",
					"write",
					"issues:create",
					"comments:create",
				},
				UsePKCE:           false,
				TokenEndpointAuth: connector.TokenAuthPost,
			},
		},
		{
			Key:   linearAuthMethod,
			Type:  connector.AuthAPIKey,
			Label: "Personal API Key",
			CredentialFields: []connector.ConfigField{
				{
					Key:       linearAPIKeyField,
					Label:     "Personal API Key",
					InputType: connector.InputText,
					Required:  true,
					Secret:    true,
					Description: "Linear personal API key created at " +
						"linear.app/settings/api. Presented to the Remote MCP " +
						"server as a Bearer token.",
					Validation: connector.FieldValidation{
						Pattern: `^\S{1,1000}\S{0,1000}\S{0,1000}\S{0,1000}\S{0,96}$`,
					},
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: linearMCPServerKey,
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				// The deprecated /sse transport is deliberately not used.
				URL: "https://mcp.linear.app/mcp",
			},
			// OAuth access tokens and personal API keys are both presented as
			// Authorization: Bearer.
			AuthBinding: connector.MCPAuthBinding{
				Scheme: "bearer",
				CredentialFieldByAuthMethod: map[string]string{
					linearAuthMethod: linearAPIKeyField,
				},
			},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				AllowedHostnames: []string{"mcp.linear.app"},
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	// Twelve of the ~25 Tools the hosted server publishes. The selection first
	// covers everything the previous GraphQL implementation could do (teams,
	// projects, issue search, issue read/create/update, comment create; the
	// authenticated profile is no longer a Tool — the server has no viewer Tool
	// and credential validation already reports that identity), then adds the
	// lookups those writes need: comments, users, labels, statuses and cycles.
	//
	// Deliberately not exposed: search_documentation (searches Linear's own help
	// centre, not the workspace), the document Tools and get/create/update_project
	// (single-source names, medium confidence), and the per-entity get_* Tools
	// whose list_* counterpart already returns the same records.
	//
	// Note for callers: unlike the previous GraphQL Tools, which required UUIDs,
	// team/state/project/assignee/label arguments here also accept human-readable
	// names, which the upstream server resolves.
	Tools: []connector.Tool{
		{
			ID:          "list_issues",
			Name:        "List issues",
			Description: "List issues, filtered by assignee, team, state, project, cycle, label or full-text query.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Full-text search over title and description"},
    "assignee": {"type": "string", "description": "User ID, name, email, or the literal \"me\""},
    "team": {"type": "string", "description": "Team name or ID"},
    "state": {"type": "string", "description": "Workflow status name or ID"},
    "project": {"type": "string", "description": "Project name or ID"},
    "cycle": {"type": "string", "description": "Cycle name or ID"},
    "label": {"type": "string", "description": "Label name or ID"},
    "parentId": {"type": "string", "description": "Parent issue UUID, for sub-issues"},
    "createdAt": {"type": "string", "description": "ISO-8601 datetime or duration such as -P1D"},
    "updatedAt": {"type": "string", "description": "ISO-8601 datetime or duration such as -P1D"},
    "limit": {"type": "number", "description": "Maximum records returned"},
    "includeArchived": {"type": "boolean"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_issues",
			},
		},
		{
			ID:          "get_issue",
			Name:        "Get issue",
			Description: "Get one issue by UUID or identifier such as ENG-42.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Issue UUID or identifier such as ENG-42"},
    "includeRelations": {"type": "boolean", "description": "Include blocking, related and duplicate relations"}
  },
  "required": ["id"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "get_issue",
			},
		},
		{
			ID:          "create_issue",
			Name:        "Create issue",
			Description: "Create one issue in a team.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "team": {"type": "string", "description": "Team name or ID"},
    "title": {"type": "string"},
    "description": {"type": "string", "description": "Markdown description"},
    "assignee": {"type": "string", "description": "User ID, name, email, or the literal \"me\""},
    "state": {"type": "string", "description": "Workflow status type, name, or ID"},
    "priority": {"type": "number", "description": "0=No priority, 1=Urgent, 2=High, 3=Normal, 4=Low"},
    "project": {"type": "string", "description": "Project name or ID"},
    "cycle": {"type": "string", "description": "Cycle name, number, or ID"},
    "labels": {"type": "array", "items": {"type": "string"}, "description": "Label names or IDs"},
    "dueDate": {"type": "string", "description": "ISO date"},
    "parentId": {"type": "string", "description": "Parent issue UUID"}
  },
  "required": ["team", "title"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"issues:create"},
			Risk:           connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "create_issue",
			},
		},
		{
			ID:          "update_issue",
			Name:        "Update issue",
			Description: "Update one issue. Relation arrays replace the existing set instead of adding to it.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "Issue UUID or identifier such as ENG-42"},
    "title": {"type": "string"},
    "description": {"type": "string", "description": "Markdown description"},
    "assignee": {"type": "string", "description": "User ID, name, email, or the literal \"me\""},
    "state": {"type": "string", "description": "Workflow status type, name, or ID"},
    "priority": {"type": "number", "description": "0=No priority, 1=Urgent, 2=High, 3=Normal, 4=Low"},
    "project": {"type": "string", "description": "Project name or ID"},
    "cycle": {"type": "string", "description": "Cycle name, number, or ID"},
    "labels": {"type": "array", "items": {"type": "string"}, "description": "Label names or IDs"},
    "dueDate": {"type": "string", "description": "ISO date"},
    "parentId": {"type": "string", "description": "Parent issue UUID"}
  },
  "required": ["id"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"write"},
			Risk:           connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "update_issue",
			},
		},
		{
			ID:          "list_comments",
			Name:        "List comments",
			Description: "List the comments of one issue.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "issueId": {"type": "string", "description": "Issue UUID"}
  },
  "required": ["issueId"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_comments",
			},
		},
		{
			// issueId, body and parentId are the one part of this Definition
			// captured from the live server rather than reconstructed.
			ID:          "create_comment",
			Name:        "Create comment",
			Description: "Create a Markdown comment on an issue, optionally replying to another comment.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "issueId": {"type": "string", "description": "Issue UUID"},
    "body": {"type": "string", "description": "Comment content as Markdown"},
    "parentId": {"type": "string", "description": "Parent comment ID to reply to"},
    "notifySubscribers": {"type": "boolean"},
    "labelIds": {"type": "array", "items": {"type": "string"}},
    "mentionIds": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["issueId", "body"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"comments:create"},
			Risk:           connector.RiskWrite,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "create_comment",
			},
		},
		{
			ID:          "list_projects",
			Name:        "List projects",
			Description: "List projects, filtered by team, state, initiative, member or name.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Search in project name"},
    "team": {"type": "string", "description": "Team name or ID"},
    "state": {"type": "string", "description": "Project state name or ID"},
    "initiative": {"type": "string", "description": "Initiative name or ID"},
    "member": {"type": "string", "description": "User ID, name, email, or the literal \"me\""},
    "createdAt": {"type": "string", "description": "ISO-8601 datetime or duration such as -P1D"},
    "updatedAt": {"type": "string", "description": "ISO-8601 datetime or duration such as -P1D"},
    "limit": {"type": "number", "description": "Maximum records returned"},
    "includeArchived": {"type": "boolean"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_projects",
			},
		},
		{
			ID:          "list_teams",
			Name:        "List teams",
			Description: "List the workspace teams, optionally searching by name.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Search in team name"},
    "createdAt": {"type": "string", "description": "ISO-8601 datetime or duration such as -P1D"},
    "updatedAt": {"type": "string", "description": "ISO-8601 datetime or duration such as -P1D"},
    "limit": {"type": "number", "description": "Maximum records returned"},
    "includeArchived": {"type": "boolean"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_teams",
			},
		},
		{
			ID:          "list_users",
			Name:        "List users",
			Description: "List workspace users, filtered by team or searched by name or email.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Search by name or email"},
    "team": {"type": "string", "description": "Team name or ID"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_users",
			},
		},
		{
			ID:          "list_issue_labels",
			Name:        "List issue labels",
			Description: "List workspace-level and team-level issue labels.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "team": {"type": "string", "description": "Team name or ID"},
    "name": {"type": "string", "description": "Filter by label name"},
    "limit": {"type": "number", "description": "Maximum records returned"}
  },
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_issue_labels",
			},
		},
		{
			ID:          "list_issue_statuses",
			Name:        "List issue statuses",
			Description: "List the workflow statuses of one team; statuses are team-scoped.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "team": {"type": "string", "description": "Exact team name or ID"}
  },
  "required": ["team"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_issue_statuses",
			},
		},
		{
			ID:          "list_cycles",
			Name:        "List cycles",
			Description: "List the cycles (sprints) of one team.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "teamId": {"type": "string", "description": "Team UUID"},
    "type": {"type": "string", "description": "current, previous or next; omit for all cycles"}
  },
  "required": ["teamId"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      linearMCPServerKey,
				RemoteToolName: "list_cycles",
			},
		},
	},
}
