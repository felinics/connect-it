// Package youtube is the managed connector for the YouTube Data API.
package youtube

import (
	"encoding/json"

	"github.com/felinics/connect-it/packages/connectors/internal/googleoauth"
	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var Definition = connector.Definition{
	Type:                "youtube",
	Name:                "YouTube",
	Description:         "YouTube video and channel service",
	Categories:          []string{"media"},
	HomepageURL:         "https://www.youtube.com",
	IconURL:             "https://cdn.simpleicons.org/youtube",
	ConfigSchemaVersion: 1,

	ConfigFields: googleoauth.ConfigFields(),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/youtube.readonly",
	)},

	Implementation: connector.Managed{Tools: []connector.ManagedTool{
		{
			Tool: mcp.Tool{
				Name:        "get_my_channel",
				Title:       "Get my YouTube channel",
				Description: "Get the authorized user's YouTube channel profile and statistics.",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`),
			},
			Handler: getMyChannel,
		},
		{
			Tool: mcp.Tool{
				Name:        "list_my_playlists",
				Title:       "List my YouTube playlists",
				Description: "List playlists created by the authorized user, up to 25 per page.",
				InputSchema: pageInputSchema,
			},
			Handler: listMyPlaylists,
		},
		{
			Tool: mcp.Tool{
				Name:        "list_subscriptions",
				Title:       "List YouTube subscriptions",
				Description: "List channels the authorized user subscribes to, up to 25 per page.",
				InputSchema: pageInputSchema,
			},
			Handler: listSubscriptions,
		},
		{
			Tool: mcp.Tool{
				Name:        "search_videos",
				Title:       "Search YouTube videos",
				Description: "Search YouTube videos by keyword, up to 25 per page.",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "Search keywords"
    },
    "page_token": {
      "type": "string",
      "description": "nextPageToken returned by the previous page of results"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`),
			},
			Handler: searchVideos,
		},
	}},
}

var pageInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "page_token": {
      "type": "string",
      "description": "nextPageToken returned by the previous page of results"
    }
  },
  "additionalProperties": false
}`)
