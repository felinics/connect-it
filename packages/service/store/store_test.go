package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

func TestConnectorConfigCRUD(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	first, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "github",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{"client_id":"abc"}`),
		SecretConfig:        []byte{1, 2, 3},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt.IsZero() || first.CreatedAt.IsZero() {
		t.Fatal("created_at/updated_at were not written")
	}

	got, err := q.GetConnectorConfig(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	var pub map[string]any
	if err := json.Unmarshal(got.PublicConfig, &pub); err != nil {
		t.Fatal(err)
	}
	if pub["client_id"] != "abc" || got.SecretKeyVersion != 1 {
		t.Fatalf("roundtrip failed: %+v", got)
	}
	// upsert updates an existing row.
	second, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "github",
		ConfigSchemaVersion: 2,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigSchemaVersion != 2 || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("upsert should bump the version and preserve created_at: %+v", second)
	}

	n, err := q.DeleteConnectorConfig(ctx, "github")
	if err != nil || n != 1 {
		t.Fatalf("the delete should affect 1 row: n=%d err=%v", n, err)
	}
	if n, _ := q.DeleteConnectorConfig(ctx, "github"); n != 0 {
		t.Fatal("deleting twice should affect 0 rows")
	}
	if _, err := q.GetConnectorConfig(ctx, "github"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("after delete it should yield ErrNoRows, got %v", err)
	}
}

func TestUpdateConnectorConfigIfMatch(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	row, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{"a":"b"}`),
		SecretConfig:        []byte{9},
		SecretKeyVersion:    1,
		UpdatedAt:           row.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("a matching updated_at should update successfully: %v", err)
	}
	if updated.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatal("updated_at should have advanced")
	}

	// Updating again with a stale updated_at must yield ErrNoRows.
	_, err = q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
		UpdatedAt:           row.UpdatedAt,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a stale If-Match should yield ErrNoRows, got %v", err)
	}
}

func TestAdminAccountQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	if _, err := q.GetAdminAccount(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("an empty table should yield ErrNoRows, got %v", err)
	}
	n, err := q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: "hash1",
	})
	if err != nil || n != 1 {
		t.Fatalf("the first insert should affect 1 row: n=%d err=%v", n, err)
	}
	n, err = q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: "hash2",
	})
	if err != nil || n != 0 {
		t.Fatalf("an existing row should affect 0 rows: n=%d err=%v", n, err)
	}
	acct, err := q.GetAdminAccount(ctx)
	if err != nil || acct.PasswordHash != "hash1" || acct.ID != 1 {
		t.Fatalf("an existing account must not be overwritten: %+v err=%v", acct, err)
	}
	if n, _ := q.UpdateAdminPassword(ctx, "hash3"); n != 1 {
		t.Fatal("changing the password should affect 1 row")
	}
	acct, _ = q.GetAdminAccount(ctx)
	if acct.PasswordHash != "hash3" {
		t.Fatalf("the password was not updated: %+v", acct)
	}
}

func TestAPITokenQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	id := uuid.New()
	if err := q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: id, Name: "ci", TokenHash: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := q.GetAPITokenByHash(ctx, "deadbeef")
	if err != nil || tok.ID != id || tok.RevokedAt != nil {
		t.Fatalf("query failed: %+v err=%v", tok, err)
	}
	list, err := q.ListAPITokens(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("the list should have 1 row: %v err=%v", list, err)
	}
	if n, _ := q.RevokeAPIToken(ctx, id); n != 1 {
		t.Fatal("revoking should affect 1 row")
	}
	if _, err := q.GetAPITokenByHash(ctx, "deadbeef"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a revoked token must not be found: %v", err)
	}
	if n, _ := q.RevokeAPIToken(ctx, id); n != 0 {
		t.Fatal("revoking twice should affect 0 rows")
	}
}
