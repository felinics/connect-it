// Package gitlab defines the official hosted GitLab MCP connector.
package gitlab

import (
	"time"

	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "gitlab",
	Name:                "GitLab",
	Description:         "GitLab code hosting and DevSecOps platform",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://gitlab.com",
	IconURL:             "https://cdn.simpleicons.org/gitlab",
	ConfigSchemaVersion: 1,
	AuthMethods: []connector.AuthMethod{{
		Key:   "oauth",
		Type:  connector.AuthOAuth2,
		Label: "GitLab OAuth",
		OAuth: &connector.OAuthConfig{Mode: connector.OAuthModeMCP},
	}},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://gitlab.com/api/v4/mcp",
		RequestTimeout: 30 * time.Second,
	},
}
