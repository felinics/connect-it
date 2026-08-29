package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
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

	// Aliases are no longer unique: duplicates and empty aliases are both allowed.
	if _, err := q.CreateConnection(ctx, store.CreateConnectionParams{
		ID: uuid.New(), ConnectorType: "github", Alias: &alias, AuthMethod: "oauth",
		Credential: []byte{1}, SecretKeyVersion: 1, Scopes: []string{}, Status: "active",
	}); err != nil {
		t.Fatalf("a duplicate alias should be allowed: %v", err)
	}
	if _, err := q.CreateConnection(ctx, store.CreateConnectionParams{
		ID: uuid.New(), ConnectorType: "github", Alias: nil, AuthMethod: "oauth",
		Credential: []byte{1}, SecretKeyVersion: 1, Scopes: []string{}, Status: "pending",
	}); err != nil {
		t.Fatalf("an empty alias should be allowed: %v", err)
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
		t.Fatalf("the transactional update did not take effect: %+v", got)
	}

	if n, _ := q.DeleteConnection(ctx, id); n != 1 {
		t.Fatal("the delete should affect 1 row")
	}
	_ = alias
	if _, err := q.GetConnection(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("after delete it should yield ErrNoRows: %v", err)
	}
}

func TestBeginTxWithoutPool(t *testing.T) {
	// A Queries built from a nil DBTX cannot start transactions.
	q := store.New(nil)
	if _, _, err := q.BeginTx(context.Background()); !errors.Is(err, store.ErrNoTransactions) {
		t.Fatalf("expected ErrNoTransactions, got %v", err)
	}
}
