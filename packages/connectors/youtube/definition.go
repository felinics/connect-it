// Package youtube 是 YouTube Data API 的 Managed Connector。
package youtube

import (
	"encoding/json"

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

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client ID。",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Google Cloud OAuth 2.0 客户端的 Client Secret。",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Google OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
				TokenEndpoint:         "https://oauth2.googleapis.com/token",
				Scopes: []string{
					"https://www.googleapis.com/auth/youtube.readonly",
				},
				TokenEndpointAuth: connector.TokenAuthPost,
				ExtraAuthParams: map[string]string{
					"access_type": "offline",
					"prompt":      "consent",
				},
			},
		},
	},

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
