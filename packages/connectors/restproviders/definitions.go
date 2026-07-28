// Package restproviders contains managed providers backed by an official REST
// API. They share one constrained request tool; only metadata, origin and
// authentication differ between definitions.
package restproviders

import (
	"github.com/memohai/connect-it/packages/connectors/internal/managedapi"
	"github.com/memohai/connect-it/packages/core/connector"
)

type providerSpec struct {
	connectorType connector.Type
	name          string
	description   string
	categories    []string
	homepage      string
	icon          string
	fields        []connector.ConfigField
	baseURL       managedapi.BaseURL
	auth          managedapi.RequestAuth
	headers       map[string]string
	pathPrefix    managedapi.Value
}

// Definitions is the managed REST provider catalog.
var Definitions = []connector.Definition{
	apiKey(providerSpec{
		connectorType: "buildium", name: "Buildium", categories: []string{"property_management"},
		homepage: "https://www.buildium.com",
		fields: []connector.ConfigField{
			secret("client_secret", "Client Secret"),
			text("client_id", "Client ID"),
		},
		baseURL: managedapi.Fixed("https://api.buildium.com/v1"),
		auth: managedapi.Headers(map[string]managedapi.Value{
			"x-buildium-client-id":     managedapi.Credential("client_id"),
			"x-buildium-client-secret": managedapi.Credential("client_secret"),
		}),
	}),
	apiKey(providerSpec{
		connectorType: "mailgun", name: "Mailgun", categories: []string{"communication", "marketing"},
		homepage: "https://www.mailgun.com", icon: "mailgun",
		fields: []connector.ConfigField{
			secret("token", "Private API Key"),
			selectField("region", "Region", "us", "eu"),
		},
		baseURL: managedapi.Selected(managedapi.Credential("region"), map[string]string{
			"us": "https://api.mailgun.net/v3",
			"eu": "https://api.eu.mailgun.net/v3",
		}),
		auth: managedapi.Basic(managedapi.Literal("api"), managedapi.Token()),
	}),
	bearer("reply_io", "Reply.io", "https://reply.io", "reply", "sales", "communication",
		"https://api.reply.io"),
	apiKey(providerSpec{
		connectorType: "klaviyo", name: "Klaviyo", categories: []string{"marketing", "commerce"},
		homepage: "https://www.klaviyo.com",
		fields:   tokenField("Private API Key"),
		baseURL:  managedapi.Fixed("https://a.klaviyo.com"),
		auth:     managedapi.Authorization("Klaviyo-API-Key "),
		headers:  map[string]string{"revision": "2025-04-15"},
	}),
	apiKey(providerSpec{
		connectorType: "close", name: "Close", categories: []string{"crm", "sales"},
		homepage: "https://www.close.com",
		fields:   tokenField("API Key"),
		baseURL:  managedapi.Fixed("https://api.close.com/api/v1"),
		auth:     managedapi.Basic(managedapi.Token(), managedapi.Literal("")),
	}),
	apiKey(providerSpec{
		connectorType: "mailchimp", name: "Mailchimp", categories: []string{"marketing", "communication"},
		homepage: "https://mailchimp.com", icon: "mailchimp",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			dnsLabel("data_center", "Data Center"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("data_center"), "api.mailchimp.com", "/3.0"),
		auth:    managedapi.Basic(managedapi.Literal("connect"), managedapi.Token()),
	}),
	header("brevo", "Brevo", "https://www.brevo.com", "brevo", "api-key",
		"https://api.brevo.com/v3", "communication", "marketing"),
	apiKey(providerSpec{
		connectorType: "trello", name: "Trello", categories: []string{"productivity", "project_management"},
		homepage: "https://trello.com", icon: "trello",
		fields: []connector.ConfigField{
			secret("api_key", "API Key"),
			secret("api_token", "API Token"),
		},
		baseURL: managedapi.Fixed("https://api.trello.com/1"),
		auth: managedapi.Query(map[string]managedapi.Value{
			"key":   managedapi.Credential("api_key"),
			"token": managedapi.Credential("api_token"),
		}),
	}),
	bearer("apify", "Apify", "https://apify.com", "apify", "automation", "data",
		"https://api.apify.com/v2"),
	header("pipedrive", "Pipedrive", "https://www.pipedrive.com", "pipedrive", "x-api-token",
		"https://api.pipedrive.com", "crm", "sales"),
	apiKey(providerSpec{
		connectorType: "twilio", name: "Twilio", categories: []string{"communication"},
		homepage: "https://www.twilio.com",
		fields: []connector.ConfigField{
			secret("auth_token", "Auth Token"),
			text("account_sid", "Account SID"),
		},
		baseURL: managedapi.Fixed("https://api.twilio.com/2010-04-01"),
		auth: managedapi.Basic(
			managedapi.Credential("account_sid"),
			managedapi.Credential("auth_token"),
		),
	}),
	bearer("x", "X", "https://x.com", "x", "social", "communication",
		"https://api.x.com/2"),
	bearer("kustomer", "Kustomer", "https://www.kustomer.com", "", "customer_support", "crm",
		"https://api.kustomerapp.com/v1"),
	apiKey(providerSpec{
		connectorType: "lemlist", name: "lemlist", categories: []string{"sales", "marketing"},
		homepage: "https://www.lemlist.com",
		fields:   tokenField("API Key"),
		baseURL:  managedapi.Fixed("https://api.lemlist.com/api"),
		auth:     managedapi.Basic(managedapi.Literal(""), managedapi.Token()),
	}),
	apiKey(providerSpec{
		connectorType: "lob", name: "Lob", categories: []string{"communication"},
		homepage: "https://www.lob.com",
		fields:   tokenField("API Key"),
		baseURL:  managedapi.Fixed("https://api.lob.com/v1"),
		auth:     managedapi.Basic(managedapi.Token(), managedapi.Literal("")),
	}),
	bearer("recruitcrm", "Recruit CRM", "https://recruitcrm.io", "", "crm", "recruiting",
		"https://api.recruitcrm.io/v1"),
	bearer("bitly", "Bitly", "https://bitly.com", "bitly", "marketing", "developer_tools",
		"https://api-ssl.bitly.com/v4"),
	bearer("paddle", "Paddle", "https://www.paddle.com", "paddle", "payments", "commerce",
		"https://api.paddle.com"),
	bearer("beehiiv", "beehiiv", "https://www.beehiiv.com", "beehiiv", "marketing", "communication",
		"https://api.beehiiv.com/v2"),
	apiKey(providerSpec{
		connectorType: "cin7_core", name: "Cin7 Core", categories: []string{"commerce", "inventory"},
		homepage: "https://www.cin7.com/solutions/core",
		fields: []connector.ConfigField{
			secret("application_key", "Application Key"),
			text("account_id", "Account ID"),
		},
		baseURL: managedapi.Fixed("https://inventory.dearsystems.com/ExternalApi/v2"),
		auth: managedapi.Headers(map[string]managedapi.Value{
			"api-auth-accountid":      managedapi.Credential("account_id"),
			"api-auth-applicationkey": managedapi.Credential("application_key"),
		}),
	}),
	bearer("resend", "Resend", "https://resend.com", "resend", "communication", "developer_tools",
		"https://api.resend.com"),
	header("apollo", "Apollo", "https://www.apollo.io", "apollo", "x-api-key",
		"https://api.apollo.io", "sales", "crm"),
	authorization("shippo", "Shippo", "https://goshippo.com", "shippo", "ShippoToken ",
		"https://api.goshippo.com", "shipping", "commerce"),
	authorization("omnisend", "Omnisend", "https://www.omnisend.com", "omnisend", "Omnisend-API-Key ",
		"https://api.omnisend.com/api", "marketing", "communication"),
	header("exa", "Exa", "https://exa.ai", "exa", "x-api-key",
		"https://api.exa.ai", "search", "ai"),
	header("leadfeeder", "Leadfeeder", "https://www.leadfeeder.com", "", "X-Api-Key",
		"https://api.leadfeeder.com", "sales", "analytics"),
	header("jotform", "Jotform", "https://www.jotform.com", "jotform", "APIKEY",
		"https://api.jotform.com", "forms", "productivity"),
	bearer("dub", "Dub", "https://dub.co", "dub", "marketing", "analytics",
		"https://api.dub.co"),
	bearer("theirstack", "TheirStack", "https://theirstack.com", "", "sales", "data",
		"https://api.theirstack.com"),
	bearer("cyberimpact", "Cyberimpact", "https://www.cyberimpact.com", "", "marketing", "communication",
		"https://api.cyberimpact.com"),
	bearer("loops", "Loops", "https://loops.so", "loops", "marketing", "communication",
		"https://app.loops.so/api/v1"),
	bearer("folk", "folk", "https://folk.app", "folk", "crm", "sales",
		"https://api.folk.app"),
	apiKey(providerSpec{
		connectorType: "easypost", name: "EasyPost", categories: []string{"shipping", "commerce"},
		homepage: "https://www.easypost.com",
		fields:   tokenField("API Key"),
		baseURL:  managedapi.Fixed("https://api.easypost.com/v2"),
		auth:     managedapi.Basic(managedapi.Token(), managedapi.Literal("")),
	}),
	bearer("replicate", "Replicate", "https://replicate.com", "replicate", "ai", "developer_tools",
		"https://api.replicate.com"),
	bearer("sendgrid", "SendGrid", "https://sendgrid.com", "sendgrid", "communication", "marketing",
		"https://api.sendgrid.com/v3"),
	apiKey(providerSpec{
		connectorType: "algolia", name: "Algolia", categories: []string{"search", "developer_tools"},
		homepage: "https://www.algolia.com", icon: "algolia",
		fields: []connector.ConfigField{
			secret("api_key", "API Key"),
			dnsLabel("application_id", "Application ID"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("application_id"), "algolia.net", "/1"),
		auth: managedapi.Headers(map[string]managedapi.Value{
			"x-algolia-api-key":        managedapi.Credential("api_key"),
			"x-algolia-application-id": managedapi.Credential("application_id"),
		}),
	}),
	bearer("typefully", "Typefully", "https://typefully.com", "typefully", "social", "productivity",
		"https://api.typefully.com"),
	apiKey(providerSpec{
		connectorType: "mailjet", name: "Mailjet", categories: []string{"communication", "marketing"},
		homepage: "https://www.mailjet.com",
		fields: []connector.ConfigField{
			secret("api_secret", "API Secret"),
			text("api_key", "API Key"),
		},
		baseURL: managedapi.Fixed("https://api.mailjet.com/v3/REST"),
		auth: managedapi.Basic(
			managedapi.Credential("api_key"),
			managedapi.Credential("api_secret"),
		),
	}),
	bearer("webflow", "Webflow", "https://webflow.com", "webflow", "website", "cms",
		"https://api.webflow.com/v2"),
	apiKey(providerSpec{
		connectorType: "telegram", name: "Telegram", categories: []string{"communication", "social"},
		homepage: "https://telegram.org", icon: "telegram",
		fields:     tokenField("Bot Token"),
		baseURL:    managedapi.Fixed("https://api.telegram.org"),
		pathPrefix: managedapi.Affix("bot", managedapi.Token(), ""),
	}),
	bearer("ahrefs", "Ahrefs", "https://ahrefs.com", "ahrefs", "marketing", "analytics",
		"https://api.ahrefs.com/v3"),
	apiKey(providerSpec{
		connectorType: "semrush", name: "Semrush", categories: []string{"marketing", "analytics"},
		homepage: "https://www.semrush.com", icon: "semrush",
		fields:  tokenField("API Key"),
		baseURL: managedapi.Fixed("https://api.semrush.com"),
		auth:    managedapi.Query(map[string]managedapi.Value{"key": managedapi.Token()}),
	}),
	authorization("pagerduty", "PagerDuty", "https://www.pagerduty.com", "pagerduty", "Token token=",
		"https://api.pagerduty.com", "incident_management", "developer_tools"),
	apiKey(providerSpec{
		connectorType: "bigcommerce", name: "BigCommerce", categories: []string{"commerce"},
		homepage: "https://www.bigcommerce.com", icon: "bigcommerce",
		fields: []connector.ConfigField{
			secret("token", "Access Token"),
			dnsLabel("store_hash", "Store Hash"),
		},
		baseURL:    managedapi.Fixed("https://api.bigcommerce.com"),
		auth:       managedapi.Header("x-auth-token"),
		pathPrefix: managedapi.Affix("stores/", managedapi.Credential("store_hash"), "/v3"),
	}),
	bearer("instantly", "Instantly", "https://instantly.ai", "", "sales", "marketing",
		"https://api.instantly.ai/api/v2"),
	bearer("ship_bob", "ShipBob", "https://www.shipbob.com", "", "shipping", "commerce",
		"https://api.shipbob.com/2026-01"),
	header("ship_station", "ShipStation", "https://www.shipstation.com", "", "API-Key",
		"https://api.shipstation.com", "shipping", "commerce"),
	bearer("fathom_analytics", "Fathom Analytics", "https://usefathom.com", "fathom", "analytics",
		"https://api.usefathom.com"),
	bearer("holded", "Holded", "https://www.holded.com", "holded", "crm", "finance",
		"https://api.holded.com/api/v2"),
	bearer("store_leads", "Store Leads", "https://storeleads.app", "", "sales", "data",
		"https://storeleads.app/json/api/v1"),
	header("stay_ai", "Stay AI", "https://stay.ai", "", "x-retextion-access-token",
		"https://api.retextion.com/api/v2", "commerce", "marketing"),
	apiKey(providerSpec{
		connectorType: "help_scout", name: "Help Scout", categories: []string{"customer_support"},
		homepage: "https://www.helpscout.com", icon: "helpscout",
		fields:  tokenField("Docs API Key"),
		baseURL: managedapi.Fixed("https://docsapi.helpscout.net/v1"),
		auth:    managedapi.Basic(managedapi.Token(), managedapi.Literal("X")),
	}),
	header("leadmagic", "LeadMagic", "https://leadmagic.io", "", "X-API-Key",
		"https://api.leadmagic.io/v1", "sales", "data"),
	apiKey(providerSpec{
		connectorType: "smartlead", name: "Smartlead", categories: []string{"sales", "marketing"},
		homepage: "https://www.smartlead.ai",
		fields:   tokenField("API Key"),
		baseURL:  managedapi.Fixed("https://server.smartlead.ai/api/v1"),
		auth:     managedapi.Query(map[string]managedapi.Value{"api_key": managedapi.Token()}),
	}),
	bearer("wiza", "Wiza", "https://wiza.co", "", "sales", "data",
		"https://wiza.co"),
	bearer("twenty_crm", "Twenty CRM", "https://twenty.com", "twenty", "crm",
		"https://api.twenty.com"),
	bearer("typeform", "Typeform", "https://www.typeform.com", "typeform", "forms", "productivity",
		"https://api.typeform.com"),
	bearer("calendly", "Calendly", "https://calendly.com", "calendly", "scheduling", "productivity",
		"https://api.calendly.com"),
	apiKey(providerSpec{
		connectorType: "chargebee", name: "Chargebee", categories: []string{"payments", "finance"},
		homepage: "https://www.chargebee.com",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			dnsLabel("site", "Site"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("site"), "chargebee.com", "/api/v2"),
		auth:    managedapi.Basic(managedapi.Token(), managedapi.Literal("")),
	}),
	apiKey(providerSpec{
		connectorType: "adyen", name: "Adyen", categories: []string{"payments", "finance"},
		homepage: "https://www.adyen.com", icon: "adyen",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			selectField("environment", "Environment", "test", "live"),
		},
		baseURL: managedapi.Selected(managedapi.Credential("environment"), map[string]string{
			"test": "https://management-test.adyen.com/v3",
			"live": "https://management-live.adyen.com/v3",
		}),
		auth: managedapi.Header("x-api-key"),
	}),
	bearer("meta", "Meta", "https://business.meta.com", "meta", "social", "marketing",
		"https://graph.facebook.com/v25.0"),
	bearer("whatsapp", "WhatsApp", "https://www.whatsapp.com", "whatsapp", "communication", "social",
		"https://graph.facebook.com/v23.0"),
	apiKey(providerSpec{
		connectorType: "anthropic", name: "Anthropic", categories: []string{"ai", "developer_tools"},
		homepage: "https://www.anthropic.com", icon: "anthropic",
		fields:  tokenField("API Key"),
		baseURL: managedapi.Fixed("https://api.anthropic.com/v1"),
		auth:    managedapi.Header("x-api-key"),
		headers: map[string]string{"anthropic-version": "2023-06-01"},
	}),
	bearer("openai", "OpenAI", "https://openai.com/api", "openai", "ai", "developer_tools",
		"https://api.openai.com/v1"),
	header("gemini", "Gemini", "https://ai.google.dev/gemini-api", "googlegemini", "x-goog-api-key",
		"https://generativelanguage.googleapis.com/v1beta", "ai", "developer_tools"),
	bearer("perplexity", "Perplexity", "https://www.perplexity.ai", "perplexity", "ai", "search",
		"https://api.perplexity.ai"),
	bearer("cohere", "Cohere", "https://cohere.com", "cohere", "ai", "developer_tools",
		"https://api.cohere.com"),
	bearer("together_ai", "Together AI", "https://www.together.ai", "", "ai", "developer_tools",
		"https://api.together.ai/v1"),
	header("pinecone", "Pinecone", "https://www.pinecone.io", "pinecone", "api-key",
		"https://api.pinecone.io", "database", "ai"),
	apiKey(providerSpec{
		connectorType: "freshdesk", name: "Freshdesk", categories: []string{"customer_support"},
		homepage: "https://www.freshworks.com/freshdesk",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			dnsLabel("subdomain", "Subdomain"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("subdomain"), "freshdesk.com", ""),
		auth:    managedapi.Basic(managedapi.Token(), managedapi.Literal("X")),
	}),
	apiKey(providerSpec{
		connectorType: "shopify", name: "Shopify", categories: []string{"commerce"},
		homepage: "https://www.shopify.com", icon: "shopify",
		fields: []connector.ConfigField{
			secret("token", "Admin API Access Token"),
			dnsLabel("shop", "Shop"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("shop"), "myshopify.com", "/admin/api/2026-04"),
		auth:    managedapi.Header("x-shopify-access-token"),
	}),
	apiKey(providerSpec{
		connectorType: "zendesk", name: "Zendesk", categories: []string{"customer_support"},
		homepage: "https://www.zendesk.com", icon: "zendesk",
		fields: []connector.ConfigField{
			secret("token", "API Token"),
			text("email", "Email"),
			dnsLabel("subdomain", "Subdomain"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("subdomain"), "zendesk.com", "/api/v2"),
		auth: managedapi.Basic(
			managedapi.Affix("", managedapi.Credential("email"), "/token"),
			managedapi.Token(),
		),
	}),
	apiKey(providerSpec{
		connectorType: "gorgias", name: "Gorgias", categories: []string{"customer_support", "commerce"},
		homepage: "https://www.gorgias.com",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			text("email", "Email"),
			dnsLabel("subdomain", "Subdomain"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("subdomain"), "gorgias.com", "/api"),
		auth: managedapi.Basic(
			managedapi.Credential("email"),
			managedapi.Token(),
		),
	}),
	apiKey(providerSpec{
		connectorType: "freshsales", name: "Freshsales", categories: []string{"crm", "sales"},
		homepage: "https://www.freshworks.com/crm/sales",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			dnsLabel("bundle_alias", "Bundle Alias"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("bundle_alias"), "myfreshworks.com", "/crm/sales"),
		auth:    managedapi.Authorization("Token token="),
	}),
	apiKey(providerSpec{
		connectorType: "okta", name: "Okta", categories: []string{"identity", "security"},
		homepage: "https://www.okta.com", icon: "okta",
		fields: []connector.ConfigField{
			secret("token", "API Token"),
			dnsLabel("subdomain", "Subdomain"),
		},
		baseURL: managedapi.Subdomain(managedapi.Credential("subdomain"), "okta.com", "/api/v1"),
		auth:    managedapi.Authorization("SSWS "),
	}),
	apiKey(providerSpec{
		connectorType: "auth0", name: "Auth0", categories: []string{"identity", "security"},
		homepage: "https://auth0.com", icon: "auth0",
		fields: []connector.ConfigField{
			secret("token", "Management API Access Token"),
			hostname("domain", "Tenant Domain"),
		},
		baseURL: managedapi.Hostname(managedapi.Credential("domain"), "auth0.com", "/api/v2"),
		auth:    managedapi.Bearer(),
	}),
	apiKey(providerSpec{
		connectorType: "customer_io", name: "Customer.io", categories: []string{"marketing", "communication"},
		homepage: "https://customer.io",
		fields: []connector.ConfigField{
			secret("api_key", "Track API Key"),
			text("site_id", "Site ID"),
			selectField("region", "Region", "us", "eu"),
		},
		baseURL: managedapi.Selected(managedapi.Credential("region"), map[string]string{
			"us": "https://track.customer.io",
			"eu": "https://track-eu.customer.io",
		}),
		auth: managedapi.Basic(
			managedapi.Credential("site_id"),
			managedapi.Credential("api_key"),
		),
	}),
	apiKey(providerSpec{
		connectorType: "n8n", name: "n8n", categories: []string{"automation", "developer_tools"},
		homepage: "https://n8n.io", icon: "n8n",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			hostname("domain", "Instance Domain"),
		},
		baseURL: managedapi.Hostname(managedapi.Credential("domain"), "app.n8n.cloud", "/api/v1"),
		auth:    managedapi.Header("x-n8n-api-key"),
	}),
	apiKey(providerSpec{
		connectorType: "grafana", name: "Grafana", categories: []string{"monitoring", "developer_tools"},
		homepage: "https://grafana.com", icon: "grafana",
		fields: []connector.ConfigField{
			secret("token", "Service Account Token"),
			hostname("domain", "Stack Domain"),
		},
		baseURL: managedapi.Hostname(managedapi.Credential("domain"), "grafana.net", ""),
		auth:    managedapi.Bearer(),
	}),
	apiKey(providerSpec{
		connectorType: "databricks", name: "Databricks", categories: []string{"data", "developer_tools"},
		homepage: "https://www.databricks.com", icon: "databricks",
		fields: []connector.ConfigField{
			secret("token", "Personal Access Token"),
			hostname("domain", "Workspace Domain"),
		},
		baseURL: managedapi.Hostname(managedapi.Credential("domain"), "cloud.databricks.com", ""),
		auth:    managedapi.Bearer(),
	}),
	apiKey(providerSpec{
		connectorType: "weaviate", name: "Weaviate", categories: []string{"database", "ai"},
		homepage: "https://weaviate.io",
		fields: []connector.ConfigField{
			secret("token", "API Key"),
			hostname("domain", "Cluster Domain"),
		},
		baseURL: managedapi.Hostname(managedapi.Credential("domain"), "weaviate.network", ""),
		auth:    managedapi.Bearer(),
	}),
	apiKey(providerSpec{
		connectorType: "activecampaign", name: "ActiveCampaign", categories: []string{"marketing", "crm"},
		homepage: "https://www.activecampaign.com",
		fields: []connector.ConfigField{
			secret("token", "API Token"),
			hostname("domain", "API Domain"),
		},
		baseURL: managedapi.Hostname(managedapi.Credential("domain"), "api-us1.com", "/api/3"),
		auth:    managedapi.Header("Api-Token"),
	}),
}

func apiKey(spec providerSpec) connector.Definition {
	description := spec.description
	if description == "" {
		description = spec.name + " 官方 REST API"
	}
	iconURL := providerIconURLs[spec.connectorType]
	if iconURL == "" && spec.icon != "" {
		iconURL = simpleIconURL(spec.icon)
	}
	return connector.Definition{
		Type:                spec.connectorType,
		Name:                spec.name,
		Description:         description,
		Categories:          spec.categories,
		HomepageURL:         spec.homepage,
		IconURL:             iconURL,
		ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{
			Key:              "api_key",
			Type:             connector.AuthAPIKey,
			Label:            "API Credential",
			CredentialFields: spec.fields,
		}},
		Implementation: managedapi.Implementation(managedapi.Spec{
			ProviderName: spec.name,
			BaseURL:      spec.baseURL,
			Auth:         spec.auth,
			Headers:      spec.headers,
			PathPrefix:   spec.pathPrefix,
		}),
	}
}

func simpleIconURL(slug string) string {
	url := "https://cdn.simpleicons.org/" + slug
	switch slug {
	case "algolia", "anthropic", "bigcommerce", "replicate", "resend", "trello",
		"twenty", "typeform", "x", "zendesk":
		return url + "/_/e5e5e5"
	default:
		return url
	}
}

func bearer(
	connectorType connector.Type,
	name, homepage, icon string,
	category string,
	moreCategoriesAndBaseURL ...string,
) connector.Definition {
	if len(moreCategoriesAndBaseURL) == 0 {
		panic("restproviders: bearer provider is missing its base URL")
	}
	last := len(moreCategoriesAndBaseURL) - 1
	baseURL := moreCategoriesAndBaseURL[last]
	categories := append([]string{category}, moreCategoriesAndBaseURL[:last]...)
	return apiKey(providerSpec{
		connectorType: connectorType,
		name:          name,
		categories:    categories,
		homepage:      homepage,
		icon:          icon,
		fields:        tokenField("API Token"),
		baseURL:       managedapi.Fixed(baseURL),
		auth:          managedapi.Bearer(),
	})
}

func header(
	connectorType connector.Type,
	name, homepage, icon, headerName, baseURL string,
	categories ...string,
) connector.Definition {
	return apiKey(providerSpec{
		connectorType: connectorType,
		name:          name,
		categories:    categories,
		homepage:      homepage,
		icon:          icon,
		fields:        tokenField("API Key"),
		baseURL:       managedapi.Fixed(baseURL),
		auth:          managedapi.Header(headerName),
	})
}

func authorization(
	connectorType connector.Type,
	name, homepage, icon, prefix, baseURL string,
	categories ...string,
) connector.Definition {
	return apiKey(providerSpec{
		connectorType: connectorType,
		name:          name,
		categories:    categories,
		homepage:      homepage,
		icon:          icon,
		fields:        tokenField("API Token"),
		baseURL:       managedapi.Fixed(baseURL),
		auth:          managedapi.Authorization(prefix),
	})
}

func tokenField(label string) []connector.ConfigField {
	return []connector.ConfigField{secret("token", label)}
}

func secret(key, label string) connector.ConfigField {
	return connector.ConfigField{
		Key: key, Label: label, InputType: connector.InputText,
		Required: true, Secret: true,
	}
}

func text(key, label string) connector.ConfigField {
	return connector.ConfigField{
		Key: key, Label: label, InputType: connector.InputText, Required: true,
	}
}

func dnsLabel(key, label string) connector.ConfigField {
	field := text(key, label)
	field.Validation.Pattern = `^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`
	return field
}

func hostname(key, label string) connector.ConfigField {
	field := text(key, label)
	field.Validation.Pattern = `^[A-Za-z0-9.-]+$`
	return field
}

func selectField(key, label string, options ...string) connector.ConfigField {
	return connector.ConfigField{
		Key: key, Label: label, InputType: connector.InputSelect, Required: true,
		Validation: connector.FieldValidation{Options: options},
	}
}
