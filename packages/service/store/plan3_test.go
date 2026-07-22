package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func TestConnectionCRUDAndForUpdate(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	id := uuid.New()
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	alias := "gh-main"
	row, err := q.CreateConnection(ctx, store.CreateConnectionParams{
		ID: id, ConnectorType: "github", Alias: &alias, AuthMethod: "oauth",
		Credential: []byte{1}, SecretKeyVersion: 1, Scopes: []string{"repo"},
		Status: "active", AccessTokenExpiresAt: &exp,
	})
	if err != nil || row.Alias == nil || *row.Alias != "gh-main" || row.AccessTokenExpiresAt == nil {
		t.Fatalf("create: %+v err=%v", row, err)
	}

	// alias 不再唯一：同名与空 alias 都允许
	if _, err := q.CreateConnection(ctx, store.CreateConnectionParams{
		ID: uuid.New(), ConnectorType: "github", Alias: &alias, AuthMethod: "oauth",
		Credential: []byte{1}, SecretKeyVersion: 1, Scopes: []string{}, Status: "active",
	}); err != nil {
		t.Fatalf("同名 alias 应允许: %v", err)
	}
	if _, err := q.CreateConnection(ctx, store.CreateConnectionParams{
		ID: uuid.New(), ConnectorType: "github", Alias: nil, AuthMethod: "oauth",
		Credential: []byte{1}, SecretKeyVersion: 1, Scopes: []string{}, Status: "pending",
	}); err != nil {
		t.Fatalf("空 alias 应允许: %v", err)
	}

	// BeginTx + FOR UPDATE
	tx, qtx, err := q.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := qtx.GetConnectionForUpdate(ctx, id)
	if err != nil || locked.ID != id {
		t.Fatalf("for update: %+v err=%v", locked, err)
	}
	if err := qtx.UpdateConnectionStatus(ctx, store.UpdateConnectionStatusParams{
		ID: id, Status: "reauth_required",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetConnection(ctx, id)
	if got.Status != "reauth_required" {
		t.Fatalf("事务更新未生效: %+v", got)
	}

	if n, _ := q.DeleteConnection(ctx, id); n != 1 {
		t.Fatal("删除应影响 1 行")
	}
	_ = alias
	if _, err := q.GetConnection(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("删除后应 ErrNoRows: %v", err)
	}
}

func TestOAuthAuthorizationQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	id := uuid.New()
	row, err := q.CreateOAuthAuthorization(ctx, store.CreateOAuthAuthorizationParams{
		ID: id, ConnectorType: "github", StateHash: "abc", PkceVerifier: []byte{7},
		SecretKeyVersion: 1, AuthMethod: "oauth", Alias: "gh-main",
		ConnectionID: nil, RedirectUrl: "https://saas.example/done",
		Status: "pending", ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if err != nil || row.ConnectionID != nil || row.Alias != "gh-main" || row.RedirectUrl != "https://saas.example/done" {
		t.Fatalf("create authz: %+v err=%v", row, err)
	}

	got, err := q.GetOAuthAuthorizationByStateHash(ctx, "abc")
	if err != nil || got.ID != id {
		t.Fatalf("by state: %+v err=%v", got, err)
	}

	connID := uuid.New()
	if err := q.CompleteOAuthAuthorization(ctx, store.CompleteOAuthAuthorizationParams{
		ID: id, Status: "completed", ConnectionID: &connID,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetOAuthAuthorizationByStateHash(ctx, "abc")
	if got.Status != "completed" || got.ConnectionID == nil || *got.ConnectionID != connID {
		t.Fatalf("complete 未生效: %+v", got)
	}

	if err := q.DeleteExpiredOAuthAuthorizations(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBeginTxWithoutPool(t *testing.T) {
	// 用 nil DBTX 构造的 Queries 不支持事务。
	q := store.New(nil)
	if _, _, err := q.BeginTx(context.Background()); !errors.Is(err, store.ErrNoTransactions) {
		t.Fatalf("应 ErrNoTransactions, got %v", err)
	}
}
