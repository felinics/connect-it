// Package googlecalendar defines the official Google Calendar remote MCP connector.
package googlecalendar

import (
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
)

var Definition = connector.Definition{
	Type:                "google_calendar",
	Name:                "Google Calendar",
	Description:         "Google Calendar scheduling and calendar management",
	Categories:          []string{"productivity"},
	HomepageURL:         "https://calendar.google.com",
	IconURL:             "https://cdn.simpleicons.org/googlecalendar",
	ConfigSchemaVersion: 1,
	ConfigFields:        googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
		"https://www.googleapis.com/auth/calendar.events",
		"https://www.googleapis.com/auth/calendar.events.freebusy",
	)},
	Implementation: connector.RemoteMCP{
		Endpoint:       "https://calendarmcp.googleapis.com/mcp/v1",
		RequestTimeout: 30 * time.Second,
	},
}
