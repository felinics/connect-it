// Package googledocs defines the official Google Docs remote MCP connector.
package googledocs

import (
	"time"

	"github.com/felinics/connect-it/packages/connectors/internal/googleoauth"
	"github.com/felinics/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_docs",
	Name:                "Google Docs",
	Description:         "Google Docs online documents",
	Categories:          []string{"productivity"},
	HomepageURL:         "https://docs.google.com/document",
	IconURL:             "https://cdn.simpleicons.org/googledocs",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/documents",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://docsmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
