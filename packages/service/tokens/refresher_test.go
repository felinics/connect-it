package tokens_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
	"github.com/memohai/connect-it/packages/service/tokens"
)

type env struct {
	r        *tokens.Refresher
	q        *store.Queries
	kr       *crypto.Keyring
	tokenHit *atomic.Int64
	respMu   sync.Mutex
	respBody map[string]any
	respCode int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("bb", 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{
		kr: kr, tokenHit: &atomic.Int64{},
		respBody: map[string]any{"access_token": "at-new", "refresh_token": "rt-new", "expires_in": 3600},
		respCode: http.StatusOK,
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.tokenHit.Add(1)
		time.Sleep(30 * time.Millisecond) // 放大并发窗口
		e.respMu.Lock()
		code, body := e.respCode, e.respBody
		e.respMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
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
				AuthorizationEndpoint: "https://p.example/authorize",
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
	if _, err := cfg.Put(context.Background(), "example_app",
		map[string]any{"client_id": "cid"},
		map[string]string{"client_secret": "cs"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	e.r = tokens.New(q, reg, cfg, kr, provider.Client())
	e.q = q
	return e
}

// seedOAuth 造一个 OAuth connection，expiresIn 控制离过期还有多久（可为负）。
func (e *env) seedOAuth(t *testing.T, expiresIn time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	cred := credential.OAuth{AccessToken: "at-old", RefreshToken: "rt-old"}
	if expiresIn != 0 {
		cred.ExpiresAt = time.Now().Add(expiresIn)
	}
	plain, err := cred.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ct, ver, err := e.kr.Encrypt(plain, []byte(id.String()))
	if err != nil {
		t.Fatal(err)
	}
	exp := cred.ExpiresAt
	var expPtr *time.Time
	if !exp.IsZero() {
		expPtr = &exp
	}
	if _, err := e.q.CreateConnection(context.Background(), store.CreateConnectionParams{
		ID: id, ConnectorType: "example_app", Alias: "a-" + id.String()[:8],
		AuthMethod: "oauth", Credential: ct, SecretKeyVersion: int32(ver),
		Scopes: []string{}, Status: "active", AccessTokenExpiresAt: expPtr,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) seedAPIKey(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	plain, _ := credential.Fields{Fields: map[string]string{"token": "tok_value"}}.Marshal()
	ct, ver, err := e.kr.Encrypt(plain, []byte(id.String()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.CreateConnection(context.Background(), store.CreateConnectionParams{
		ID: id, ConnectorType: "example_app", Alias: "k-" + id.String()[:8],
		AuthMethod: "pat", Credential: ct, SecretKeyVersion: int32(ver),
		Scopes: []string{}, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAPIKeyPassthrough(t *testing.T) {
	e := newEnv(t)
	id := e.seedAPIKey(t)
	got, err := e.r.AccessToken(context.Background(), id)
	if err != nil || got != "tok_value" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if e.tokenHit.Load() != 0 {
		t.Fatal("api_key 不应打 token endpoint")
	}
}

func TestFreshTokenNoRefresh(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, time.Hour)
	got, err := e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-old" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if e.tokenHit.Load() != 0 {
		t.Fatal("未过期不应刷新")
	}
}

func TestNoExpiryNeverRefreshes(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, 0) // 无过期时间
	got, err := e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-old" || e.tokenHit.Load() != 0 {
		t.Fatalf("无过期时间应直通: %q err=%v hits=%d", got, err, e.tokenHit.Load())
	}
}

func TestExpiredTriggersRefresh(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	got, err := e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-new" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if e.tokenHit.Load() != 1 {
		t.Fatalf("应恰好刷新一次: %d", e.tokenHit.Load())
	}
	// 新 credential 已落库：再次调用直接用新 token，不再刷新
	got, err = e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-new" || e.tokenHit.Load() != 1 {
		t.Fatalf("落库后应直通: %q err=%v hits=%d", got, err, e.tokenHit.Load())
	}
}

func TestRefreshKeepsOldRefreshTokenWhenAbsent(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respBody = map[string]any{"access_token": "at-new", "expires_in": 3600} // 无 refresh_token
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	if _, err := e.r.AccessToken(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	row, err := e.q.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := e.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(id.String()))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := credential.UnmarshalOAuth(plain)
	if err != nil || cred.RefreshToken != "rt-old" {
		t.Fatalf("响应缺省时应保留旧 refresh token: %+v err=%v", cred, err)
	}
}

func TestConcurrentSingleFlight(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)

	const n = 10
	var wg sync.WaitGroup
	results := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = e.r.AccessToken(context.Background(), id)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil || results[i] != "at-new" {
			t.Fatalf("goroutine %d: %q err=%v", i, results[i], errs[i])
		}
	}
	if hits := e.tokenHit.Load(); hits != 1 {
		t.Fatalf("并发下应恰好刷新一次, got %d", hits)
	}
}

func TestRefreshFailureMarksReauth(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respCode = http.StatusBadRequest
	e.respBody = map[string]any{"error": "invalid_grant"}
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)

	if _, err := e.r.AccessToken(context.Background(), id); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("刷新失败应 ErrReauthRequired, got %v", err)
	}
	row, _ := e.q.GetConnection(context.Background(), id)
	if row.Status != "reauth_required" {
		t.Fatalf("状态应为 reauth_required: %s", row.Status)
	}
	// 再次调用：状态挡住，不再打 endpoint
	hits := e.tokenHit.Load()
	if _, err := e.r.AccessToken(context.Background(), id); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatal("reauth_required 状态应直接拒绝")
	}
	if e.tokenHit.Load() != hits {
		t.Fatal("拒绝路径不应再打 endpoint")
	}
}

func TestUnknownConnection(t *testing.T) {
	e := newEnv(t)
	if _, err := e.r.AccessToken(context.Background(), uuid.New()); !errors.Is(err, tokens.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
