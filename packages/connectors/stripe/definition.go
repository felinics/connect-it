// Package stripe exposes Stripe through its official Remote MCP server at
// https://mcp.stripe.com. There is no hand-written REST client here: every
// Tool is a fixed mapping onto one reviewed upstream tool name, and arguments
// pass through to the upstream server unchanged.
//
// Authentication is a bearer token. A Stripe API key is accepted directly as
// that token, and a Restricted API Key (rk_...) is strongly preferred over a
// full secret key: tool-level permission is NOT negotiated in the MCP
// protocol, so whatever the key grants is exactly what an Agent can reach.
// The only place to narrow it is the Stripe Dashboard.
//
// Limitation: Stripe Connect platform use requires an extra
// "Stripe-Account: acct_..." request header. MCPAuthBinding carries only a
// scheme plus a credential field and cannot attach additional headers, so
// connected sub-accounts are out of scope for now. One connection therefore
// speaks for exactly one Stripe account: the account owning the key.
//
// Tool selection. The upstream server publishes 13 tools; five are exposed.
//   - stripe_api_read / stripe_api_write / stripe_api_search /
//     stripe_api_details are a generic REST proxy over roughly 86 Stripe API
//     methods. The blueprint rules out generic proxy Tools, and
//     stripe_api_write is the clearest case for that rule: it would hand an
//     Agent one universal key to every write endpoint the credential permits,
//     with no per-capability grant, no reviewable per-Tool Risk, and no
//     argument surface a Definition could police. Deliberately not exposed.
//   - search_stripe_documentation, stripe_implementation_planner and
//     send_stripe_mcp_feedback are developer aids, not account capabilities.
//     Not exposed.
//   - stripe_report mixes report reads with report-run creation behind a
//     single undocumented schema, so no honest Risk can be assigned yet.
//     Deferred until an mcp-probe tools/list dump settles its shape.
//
// Schemas are loose approximations. Stripe publishes tool names but not tool
// input schemas, so properties list only what has been confirmed and every
// schema keeps "additionalProperties": true — Remote MCP arguments pass
// through and the upstream server is the authority. Check them against
// tools/list with mcp-probe (mcp:verify only reconciles tool names, never
// schemas). No Tool declares an OutputSchema: no captured
// response sample exists, and a wrong OutputSchema would make the Tool fail
// permanently at runtime.
package stripe

import (
	"encoding/json"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

const (
	stripeAuthMethod  = "api_key"
	stripeAPIKeyField = "api_key"

	stripeMCPEndpoint = "https://mcp.stripe.com"
	stripeMCPHostname = "mcp.stripe.com"
)

var Definition = connector.Definition{
	Type:                "stripe",
	Name:                "Stripe",
	Description:         "Stripe payments platform (via the official Remote MCP server)",
	Categories:          []string{"finance", "developer_tools"},
	HomepageURL:         "https://stripe.com",
	IconURL:             "https://cdn.simpleicons.org/stripe",
	ConfigSchemaVersion: 1,

	AuthMethods: []connector.AuthMethod{
		{
			Key:   stripeAuthMethod,
			Type:  connector.AuthAPIKey,
			Label: "Secret or restricted API key",
			CredentialFields: []connector.ConfigField{
				{
					Key:       stripeAPIKeyField,
					Label:     "Stripe API key",
					InputType: connector.InputText,
					Required:  true,
					Secret:    true,
					Description: "Opaque server-side Stripe API key, presented to the " +
						"official MCP server as a bearer token. Use a Restricted API Key " +
						"(rk_...) scoped in the Stripe Dashboard rather than a full secret " +
						"key (sk_...): MCP does not negotiate tool-level permissions, so " +
						"the key's own grants are the only limit on what an Agent reaches.",
					Validation: connector.FieldValidation{
						Pattern: `^[^:[:space:]]+$`,
					},
				},
			},
		},
	},

	RemoteMCPServers: []connector.RemoteMCPServer{
		{
			Key: "official",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    stripeMCPEndpoint,
			},
			AuthBinding: connector.MCPAuthBinding{
				Scheme: "bearer",
				CredentialFieldByAuthMethod: map[string]string{
					stripeAuthMethod: stripeAPIKeyField,
				},
			},
			Provenance: connector.Provenance{
				Kind:             connector.ProvenanceOfficial,
				AllowedHostnames: []string{stripeMCPHostname},
			},
			RequestTimeout: 30 * time.Second,
		},
	},

	// Tool IDs are the upstream tool names. RequiredScopes name Stripe
	// restricted-key permission units and are documentation only: a Stripe key
	// exposes no reliable permission introspection, so the validator always
	// reports ScopesKnown=false and nothing is preflighted against them. Tools
	// that legitimately span arbitrary resources declare none.
	Tools: []connector.Tool{
		{
			ID:          "get_stripe_account_info",
			Name:        "Get account info",
			Description: "Retrieve the Stripe account this credential authenticates.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			RequiredScopes: []string{"connected_account_read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "official",
				RemoteToolName: "get_stripe_account_info",
			},
		},
		{
			ID:   "get_balance_summary",
			Name: "Get balance summary",
			Description: "Summarize funds across the Stripe balance and Treasury accounts. " +
				"Public preview upstream; may be absent from tools/list for accounts " +
				"without Treasury.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			RequiredScopes: []string{"balance_read"},
			Risk:           connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "official",
				RemoteToolName: "get_balance_summary",
			},
		},
		{
			ID:   "search_stripe_resources",
			Name: "Search resources",
			Description: "Search Stripe objects through the Stripe Search API. Argument " +
				"names come from the upstream server; confirm schemas with mcp-probe.",
			// Upstream publishes no schema for this tool, and guessing a
			// property name would be worse than publishing none: arguments
			// pass through either way.
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk: connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "official",
				RemoteToolName: "search_stripe_resources",
			},
		},
		{
			ID:   "fetch_stripe_resources",
			Name: "Fetch resources",
			Description: "Fetch Stripe objects by identifier. Argument names come from " +
				"the upstream server; confirm schemas with mcp-probe.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": true
}`),
			Risk: connector.RiskRead,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "official",
				RemoteToolName: "fetch_stripe_resources",
			},
		},
		{
			ID:          "create_refund",
			Name:        "Create refund",
			Description: "Refund a PaymentIntent, fully or by amount in the smallest currency unit.",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "payment_intent": {
      "type": "string",
      "description": "ID of the PaymentIntent to refund"
    },
    "amount": {
      "type": "integer",
      "description": "Amount to refund in the smallest currency unit; omit to refund in full"
    }
  },
  "required": ["payment_intent"],
  "additionalProperties": true
}`),
			RequiredScopes: []string{"refund_write"},
			// Refunds move real money and cannot be undone.
			Risk: connector.RiskDestructive,
			Backend: connector.RemoteMCPBackend{
				ServerKey:      "official",
				RemoteToolName: "create_refund",
			},
		},
	},
}
