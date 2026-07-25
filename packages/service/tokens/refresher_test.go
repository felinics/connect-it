package tokens_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/providerkit"
	providertest "github.com/memohai/connect-it/packages/core/providerkit/testkit"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
	"github.com/memohai/connect-it/packages/service/tokens"
)

type env struct {
	r        *tokens.Refresher
	q        *store.Queries
	pool     *pgxpool.Pool
	kr       *crypto.Keyring
	reg      *registry.Registry
	cfg      *configsvc.Service
	factory  *providerkit.Factory
	tokenHit *atomic.Int64
	respMu   sync.Mutex
	respBody map[string]any
	respCode int
	started  chan struct{}
	release  <-chan struct{}
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
		respBody: map[string]any{
			"access_token": "at-new", "token_type": "Bearer",
			"refresh_token": "rt-new", "expires_in": 3600,
		},
		respCode: http.StatusOK,
	}
	provider := testutil.NewProviderServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.tokenHit.Add(1)
		time.Sleep(30 * time.Millisecond) // 放大并发窗口
		e.respMu.Lock()
		code, body := e.respCode, e.respBody
		started, release := e.started, e.release
		e.respMu.Unlock()
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
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
				AuthorizationEndpoint: "https://p.example/authorize",
				TokenEndpoint:         provider.BaseURL + "/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://p.example:443"},
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
	if _, err := cfg.Put(context.Background(), "example_app",
		map[string]any{"client_id": "cid"},
		map[string]string{"client_secret": "cs"}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	e.r = tokens.New(q, reg, cfg, kr, provider.Factory, nil)
	e.q = q
	e.pool = pool
	e.reg = reg
	e.cfg = cfg
	e.factory = provider.Factory
	return e
}

func (e *env) createConnection(
	t *testing.T,
	id uuid.UUID,
	alias string,
	authMethod string,
	ciphertext []byte,
	keyVersion int32,
	expiresAt *time.Time,
) {
	t.Helper()
	_, policy, err := e.cfg.ResolvedWithPolicy(
		context.Background(),
		"example_app",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.CreateConnectionAtPolicyIdentity(
		context.Background(),
		store.CreateConnectionAtPolicyIdentityParams{
			ID:                       id,
			ConnectorType:            "example_app",
			Alias:                    &alias,
			AuthMethod:               authMethod,
			Credential:               ciphertext,
			SecretKeyVersion:         keyVersion,
			Profile:                  []byte(`{}`),
			Scopes:                   []string{},
			Status:                   "active",
			AccessTokenExpiresAt:     expiresAt,
			ExpectedIdentityVersion:  policy.IdentityVersion,
			ExpectedIdentityDigest:   policy.IdentityDigest,
			ExpectedDefinitionDigest: policy.DefinitionDigest,
		},
	); err != nil {
		t.Fatal(err)
	}
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
	alias := "a-" + id.String()[:8]
	e.createConnection(t, id, alias, "oauth", ct, int32(ver), expPtr)
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
	e.createConnection(t, id, alias, "pat", ct, int32(ver), nil)
	return id
}

func (e *env) access(
	ctx context.Context,
	id uuid.UUID,
) (tokens.OAuthToken, error) {
	row, err := e.q.GetConnection(ctx, id)
	if err != nil {
		return tokens.OAuthToken{}, err
	}
	return e.r.AccessToken(ctx, id, row.AuthorizationGeneration)
}

// expireRefreshLease 让 DB 时间判定当前 lease 已过期，交给下一次调用回收。
func (e *env) expireRefreshLease(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := e.pool.Exec(
		t.Context(),
		`update connections
		    set refresh_lease_until = now() - interval '1 second'
		  where id = $1`,
		id,
	); err != nil {
		t.Fatal(err)
	}
}

// assertRefreshState 断言 connection 的状态与 refresh lease：state 为空表示
// lease 必须已完全释放，非空表示 owner/租期/state 三者都还在。
func (e *env) assertRefreshState(
	t *testing.T,
	id uuid.UUID,
	status string,
	state string,
) {
	t.Helper()
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	if row.RefreshState != nil {
		got = *row.RefreshState
	}
	held := row.RefreshOwner != nil && row.RefreshLeaseUntil != nil
	if row.Status != status || got != state || held != (state != "") {
		t.Fatalf(
			"refresh state = status=%s state=%q owner=%v until=%v",
			row.Status,
			got,
			row.RefreshOwner,
			row.RefreshLeaseUntil,
		)
	}
}

// assertUncertainLeaseIsNeverReplayed 断言不确定的 rotating refresh token 在
// lease 到期前不会被重放，到期后落入 reauth_required 且仍未重放。
func (e *env) assertUncertainLeaseIsNeverReplayed(
	t *testing.T,
	refresher *tokens.Refresher,
	id uuid.UUID,
	generation int64,
	requests *atomic.Int64,
) {
	t.Helper()
	hits := requests.Load()
	waitCtx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	if _, err := refresher.AccessToken(
		waitCtx,
		id,
		generation,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v, want bounded deadline", err)
	}
	if requests.Load() != hits {
		t.Fatal("rotating token was replayed before lease expiry")
	}
	e.expireRefreshLease(t, id)
	if _, err := refresher.AccessToken(
		t.Context(),
		id,
		generation,
	); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("expired uncertain lease error = %v", err)
	}
	if requests.Load() != hits {
		t.Fatal("expired uncertain lease replayed the rotating token")
	}
	e.assertRefreshState(t, id, "reauth_required", "")
}

func TestAPIKeyIsNotHandledByOAuthRefresher(t *testing.T) {
	e := newEnv(t)
	id := e.seedAPIKey(t)
	_, err := e.access(context.Background(), id)
	if !errors.Is(err, tokens.ErrNotOAuth) {
		t.Fatalf("got err=%v, want ErrNotOAuth", err)
	}
	if e.tokenHit.Load() != 0 {
		t.Fatal("api_key 不应打 token endpoint")
	}
}

// 未到期（含完全没有过期时间）的 credential 直通，不打 token endpoint。
func TestFreshCredentialNeverRefreshes(t *testing.T) {
	for name, expiresIn := range map[string]time.Duration{
		"expires in an hour": time.Hour,
		"never expires":      0,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id := e.seedOAuth(t, expiresIn)
			got, err := e.access(context.Background(), id)
			if err != nil ||
				got.AccessToken != "at-old" ||
				got.TokenType != "Bearer" ||
				got.CredentialVersion != 1 ||
				got.AuthorizationGeneration != 1 {
				t.Fatalf("got %+v err=%v", got, err)
			}
			if e.tokenHit.Load() != 0 {
				t.Fatalf("未过期不应刷新: hits=%d", e.tokenHit.Load())
			}
		})
	}
}

func TestExpiredTriggersRefresh(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	got, err := e.access(context.Background(), id)
	if err != nil ||
		got.AccessToken != "at-new" ||
		got.TokenType != "Bearer" ||
		got.CredentialVersion != 2 ||
		got.AuthorizationGeneration != 1 {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if e.tokenHit.Load() != 1 {
		t.Fatalf("应恰好刷新一次: %d", e.tokenHit.Load())
	}
	// 新 credential 已落库：再次调用直接用新 token，不再刷新
	got, err = e.access(context.Background(), id)
	if err != nil ||
		got.AccessToken != "at-new" ||
		got.CredentialVersion != 2 ||
		got.AuthorizationGeneration != 1 ||
		e.tokenHit.Load() != 1 {
		t.Fatalf("落库后应直通: %+v err=%v hits=%d", got, err, e.tokenHit.Load())
	}
}

func TestRefreshKeepsOldRefreshTokenWhenAbsent(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respBody = map[string]any{
		"access_token": "at-new", "token_type": "Bearer", "expires_in": 3600,
	} // 无 refresh_token
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	if _, err := e.access(context.Background(), id); err != nil {
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
	if err != nil || cred.RefreshToken != "rt-old" || cred.TokenType != "Bearer" {
		t.Fatalf("响应缺省时应保留旧 refresh token: %+v err=%v", cred, err)
	}
}

// 并发刷新必须恰好打一次 token endpoint，无论并发发生在同一个 Refresher
// （进程内 single-flight）还是不同实例之间（数据库 lease）；任一机制失效
// hits 都会大于 1，同时每个调用方都必须拿到刷新后的 token。
func TestConcurrentRefreshesIssueExactlyOneTokenRequest(t *testing.T) {
	for name, separateInstances := range map[string]bool{
		"ten callers on one refresher": false,
		"two refresher instances":      true,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id := e.seedOAuth(t, -time.Minute)
			row, err := e.q.GetConnection(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			refreshers := []*tokens.Refresher{e.r}
			if separateInstances {
				refreshers = append(
					refreshers,
					tokens.New(e.q, e.reg, e.cfg, e.kr, e.factory, nil),
				)
			} else {
				for range 9 {
					refreshers = append(refreshers, e.r)
				}
			}

			start := make(chan struct{})
			type outcome struct {
				token tokens.OAuthToken
				err   error
			}
			results := make(chan outcome, len(refreshers))
			for _, refresher := range refreshers {
				go func(r *tokens.Refresher) {
					<-start
					token, err := r.AccessToken(
						t.Context(),
						id,
						row.AuthorizationGeneration,
					)
					results <- outcome{token: token, err: err}
				}(refresher)
			}
			close(start)
			for range refreshers {
				result := <-results
				if result.err != nil || result.token.AccessToken != "at-new" {
					t.Fatalf("concurrent result = %+v", result)
				}
			}
			if hits := e.tokenHit.Load(); hits != 1 {
				t.Fatalf("concurrent refresh made %d token requests, want 1", hits)
			}
		})
	}
}

func TestRefreshCannotOverwriteConcurrentGenerationChange(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	e.respMu.Lock()
	e.started = started
	e.release = release
	e.respMu.Unlock()

	result := make(chan error, 1)
	go func() {
		_, refreshErr := e.r.AccessToken(
			t.Context(),
			id,
			row.AuthorizationGeneration,
		)
		result <- refreshErr
	}()
	<-started
	if _, err := e.pool.Exec(
		t.Context(),
		`update connections
		    set authorization_generation = authorization_generation + 1,
		        updated_at = now()
		  where id = $1`,
		id,
	); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, tokens.ErrAuthorizationChanged) {
		t.Fatalf("stale refresh error = %v, want authorization changed", err)
	}
	updated, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := e.kr.Decrypt(
		updated.Credential,
		int(updated.SecretKeyVersion),
		[]byte(id.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := credential.UnmarshalOAuth(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "at-old" {
		t.Fatalf("stale refresh overwrote credential: %+v", stored)
	}
}

func TestMalformedSuccessIsUncertainAndNeverReplayed(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respBody = map[string]any{"unexpected": "shape"}
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	)
	var endpointErr *oauthsvc.TokenEndpointError
	if !errors.As(err, &endpointErr) ||
		!endpointErr.RequestUncertain ||
		endpointErr.Code() != connector.FailureInvalidResponse {
		t.Fatalf("malformed success error = %#v", err)
	}
	e.assertRefreshState(t, id, "active", "requesting")
	e.assertUncertainLeaseIsNeverReplayed(
		t,
		e.r,
		id,
		row.AuthorizationGeneration,
		e.tokenHit,
	)
}

func TestExpiredLeasedStateCanBeTakenOverBeforeNetwork(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	if _, err := e.q.AcquireRefreshLease(
		t.Context(),
		store.AcquireRefreshLeaseParams{
			Owner:                           &owner,
			LeaseSeconds:                    30,
			ConnectionID:                    id,
			ExpectedAuthorizationGeneration: row.AuthorizationGeneration,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(
		t.Context(),
		`update connections
		    set refresh_lease_until = now() - interval '1 second'
		  where id = $1`,
		id,
	); err != nil {
		t.Fatal(err)
	}
	token, err := e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	)
	if err != nil || token.AccessToken != "at-new" {
		t.Fatalf("lease takeover token=%+v err=%v", token, err)
	}
	if hits := e.tokenHit.Load(); hits != 1 {
		t.Fatalf("lease takeover requests = %d, want 1", hits)
	}
}

func TestRefreshScopeChangeAdvancesGenerationAndRejectsOldSession(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respBody = map[string]any{
		"access_token":  "at-new",
		"token_type":    "Bearer",
		"refresh_token": "rt-new",
		"expires_in":    3600,
		"scope":         "read",
	}
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	if _, err := e.pool.Exec(
		t.Context(),
		`update connections
		    set scopes = array['read','write']::text[],
		        scopes_known = true
		  where id = $1`,
		id,
	); err != nil {
		t.Fatal(err)
	}
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	)
	if !errors.Is(err, tokens.ErrAuthorizationChanged) {
		t.Fatalf("scope-changing refresh error = %v", err)
	}
	updated, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AuthorizationGeneration != row.AuthorizationGeneration+1 ||
		!updated.ScopesKnown ||
		len(updated.Scopes) != 1 ||
		updated.Scopes[0] != "read" {
		t.Fatalf("scope-changing snapshot = %+v", updated)
	}
	if _, err := e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	); !errors.Is(err, tokens.ErrAuthorizationChanged) {
		t.Fatalf("old generation obtained refreshed token: %v", err)
	}
}

func TestRefreshFailureMarksReauth(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respCode = http.StatusBadRequest
	e.respBody = map[string]any{"error": "invalid_grant"}
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)

	if _, err := e.access(context.Background(), id); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("刷新失败应 ErrReauthRequired, got %v", err)
	}
	row, _ := e.q.GetConnection(context.Background(), id)
	if row.Status != "reauth_required" {
		t.Fatalf("状态应为 reauth_required: %s", row.Status)
	}
	// 再次调用：状态挡住，不再打 endpoint
	hits := e.tokenHit.Load()
	if _, err := e.access(context.Background(), id); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatal("reauth_required 状态应直接拒绝")
	}
	if e.tokenHit.Load() != hits {
		t.Fatal("拒绝路径不应再打 endpoint")
	}
}

func TestExpiredCredentialWithoutRefreshTokenMarksReauthFromLeasedState(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	withoutRefresh := credential.OAuth{
		AccessToken: "at-old",
		TokenType:   "Bearer",
		ExpiresAt:   time.Now().Add(-time.Minute),
	}
	plaintext, err := withoutRefresh.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, keyVersion, err := e.kr.Encrypt(
		plaintext,
		[]byte(id.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(
		t.Context(),
		`update connections
		    set credential = $2,
		        secret_key_version = $3,
		        access_token_expires_at = $4
		  where id = $1`,
		id,
		ciphertext,
		int32(keyVersion),
		withoutRefresh.ExpiresAt,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("no-refresh-token error = %v", err)
	}
	e.assertRefreshState(t, id, "reauth_required", "")
	if e.tokenHit.Load() != 0 {
		t.Fatal("credential without a refresh token contacted the Provider")
	}
}

// 逐个状态码的分类由 oauthsvc 的 exchange 测试无 DB 覆盖；这里只需要一例证明
// 「完整的 HTTP 错误响应仍然是不确定的」这一 DB 侧后果。
func TestRefreshHTTPFailureNeverReplaysRotatingToken(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respCode = http.StatusTooManyRequests
	e.respBody = map[string]any{"error": "slow_down"}
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.r.AccessToken(t.Context(), id, row.AuthorizationGeneration)
	var endpointErr *oauthsvc.TokenEndpointError
	if !errors.As(err, &endpointErr) ||
		!endpointErr.RequestUncertain ||
		endpointErr.Code() != connector.FailureRateLimited {
		t.Fatalf("HTTP failure classification = %#v", err)
	}
	e.assertRefreshState(t, id, "active", "requesting")
	if hits := e.tokenHit.Load(); hits != 1 {
		t.Fatalf("initial refresh requests = %d, want 1", hits)
	}
	e.assertUncertainLeaseIsNeverReplayed(
		t,
		e.r,
		id,
		row.AuthorizationGeneration,
		e.tokenHit,
	)
}

func TestRefreshDialFailureKeepsConnectionActiveAndReleasesLease(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	factory := providerkit.NewFactory(
		providerkit.WithResolver(providertest.Resolver{}),
		providerkit.WithDialContext(
			func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: errors.New("provider address unavailable"),
				}
			},
		),
	)
	refresher := tokens.New(e.q, e.reg, e.cfg, e.kr, factory, nil)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	_, err = refresher.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	)
	var endpointErr *oauthsvc.TokenEndpointError
	if !errors.As(err, &endpointErr) || endpointErr.RequestUncertain {
		t.Fatalf("dial classification = %#v", err)
	}
	e.assertRefreshState(t, id, "active", "")
}

func TestRefreshCanceledBeforeWriteEventuallyClearsLease(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	transportStarted := make(chan struct{})
	var requests atomic.Int64
	factory := providerkit.NewFactory(
		providerkit.WithResolver(providertest.Resolver{}),
		providerkit.WithDialContext(
			func(context.Context, string, string) (net.Conn, error) {
				requests.Add(1)
				close(transportStarted)
				<-callerCtx.Done()
				return nil, &net.OpError{
					Op:  "dial",
					Net: "tcp",
					Err: context.Canceled,
				}
			},
		),
	)
	refresher := tokens.New(e.q, e.reg, e.cfg, e.kr, factory, nil)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, refreshErr := refresher.AccessToken(
			callerCtx,
			id,
			row.AuthorizationGeneration,
		)
		result <- refreshErr
	}()
	<-transportStarted
	cancelCaller()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller error = %v", err)
	}
	waitForActiveConnectionWithoutRefreshLease(t, e.q, id)
	if requests.Load() != 1 {
		t.Fatalf("pre-write request attempts = %d, want 1", requests.Load())
	}

	token, err := e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	)
	if err != nil || token.AccessToken != "at-new" {
		t.Fatalf("retry after safe pre-write cancel = %+v, %v", token, err)
	}
}

func TestRefreshCompletedFiveHundredAfterCallerCancelRemainsUncertain(
	t *testing.T,
) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respCode = http.StatusServiceUnavailable
	e.respBody = map[string]any{"error": "temporarily_unavailable"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	e.started = started
	e.release = release
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, refreshErr := e.r.AccessToken(
			callerCtx,
			id,
			row.AuthorizationGeneration,
		)
		result <- refreshErr
	}()
	<-started
	cancelCaller()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller error = %v", err)
	}
	e.assertRefreshState(t, id, "active", "requesting")
	if e.tokenHit.Load() != 1 {
		t.Fatalf("completed 5xx requests = %d, want 1", e.tokenHit.Load())
	}
	e.expireRefreshLease(t, id)
	if _, err := e.r.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("expired completed 5xx error = %v", err)
	}
	if e.tokenHit.Load() != 1 {
		t.Fatal("completed 5xx replayed rotating token")
	}
}

func waitForActiveConnectionWithoutRefreshLease(
	t *testing.T,
	queries *store.Queries,
	id uuid.UUID,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		connection, err := queries.GetConnection(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if connection.Status == "active" &&
			connection.RefreshOwner == nil &&
			connection.RefreshLeaseUntil == nil &&
			connection.RefreshState == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"refresh lease was not safely cleared before deadline: %+v",
				connection,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRefreshResponseLossNeverReplaysAndExpiresToReauth(t *testing.T) {
	e := newEnv(t)
	id := e.seedOAuth(t, -time.Minute)
	var requests atomic.Int64
	responseLoss := testutil.NewProviderServer(t, http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			hijacker, ok := writer.(http.Hijacker)
			if !ok {
				t.Error("test server does not support hijacking")
				return
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("hijack response: %v", err)
				return
			}
			_ = connection.Close()
		},
	))
	refresher := tokens.New(
		e.q,
		e.reg,
		e.cfg,
		e.kr,
		responseLoss.Factory,
		nil,
	)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	_, err = refresher.AccessToken(
		t.Context(),
		id,
		row.AuthorizationGeneration,
	)
	var endpointErr *oauthsvc.TokenEndpointError
	if !errors.As(err, &endpointErr) || !endpointErr.RequestUncertain {
		t.Fatalf("response-loss classification = %#v", err)
	}
	e.assertRefreshState(t, id, "active", "requesting")
	e.assertUncertainLeaseIsNeverReplayed(
		t,
		refresher,
		id,
		row.AuthorizationGeneration,
		&requests,
	)
	if requests.Load() != 1 {
		t.Fatalf("uncertain rotating token requests = %d, want 1", requests.Load())
	}
}

func TestStaleInvalidGrantCannotDowngradeRecredentialedConnection(t *testing.T) {
	e := newEnv(t)
	e.respMu.Lock()
	e.respCode = http.StatusBadRequest
	e.respBody = map[string]any{"error": "invalid_grant"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	e.started = started
	e.release = release
	e.respMu.Unlock()
	id := e.seedOAuth(t, -time.Minute)
	row, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, refreshErr := e.r.AccessToken(
			t.Context(),
			id,
			row.AuthorizationGeneration,
		)
		result <- refreshErr
	}()
	<-started
	newCredential := credential.OAuth{
		AccessToken:  "at-recredentialed",
		TokenType:    "Bearer",
		RefreshToken: "rt-recredentialed",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	plaintext, err := newCredential.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, keyVersion, err := e.kr.Encrypt(
		plaintext,
		[]byte(id.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(
		t.Context(),
		`update connections
		    set credential = $2,
		        secret_key_version = $3,
		        credential_version = credential_version + 1,
		        authorization_generation = authorization_generation + 1,
		        status = 'active',
		        access_token_expires_at = $4,
		        refresh_owner = null,
		        refresh_lease_until = null,
		        refresh_state = null
		  where id = $1`,
		id,
		ciphertext,
		int32(keyVersion),
		newCredential.ExpiresAt,
	); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, tokens.ErrAuthorizationChanged) {
		t.Fatalf("stale invalid_grant error = %v", err)
	}
	updated, err := e.q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "active" ||
		updated.CredentialVersion != row.CredentialVersion+1 ||
		updated.AuthorizationGeneration != row.AuthorizationGeneration+1 {
		t.Fatalf("stale invalid_grant changed new authorization = %+v", updated)
	}
	storedPlaintext, err := e.kr.Decrypt(
		updated.Credential,
		int(updated.SecretKeyVersion),
		[]byte(id.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := credential.UnmarshalOAuth(storedPlaintext)
	if err != nil || stored.AccessToken != "at-recredentialed" {
		t.Fatalf("stale invalid_grant overwrote credential = %+v err=%v", stored, err)
	}
}

func TestUnknownConnection(t *testing.T) {
	e := newEnv(t)
	if _, err := e.r.AccessToken(context.Background(), uuid.New(), 1); !errors.Is(err, tokens.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
