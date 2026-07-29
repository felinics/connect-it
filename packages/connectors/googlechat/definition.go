// Package googlechat defines the official Google Chat remote MCP connector.
package googlechat

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_chat",
	Name:                "Google Chat",
	Description:         "Google Chat team messaging service",
	Categories:          []string{"communication"},
	HomepageURL:         "https://chat.google.com",
	IconURL:             "https://cdn.simpleicons.org/googlechat",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/chat.spaces.readonly",
		"https://www.googleapis.com/auth/chat.memberships.readonly",
		"https://www.googleapis.com/auth/chat.messages.readonly",
		"https://www.googleapis.com/auth/chat.messages.create",
		"https://www.googleapis.com/auth/chat.users.readstate.readonly",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://chatmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
