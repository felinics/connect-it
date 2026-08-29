package googleads

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNormalizeCustomerID(t *testing.T) {
	for input, want := range map[string]string{
		"1234567890":   "1234567890",
		"123-456-7890": "1234567890",
		" 1234567890 ": "1234567890",
		"123":          "",
		"123456789x":   "",
	} {
		got, ok := normalizeCustomerID(input)
		if got != want || ok != (want != "") {
			t.Errorf("normalizeCustomerID(%q) = %q, %v", input, got, ok)
		}
	}
}

func TestSearchBuildsGoogleAdsRequest(t *testing.T) {
	useTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/customers/1234567890/googleAds:search" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access" ||
			r.Header.Get("developer-token") != "developer" ||
			r.Header.Get("login-customer-id") != "0987654321" {
			t.Errorf("headers=%v", r.Header)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["query"] != "SELECT campaign.id FROM campaign" || body["pageToken"] != "next" {
			t.Errorf("body=%v", body)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))

	result, err := search(context.Background(), connector.ManagedCall{
		Arguments: json.RawMessage(`{
			"customer_id":"123-456-7890",
			"login_customer_id":"098-765-4321",
			"query":"SELECT campaign.id FROM campaign",
			"page_token":"next"
		}`),
		Config:      map[string]any{"developer_token": "developer"},
		AccessToken: "access",
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGoogleAdsErrorMessage(t *testing.T) {
	useTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad query"}}`))
	}))

	result, err := listAccessibleCustomers(context.Background(), connector.ManagedCall{
		Config:      map[string]any{"developer_token": "developer"},
		AccessToken: "access",
	})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "400: bad query") {
		t.Fatalf("text=%q", text)
	}
}

func useTestServer(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	oldBaseURL, oldHTTPClient := apiBaseURL, apiHTTPClient
	apiBaseURL, apiHTTPClient = server.URL, server.Client()
	t.Cleanup(func() {
		apiBaseURL, apiHTTPClient = oldBaseURL, oldHTTPClient
		server.Close()
	})
}
