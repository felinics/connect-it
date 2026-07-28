package catalogsvc

import "sort"

// preferredConnectorOrder is product-level presentation order. Only commonly
// used connectors need to be listed; everything else falls back to type order.
var preferredConnectorOrder = []string{
	"github",
	"google_drive",
	"gmail",
	"google_calendar",
	"notion",
	"slack",
	"linear",
	"google_sheets",
	"google_docs",
	"youtube",
	"one_drive",
	"dropbox",
	"asana",
	"airtable",
	"stripe",
	"hubspot",
	"intercom",
	"gitlab",
	"supabase",
	"google_slides",
	"google_chat",
	"google_people",
	"box",
	"monday",
	"postman",
	"cloudflare",
	"sentry",
	"datadog",
	"posthog",
	"google_ads",
	"openai",
	"anthropic",
	"shopify",
	"zendesk",
	"twilio",
	"mailchimp",
	"trello",
	"telegram",
	"sendgrid",
	"pagerduty",
	"grafana",
	"n8n",
	"databricks",
	"okta",
	"auth0",
}

var preferredConnectorRank = func() map[string]int {
	ranks := make(map[string]int, len(preferredConnectorOrder))
	for rank, connectorType := range preferredConnectorOrder {
		ranks[connectorType] = rank
	}
	return ranks
}()

func sortItems(items []Item) {
	sort.Slice(items, func(i, j int) bool {
		leftRank, leftPreferred := preferredConnectorRank[items[i].Type]
		rightRank, rightPreferred := preferredConnectorRank[items[j].Type]
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		if leftPreferred && leftRank != rightRank {
			return leftRank < rightRank
		}
		return items[i].Type < items[j].Type
	})
}
