// Package youtube 是 YouTube Data API 的 Managed Connector。
package youtube

import (
	"encoding/json"

	"github.com/memohai/connect-it/packages/connectors/internal/googleoauth"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var Definition = connector.Definition{
	Type:                "youtube",
	Name:                "YouTube",
	Description:         "YouTube 视频与频道服务",
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
				Description: "获取当前授权用户的 YouTube 频道资料与统计信息。",
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
				Description: "列出当前授权用户创建的播放列表，每页最多 25 条。",
				InputSchema: pageInputSchema,
			},
			Handler: listMyPlaylists,
		},
		{
			Tool: mcp.Tool{
				Name:        "list_subscriptions",
				Title:       "List YouTube subscriptions",
				Description: "列出当前授权用户订阅的频道，每页最多 25 条。",
				InputSchema: pageInputSchema,
			},
			Handler: listSubscriptions,
		},
		{
			Tool: mcp.Tool{
				Name:        "search_videos",
				Title:       "Search YouTube videos",
				Description: "按关键词搜索 YouTube 视频，每页最多 25 条。",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "搜索关键词"
    },
    "page_token": {
      "type": "string",
      "description": "上一页结果返回的 nextPageToken"
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
      "description": "上一页结果返回的 nextPageToken"
    }
  },
  "additionalProperties": false
}`)
