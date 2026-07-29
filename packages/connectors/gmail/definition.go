// Package gmail holds the code-fixed Definition of the Gmail connector.
package gmail

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "gmail",
	Name:                "Gmail",
	Description:         "Google Gmail email service",
	Categories:          []string{"communication"},
	HomepageURL:         "https://mail.google.com",
	IconURL:             "https://cdn.simpleicons.org/gmail",
	ConfigSchemaVersion: 1,

	ConfigFields: googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/gmail.readonly",
		"https://www.googleapis.com/auth/gmail.compose",
	)},

	Implementation: connector.RemoteMCP{
		Endpoint:       "https://gmailmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
