package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type sessionEnv struct {
	svc        *sessions.Service
	q          *store.Queries
	connID     uuid.UUID
	apiTokenID uuid.UUID
}

type fakeLister map[uuid.UUID][]*mcp.Tool

func (f fakeLister) ListTools(_ context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error) {
	return f[connectionID], nil
}

func newSvc(t *testing.T) sessionEnv {
	t.Helper()
	pool := testutil.NewDB(t)
	q := store.New(pool)
	apiTokenID := uuid.New()
	if err := q.InsertAPIToken(t.Context(), store.InsertAPITokenParams{
		ID: apiTokenID, Name: "test", TokenHash: uuid.NewString(),
	}); err != nil {
		t.Fatal(err)
	}
	connID := uuid.New()
	if _, err := pool.Exec(context.Background(), `insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'github', 'gh-main', 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`, connID); err != nil {
		t.Fatal(err)
	}
	lister := fakeLister{connID: {
		{Name: "list_issues", InputSchema: map[string]any{"type": "object"}},
		{Name: "create_issue", InputSchema: map[string]any{"type": "object"}},
	}}
	return sessionEnv{
		svc: sessions.New(q, lister), q: q, connID: connID, apiTokenID: apiTokenID,
	}
}

func TestCreateAndResolve(t *testing.T) {
	env := newSvc(t)
	ctx := context.Background()

	token, err := env.svc.Create(ctx, env.apiTokenID, env.connID,
		[]string{"list_issues"}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Fatalf("token 应为 64 位 hex: %d", len(token))
	}

	view, err := env.svc.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if view.ConnectionID != env.connID || !view.AllowedTools["list_issues"] ||
		len(view.Tools) != 1 || view.Tools[0].Name != "list_issues" {
		t.Fatalf("view 不符: %+v", view)
	}
	if until := time.Until(view.ExpiresAt); until < time.Hour || until > 2*time.Hour {
		t.Fatalf("ttl 不符: %v", until)
	}

	if _, err := env.svc.Resolve(ctx, "deadbeef"); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("未知 token 应 ErrInvalidSession: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	env := newSvc(t)
	ctx := context.Background()

	cases := []struct {
		name         string
		connectionID uuid.UUID
		allowlist    []string
		ttl          time.Duration
		wantCode     string
	}{
		{"未知 connection", uuid.New(), nil, 0, "unknown_connection"},
		{"负 ttl", env.connID, nil, -time.Second, "invalid_ttl"},
		{"超上限 ttl", env.connID, nil, 25 * time.Hour, "invalid_ttl"},
		{"allowlist 非法名", env.connID, []string{"bad/tool"}, 0, "invalid_allowlist"},
		{"allowlist 工具不存在", env.connID, []string{"missing"}, 0, "invalid_allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.Create(ctx, env.apiTokenID, tc.connectionID, tc.allowlist, tc.ttl)
			var verr *sessions.ValidationError
			if !errors.As(err, &verr) || verr.Code != tc.wantCode {
				t.Fatalf("want %s, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestResolveExpired(t *testing.T) {
	env := newSvc(t)
	ctx := context.Background()

	token, err := env.svc.Create(ctx, env.apiTokenID, env.connID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// 过期
	if _, err := qExec(env.q, ctx, `update mcp_sessions set expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Resolve(ctx, token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("过期应 ErrInvalidSession: %v", err)
	}
}

// qExec 借 store.Queries 的底层连接执行裸 SQL（测试专用）。
func qExec(q *store.Queries, ctx context.Context, sql string) (any, error) {
	tx, qtx, err := q.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	_ = qtx
	if _, err := tx.Exec(ctx, sql); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return nil, tx.Commit(ctx)
}

func TestCreateRejectsInactiveConnection(t *testing.T) {
	env := newSvc(t)
	if _, err := qExec(env.q, t.Context(), `update connections set status = 'reauth_required'`); err != nil {
		t.Fatal(err)
	}
	_, err := env.svc.Create(t.Context(), env.apiTokenID, env.connID, nil, time.Hour)
	var validation *sessions.ValidationError
	if !errors.As(err, &validation) || validation.Code != "connection_not_active" {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveRejectsSessionAfterParentTokenRevoked(t *testing.T) {
	env := newSvc(t)
	token, err := env.svc.Create(t.Context(), env.apiTokenID, env.connID, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := env.q.RevokeAPIToken(t.Context(), env.apiTokenID); err != nil || n != 1 {
		t.Fatalf("撤销 API token: n=%d err=%v", n, err)
	}
	if _, err := env.svc.Resolve(t.Context(), token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("父 API token 撤销后 session 应失效: %v", err)
	}
}
