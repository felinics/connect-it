package oauthsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type testEnv struct {
	svc      *oauthsvc.Service
	conns    *connsvc.Service
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

	env := &testEnv{tokenHit: &atomic.Int64{}}
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
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	if _, err := cfg.Put(context.Background(), "example_app",
		map[string]any{"client_id": "cid"},
		map[string]string{"client_secret": "csecret"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	env.svc = oauthsvc.New(q, reg, cfg, kr, provider.Client(), "https://connect.internal")
	env.conns = connsvc.New(q, reg, kr)
	env.q = q
	env.pool = pool
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

	authURL, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	qs := u.Query()
	if qs.Get("client_id") != "cid" || qs.Get("response_type") != "code" ||
		qs.Get("redirect_uri") != "https://connect.internal/v1/oauth/callback" ||
		qs.Get("scope") != "read write" ||
		qs.Get("code_challenge") == "" || qs.Get("code_challenge_method") != "S256" {
		t.Fatalf("授权 URL 参数不符: %s", authURL)
	}

	connID, err := env.svc.HandleCallback(ctx, qs.Get("state"), "auth-code")
	if err != nil {
		t.Fatal(err)
	}
	if env.lastForm.Get("grant_type") != "authorization_code" ||
		env.lastForm.Get("code") != "auth-code" ||
		env.lastForm.Get("code_verifier") == "" {
		t.Fatalf("token 请求参数不符: %v", env.lastForm)
	}

	row, err := env.q.GetConnection(ctx, connID)
	if err != nil || row.Status != "active" || row.Alias != "acct-1" ||
		row.AccessTokenExpiresAt == nil {
		t.Fatalf("connection 不符: %+v err=%v", row, err)
	}
	// state 只能用一次
	if _, err := env.svc.HandleCallback(ctx, qs.Get("state"), "again"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("重放 state 应 ErrInvalidState, got %v", err)
	}
}

func TestReauthUpdatesExistingConnection(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	// 第一次授权
	authURL, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := env.svc.HandleCallback(ctx, stateFrom(t, authURL), "code-1")
	if err != nil {
		t.Fatal(err)
	}

	// BeginReauth 走既有 connection
	authURL2, err := env.svc.BeginReauth(ctx, firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := env.svc.HandleCallback(ctx, stateFrom(t, authURL2), "code-2")
	if err != nil {
		t.Fatal(err)
	}
	if secondID != firstID {
		t.Fatalf("重授权应更新同一 connection: %s != %s", secondID, firstID)
	}
	list, err := env.conns.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("应只有 1 个 connection: %+v err=%v", list, err)
	}

	// 同 alias 再 Begin（非 reauth 入口）也进入重授权而不是报冲突
	if _, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1"); err != nil {
		t.Fatalf("同类型同 method 的 alias 应可重授权: %v", err)
	}
}

func TestInvalidState(t *testing.T) {
	env := newEnv(t, false)
	if _, err := env.svc.HandleCallback(context.Background(), "bogus", "code"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("未知 state 应 ErrInvalidState, got %v", err)
	}
}

func TestBeginValidation(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	if _, err := env.svc.Begin(ctx, "example_app", "oauth", "Bad_Alias"); !errors.Is(err, connsvc.ErrInvalidAlias) {
		t.Fatalf("非法 alias: %v", err)
	}
	if _, err := env.svc.Begin(ctx, "nope", "oauth", "a1"); !errors.Is(err, oauthsvc.ErrUnknownConnector) {
		t.Fatalf("未知 connector: %v", err)
	}
	if _, err := env.svc.Begin(ctx, "example_app", "nope", "a1"); !errors.Is(err, oauthsvc.ErrUnknownAuthMethod) {
		t.Fatalf("未知 method: %v", err)
	}
}

func TestBeginAliasConflict(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	authURL, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.HandleCallback(ctx, stateFrom(t, authURL), "code"); err != nil {
		t.Fatal(err)
	}
	// 直接改行的 connector_type，模拟 alias 已被其他 connector 占用
	if _, err := env.pool.Exec(ctx,
		"update connections set connector_type = 'other_app' where alias = 'acct-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1"); !errors.Is(err, oauthsvc.ErrAliasTaken) {
		t.Fatalf("被占用的 alias 应 ErrAliasTaken, got %v", err)
	}
}
