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

	begin, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1", "https://saas.example/done")
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

	result, err := env.svc.HandleCallback(ctx, qs.Get("state"), "auth-code")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionID != begin.ConnectionID {
		t.Fatalf("回调应绑定同一 ID: %s != %s", result.ConnectionID, begin.ConnectionID)
	}
	if result.RedirectURL != "https://saas.example/done" {
		t.Fatalf("redirect_url 未透传: %q", result.RedirectURL)
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
	if _, err := env.svc.HandleCallback(ctx, qs.Get("state"), "again"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("重放 state 应 ErrInvalidState, got %v", err)
	}
}

func TestBeginWithoutAlias(t *testing.T) {
	env := newEnv(t, false)
	begin, err := env.svc.Begin(context.Background(), "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := env.q.GetConnection(context.Background(), begin.ConnectionID)
	if err != nil || row.Alias != nil {
		t.Fatalf("无 alias 应存 NULL: %+v err=%v", row, err)
	}
}

func TestReauthKeepsSameConnection(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	begin, err := env.svc.Begin(ctx, "example_app", "oauth", "acct-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.HandleCallback(ctx, stateFrom(t, begin.AuthorizationURL), "code-1"); err != nil {
		t.Fatal(err)
	}

	re, err := env.svc.BeginReauth(ctx, begin.ConnectionID, "https://saas.example/back")
	if err != nil {
		t.Fatal(err)
	}
	if re.ConnectionID != begin.ConnectionID {
		t.Fatalf("reauth 应复用同一 ID")
	}
	result, err := env.svc.HandleCallback(ctx, stateFrom(t, re.AuthorizationURL), "code-2")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionID != begin.ConnectionID || result.RedirectURL != "https://saas.example/back" {
		t.Fatalf("reauth 回调不符: %+v", result)
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
	if _, err := env.svc.HandleCallback(context.Background(), "bogus", "code"); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("未知 state 应 ErrInvalidState, got %v", err)
	}
}

func TestBeginValidation(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	if _, err := env.svc.Begin(ctx, "example_app", "oauth", "Bad_Alias", ""); !errors.Is(err, connsvc.ErrInvalidAlias) {
		t.Fatalf("非法 alias: %v", err)
	}
	if _, err := env.svc.Begin(ctx, "nope", "oauth", "", ""); !errors.Is(err, oauthsvc.ErrUnknownConnector) {
		t.Fatalf("未知 connector: %v", err)
	}
	if _, err := env.svc.Begin(ctx, "example_app", "nope", "", ""); !errors.Is(err, oauthsvc.ErrUnknownAuthMethod) {
		t.Fatalf("未知 method: %v", err)
	}
	if _, err := env.svc.BeginReauth(ctx, [16]byte{1}, ""); !errors.Is(err, oauthsvc.ErrConnectionGone) {
		t.Fatalf("不存在的连接 reauth: %v", err)
	}
}
