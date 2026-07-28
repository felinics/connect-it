// Package googledrive defines the official Google Drive remote MCP connector.
package googledrive

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_drive",
	Name:                "Google Drive",
	Description:         "Google Drive 云存储与文件协作",
	Categories:          []string{"storage", "productivity"},
	HomepageURL:         "https://drive.google.com",
	IconURL:             "https://cdn.simpleicons.org/googledrive",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/drive.file",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://drivemcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
