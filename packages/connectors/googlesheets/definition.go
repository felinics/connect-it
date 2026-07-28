// Package googlesheets defines the official Google Sheets remote MCP connector.
package googlesheets

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_sheets",
	Name:                "Google Sheets",
	Description:         "Google Sheets 在线表格",
	Categories:          []string{"productivity"},
	HomepageURL:         "https://docs.google.com/spreadsheets",
	IconURL:             "https://cdn.simpleicons.org/googlesheets",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/spreadsheets",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://sheetsmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
