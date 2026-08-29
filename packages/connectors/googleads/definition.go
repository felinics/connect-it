// Package googleads holds the code-fixed Definition and managed handlers of
// the Google Ads connector.
package googleads

import (
	"encoding/json"

	"github.com/felinics/connect-it/packages/connectors/internal/googleoauth"
	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var Definition = connector.Definition{
	Type:                "google_ads",
	Name:                "Google Ads",
	Description:         "Google Ads advertising platform",
	Categories:          []string{"advertising"},
	HomepageURL:         "https://ads.google.com",
	IconURL:             "https://cdn.simpleicons.org/googleads",
	ConfigSchemaVersion: 1,

	ConfigFields: append(googleoauth.ConfigFields(),
		connector.ConfigField{
			Key:         "developer_token",
			Label:       "Developer Token",
			InputType:   connector.InputText,
			Required:    true,
			Secret:      true,
			Description: "Google Ads API Developer Token。",
		},
	),
	AuthMethods: []connector.AuthMethod{googleoauth.Method(
		"https://www.googleapis.com/auth/adwords",
	)},

	Implementation: connector.Managed{Tools: []connector.ManagedTool{
		{
			Tool: mcp.Tool{
				Name:        "list_accessible_customers",
				Title:       "List accessible customers",
				Description: "List the Google Ads customers directly accessible to the authorized Google account.",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`),
			},
			Handler: listAccessibleCustomers,
		},
		{
			Tool: mcp.Tool{
				Name:        "search",
				Title:       "Search Google Ads",
				Description: "Run a GAQL query against one customer and return a single page of results.",
				InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "customer_id": {
      "type": "string",
      "description": "Google Ads customer ID: 10 digits, hyphens allowed"
    },
    "query": {
      "type": "string",
      "description": "Google Ads Query Language (GAQL) query"
    },
    "page_token": {
      "type": "string",
      "description": "nextPageToken returned by the previous page"
    },
    "login_customer_id": {
      "type": "string",
      "description": "Customer ID of the manager account, when access goes through one"
    }
  },
  "required": ["customer_id", "query"],
  "additionalProperties": false
}`),
			},
			Handler: search,
		},
	}},
}
