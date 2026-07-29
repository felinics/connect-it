// Package googlepeople defines the official Google People remote MCP connector.
package googlepeople

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_people",
	Name:                "Google People",
	Description:         "Google profile, contacts and organization directory",
	Categories:          []string{"productivity"},
	HomepageURL:         "https://contacts.google.com",
	IconURL:             "https://cdn.simpleicons.org/google",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/directory.readonly",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/contacts.readonly",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://people.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
