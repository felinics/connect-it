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

// newTestServer 装配真实 service 栈（真库）＋Echo，返回 base URL 与 authsvc。
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
	cat := catalogsvc.New(q, reg, cfg)
	oauth := oauthsvc.New(q, reg, cfg, kr, http.DefaultClient, "http://connect.test")
	conns := connsvc.New(q, reg, kr)

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

// adminLogin 登录并返回带会话 cookie 的 header。
func adminLogin(t *testing.T, srv *httptest.Server) http.Header {
	t.Helper()
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/login",
		`{"username":"admin","password":"`+adminPassword+`"}`, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("登录失败: %d %s", resp.StatusCode, body)
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		t.Fatal("登录未下发 cookie")
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
		t.Fatalf("无 cookie 应 401: %d %s", resp.StatusCode, body)
	}
	// 伪造签名
	h := http.Header{}
	h.Set("Cookie", "connect_it_admin=admin|9999999999|deadbeef")
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/admin/connectors", "", h)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie 应 401: %d", resp.StatusCode)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/login",
		`{"username":"admin","password":"wrong"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "invalid_credentials") {
		t.Fatalf("错误密码应 401: %d %s", resp.StatusCode, body)
	}
}

func TestV1RequiresBearerToken(t *testing.T) {
	srv, auth := newTestServer(t)
	resp, _ := doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 应 401: %d", resp.StatusCode)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer cit_bogus")
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", h)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无效 token 应 401: %d", resp.StatusCode)
	}

	plaintext, _, err := auth.CreateAPIToken(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	h.Set("Authorization", "Bearer "+plaintext)
	resp, body := doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", h)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "example_app") {
		t.Fatalf("有效 token 应 200: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "needs_config") {
		t.Fatalf("未配置应 needs_config: %s", body)
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

	// 写入
	resp, body = doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/config",
		`{"public":{"client_id":"abc"},"secrets":{"client_secret":"shh-value"}}`, h)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "shh-value") {
		t.Fatalf("响应不得包含 secret 值: %s", body)
	}
	var view struct {
		SecretKeysSet []string `json:"secret_keys_set"`
		UpdatedAt     string   `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.SecretKeysSet) != 1 || view.SecretKeysSet[0] != "client_secret" {
		t.Fatalf("secret_keys_set 不符: %+v", view)
	}

	// if_match 冲突
	resp, body = doReq(t, http.MethodPut, srv.URL+"/admin/connectors/example_app/config",
		`{"public":{"client_id":"x"},"secrets":{},"if_match":"2000-01-01T00:00:00Z"}`, h)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"conflict"`) {
		t.Fatalf("过期 if_match 应 409: %d %s", resp.StatusCode, body)
	}

	// 读取
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/connectors/example_app/config", "", h)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, "shh-value") {
		t.Fatalf("get 不得含 secret: %d %s", resp.StatusCode, body)
	}

	// 删除
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/connectors/example_app/config", "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/admin/connectors/example_app/config", "", h)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删除后应 404: %d", resp.StatusCode)
	}
}

func TestAPITokenRoutes(t *testing.T) {
	srv, _ := newTestServer(t)
	h := adminLogin(t, srv)

	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/api-tokens", `{"name":"ci"}`, h)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(body, "cit_") {
		t.Fatalf("创建 token: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}

	// 用新 token 走 /v1
	th := http.Header{}
	th.Set("Authorization", "Bearer "+created.Token)
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", th)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("新 token 应可用: %d", resp.StatusCode)
	}

	// 列表不含明文
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/api-tokens", "", h)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, created.Token) {
		t.Fatalf("列表不得含明文: %d %s", resp.StatusCode, body)
	}

	// 撤销后失效
	resp, _ = doReq(t, http.MethodDelete, srv.URL+"/admin/api-tokens/"+created.ID, "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("撤销: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodGet, srv.URL+"/v1/connectors", "", th)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("撤销后应 401: %d", resp.StatusCode)
	}
}

func TestChangePassword(t *testing.T) {
	srv, _ := newTestServer(t)
	h := adminLogin(t, srv)

	resp, _ := doReq(t, http.MethodPut, srv.URL+"/admin/account/password", `{"password":"short"}`, h)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("短密码应 422: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodPut, srv.URL+"/admin/account/password", `{"password":"new-password-1"}`, h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("改密: %d", resp.StatusCode)
	}
	resp, _ = doReq(t, http.MethodPost, srv.URL+"/admin/login",
		`{"username":"admin","password":"new-password-1"}`, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("新密码应可登录: %d", resp.StatusCode)
	}
}
