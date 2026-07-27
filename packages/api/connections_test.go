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

// newConnServer 装配含假 OAuth provider 的完整服务栈，返回服务与一个 Bearer token 头。
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
	cat := catalogsvc.New(q, reg, cfg)
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

func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestV1APIKeyConnectionLifecycle(t *testing.T) {
	srv, bh := newConnServer(t)

	// 无鉴权 → 401
	resp, _ := doReq(t, http.MethodPost, srv.URL+"/v1/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","fields":{"token":"tok-1"}}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 应 401: %d", resp.StatusCode)
	}

	// 创建（无 alias）→ 返回持久 ID
	resp, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","fields":{"token":"tok-1"}}`, bh)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || created.ConnectionID == "" {
		t.Fatalf("应返回 connection_id: %s", body)
	}

	// 查询状态
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+created.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"active"`) {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "tok-1") {
		t.Fatalf("响应不得含 credential: %s", body)
	}

	// 删除
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/v1/connections/"+created.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+created.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后应 404: %d", resp.StatusCode)
	}
}

func TestV1OAuthFlowWithRedirect(t *testing.T) {
	srv, bh := newConnServer(t)

	// 发起：立即拿到 pending 连接 ID＋授权 URL
	resp, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth","alias":"user-42-gh","redirect_url":"https://saas.example/oauth/done"}`, bh)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("begin: %d %s", resp.StatusCode, body)
	}
	var begin struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil || begin.ConnectionID == "" {
		t.Fatalf("begin 应返回 connection_id: %s", body)
	}

	// 未完成授权前状态是 pending（SaaS 可轮询）
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+begin.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"pending"`) {
		t.Fatalf("pending 状态: %d %s", resp.StatusCode, body)
	}

	// 终端用户完成授权 → 回调 302 回 SaaS 登记的 redirect_url，带 connection_id
	u, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	cbResp, err := noRedirectClient().Get(srv.URL + "/v1/oauth/callback?state=" + url.QueryEscape(state) + "&code=abc")
	if err != nil {
		t.Fatal(err)
	}
	cbResp.Body.Close()
	loc := cbResp.Header.Get("Location")
	if cbResp.StatusCode != http.StatusFound ||
		!strings.HasPrefix(loc, "https://saas.example/oauth/done") ||
		!strings.Contains(loc, "status=connected") ||
		!strings.Contains(loc, "connection_id="+begin.ConnectionID) {
		t.Fatalf("回调应 302 回 redirect_url: %d %s", cbResp.StatusCode, loc)
	}

	// 状态变 active
	resp, body = doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+begin.ConnectionID, "", bh)
	if !strings.Contains(body, `"active"`) {
		t.Fatalf("回调后应 active: %s", body)
	}

	// reauth：同一 ID 再发起
	resp, body = doReq(t, http.MethodPost, srv.URL+"/v1/connections/"+begin.ConnectionID+"/reauth",
		`{"redirect_url":"https://saas.example/back"}`, bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, begin.ConnectionID) {
		t.Fatalf("reauth: %d %s", resp.StatusCode, body)
	}
}

func TestCallbackWithoutRedirectShowsPage(t *testing.T) {
	srv, bh := newConnServer(t)

	_, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth"}`, bh)
	var begin struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(begin.AuthorizationURL)
	state := u.Query().Get("state")

	cbResp, err := noRedirectClient().Get(srv.URL + "/v1/oauth/callback?state=" + url.QueryEscape(state) + "&code=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer cbResp.Body.Close()
	page := make([]byte, 4096)
	n, _ := cbResp.Body.Read(page)
	if cbResp.StatusCode != http.StatusOK || !strings.Contains(string(page[:n]), "授权完成") {
		t.Fatalf("无 redirect_url 应渲染完成页: %d %s", cbResp.StatusCode, page[:n])
	}
}

func TestOAuthProviderRejectionEndsPendingConnection(t *testing.T) {
	srv, bh := newConnServer(t)

	_, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth","redirect_url":"https://saas.example/oauth/done"}`, bh)
	var begin struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(begin.AuthorizationURL)
	state := u.Query().Get("state")

	cbResp, err := noRedirectClient().Get(srv.URL + "/v1/oauth/callback?state=" +
		url.QueryEscape(state) + "&error=access_denied")
	if err != nil {
		t.Fatal(err)
	}
	cbResp.Body.Close()
	location := cbResp.Header.Get("Location")
	if cbResp.StatusCode != http.StatusFound ||
		!strings.HasPrefix(location, "https://saas.example/oauth/done") ||
		!strings.Contains(location, "status=error") ||
		!strings.Contains(location, "code=access_denied") {
		t.Fatalf("拒绝后应回跳下游: %d %s", cbResp.StatusCode, location)
	}

	resp, body := doReq(t, http.MethodGet, srv.URL+"/v1/connections/"+begin.ConnectionID, "", bh)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"authorization_failed"`) {
		t.Fatalf("拒绝后 connection 不应保持 pending: %d %s", resp.StatusCode, body)
	}
}

func TestAdminConnectionsOpsView(t *testing.T) {
	srv, bh := newConnServer(t)
	h := adminLogin(t, srv)

	// 造一条连接
	_, body := doReq(t, http.MethodPost, srv.URL+"/v1/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","alias":"ops-1","fields":{"token":"tok"}}`, bh)
	var created struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	// 管理台可见
	resp, body := doReq(t, http.MethodGet, srv.URL+"/admin/connections", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "ops-1") {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}

	// 管理台创建入口已移除
	resp, _ = doReq(t, http.MethodPost, srv.URL+"/admin/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth"}`, h)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin 创建入口应已移除: %d", resp.StatusCode)
	}

	// 管理台删除
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/connections/"+created.ConnectionID, "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("admin delete: %d", resp.StatusCode)
	}
}
