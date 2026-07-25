package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	srv, bearer, _, _ := newConnServerWithOAuth(t)
	return srv, bearer
}

func newConnServerWithOAuth(
	t *testing.T,
) (*httptest.Server, http.Header, *pgxpool.Pool, *oauthsvc.Service) {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ee", 32))
	if err != nil {
		t.Fatal(err)
	}
	provider := testutil.NewProviderServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer",
			"refresh_token": "rt", "expires_in": 3600,
		})
	}))

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
				TokenEndpoint:         provider.BaseURL + "/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://provider.example:443"},
					TokenOrigins:         []string{provider.Origin},
				},
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
	validators := connector.CredentialValidatorMap{
		"example_app": {
			"oauth": func(
				context.Context,
				connector.CredentialValidationInput,
			) (connector.CredentialValidationResult, error) {
				return connector.CredentialValidationResult{}, nil
			},
			"pat": func(
				context.Context,
				connector.CredentialValidationInput,
			) (connector.CredentialValidationResult, error) {
				return connector.CredentialValidationResult{}, nil
			},
		},
	}
	oauth := oauthsvc.New(
		q,
		reg,
		cfg,
		kr,
		provider.Factory,
		"http://connect.test",
		connector.AuthorizationRuntime{
			CredentialValidators: validators,
		},
	)
	conns := connsvc.New(q, reg, kr, cfg, validators, nil)

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
		Registry: reg, Config: cfg, Catalog: cat, Auth: auth,
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
	return srv, bh, pool, oauth
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

func TestProviderErrorCleanupFailureIsNotReportedAsAuthorizationDenied(
	t *testing.T,
) {
	srv, bearer, pool, oauth := newConnServerWithOAuth(t)
	ctx := context.Background()
	resp, body := doReq(
		t,
		http.MethodPost,
		srv.URL+"/v1/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth","redirect_url":"https://saas.example/oauth/done"}`,
		bearer,
	)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("begin: %d %s", resp.StatusCode, body)
	}
	var begin struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationURL.Query().Get("state")

	if _, err := pool.Exec(
		ctx,
		`create function reject_api_oauth_cleanup() returns trigger
		 language plpgsql as $$
		 begin
		   raise exception 'injected API OAuth cleanup failure';
		 end
		 $$`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`create trigger reject_api_oauth_cleanup
		 before delete on connections
		 for each row execute function reject_api_oauth_cleanup()`,
	); err != nil {
		t.Fatal(err)
	}

	callbackResponse, err := noRedirectClient().Get(
		srv.URL + "/v1/oauth/callback?state=" + url.QueryEscape(state) +
			"&error=access_denied&error_description=" +
			url.QueryEscape("provider-controlled-secret"),
	)
	if err != nil {
		t.Fatal(err)
	}
	callbackResponse.Body.Close()
	location, err := url.Parse(callbackResponse.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if callbackResponse.StatusCode != http.StatusFound ||
		location.Query().Get("code") != "oauth_failed" ||
		location.Query().Get("code") == "authorization_denied" ||
		strings.Contains(location.String(), "provider-controlled-secret") {
		t.Fatalf(
			"cleanup failure redirect status=%d location=%s",
			callbackResponse.StatusCode,
			location.String(),
		)
	}

	if _, err := pool.Exec(
		ctx,
		`update oauth_authorizations
		 set expires_at = CURRENT_TIMESTAMP - interval '3 minutes'
		 where connection_id = $1`,
		begin.ConnectionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`drop trigger reject_api_oauth_cleanup on connections`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`drop function reject_api_oauth_cleanup()`,
	); err != nil {
		t.Fatal(err)
	}
	if err := oauth.MaintainAuthorizations(ctx); err != nil {
		t.Fatalf("OAuth janitor recovery failed: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(
		ctx,
		`select count(*) from connections where id = $1`,
		begin.ConnectionID,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("cleanup recovery left %d pending Connection(s)", remaining)
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

func TestAdminConnectionLifecycle(t *testing.T) {
	srv, bh := newConnServer(t)
	h := adminLogin(t, srv)

	const (
		firstToken  = "admin-secret-v1"
		secondToken = "admin-secret-v2"
	)
	apiKeyRequest := `{"connector_type":"example_app","auth_method":"pat",` +
		`"alias":"ops-api","fields":{"token":"` + firstToken + `"}}`

	// 三个管理台写入口都必须使用管理会话；Bearer token 不能替代 cookie。
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{
			http.MethodPost,
			"/admin/connections/oauth",
			`{"connector_type":"example_app","auth_method":"oauth"}`,
		},
		{
			http.MethodPost,
			"/admin/connections/api-key",
			apiKeyRequest,
		},
		{
			http.MethodPut,
			"/admin/connections/00000000-0000-0000-0000-000000000000/credential",
			`{"fields":{"token":"replacement"}}`,
		},
	} {
		resp, body := doReq(t, tc.method, srv.URL+tc.path, tc.body, bh)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf(
				"%s %s 只有 Bearer 时应 401: %d %s",
				tc.method,
				tc.path,
				resp.StatusCode,
				body,
			)
		}
	}

	// 反向也保持隔离：管理会话不能绕过 /v1 的 Bearer 门禁。
	resp, body := doReq(
		t,
		http.MethodPost,
		srv.URL+"/v1/connections/api-key",
		apiKeyRequest,
		h,
	)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie 不得替代 /v1 Bearer: %d %s", resp.StatusCode, body)
	}

	// Cookie 本身不足以执行写请求；缺少同源自定义 header 必须在绑定
	// credential 之前 fail closed。GET 列表仍是安全方法。
	withoutCSRF := h.Clone()
	withoutCSRF.Del("X-Connect-It-CSRF")
	resp, body = doReq(
		t,
		http.MethodPost,
		srv.URL+"/admin/connections/api-key",
		apiKeyRequest,
		withoutCSRF,
	)
	if resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(body, `"csrf_failed"`) {
		t.Fatalf("缺少 CSRF header 应 403: %d %s", resp.StatusCode, body)
	}
	resp, body = doReq(
		t,
		http.MethodGet,
		srv.URL+"/admin/connections",
		"",
		withoutCSRF,
	)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, firstToken) {
		t.Fatalf("安全 GET 不应要求 CSRF header: %d %s", resp.StatusCode, body)
	}

	// 管理台创建 API-key 连接，只返回持久 ID，不回显 credential。
	resp, body = doReq(
		t,
		http.MethodPost,
		srv.URL+"/admin/connections/api-key",
		apiKeyRequest,
		h,
	)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin API-key create: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, firstToken) {
		t.Fatalf("创建响应不得含 credential: %s", body)
	}
	var createdAPIKey struct {
		ConnectionID string `json:"connection_id"`
	}
	if err := json.Unmarshal([]byte(body), &createdAPIKey); err != nil ||
		createdAPIKey.ConnectionID == "" {
		t.Fatalf("admin API-key create 应返回 connection_id: %s", body)
	}

	// 列表展示连接元数据，但不得包含 credential。
	resp, body = doReq(t, http.MethodGet, srv.URL+"/admin/connections", "", h)
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(body, "ops-api") ||
		strings.Contains(body, firstToken) {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}

	// 换密是整组 PUT：缺少 required 字段必须拒绝。
	resp, body = doReq(
		t,
		http.MethodPut,
		srv.URL+"/admin/connections/"+createdAPIKey.ConnectionID+"/credential",
		`{"fields":{}}`,
		h,
	)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("不完整 credential 组应 422: %d %s", resp.StatusCode, body)
	}

	// 完整组验证通过后原 ID 不变，且新旧 credential 都不回显。
	resp, body = doReq(
		t,
		http.MethodPut,
		srv.URL+"/admin/connections/"+createdAPIKey.ConnectionID+"/credential",
		`{"fields":{"token":"`+secondToken+`"}}`,
		h,
	)
	if resp.StatusCode != http.StatusOK ||
		!strings.Contains(body, createdAPIKey.ConnectionID) {
		t.Fatalf("admin recredential: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, firstToken) || strings.Contains(body, secondToken) {
		t.Fatalf("换密响应不得含 credential: %s", body)
	}

	// 管理台也能发起 OAuth；其响应只含连接 ID 和授权 URL。
	resp, body = doReq(
		t,
		http.MethodPost,
		srv.URL+"/admin/connections/oauth",
		`{"connector_type":"example_app","auth_method":"oauth","alias":"ops-oauth",`+
			`"redirect_url":"https://attacker.example/redirect"}`,
		h,
	)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("admin OAuth begin: %d %s", resp.StatusCode, body)
	}
	var createdOAuth struct {
		ConnectionID     string `json:"connection_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal([]byte(body), &createdOAuth); err != nil ||
		createdOAuth.ConnectionID == "" ||
		createdOAuth.AuthorizationURL == "" {
		t.Fatalf("admin OAuth begin 响应不完整: %s", body)
	}
	if strings.Contains(body, firstToken) || strings.Contains(body, secondToken) {
		t.Fatalf("OAuth 响应不得含其他连接 credential: %s", body)
	}
	authorizationURL, err := url.Parse(createdOAuth.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	state := authorizationURL.Query().Get("state")
	callbackResponse, err := noRedirectClient().Get(
		srv.URL + "/v1/oauth/callback?state=" + url.QueryEscape(state) + "&code=abc",
	)
	if err != nil {
		t.Fatal(err)
	}
	callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusOK ||
		callbackResponse.Header.Get("Location") != "" {
		t.Fatalf(
			"admin OAuth 必须固定回完成页，不得接受 caller redirect: %d %q",
			callbackResponse.StatusCode,
			callbackResponse.Header.Get("Location"),
		)
	}

	// 管理台可删除两类连接。
	for _, connectionID := range []string{
		createdAPIKey.ConnectionID,
		createdOAuth.ConnectionID,
	} {
		resp, body = doReq(
			t,
			http.MethodDelete,
			srv.URL+"/admin/connections/"+connectionID,
			"",
			h,
		)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("admin delete %s: %d %s", connectionID, resp.StatusCode, body)
		}
	}
}
