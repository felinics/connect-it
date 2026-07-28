package oauthsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
	"github.com/memohai/connect-it/packages/service/tokens"
)

type testEnv struct {
	svc      *oauthsvc.Service
	q        *store.Queries
	pool     *pgxpool.Pool
	tokenHit *atomic.Int64
	lastForm url.Values
}

// newEnv 起假 provider（token endpoint），装配 oauthsvc；配置已写好 client 凭证。
func newEnv(t *testing.T, usePKCE bool) *testEnv {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("aa", 32))
	if err != nil {
		t.Fatal(err)
	}

	env := &testEnv{tokenHit: &atomic.Int64{}, pool: pool}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.tokenHit.Add(1)
		_ = r.ParseForm()
		env.lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600,
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
				Scopes:                []string{"read", "write"},
				UsePKCE:               usePKCE,
			}},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	if _, err := cfg.Put(context.Background(), "example_app",
		map[string]any{"client_id": "cid"},
		map[string]string{"client_secret": "csecret"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	env.svc = oauthsvc.New(q, reg, cfg, kr, provider.Client(), "https://connect.internal")
	env.q = q
	return env
}

func stateFrom(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func TestFullAuthorizationFlow(t *testing.T) {
	env := newEnv(t, true)
	ctx := context.Background()

	begin, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1")
	if err != nil {
		t.Fatal(err)
	}

	// Begin 即返回持久 ID，连接已以 pending 落库
	row, err := env.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil || row.Status != "pending" || row.Alias == nil || *row.Alias != "acct-1" {
		t.Fatalf("pending 连接不符: %+v err=%v", row, err)
	}

	u, _ := url.Parse(begin.AuthorizationURL)
	qs := u.Query()
	if qs.Get("client_id") != "cid" || qs.Get("response_type") != "code" ||
		qs.Get("redirect_uri") != "https://connect.internal/v1/oauth/callback" ||
		qs.Get("scope") != "read write" ||
		qs.Get("code_challenge") == "" || qs.Get("code_challenge_method") != "S256" {
		t.Fatalf("授权 URL 参数不符: %s", begin.AuthorizationURL)
	}

	if err := env.svc.HandleCallback(ctx, qs.Get("state"), "auth-code"); err != nil {
		t.Fatal(err)
	}
	if env.lastForm.Get("grant_type") != "authorization_code" ||
		env.lastForm.Get("code") != "auth-code" ||
		env.lastForm.Get("code_verifier") == "" {
		t.Fatalf("token 请求参数不符: %v", env.lastForm)
	}

	row, err = env.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil || row.Status != "active" || row.AccessTokenExpiresAt == nil {
		t.Fatalf("回调后连接应 active: %+v err=%v", row, err)
	}
	// state 只能用一次
	if err := env.svc.HandleCallback(ctx, qs.Get("state"), "again"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("重放 state 应 ErrInvalidState, got %v", err)
	}
}

func TestNativeMCPOAuthFlowAndRegistrationReuse(t *testing.T) {
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("cc", 32))
	if err != nil {
		t.Fatal(err)
	}

	var (
		provider     *httptest.Server
		registerHits atomic.Int64
		tokenHits    atomic.Int64
		rejectClient atomic.Bool
		lastForm     url.Values
	)
	provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mcp":
			w.Header().Set("WWW-Authenticate",
				`Bearer resource_metadata="`+provider.URL+
					`/.well-known/oauth-protected-resource/mcp", scope="tools"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
		case "/.well-known/oauth-protected-resource/mcp":
			// GitLab currently emits this RFC 9728 field as an array.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":              []string{provider.URL + "/mcp"},
				"authorization_servers": []string{provider.URL},
			})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                                provider.URL,
				"authorization_endpoint":                provider.URL + "/authorize",
				"token_endpoint":                        provider.URL + "/token",
				"registration_endpoint":                 provider.URL + "/register",
				"response_types_supported":              []string{"code"},
				"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
				"token_endpoint_auth_methods_supported": []string{"none"},
				"code_challenge_methods_supported":      []string{"S256"},
			})
		case "/register":
			registerHits.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"client_id":                  "native-client",
				"token_endpoint_auth_method": "none",
			})
		case "/token":
			tokenHits.Add(1)
			_ = r.ParseForm()
			lastForm = r.PostForm
			if rejectClient.Load() && r.PostForm.Get("grant_type") == "refresh_token" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-native", "refresh_token": "rt-native", "expires_in": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(provider.Close)

	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type: "native_mcp", Name: "Native MCP", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{
			Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth",
			OAuth: &connector.OAuthConfig{Mode: connector.OAuthModeMCP},
		}},
		Implementation: connector.RemoteMCP{Endpoint: provider.URL + "/mcp"},
	})
	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	svc := oauthsvc.New(q, reg, cfg, kr, provider.Client(), "https://connect.internal")

	begin, err := svc.Begin(context.Background(), "native_mcp", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(begin.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := authURL.Query()
	if query.Get("client_id") != "native-client" ||
		query.Get("scope") != "tools" ||
		query.Get("resource") != provider.URL+"/mcp" ||
		query.Get("code_challenge_method") != "S256" {
		t.Fatalf("native MCP authorization URL 参数不符: %s", begin.AuthorizationURL)
	}

	if err := svc.HandleCallback(context.Background(), query.Get("state"), "code"); err != nil {
		t.Fatal(err)
	}
	if lastForm.Get("client_id") != "native-client" ||
		lastForm.Get("resource") != provider.URL+"/mcp" ||
		lastForm.Get("code_verifier") == "" {
		t.Fatalf("native MCP token 请求参数不符: %v", lastForm)
	}
	row, err := q.GetConnection(context.Background(), begin.ConnectionID)
	if err != nil || row.OauthClientID == nil {
		t.Fatalf("connection 未绑定 OAuth client: %+v err=%v", row, err)
	}

	refresher := tokens.New(q, reg, cfg, kr, provider.Client())
	if got, err := refresher.AccessToken(context.Background(), begin.ConnectionID); err != nil ||
		got != "at-native" {
		t.Fatalf("native MCP refresh: token=%q err=%v", got, err)
	}
	if lastForm.Get("grant_type") != "refresh_token" ||
		lastForm.Get("resource") != provider.URL+"/mcp" {
		t.Fatalf("native MCP refresh 参数不符: %v", lastForm)
	}

	if _, err := svc.Begin(context.Background(), "native_mcp", "oauth", ""); err != nil {
		t.Fatal(err)
	}
	if registerHits.Load() != 1 {
		t.Fatalf("同一 MCP resource 应复用 DCR client，register hits=%d", registerHits.Load())
	}
	if tokenHits.Load() != 2 {
		t.Fatalf("callback + refresh 应请求两次 token endpoint，hits=%d", tokenHits.Load())
	}

	rejectClient.Store(true)
	if _, err := refresher.AccessToken(context.Background(), begin.ConnectionID); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("DCR invalid_client 应要求重新授权: %v", err)
	}
	row, err = q.GetConnection(context.Background(), begin.ConnectionID)
	if err != nil || row.Status != "reauth_required" {
		t.Fatalf("invalid_client 后状态不符: %+v err=%v", row, err)
	}
	rejectClient.Store(false)
	if _, err := svc.BeginReauth(context.Background(), begin.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if registerHits.Load() != 2 {
		t.Fatalf("invalid_client 后 reauth 应重新注册 DCR client，register hits=%d", registerHits.Load())
	}
}

func TestBeginWithoutAlias(t *testing.T) {
	env := newEnv(t, false)
	begin, err := env.svc.Begin(context.Background(), "example_app", "oauth", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.q.GetConnection(context.Background(), begin.ConnectionID)
	if err != nil || row.Alias != nil {
		t.Fatalf("无 alias 应存 NULL: %+v err=%v", row, err)
	}
}

func TestConcurrentCallbackClaimsStateOnce(t *testing.T) {
	env := newEnv(t, false)
	begin, err := env.svc.Begin(context.Background(), "example_app", "oauth", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFrom(t, begin.AuthorizationURL)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = env.svc.HandleCallback(context.Background(), state, "code")
		}(i)
	}
	close(start)
	wg.Wait()

	var succeeded, rejected int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, oauthsvc.ErrInvalidState):
			rejected++
		default:
			t.Fatalf("callback 返回意外错误: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 || env.tokenHit.Load() != 1 {
		t.Fatalf("state 应只兑换一次: success=%d rejected=%d token_hits=%d", succeeded, rejected, env.tokenHit.Load())
	}
}

func TestReauthKeepsSameConnection(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	begin, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.svc.HandleCallback(ctx, stateFrom(t, begin.AuthorizationURL), "code-1"); err != nil {
		t.Fatal(err)
	}
	if err := env.q.UpdateConnectionStatus(ctx, store.UpdateConnectionStatusParams{
		ID: begin.ConnectionID, Status: "reauth_required",
	}); err != nil {
		t.Fatal(err)
	}

	re, err := env.svc.BeginReauth(ctx, begin.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if re.ConnectionID != begin.ConnectionID {
		t.Fatalf("reauth 应复用同一 ID")
	}
	row, err := env.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil || row.Status != "pending" {
		t.Fatalf("reauth_required 重新授权后应 pending: %+v err=%v", row, err)
	}
	if err := env.svc.HandleCallback(ctx, stateFrom(t, re.AuthorizationURL), "code-2"); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := env.pool.QueryRow(ctx, "select count(*) from connections").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reauth 不应新建连接: %d", count)
	}
}

func TestInvalidState(t *testing.T) {
	env := newEnv(t, false)
	if err := env.svc.HandleCallback(context.Background(), "bogus", "code"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("未知 state 应 ErrInvalidState, got %v", err)
	}
}

func TestBeginValidation(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	if _, err := env.svc.Begin(ctx, "nope", "oauth", ""); !errors.Is(err, oauthsvc.ErrUnknownConnector) {
		t.Fatalf("未知 connector: %v", err)
	}
	if _, err := env.svc.Begin(ctx, "example_app", "nope", ""); !errors.Is(err, oauthsvc.ErrUnknownAuthMethod) {
		t.Fatalf("未知 method: %v", err)
	}
	if _, err := env.svc.BeginReauth(ctx, [16]byte{1}); !errors.Is(err, oauthsvc.ErrConnectionGone) {
		t.Fatalf("不存在的连接 reauth: %v", err)
	}
}
