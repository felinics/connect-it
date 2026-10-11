package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/felinics/connect-it/packages/api"
	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/authsvc"
	"github.com/felinics/connect-it/packages/service/catalogsvc"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/connsvc"
	"github.com/felinics/connect-it/packages/service/oauthsvc"
	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

const (
	oauthClientIDValue     = "visible-client-id-7f3a"
	oauthClientSecretValue = "private-client-secret-9c1e"
)

type oauthConfigServer struct {
	srv    *httptest.Server
	bearer http.Header
	admin  http.Header
	logs   *bytes.Buffer
}

// newOAuthConfigServer starts the real stack with no connector config.
// github_like mirrors the GitHub definition: OAuth only, client fields
// required. hybrid_app also accepts a PAT, so its OAuth client fields are
// optional and an operator can save half of them.
func newOAuthConfigServer(t *testing.T) oauthConfigServer {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ab", 32))
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

	oauth := connector.AuthMethod{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
		AuthorizationEndpoint: "https://provider.example/authorize",
		TokenEndpoint:         provider.URL + "/token",
		TokenEndpointAuth:     connector.TokenAuthPost,
	}}
	clientFields := func(required bool) []connector.ConfigField {
		return []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: required},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: required, Secret: true},
		}
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type: "github_like", Name: "GitHub-like", ConfigSchemaVersion: 1,
		ConfigFields:   clientFields(true),
		AuthMethods:    []connector.AuthMethod{oauth},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})
	reg.MustRegister(connector.Definition{
		Type: "hybrid_app", Name: "Hybrid", ConfigSchemaVersion: 1,
		ConfigFields: clientFields(false),
		AuthMethods: []connector.AuthMethod{oauth, {
			Key: "pat", Type: connector.AuthAPIKey, Label: "PAT",
			CredentialFields: []connector.ConfigField{
				{Key: "token", Label: "Token", InputType: connector.InputText, Required: true},
			},
		}},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	auth := authsvc.New(q)
	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := api.New(api.Deps{
		Registry: reg, Store: q, Config: cfg, Catalog: catalogsvc.New(reg, cfg), Auth: auth,
		OAuth:        oauthsvc.New(q, reg, cfg, kr, provider.Client(), "http://connect.test"),
		Conns:        connsvc.New(q, reg, cfg, kr),
		CookieSecret: []byte("test-cookie-secret"),
	})
	logs := &bytes.Buffer{}
	e.Logger.SetOutput(logs)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	plaintext, _, err := auth.CreateAPIToken(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	bearer := http.Header{}
	bearer.Set("Authorization", "Bearer "+plaintext)
	return oauthConfigServer{srv: srv, bearer: bearer, admin: adminLogin(t, srv), logs: logs}
}

func (s oauthConfigServer) putConfig(t *testing.T, connectorType, body string) {
	t.Helper()
	resp, out := doReq(t, http.MethodPut, s.srv.URL+"/admin/connectors/"+connectorType+"/config", body, s.admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put config: %d %s", resp.StatusCode, out)
	}
}

func decodeError(t *testing.T, body string) api.ErrorResponse {
	t.Helper()
	var out api.ErrorResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("error body is not JSON: %s", body)
	}
	return out
}

func assertOAuthClientNotConfigured(t *testing.T, s oauthConfigServer, resp *http.Response, body string) {
	t.Helper()
	got := decodeError(t, body)
	if resp.StatusCode != http.StatusUnprocessableEntity || got.Error != "oauth_client_not_configured" {
		t.Fatalf("want 422 oauth_client_not_configured, got %d %s", resp.StatusCode, body)
	}
	for _, leaked := range []string{oauthClientIDValue, oauthClientSecretValue, "oauthsvc:"} {
		if strings.Contains(body, leaked) || strings.Contains(s.logs.String(), leaked) {
			t.Fatalf("%q escaped into the response or logs: %s | %s", leaked, body, s.logs.String())
		}
	}
}

func TestBeginOAuthReportsMissingOAuthClient(t *testing.T) {
	s := newOAuthConfigServer(t)

	resp, body := doReq(t, http.MethodGet, s.srv.URL+"/v1/connectors/github_like", "", s.bearer)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"needs_config"`) {
		t.Fatalf("an unconfigured connector should report needs_config: %d %s", resp.StatusCode, body)
	}

	for _, tc := range []struct {
		name, connectorType, config string
	}{
		{"no config", "github_like", ""},
		{"client_id only", "hybrid_app", `{"public":{"client_id":"` + oauthClientIDValue + `"},"secrets":{}}`},
		{"client_secret only", "hybrid_app", `{"public":{},"secrets":{"client_secret":"` + oauthClientSecretValue + `"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.config != "" {
				s.putConfig(t, tc.connectorType, tc.config)
			}
			resp, body := doReq(t, http.MethodPost, s.srv.URL+"/v1/connections/oauth",
				`{"connector_type":"`+tc.connectorType+`","auth_method":"oauth"}`, s.bearer)
			assertOAuthClientNotConfigured(t, s, resp, body)

			resp, body = doReq(t, http.MethodGet, s.srv.URL+"/admin/connections", "", s.admin)
			if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != "[]" {
				t.Fatalf("a refused authorization must not leave a connection behind: %d %s", resp.StatusCode, body)
			}
		})
	}
}

func TestBeginOAuthKeepsRequestValidationSeparate(t *testing.T) {
	s := newOAuthConfigServer(t)

	for _, tc := range []struct {
		name, body, code string
		status           int
	}{
		{"unknown auth method", `{"connector_type":"github_like","auth_method":"nope"}`, "validation_failed", http.StatusUnprocessableEntity},
		{"auth method is not OAuth", `{"connector_type":"hybrid_app","auth_method":"pat"}`, "validation_failed", http.StatusUnprocessableEntity},
		{"unknown connector", `{"connector_type":"nope","auth_method":"oauth"}`, "not_found", http.StatusNotFound},
		{"malformed body", `{"connector_type":`, "bad_request", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doReq(t, http.MethodPost, s.srv.URL+"/v1/connections/oauth", tc.body, s.bearer)
			if got := decodeError(t, body); resp.StatusCode != tc.status || got.Error != tc.code {
				t.Fatalf("want %d %s, got %d %s", tc.status, tc.code, resp.StatusCode, body)
			}
		})
	}
}

func TestOAuthResumesOnceClientConfigured(t *testing.T) {
	s := newOAuthConfigServer(t)
	begin := `{"connector_type":"github_like","auth_method":"oauth"}`

	resp, body := doReq(t, http.MethodPost, s.srv.URL+"/v1/connections/oauth", begin, s.bearer)
	assertOAuthClientNotConfigured(t, s, resp, body)

	s.putConfig(t, "github_like",
		`{"public":{"client_id":"`+oauthClientIDValue+`"},"secrets":{"client_secret":"`+oauthClientSecretValue+`"}}`)
	resp, body = doReq(t, http.MethodPost, s.srv.URL+"/v1/connections/oauth", begin, s.bearer)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("begin after configuring: %d %s", resp.StatusCode, body)
	}
	var started struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(started.AuthorizationURL)
	if err != nil || authURL.Query().Get("client_id") != oauthClientIDValue {
		t.Fatalf("authorization URL should carry the configured client: %s", started.AuthorizationURL)
	}
	callback, err := http.Get(s.srv.URL + "/v1/oauth/callback?code=abc&state=" + url.QueryEscape(authURL.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	_ = callback.Body.Close()
	resp, body = doReq(t, http.MethodGet, s.srv.URL+"/v1/connections/"+started.ConnectionID, "", s.bearer)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"active"`) {
		t.Fatalf("the connection should be active after the callback: %d %s", resp.StatusCode, body)
	}

	resp, body = doReq(t, http.MethodDelete, s.srv.URL+"/admin/connectors/github_like/config", "", s.admin)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete config: %d %s", resp.StatusCode, body)
	}
	for _, reauth := range []struct {
		path   string
		header http.Header
	}{
		{"/v1/connections/" + started.ConnectionID + "/reauth", s.bearer},
		{"/admin/connections/" + started.ConnectionID + "/reauth", s.admin},
	} {
		resp, body = doReq(t, http.MethodPost, s.srv.URL+reauth.path, "", reauth.header)
		assertOAuthClientNotConfigured(t, s, resp, body)
	}
	resp, body = doReq(t, http.MethodGet, s.srv.URL+"/v1/connections/"+started.ConnectionID, "", s.bearer)
	if !strings.Contains(body, `"active"`) {
		t.Fatalf("a refused reauth must keep the connection usable: %d %s", resp.StatusCode, body)
	}

	s.putConfig(t, "github_like",
		`{"public":{"client_id":"`+oauthClientIDValue+`"},"secrets":{"client_secret":"`+oauthClientSecretValue+`"}}`)
	resp, body = doReq(t, http.MethodPost, s.srv.URL+"/v1/connections/"+started.ConnectionID+"/reauth", "", s.bearer)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, started.ConnectionID) {
		t.Fatalf("reauth after configuring again: %d %s", resp.StatusCode, body)
	}
}
