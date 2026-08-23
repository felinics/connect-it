package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

const adminPassword = "test-admin-pass"

// newTestServer wires the real service stack against a real database plus
// Echo, returning the base URL and the authsvc.
func newTestServer(t *testing.T) (*httptest.Server, *authsvc.Service) {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type: "example_app", Name: "Example", ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	auth := authsvc.New(q)
	cat := catalogsvc.New(reg, cfg)
	oauth := oauthsvc.New(q, reg, cfg, kr, http.DefaultClient, "http://connect.test")
	conns := connsvc.New(q, reg, cfg, kr)

	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}

	e := api.New(api.Deps{
		Registry: reg, Store: q, Config: cfg, Catalog: cat, Auth: auth,
		OAuth: oauth, Conns: conns,
		CookieSecret: []byte("test-cookie-secret"),
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv, auth
}

func doReq(t *testing.T, method, url, body string, header http.Header) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(data)
}

// adminLogin logs in and returns a header carrying the session cookie.
func adminLogin(t *testing.T, srv *httptest.Server) http.Header {
	t.Helper()
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/login",
		`{"username":"admin","password":"`+adminPassword+`"}`, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login failed: %d %s", resp.StatusCode, body)
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		t.Fatal("login did not set a cookie")
	}
	h := http.Header{}
	h.Set("Cookie", strings.Split(cookies[0], ";")[0])
	return h
}

func TestHealthz(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, body := doReq(t, http.MethodGet, srv.URL+"/healthz", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("healthz: %d %s", resp.StatusCode, body)
	}
}

func TestAdminRoutesRequireSession(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, body := doReq(t, http.MethodGet, srv.URL+"/admin/connectors", "", nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"unauthorized"`) {
		t.Fatalf("no cookie should give 401: %d %s", resp.StatusCode, body)
	}
	// Forged signature.
	h := http.Header{}
	h.Set("Cookie", "connect_it_admin=admin|9999999999|deadbeef")
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/admin/connectors", "", h)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged cookie should give 401: %d", resp.StatusCode)
	}
}

func TestOAuthRedirectURL(t *testing.T) {
	srv, _ := newTestServer(t)
	h := adminLogin(t, srv)

	resp, body := doReq(t, http.MethodGet, srv.URL+"/admin/oauth/redirect-url", "", h)
	if resp.StatusCode != http.StatusOK || body != `{"redirect_url":"http://connect.test/v1/oauth/callback"}`+"\n" {
		t.Fatalf("redirect URL: %d %s", resp.StatusCode, body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/login",
		`{"username":"admin","password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "invalid_credentials") {
		t.Fatalf("a wrong password should give 401: %d %s", resp.StatusCode, body)
	}
}

func TestV1RequiresBearerToken(t *testing.T) {
	srv, auth := newTestServer(t)
	resp, _ := doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no Bearer should give 401: %d", resp.StatusCode)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer cit_bogus")
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", h)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an invalid token should give 401: %d", resp.StatusCode)
	}

	plaintext, _, err := auth.CreateAPIToken(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	h.Set("Authorization", "Bearer "+plaintext)
	resp, body := doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "example_app") {
		t.Fatalf("a valid token should give 200: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "needs_config") {
		t.Fatalf("an unconfigured connector should be needs_config: %s", body)
	}
}

func TestConnectorEnabledLifecycle(t *testing.T) {
	srv, auth := newTestServer(t)
	adminHeader := adminLogin(t, srv)
	token, _, err := auth.CreateAPIToken(t.Context(), "enabled-test")
	if err != nil {
		t.Fatal(err)
	}
	apiHeader := http.Header{}
	apiHeader.Set("Authorization", "Bearer "+token)

	resp, body := doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/enabled",
		`{"enabled":false}`, adminHeader)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"enabled":false`) ||
		!strings.Contains(body, `"status":"disabled"`) {
		t.Fatalf("disable connector: %d %s", resp.StatusCode, body)
	}
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/connectors", "", adminHeader)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"type":"example_app"`) ||
		!strings.Contains(body, `"enabled":false`) {
		t.Fatalf("admin catalog should retain disabled connectors: %d %s", resp.StatusCode, body)
	}
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", apiHeader)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "example_app") {
		t.Fatalf("downstream catalog should omit disabled connectors: %d %s", resp.StatusCode, body)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors/example_app", "", apiHeader)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("downstream get should hide a disabled connector: %d", resp.StatusCode)
	}

	resp, body = doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/enabled",
		`{"enabled":true}`, adminHeader)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"enabled":true`) {
		t.Fatalf("re-enable connector: %d %s", resp.StatusCode, body)
	}
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", apiHeader)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "example_app") {
		t.Fatalf("re-enabled connector should return downstream: %d %s", resp.StatusCode, body)
	}

	resp, _ = doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/enabled", `{}`, adminHeader)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("enabled is required: %d", resp.StatusCode)
	}
}

func TestConfigLifecycle(t *testing.T) {
	srv, _ := newTestServer(t)
	h := adminLogin(t, srv)

	// schema
	resp, body := doReq(t, http.MethodGet, srv.URL+"/admin/connectors/example_app/config-schema", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"client_secret"`) {
		t.Fatalf("schema: %d %s", resp.StatusCode, body)
	}

	// Write.
	resp, body = doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/config",
		`{"public":{"client_id":"abc"},"secrets":{"client_secret":"shh-value"}}`, h)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "shh-value") {
		t.Fatalf("the response must not contain secret values: %s", body)
	}
	var view struct {
		SecretKeysSet []string `json:"secret_keys_set"`
		UpdatedAt     string   `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.SecretKeysSet) != 1 || view.SecretKeysSet[0] != "client_secret" {
		t.Fatalf("unexpected secret_keys_set: %+v", view)
	}

	// if_match conflict.
	resp, body = doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/config",
		`{"public":{"client_id":"x"},"secrets":{},"if_match":"2000-01-01T00:00:00Z"}`, h)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"conflict"`) {
		t.Fatalf("a stale if_match should give 409: %d %s", resp.StatusCode, body)
	}

	// Read.
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/connectors/example_app/config", "", h)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "shh-value") {
		t.Fatalf("get must not include secrets: %d %s", resp.StatusCode, body)
	}

	// Delete.
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/connectors/example_app/config", "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/admin/connectors/example_app/config", "", h)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete it should give 404: %d", resp.StatusCode)
	}
}

func TestAPITokenRoutes(t *testing.T) {
	srv, _ := newTestServer(t)
	h := adminLogin(t, srv)

	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/api-tokens", `{"name":"ci"}`, h)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(body, "cit_") {
		t.Fatalf("create token: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	// Use the new token against /v1.
	th := http.Header{}
	th.Set("Authorization", "Bearer "+created.Token)
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", th)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the new token should work: %d", resp.StatusCode)
	}

	// The list must not contain plaintext.
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/api-tokens", "", h)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, created.Token) {
		t.Fatalf("the list must not contain plaintext: %d %s", resp.StatusCode, body)
	}

	// Revoking invalidates it.
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/api-tokens/"+created.ID, "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", th)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after revoke it should give 401: %d", resp.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	srv, _ := newTestServer(t)
	h := adminLogin(t, srv)

	resp, _ := doReq(t, http.MethodPut, srv.URL+"/admin/account/password", `{"password":"short"}`, h)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a short password should give 422: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodPut, srv.URL+"/admin/account/password", `{"password":"new-password-1"}`, h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("change password: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodPost, srv.URL+"/admin/login",
		`{"username":"admin","password":"new-password-1"}`, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("the new password should log in: %d", resp.StatusCode)
	}
}
