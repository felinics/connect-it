package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/api"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

// newConnServer wires the full service stack with a fake OAuth provider and
// returns the server plus a header carrying a Bearer token.
func newConnServer(t *testing.T) (*httptest.Server, http.Header) {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ee", 32))
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600,
		})
	}))
	t.Cleanup(provider.Close)

	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type: "example_app", Name: "Example", ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
		},
		AuthMethods: []connector.AuthMethod{
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://provider.example/authorize",
				TokenEndpoint:         provider.URL + "/token",
			}},
			{Key: "pat", Type: connector.AuthAPIKey, Label: "PAT",
				CredentialFields: []connector.ConfigField{
					{Key: "token", Label: "Token", InputType: connector.InputText, Required: true},
				}},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	auth := authsvc.New(q)
	cat := catalogsvc.New(reg, cfg)
	oauth := oauthsvc.New(q, reg, cfg, kr, provider.Client(), "http://connect.test")
	conns := connsvc.New(q, reg, kr)

	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Put(t.Context(), "example_app",
		map[string]any{"client_id": "cid"},
		map[string]string{"client_secret": "cs"}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	e := api.New(api.Deps{
		Registry: reg, Store: q, Config: cfg, Catalog: cat, Auth: auth,
		OAuth: oauth, Conns: conns,
		CookieSecret: []byte("test-cookie-secret"),
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	plaintext, _, err := auth.CreateAPIToken(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	bh := http.Header{}
	bh.Set("Authorization", "Bearer "+plaintext)
	return srv, bh
}

func TestV1APIKeyConnectionLifecycle(t *testing.T) {
	srv, bh := newConnServer(t)

	// Unauthenticated requests get 401.
	resp, _ := doReq(t, http.MethodPost, srv.URL+"/v1/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","fields":{"token":"tok-1"}}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no Bearer should give 401: %d", resp.StatusCode)
	}

	// Create without an alias returns the durable ID.
	resp, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","fields":{"token":"tok-1"}}`, bh)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ConnectionID == "" {
		t.Fatalf("expected a connection_id: %s", body)
	}

	// Query the status.
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+created.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"active"`) {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "tok-1") {
		t.Fatalf("the response must not contain a credential: %s", body)
	}

	// Delete.
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/v1/connections/"+created.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+created.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete it should give 404: %d", resp.StatusCode)
	}
}

func TestV1OAuthFlow(t *testing.T) {
	srv, bh := newConnServer(t)

	// Begin returns a pending connection ID and an authorization URL right away.
	resp, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth","alias":"user-42-gh"}`, bh)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("begin: %d %s", resp.StatusCode, body)
	}
	var begin struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil || begin.ConnectionID == "" {
		t.Fatalf("begin should return a connection_id: %s", body)
	}

	// Until the authorization completes the status stays pending, so a SaaS
	// caller can poll it.
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+begin.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"pending"`) {
		t.Fatalf("pending status: %d %s", resp.StatusCode, body)
	}

	// The end user finishes authorizing, connect-it shows its completion page,
	// and the downstream service keeps polling the connection status.
	u, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	cbResp, err := http.Get(srv.URL + "/v1/oauth/callback?state=" + url.QueryEscape(state) + "&code=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer cbResp.Body.Close()
	page := make([]byte, 4096)
	n, _ := cbResp.Body.Read(page)
	if cbResp.StatusCode != http.StatusOK || !strings.Contains(string(page[:n]), "Authorization complete") {
		t.Fatalf("the callback should render the completion page: %d %s", cbResp.StatusCode, page[:n])
	}

	// The status flips to active.
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+begin.ConnectionID, "", bh)
	if !strings.Contains(body, `"active"`) {
		t.Fatalf("it should be active after the callback: %s", body)
	}

	// reauth: start again on the same ID.
	resp, body = doReq(t, http.MethodPost, srv.URL+"/v1/connections/"+begin.ConnectionID+"/reauth",
		"", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, begin.ConnectionID) {
		t.Fatalf("reauth: %d %s", resp.StatusCode, body)
	}
}

func TestOAuthProviderRejectionEndsPendingConnection(t *testing.T) {
	srv, bh := newConnServer(t)

	_, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth"}`, bh)
	var begin struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(begin.AuthorizationURL)
	state := u.Query().Get("state")

	cbResp, err := http.Get(srv.URL + "/v1/oauth/callback?state=" +
		url.QueryEscape(state) + "&error=access_denied")
	if err != nil {
		t.Fatal(err)
	}
	defer cbResp.Body.Close()
	page := make([]byte, 4096)
	n, _ := cbResp.Body.Read(page)
	if cbResp.StatusCode != http.StatusOK ||
		!strings.Contains(string(page[:n]), "access_denied") {
		t.Fatalf("a denial should render the failure page: %d %s", cbResp.StatusCode, page[:n])
	}

	resp, body := doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+begin.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"authorization_failed"`) {
		t.Fatalf("a connection must not stay pending after a denial: %d %s", resp.StatusCode, body)
	}
}

func TestAdminConnectionsOpsView(t *testing.T) {
	srv, bh := newConnServer(t)
	h := adminLogin(t, srv)

	// Seed one connection.
	_, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","alias":"ops-1","fields":{"token":"tok"}}`, bh)
	var created struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	// Visible in the admin UI.
	resp, body := doReq(t, http.MethodGet, srv.URL+"/admin/connections", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "ops-1") {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}

	// The admin create endpoint has been removed.
	resp, _ = doReq(t, http.MethodPost, srv.URL+"/admin/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth"}`, h)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the admin create endpoint should be gone: %d", resp.StatusCode)
	}

	// Delete from the admin UI.
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/connections/"+created.ConnectionID, "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin delete: %d", resp.StatusCode)
	}
}
