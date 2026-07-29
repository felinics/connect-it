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
		time.Sleep(30 * time.Millisecond) // widen the concurrency window
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
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
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

// seedOAuth creates an OAuth connection; expiresIn controls how long until
// it expires and may be negative.
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
	alias := "a-" + id.String()[:8]
	if _, err := e.q.CreateConnection(context.Background(), store.CreateConnectionParams{
		ID: id, ConnectorType: "example_app", Alias: &alias,
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
	alias := "k-" + id.String()[:8]
	if _, err := e.q.CreateConnection(context.Background(), store.CreateConnectionParams{
		ID: id, ConnectorType: "example_app", Alias: &alias,
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
		t.Fatal("api_key must not hit the token endpoint")
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
		t.Fatal("an unexpired token must not be refreshed")
	}
}

func TestNoExpiryNeverRefreshes(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, 0) // no expiry
	got, err := e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-old" || e.tokenHit.Load() != 0 {
		t.Fatalf("a token without expiry should pass straight through: %q err=%v hits=%d", got, err, e.tokenHit.Load())
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
		t.Fatalf("expected exactly one refresh: %d", e.tokenHit.Load())
	}
	// The new credential is stored: a second call uses it directly, no refresh.
	got, err = e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-new" || e.tokenHit.Load() != 1 {
		t.Fatalf("once stored it should pass straight through: %q err=%v hits=%d", got, err, e.tokenHit.Load())
	}
}

func TestRefreshKeepsOldRefreshTokenWhenAbsent(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respBody = map[string]any{"access_token": "at-new", "expires_in": 3600} // no refresh_token
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
		t.Fatalf("an omitted response field should keep the old refresh token: %+v err=%v", cred, err)
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
		t.Fatalf("concurrent callers should trigger exactly one refresh, got %d", hits)
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
		t.Fatalf("a failed refresh should yield ErrReauthRequired, got %v", err)
	}
	row, _ := e.q.GetConnection(context.Background(), id)
	if row.Status != "reauth_required" {
		t.Fatalf("status should be reauth_required: %s", row.Status)
	}
	// A second call is blocked by the status and never reaches the endpoint.
	hits := e.tokenHit.Load()
	if _, err := e.r.AccessToken(context.Background(), id); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatal("the reauth_required status should reject outright")
	}
	if e.tokenHit.Load() != hits {
		t.Fatal("the reject path must not hit the endpoint again")
	}
}

func TestTransientRefreshFailureKeepsConnectionActive(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respCode = http.StatusServiceUnavailable
	e.respBody = map[string]any{"error": "temporarily_unavailable"}
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)

	if _, err := e.r.AccessToken(context.Background(), id); err == nil ||
		errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("a transient failure should return a retryable error, not ErrReauthRequired: %v", err)
	}
	row, err := e.q.GetConnection(context.Background(), id)
	if err != nil || row.Status != "active" {
		t.Fatalf("a connection should stay active after a transient failure: %+v err=%v", row, err)
	}

	e.respMu.Lock()
	e.respCode = http.StatusOK
	e.respBody = map[string]any{"access_token": "at-retry", "expires_in": 3600}
	e.respMu.Unlock()
	got, err := e.r.AccessToken(context.Background(), id)
	if err != nil || got != "at-retry" {
		t.Fatalf("a later call should retry successfully: token=%q err=%v", got, err)
	}
	if e.tokenHit.Load() != 2 {
		t.Fatalf("expected two token endpoint requests: %d", e.tokenHit.Load())
	}
}

func TestUnknownConnection(t *testing.T) {
	e := newEnv(t)
	if _, err := e.r.AccessToken(context.Background(), uuid.New()); !errors.Is(err, tokens.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
