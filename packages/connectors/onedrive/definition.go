// Package onedrive holds the code-fixed Definition and managed handlers of
// the OneDrive connector.
package onedrive

import (
	"encoding/json"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func strPtr(s string) *string { return &s }

var Definition = connector.Definition{
	Type:                "one_drive",
	Name:                "OneDrive",
	Description:         "Microsoft OneDrive cloud storage",
	Categories:          []string{"storage"},
	HomepageURL:         "https://onedrive.live.com",
	IconURL:             "https://api.iconify.design/logos:microsoft-onedrive.svg",
	ConfigSchemaVersion: 1,

	ConfigFields: []connector.ConfigField{
		{
			Key:         "client_id",
			Label:       "Client ID",
			InputType:   connector.InputText,
			Required:    true,
			Description: "Application (client) ID of the Microsoft Entra app registration.",
		},
		{
			Key:         "client_secret",
			Label:       "Client Secret",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Client secret of the Microsoft Entra app registration.",
		},
		{
			Key:          "tenant",
			Label:        "Tenant",
			InputType:    connector.InputText,
			Required:     true,
			DefaultValue: strPtr("common"),
			Description:  "Microsoft tenant: common, organizations, consumers, or a specific tenant ID.",
		},
	},

	AuthMethods: []connector.AuthMethod{
		{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "Microsoft OAuth",
			OAuth: &connector.OAuthConfig{
				// The {tenant} placeholder is replaced at runtime by
				// oauthsvc.ExpandEndpoint using the administrator config,
				// which defaults to common.
				AuthorizationEndpoint: "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
				TokenEndpoint:         "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
				// offline_access yields a refresh token; Files.ReadWrite covers both tools.
				Scopes:            []string{"offline_access", "Files.ReadWrite"},
				UsePKCE:           true,
				TokenEndpointAuth: connector.TokenAuthPost,
			},
		},
	},

	Implementation: connector.Managed{Tools: []connector.ManagedTool{
		{
			Tool: mcp.Tool{
				Name:        "list_drive_items",
				Title:       "List drive items",
				Description: "List files and subfolders in a OneDrive folder, defaulting to the root.",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Folder path relative to the OneDrive root; leave empty to list the root"
    }
  },
  "additionalProperties": false
}`),
			},
			Handler: listDriveItems,
		},
		{
			Tool: mcp.Tool{
				Name:        "upload_file",
				Title:       "Upload file",
				Description: "Upload a text file to a path in OneDrive via simple upload, up to 4MB.",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Target file path including the file name, relative to the OneDrive root"
    },
    "content": {
      "type": "string",
      "description": "File contents as UTF-8 text"
    }
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`),
			},
			Handler: uploadFile,
		},
	}},
}
