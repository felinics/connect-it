package connectors_test

import (
	"encoding/json"
	"testing"

	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/connectors/restproviders"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func definitions(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	connectors.RegisterAll(r)
	return r
}

func TestRegisterAllAndModes(t *testing.T) {
	r := definitions(t)
	const providerCount = 112
	want := map[connector.Type]connector.Mode{
		"airtable":        connector.ModeRemoteMCP,
		"asana":           connector.ModeRemoteMCP,
		"box":             connector.ModeRemoteMCP,
		"cloudflare":      connector.ModeRemoteMCP,
		"datadog":         connector.ModeRemoteMCP,
		"dropbox":         connector.ModeRemoteMCP,
		"github":          connector.ModeRemoteMCP,
		"gmail":           connector.ModeRemoteMCP,
		"gitlab":          connector.ModeRemoteMCP,
		"google_ads":      connector.ModeManaged,
		"google_calendar": connector.ModeRemoteMCP,
		"google_chat":     connector.ModeRemoteMCP,
		"google_docs":     connector.ModeRemoteMCP,
		"google_drive":    connector.ModeRemoteMCP,
		"google_people":   connector.ModeRemoteMCP,
		"google_sheets":   connector.ModeRemoteMCP,
		"google_slides":   connector.ModeRemoteMCP,
		"hubspot":         connector.ModeRemoteMCP,
		"intercom":        connector.ModeRemoteMCP,
		"linear":          connector.ModeRemoteMCP,
		"monday":          connector.ModeRemoteMCP,
		"notion":          connector.ModeRemoteMCP,
		"one_drive":       connector.ModeManaged,
		"posthog":         connector.ModeRemoteMCP,
		"postman":         connector.ModeRemoteMCP,
		"sentry":          connector.ModeRemoteMCP,
		"slack":           connector.ModeRemoteMCP,
		"stripe":          connector.ModeRemoteMCP,
		"supabase":        connector.ModeRemoteMCP,
		"youtube":         connector.ModeManaged,
	}
	for _, def := range restproviders.Definitions {
		want[def.Type] = connector.ModeManaged
	}
	if len(want) != providerCount || len(r.All()) != providerCount {
		t.Fatalf("got registry=%d expected-map=%d providers", len(r.All()), len(want))
	}
	for connectorType, mode := range want {
		def, ok := r.Get(connectorType)
		if !ok || def.Mode() != mode {
			t.Fatalf("%s: found=%v mode=%q", connectorType, ok, def.Mode())
		}
	}
}

func TestRemoteEndpoints(t *testing.T) {
	r := definitions(t)
	cases := map[connector.Type]string{
		"airtable":        "https://mcp.airtable.com/mcp",
		"asana":           "https://mcp.asana.com/v2/mcp",
		"box":             "https://mcp.box.com",
		"cloudflare":      "https://mcp.cloudflare.com/mcp",
		"dropbox":         "https://mcp.dropbox.com/mcp",
		"github":          "https://api.githubcopilot.com/mcp/",
		"gmail":           "https://gmailmcp.googleapis.com/mcp/v1",
		"gitlab":          "https://gitlab.com/api/v4/mcp",
		"google_calendar": "https://calendarmcp.googleapis.com/mcp/v1",
		"google_chat":     "https://chatmcp.googleapis.com/mcp/v1",
		"google_docs":     "https://docsmcp.googleapis.com/mcp/v1",
		"google_drive":    "https://drivemcp.googleapis.com/mcp/v1",
		"google_people":   "https://people.googleapis.com/mcp/v1",
		"google_sheets":   "https://sheetsmcp.googleapis.com/mcp/v1",
		"google_slides":   "https://slidesmcp.googleapis.com/mcp/v1",
		"hubspot":         "https://mcp.hubspot.com/",
		"intercom":        "https://mcp.intercom.com/mcp",
		"linear":          "https://mcp.linear.app/mcp",
		"monday":          "https://mcp.monday.com/mcp",
		"notion":          "https://mcp.notion.com/mcp",
		"posthog":         "https://mcp.posthog.com/mcp",
		"postman":         "https://mcp.postman.com/mcp",
		"sentry":          "https://mcp.sentry.dev/mcp",
		"slack":           "https://mcp.slack.com/mcp",
		"stripe":          "https://mcp.stripe.com",
		"supabase":        "https://mcp.supabase.com/mcp",
	}
	for connectorType, endpoint := range cases {
		def, _ := r.Get(connectorType)
		remote := def.Implementation.(connector.RemoteMCP)
		if remote.Endpoint != endpoint {
			t.Fatalf("%s endpoint=%q", connectorType, remote.Endpoint)
		}
	}
}

func TestSelectedRemoteEndpoints(t *testing.T) {
	r := definitions(t)
	def, _ := r.Get("datadog")
	remote := def.Implementation.(connector.RemoteMCP)
	if remote.EndpointSelector == nil ||
		remote.EndpointSelector.Endpoints["us1"] != "https://mcp.datadoghq.com/v1/mcp" ||
		remote.EndpointSelector.Endpoints["eu1"] != "https://mcp.datadoghq.eu/v1/mcp" {
		t.Fatalf("datadog endpoint selector=%+v", remote.EndpointSelector)
	}
}

func TestManagedTools(t *testing.T) {
	r := definitions(t)
	want := map[connector.Type]int{
		"one_drive":  2,
		"google_ads": 2,
		"youtube":    4,
	}
	for connectorType, toolCount := range want {
		def, _ := r.Get(connectorType)
		managed := def.Implementation.(connector.Managed)
		if len(managed.Tools) != toolCount {
			t.Fatalf("%s: got %d tools", connectorType, len(managed.Tools))
		}
		for _, managedTool := range managed.Tools {
			if managedTool.Handler == nil {
				t.Fatalf("%s: %s handler is nil", connectorType, managedTool.Tool.Name)
			}
			data, err := json.Marshal(managedTool.Tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(data, &schema); err != nil || schema["type"] != "object" {
				t.Fatalf("%s: %s schema=%s err=%v",
					connectorType, managedTool.Tool.Name, data, err)
			}
		}
	}
}
