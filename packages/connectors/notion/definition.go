// Package notion defines the official hosted Notion MCP connector.
package notion

import (
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "notion",
	Name:                "Notion",
	Description:         "Notion 文档、知识库与协作平台",
	Categories:          []string{"productivity"},
	HomepageURL:         "https://www.notion.so",
	IconURL:             "https://cdn.simpleicons.org/notion/_/e5e5e5",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "Notion OAuth",
		OAuth: &connector.OAuthConfig{Mode: connector.OAuthModeMCP},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://mcp.notion.com/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
