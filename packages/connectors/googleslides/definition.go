// Package googleslides defines the official Google Slides remote MCP connector.
package googleslides

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_slides",
	Name:                "Google Slides",
	Description:         "Google Slides online presentations",
	Categories:          []string{"productivity"},
	HomepageURL:         "https://docs.google.com/presentation",
	IconURL:             "https://cdn.simpleicons.org/googleslides",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/presentations",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://slidesmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
