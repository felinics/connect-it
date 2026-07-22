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

// newConnServer 装配含假 OAuth provider 的完整服务栈。
func newConnServer(t *testing.T) *httptest.Server {
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
	return srv
}

func TestAPIKeyConnectionLifecycleHTTP(t *testing.T) {
	srv := newConnServer(t)
	h := adminLogin(t, srv)

	// 校验失败：alias 非法
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","alias":"BAD","fields":{"token":"x"}}`, h)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("非法 alias 应 422: %d %s", resp.StatusCode, body)
	}

	// 创建
	resp, body = doReq(t, http.MethodPost, srv.URL+"/admin/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","alias":"acct-1","fields":{"token":"tok-1"}}`, h)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	// 列表：包含 connection 且不含 credential
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/connections", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "acct-1") {
		t.Fatalf("列表: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "tok-1") {
		t.Fatalf("列表不得含 credential: %s", body)
	}

	// 重复 alias → 409
	resp, _ = doReq(t, http.MethodPost, srv.URL+"/admin/connections/api-key",
		`{"connector_type":"example_app","auth_method":"pat","alias":"acct-1","fields":{"token":"tok-2"}}`, h)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复 alias 应 409: %d", resp.StatusCode)
	}

	// 删除
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/connections/"+created.ID, "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("删除: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/connections/"+created.ID, "", h)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("重复删除应 404: %d", resp.StatusCode)
	}
}

func TestOAuthFlowHTTP(t *testing.T) {
	srv := newConnServer(t)
	h := adminLogin(t, srv)

	// 发起
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth","alias":"gh-main"}`, h)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("发起: %d %s", resp.StatusCode, body)
	}
	var started struct {
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("授权 URL 缺少 state: %s", started.AuthorizationURL)
	}

	// 回调（禁跟随重定向以断言 Location）
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	cbResp, err := client.Get(srv.URL + "/v1/oauth/callback?state=" + url.QueryEscape(state) + "&code=abc")
	if err != nil {
		t.Fatal(err)
	}
	cbResp.Body.Close()
	loc := cbResp.Header.Get("Location")
	if cbResp.StatusCode != http.StatusFound || !strings.Contains(loc, "connected=gh-main") {
		t.Fatalf("回调应 302 到 connected=gh-main: %d %s", cbResp.StatusCode, loc)
	}

	// 列表可见
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/connections", "", h)
	if !strings.Contains(body, "gh-main") || !strings.Contains(body, `"active"`) {
		t.Fatalf("connection 未出现: %s", body)
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 1 {
		t.Fatalf("解析列表: %v %s", err, body)
	}

	// reauth
	resp, body = doReq(t, http.MethodPost, srv.URL+"/admin/connections/"+list[0].ID+"/reauth", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "authorization_url") {
		t.Fatalf("reauth: %d %s", resp.StatusCode, body)
	}

	// provider 报错回调
	errResp, err := client.Get(srv.URL + "/v1/oauth/callback?error=access_denied")
	if err != nil {
		t.Fatal(err)
	}
	errResp.Body.Close()
	if loc := errResp.Header.Get("Location"); !strings.Contains(loc, "error=access_denied") {
		t.Fatalf("错误回调应带 error: %s", loc)
	}
}
