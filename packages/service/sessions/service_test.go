package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func newSvc(t *testing.T) (*sessions.Service, *store.Queries, uuid.UUID) {
	t.Helper()
	pool := testutil.NewDB(t)
	q := store.New(pool)
	connID := uuid.New()
	if _, err := pool.Exec(context.Background(), `insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'github', 'gh-main', 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`, connID); err != nil {
		t.Fatal(err)
	}
	return sessions.New(q), q, connID
}

func TestSplitExposedName(t *testing.T) {
	cases := []struct {
		name        string
		alias, tool string
		ok          bool
	}{
		{"gh-main__list_issues", "gh-main", "list_issues", true},
		{"a__b__c", "a", "b__c", false}, // tool 段含连字符外字符集校验：b__c 合法（_ 允许）
		{"noseparator", "", "", false},
		{"Bad__tool", "", "", false},
		{"gh__Bad-Tool", "", "", false},
	}
	for _, tc := range cases {
		alias, tool, ok := sessions.SplitExposedName(tc.name)
		if tc.name == "a__b__c" {
			// "__" 取第一个：alias=a tool=b__c，tool 字符集 [a-z0-9_]+ 允许下划线 → 合法
			if !ok || alias != "a" || tool != "b__c" {
				t.Fatalf("a__b__c: got %q %q %v", alias, tool, ok)
			}
			continue
		}
		if ok != tc.ok || alias != tc.alias || tool != tc.tool {
			t.Fatalf("%s: got %q %q %v", tc.name, alias, tool, ok)
		}
	}
}

func TestCreateAndResolve(t *testing.T) {
	svc, _, connID := newSvc(t)
	ctx := context.Background()

	token, err := svc.Create(ctx, map[string]uuid.UUID{"gh-main": connID},
		[]string{"gh-main__list_issues"}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Fatalf("token 应为 64 位 hex: %d", len(token))
	}

	view, err := svc.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if view.Bindings["gh-main"] != connID || !view.Allowlist["gh-main__list_issues"] {
		t.Fatalf("view 不符: %+v", view)
	}
	if until := time.Until(view.ExpiresAt); until < time.Hour || until > 2*time.Hour {
		t.Fatalf("ttl 不符: %v", until)
	}

	if _, err := svc.Resolve(ctx, "deadbeef"); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("未知 token 应 ErrInvalidSession: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _, connID := newSvc(t)
	ctx := context.Background()

	cases := []struct {
		name      string
		bindings  map[string]uuid.UUID
		allowlist []string
		ttl       time.Duration
		wantCode  string
	}{
		{"空 bindings", map[string]uuid.UUID{}, nil, 0, "empty_bindings"},
		{"非法 alias", map[string]uuid.UUID{"Bad_Alias": connID}, nil, 0, "invalid_alias"},
		{"未知 connection", map[string]uuid.UUID{"gh": uuid.New()}, nil, 0, "unknown_connection"},
		{"负 ttl", map[string]uuid.UUID{"gh-main": connID}, nil, -time.Second, "invalid_ttl"},
		{"超上限 ttl", map[string]uuid.UUID{"gh-main": connID}, nil, 25 * time.Hour, "invalid_ttl"},
		{"allowlist 非法名", map[string]uuid.UUID{"gh-main": connID}, []string{"nounderscore"}, 0, "invalid_allowlist"},
		{"allowlist 未绑定 alias", map[string]uuid.UUID{"gh-main": connID}, []string{"other__tool"}, 0, "invalid_allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(ctx, tc.bindings, tc.allowlist, tc.ttl)
			var verr *sessions.ValidationError
			if !errors.As(err, &verr) || verr.Code != tc.wantCode {
				t.Fatalf("want %s, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestResolveExpiredAndRevoked(t *testing.T) {
	svc, q, connID := newSvc(t)
	ctx := context.Background()

	token, err := svc.Create(ctx, map[string]uuid.UUID{"gh-main": connID}, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// 过期
	if _, err := qExec(q, ctx, `update mcp_sessions set expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("过期应 ErrInvalidSession: %v", err)
	}
	// 吊销
	if _, err := qExec(q, ctx, `update mcp_sessions set expires_at = now() + interval '1 hour', status = 'revoked'`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("吊销应 ErrInvalidSession: %v", err)
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

func TestConnectionConnectorTypes(t *testing.T) {
	svc, _, connID := newSvc(t)
	got, err := svc.ConnectionConnectorTypes(context.Background(), []uuid.UUID{connID, uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[connID] != "github" {
		t.Fatalf("got %v", got)
	}
}
