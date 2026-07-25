package oauthsvc_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type testEnv struct {
	svc       *oauthsvc.Service
	q         *store.Queries
	cfg       *configsvc.Service
	pool      *pgxpool.Pool
	kr        *crypto.Keyring
	tokenHit  *atomic.Int64
	tokenMu   sync.Mutex
	lastForm  url.Values
	started   chan struct{}
	release   <-chan struct{}
	omitScope bool

	validatorMu     sync.RWMutex
	validatorResult connector.CredentialValidationResult
	validatorErr    error
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
	provider := testutil.NewProviderServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.tokenHit.Add(1)
		_ = r.ParseForm()
		env.tokenMu.Lock()
		env.lastForm = r.PostForm
		started, release := env.started, env.release
		omitScope := env.omitScope
		env.tokenMu.Unlock()
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
		accessToken := "at-" + r.PostForm.Get("code")
		w.Header().Set("Content-Type", "application/json")
		body := map[string]any{
			"access_token": accessToken, "token_type": "Bearer",
			"refresh_token": "rt-1", "expires_in": 3600,
		}
		if !omitScope {
			body["scope"] = "read,write"
		}
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
				AuthorizationEndpoint: "https://provider.example/authorize",
				TokenEndpoint:         provider.BaseURL + "/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://provider.example:443"},
					TokenOrigins:         []string{provider.Origin},
				},
				Scopes:              []string{"read", "write"},
				TokenScopeSeparator: connector.OAuthScopeComma,
				UsePKCE:             usePKCE,
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
	env.validatorResult = connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   "account-1",
			DisplayName: "Example Account",
		},
		GrantedScopes: []string{"read", "write"},
		ScopesKnown:   true,
	}
	validators := connector.CredentialValidatorMap{
		"example_app": {
			"oauth": func(
				_ context.Context,
				input connector.CredentialValidationInput,
			) (connector.CredentialValidationResult, error) {
				if input.AccessToken == "" ||
					input.TokenType != "Bearer" ||
					len(input.Fields) != 0 {
					t.Fatalf("validator input = %+v", input)
				}
				env.validatorMu.RLock()
				defer env.validatorMu.RUnlock()
				return env.validatorResult, env.validatorErr
			},
		},
	}
	env.svc = oauthsvc.New(
		q,
		reg,
		cfg,
		kr,
		provider.Factory,
		"https://connect.internal",
		connector.AuthorizationRuntime{
			CredentialValidators: validators,
		},
	)
	env.q = q
	env.cfg = cfg
	env.kr = kr
	return env
}

func (e *testEnv) begin(
	ctx context.Context,
	t connector.Type,
	authMethodKey string,
	alias string,
	redirectURL string,
) (oauthsvc.BeginResult, error) {
	return e.svc.BeginWithInput(ctx, oauthsvc.BeginInput{
		ConnectorType: t,
		AuthMethodKey: authMethodKey,
		Alias:         alias,
		RedirectURL:   redirectURL,
	})
}

func (e *testEnv) beginReauth(
	ctx context.Context,
	connectionID [16]byte,
	redirectURL string,
) (oauthsvc.BeginResult, error) {
	return e.svc.BeginReauthWithInput(ctx, oauthsvc.ReauthInput{
		ConnectionID: connectionID,
		RedirectURL:  redirectURL,
	})
}

func (e *testEnv) handleCallback(
	ctx context.Context,
	state string,
	code string,
) (oauthsvc.CallbackResult, error) {
	return e.svc.HandleCallbackWithInput(ctx, oauthsvc.CallbackInput{
		State: state,
		Code:  code,
	})
}

func (e *testEnv) handleProviderError(
	ctx context.Context,
	state string,
) (oauthsvc.CallbackResult, error) {
	return e.svc.HandleProviderErrorWithInput(
		ctx,
		oauthsvc.ProviderErrorInput{State: state},
	)
}

// callbackPausedAtTokenEndpoint 启动一次回调并让它停在假 token endpoint 内，
// 返回的函数放行 Provider 响应并交出回调结果。
func (e *testEnv) callbackPausedAtTokenEndpoint(
	t *testing.T,
	ctx context.Context,
	state string,
	code string,
) func() error {
	t.Helper()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	e.tokenMu.Lock()
	e.started, e.release = started, release
	e.tokenMu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, callbackErr := e.handleCallback(ctx, state, code)
		done <- callbackErr
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not claim and reach the token endpoint")
	}
	return func() error {
		close(release)
		callbackErr := <-done
		e.tokenMu.Lock()
		e.started, e.release = nil, nil
		e.tokenMu.Unlock()
		return callbackErr
	}
}

// activeConnection 走完一次完整的初始授权，返回已 active 的 Connection 行。
// 所有 reauth 用例都以这个状态开场。
func (e *testEnv) activeConnection(
	t *testing.T,
	ctx context.Context,
) store.Connection {
	t.Helper()
	begin, err := e.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.handleCallback(
		ctx,
		stateFrom(t, begin.AuthorizationURL),
		"initial",
	); err != nil {
		t.Fatal(err)
	}
	row, err := e.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func stateFrom(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func hashForTest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestFullAuthorizationFlow(t *testing.T) {
	env := newEnv(t, true)
	ctx := context.Background()

	begin, err := env.begin(ctx, "example_app", "oauth", "acct-1", "https://saas.example/done")
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

	result, err := env.handleCallback(ctx, qs.Get("state"), "auth-code")
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
	if !row.ScopesKnown ||
		len(row.Scopes) != 2 ||
		row.Scopes[0] != "read" ||
		row.Scopes[1] != "write" {
		t.Fatalf("validated scopes 未原子保存: known=%v scopes=%v", row.ScopesKnown, row.Scopes)
	}
	var profile struct {
		AccountID   string `json:"account_id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal(row.Profile, &profile); err != nil ||
		profile.AccountID != "account-1" ||
		profile.DisplayName != "Example Account" {
		t.Fatalf("validated profile 未原子保存: %+v err=%v", profile, err)
	}
	plaintext, err := env.kr.Decrypt(
		row.Credential,
		int(row.SecretKeyVersion),
		[]byte(row.ID.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	savedCredential, err := credential.UnmarshalOAuth(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if savedCredential.AccessToken != "at-auth-code" ||
		savedCredential.TokenType != "Bearer" ||
		savedCredential.RefreshToken != "rt-1" {
		t.Fatalf("OAuth credential snapshot 不符: %+v", savedCredential)
	}
	// state 只能用一次
	maintenanceStatements := newAuthorizationMaintenanceStatementProbe(
		t,
		ctx,
		env.pool,
	)
	if _, err := env.handleCallback(
		ctx,
		qs.Get("state"),
		"again",
	); err != oauthsvc.ErrInvalidState {
		t.Fatalf("重放 state 应 ErrInvalidState, got %v", err)
	}
	if got := maintenanceStatements(); got != 0 {
		t.Fatalf("OAuth replay ran %d global maintenance statements", got)
	}
}

// 省略 scope 的 token 响应按 RFC 6749 回退到请求的 scope，并原子落库。
func TestCallbackPreservesTokenScopeProvenance(t *testing.T) {
	env := newEnv(t, false)
	env.tokenMu.Lock()
	env.omitScope = true
	env.tokenMu.Unlock()
	env.validatorMu.Lock()
	env.validatorResult.GrantedScopes = nil
	env.validatorResult.ScopesKnown = false
	env.validatorMu.Unlock()

	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, begin.AuthorizationURL),
		"standard-omitted",
	); err != nil {
		t.Fatal(err)
	}

	connection, err := env.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != "active" ||
		!connection.ScopesKnown ||
		len(connection.Scopes) != 2 ||
		connection.Scopes[0] != "read" ||
		connection.Scopes[1] != "write" {
		t.Fatalf(
			"standard omitted scope snapshot = status=%s known=%v scopes=%v",
			connection.Status,
			connection.ScopesKnown,
			connection.Scopes,
		)
	}
}

func TestBeginSnapshotsRequestedScopes(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}

	var scopes []string
	if err := env.pool.QueryRow(
		ctx,
		`select requested_scopes
		 from oauth_authorizations
		 where connection_id = $1`,
		begin.ConnectionID,
	).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0] != "read" || scopes[1] != "write" {
		t.Fatalf("requested scope snapshot = %v", scopes)
	}
}

func TestConcurrentCallbackOnlyClaimWinnerContactsProvider(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFrom(t, begin.AuthorizationURL)

	start := make(chan struct{})
	type outcome struct {
		result oauthsvc.CallbackResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, code := range []string{"first", "second"} {
		code := code
		go func() {
			<-start
			result, err := env.handleCallback(ctx, state, code)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)

	var successes, rejected int
	for range 2 {
		got := <-outcomes
		switch {
		case got.err == nil:
			successes++
			if got.result.ConnectionID != begin.ConnectionID {
				t.Fatalf("winner connection = %s", got.result.ConnectionID)
			}
		case errors.Is(got.err, oauthsvc.ErrInvalidState):
			rejected++
		default:
			t.Fatalf("concurrent callback error = %v", got.err)
		}
	}
	if successes != 1 || rejected != 1 || env.tokenHit.Load() != 1 {
		t.Fatalf(
			"success=%d rejected=%d token calls=%d",
			successes,
			rejected,
			env.tokenHit.Load(),
		)
	}
}

func TestBeginWithoutAlias(t *testing.T) {
	env := newEnv(t, false)
	begin, err := env.begin(context.Background(), "example_app", "oauth", "", "")
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
	connection := env.activeConnection(t, ctx)

	re, err := env.beginReauth(ctx, connection.ID, "https://saas.example/back")
	if err != nil {
		t.Fatal(err)
	}
	if re.ConnectionID != connection.ID {
		t.Fatalf("reauth 应复用同一 ID")
	}
	result, err := env.handleCallback(ctx, stateFrom(t, re.AuthorizationURL), "code-2")
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionID != connection.ID || result.RedirectURL != "https://saas.example/back" {
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

func TestSupersededReauthCallbackDoesNotContactProvider(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	connection := env.activeConnection(t, ctx)
	first, err := env.beginReauth(ctx, connection.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := env.beginReauth(ctx, connection.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	callsBefore := env.tokenHit.Load()

	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, first.AuthorizationURL),
		"stale",
	); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("superseded callback = %v", err)
	}
	if env.tokenHit.Load() != callsBefore {
		t.Fatalf("superseded callback contacted token endpoint")
	}
	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, second.AuthorizationURL),
		"current",
	); err != nil {
		t.Fatal(err)
	}
}

func TestReauthReverseCompletionCannotOverwriteNewerAttempt(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	before := env.activeConnection(t, ctx)
	first, err := env.beginReauth(ctx, before.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	finishOld := env.callbackPausedAtTokenEndpoint(
		t,
		ctx,
		stateFrom(t, first.AuthorizationURL),
		"old",
	)
	second, err := env.beginReauth(ctx, before.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := finishOld(); !errors.Is(err, oauthsvc.ErrAuthorizationConflict) {
		t.Fatalf("old callback completion = %v", err)
	}

	afterOld, err := env.q.GetConnection(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterOld.Credential, before.Credential) {
		t.Fatal("stale callback overwrote the active credential")
	}

	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, second.AuthorizationURL),
		"new",
	); err != nil {
		t.Fatal(err)
	}
	finalRow, err := env.q.GetConnection(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := env.kr.Decrypt(
		finalRow.Credential,
		int(finalRow.SecretKeyVersion),
		[]byte(finalRow.ID.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	finalCredential, err := credential.UnmarshalOAuth(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if finalCredential.AccessToken != "at-new" {
		t.Fatalf("newest callback credential = %q", finalCredential.AccessToken)
	}
}

func TestInitialConfigGenerationBumpCleansSupersededPendingConnection(
	t *testing.T,
) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFrom(t, begin.AuthorizationURL)

	finishCallback := env.callbackPausedAtTokenEndpoint(
		t,
		ctx,
		state,
		"old-policy",
	)

	if _, err := env.cfg.Put(
		ctx,
		"example_app",
		map[string]any{"client_id": "cid-rotated"},
		nil,
		time.Time{},
	); err != nil {
		t.Fatal(err)
	}
	bumped, err := env.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if bumped.Status != "pending" || bumped.AuthorizationGeneration != 2 {
		t.Fatalf("config bump did not advance pending generation: %+v", bumped)
	}

	if err := finishCallback(); !errors.Is(
		err,
		oauthsvc.ErrAuthorizationConflict,
	) {
		t.Fatalf("config-invalidated callback error = %v", err)
	}
	if _, err := env.q.GetConnection(ctx, begin.ConnectionID); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("superseded initial pending Connection lookup error = %v", err)
	}
	if _, err := env.q.GetOAuthAuthorizationByStateHash(
		ctx,
		hashForTest(state),
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("superseded initial authorization lookup error = %v", err)
	}
}

func TestReauthConfigGenerationBumpPreservesActiveCredential(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	before := env.activeConnection(t, ctx)
	reauth, err := env.beginReauth(ctx, before.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFrom(t, reauth.AuthorizationURL)

	finishCallback := env.callbackPausedAtTokenEndpoint(
		t,
		ctx,
		state,
		"replacement",
	)
	if _, err := env.cfg.Put(
		ctx,
		"example_app",
		map[string]any{"client_id": "cid-rotated"},
		nil,
		time.Time{},
	); err != nil {
		t.Fatal(err)
	}
	if err := finishCallback(); !errors.Is(
		err,
		oauthsvc.ErrAuthorizationConflict,
	) {
		t.Fatalf("config-invalidated reauth callback error = %v", err)
	}
	after, err := env.q.GetConnection(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "active" ||
		!bytes.Equal(after.Credential, before.Credential) {
		t.Fatalf("config-invalidated reauth changed active credential: %+v", after)
	}
	var authStatus string
	if err := env.pool.QueryRow(
		ctx,
		`select status
		 from oauth_authorizations
		 where state_hash = $1`,
		hashForTest(state),
	).Scan(&authStatus); err != nil {
		t.Fatal(err)
	}
	if authStatus != "superseded" {
		t.Fatalf("config-invalidated reauth status = %q", authStatus)
	}
}

func TestInitialValidatorFailureCleansPendingConnection(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	env.validatorMu.Lock()
	env.validatorErr = errors.New("validator rejected credential")
	env.validatorMu.Unlock()

	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, begin.AuthorizationURL),
		"rejected",
	); err == nil {
		t.Fatal("validator failure should fail callback")
	}
	if _, err := env.q.GetConnection(ctx, begin.ConnectionID); err == nil {
		t.Fatal("failed initial validation left a pending connection")
	}
}

func TestClaimedInitialCleanupFailureIsObservableAndJanitorRecovers(
	t *testing.T,
) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	validatorFailure := errors.New("injected validator failure")
	env.validatorMu.Lock()
	env.validatorErr = validatorFailure
	env.validatorMu.Unlock()

	if _, err := env.pool.Exec(
		ctx,
		`create function reject_oauth_cleanup() returns trigger
		 language plpgsql as $$
		 begin
		   raise exception 'injected oauth cleanup failure';
		 end
		 $$`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(
		ctx,
		`create trigger reject_oauth_cleanup
		 before delete on connections
		 for each row execute function reject_oauth_cleanup()`,
	); err != nil {
		t.Fatal(err)
	}

	_, callbackErr := env.handleCallback(
		ctx,
		stateFrom(t, begin.AuthorizationURL),
		"rejected",
	)
	if !errors.Is(callbackErr, validatorFailure) {
		t.Fatalf("callback error lost original failure: %v", callbackErr)
	}
	if callbackErr == nil ||
		!strings.Contains(callbackErr.Error(), "cleanup claimed authorization") ||
		!strings.Contains(callbackErr.Error(), "injected oauth cleanup failure") {
		t.Fatalf("callback cleanup failure was not observable: %v", callbackErr)
	}
	if _, err := env.q.GetConnection(ctx, begin.ConnectionID); err != nil {
		t.Fatalf("injected cleanup failure should leave recoverable row: %v", err)
	}
	var status string
	if err := env.pool.QueryRow(
		ctx,
		`select status
		 from oauth_authorizations
		 where connection_id = $1`,
		begin.ConnectionID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "claimed" {
		t.Fatalf("authorization status after cleanup failure = %q", status)
	}
	if _, err := env.pool.Exec(
		ctx,
		`update oauth_authorizations
		 set expires_at = CURRENT_TIMESTAMP - interval '3 minutes'
		 where connection_id = $1`,
		begin.ConnectionID,
	); err != nil {
		t.Fatal(err)
	}
	maintenanceErr := env.svc.MaintainAuthorizations(ctx)
	if maintenanceErr == nil ||
		!strings.Contains(
			maintenanceErr.Error(),
			"recover stale claimed initial authorizations",
		) {
		t.Fatalf("maintenance cleanup failure was not observable: %v", maintenanceErr)
	}

	if _, err := env.pool.Exec(
		ctx,
		`drop trigger reject_oauth_cleanup on connections`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(
		ctx,
		`drop function reject_oauth_cleanup()`,
	); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.MaintainAuthorizations(ctx); err != nil {
		t.Fatalf("maintenance did not recover after DB repair: %v", err)
	}
	if _, err := env.q.GetConnection(ctx, begin.ConnectionID); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("recovered Connection lookup error = %v", err)
	}
}

func TestReauthValidatorFailurePreservesActiveCredential(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	before := env.activeConnection(t, ctx)
	reauth, err := env.beginReauth(ctx, before.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	env.validatorMu.Lock()
	env.validatorErr = errors.New("validator rejected replacement")
	env.validatorMu.Unlock()

	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, reauth.AuthorizationURL),
		"replacement",
	); err == nil {
		t.Fatal("validator failure should fail reauth callback")
	}
	after, err := env.q.GetConnection(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "active" || !bytes.Equal(after.Credential, before.Credential) {
		t.Fatalf("failed reauth changed active connection: %+v", after)
	}
}

func TestProviderErrorConsumesAttemptWithoutTokenExchange(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := stateFrom(t, begin.AuthorizationURL)

	result, err := env.handleProviderError(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if result.ConnectionID != begin.ConnectionID || env.tokenHit.Load() != 0 {
		t.Fatalf("provider error result=%+v token calls=%d", result, env.tokenHit.Load())
	}
	if _, err := env.q.GetConnection(ctx, begin.ConnectionID); err == nil {
		t.Fatal("provider-denied initial attempt left pending connection")
	}
	maintenanceStatements := newAuthorizationMaintenanceStatementProbe(
		t,
		ctx,
		env.pool,
	)
	if _, err := env.handleProviderError(
		ctx,
		state,
	); err != oauthsvc.ErrInvalidState {
		t.Fatalf("provider error state replay = %v", err)
	}
	if got := maintenanceStatements(); got != 0 {
		t.Fatalf(
			"provider-error replay ran %d global maintenance statements",
			got,
		)
	}
}

func TestExpiredInitialCallbackDefersSafeCleanupToMaintenance(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()
	begin, err := env.begin(ctx, "example_app", "oauth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(
		ctx,
		`update oauth_authorizations
		 set expires_at = CURRENT_TIMESTAMP - interval '1 second'
		 where connection_id = $1`,
		begin.ConnectionID,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := env.handleCallback(
		ctx,
		stateFrom(t, begin.AuthorizationURL),
		"expired",
	); !errors.Is(err, oauthsvc.ErrInvalidState) {
		t.Fatalf("expired callback = %v", err)
	}
	if env.tokenHit.Load() != 0 {
		t.Fatal("expired callback contacted token endpoint")
	}
	pending, err := env.q.GetConnection(ctx, begin.ConnectionID)
	if err != nil {
		t.Fatalf("rejected callback performed synchronous cleanup: %v", err)
	}
	if pending.Status != "pending" {
		t.Fatalf("expired initial connection status = %q", pending.Status)
	}

	if err := env.svc.MaintainAuthorizations(ctx); err != nil {
		t.Fatalf("periodic authorization maintenance: %v", err)
	}
	if _, err := env.q.GetConnection(ctx, begin.ConnectionID); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("periodic cleanup connection lookup error = %v", err)
	}
}

func newAuthorizationMaintenanceStatementProbe(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) func() int {
	t.Helper()
	if _, err := pool.Exec(
		ctx,
		`create table callback_maintenance_sql_probe (
		   operation text not null
		 );
		 create function record_callback_maintenance_sql()
		 returns trigger
		 language plpgsql
		 as $$
		 begin
		   insert into callback_maintenance_sql_probe(operation)
		   values (TG_TABLE_NAME || ':' || TG_OP);
		   return null;
		 end
		 $$;
		 create trigger probe_callback_connection_delete
		 after delete on connections
		 for each statement execute function record_callback_maintenance_sql();
		 create trigger probe_callback_authorization_update
		 after update on oauth_authorizations
		 for each statement execute function record_callback_maintenance_sql()`,
	); err != nil {
		t.Fatal(err)
	}
	return func() int {
		t.Helper()
		var statements int
		if err := pool.QueryRow(
			ctx,
			`select count(*) from callback_maintenance_sql_probe`,
		).Scan(&statements); err != nil {
			t.Fatal(err)
		}
		return statements
	}
}

func TestRandomInvalidStateDoesNotRunGlobalAuthorizationMaintenance(
	t *testing.T,
) {
	env := newEnv(t, false)
	ctx := context.Background()
	maintenanceStatements := newAuthorizationMaintenanceStatementProbe(
		t,
		ctx,
		env.pool,
	)

	for name, invoke := range map[string]func() error{
		"oauth callback": func() error {
			_, err := env.handleCallback(
				ctx,
				"random-invalid-oauth-state",
				"code",
			)
			return err
		},
		"provider error callback": func() error {
			_, err := env.handleProviderError(
				ctx,
				"random-invalid-provider-error-state",
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); err != oauthsvc.ErrInvalidState {
				t.Fatalf("invalid state error = %#v", err)
			}
		})
	}

	if got := maintenanceStatements(); got != 0 {
		t.Fatalf(
			"invalid states ran %d global maintenance statements",
			got,
		)
	}
}

func TestBeginValidation(t *testing.T) {
	env := newEnv(t, false)
	ctx := context.Background()

	if _, err := env.begin(ctx, "example_app", "oauth", "Bad_Alias", ""); !errors.Is(err, connsvc.ErrInvalidAlias) {
		t.Fatalf("非法 alias: %v", err)
	}
	if _, err := env.begin(ctx, "nope", "oauth", "", ""); !errors.Is(err, oauthsvc.ErrUnknownConnector) {
		t.Fatalf("未知 connector: %v", err)
	}
	if _, err := env.begin(ctx, "example_app", "nope", "", ""); !errors.Is(err, oauthsvc.ErrUnknownAuthMethod) {
		t.Fatalf("未知 method: %v", err)
	}
	if _, err := env.beginReauth(ctx, [16]byte{1}, ""); !errors.Is(err, oauthsvc.ErrConnectionGone) {
		t.Fatalf("不存在的连接 reauth: %v", err)
	}
}
