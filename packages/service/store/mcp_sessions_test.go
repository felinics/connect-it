package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func TestCreateMCPSessionUsesDatabaseTimeAndCheckedTTL(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()
	connectionID := insertActiveSessionTestConnection(t, pool, "db-time")
	bindings := sessionBindingsJSON(connectionID)

	sessionID := uuid.New()
	expiresAt, err := q.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:            sessionID,
		TokenHash:     uuid.NewString(),
		ToolAllowlist: []byte(`{"version":1,"tools":[]}`),
		TtlSeconds:    86400,
		Bindings:      bindings,
	})
	if err != nil {
		t.Fatal(err)
	}

	var storedExpiry, createdAt time.Time
	if err := pool.QueryRow(
		ctx,
		`select expires_at, created_at from mcp_sessions where id = $1`,
		sessionID,
	).Scan(&storedExpiry, &createdAt); err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(storedExpiry) {
		t.Fatalf("returned expiry = %s, stored expiry = %s", expiresAt, storedExpiry)
	}
	if got := storedExpiry.Sub(createdAt); got != 24*time.Hour {
		t.Fatalf("database TTL = %s, want 24h", got)
	}
	var bindingCount int
	if err := pool.QueryRow(
		ctx,
		`select count(*) from mcp_session_connections where session_id = $1`,
		sessionID,
	).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 {
		t.Fatalf("binding count = %d, want 1", bindingCount)
	}

	for _, ttl := range []int64{-1, 0, 86401} {
		rejectedID := uuid.New()
		_, err := q.CreateMCPSession(ctx, store.CreateMCPSessionParams{
			ID:            rejectedID,
			TokenHash:     uuid.NewString(),
			ToolAllowlist: []byte(`{"version":1,"tools":[]}`),
			TtlSeconds:    ttl,
			Bindings:      bindings,
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("TTL %v error = %v, want pgx.ErrNoRows", ttl, err)
		}
		var count int
		if err := pool.QueryRow(
			ctx,
			`select count(*) from mcp_sessions where id = $1`,
			rejectedID,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("TTL %v inserted %d sessions", ttl, count)
		}
	}
}

func TestCreateMCPSessionStatementRollsBackSessionWhenBindingFails(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()
	connectionID := insertActiveSessionTestConnection(t, pool, "atomic")
	if _, err := pool.Exec(ctx, `
		create function fail_atomic_session_binding() returns trigger language plpgsql as $$
		begin
			raise exception 'injected binding failure';
		end
		$$;
		create trigger fail_atomic_session_binding
		before insert on mcp_session_connections
		for each row execute function fail_atomic_session_binding()
	`); err != nil {
		t.Fatal(err)
	}

	sessionID := uuid.New()
	if _, err := q.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:            sessionID,
		TokenHash:     uuid.NewString(),
		ToolAllowlist: []byte(`{"version":1,"tools":[]}`),
		TtlSeconds:    60,
		Bindings:      sessionBindingsJSON(connectionID),
	}); err == nil {
		t.Fatal("atomic Session writer succeeded despite binding trigger failure")
	}
	var count int
	if err := pool.QueryRow(
		ctx,
		`select count(*) from mcp_sessions where id = $1`,
		sessionID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed atomic writer left %d Session rows", count)
	}
}

func insertActiveSessionTestConnection(
	t *testing.T,
	pool *pgxpool.Pool,
	alias string,
) uuid.UUID {
	t.Helper()
	return insertConnection(t, pool, connectionFixture{
		Alias:      &alias,
		AuthMethod: "none",
	}).ID
}

func sessionBindingsJSON(connectionID uuid.UUID) []byte {
	return []byte(fmt.Sprintf(
		`[{"alias":"gh","connection_id":%q,"authorization_generation":1}]`,
		connectionID.String(),
	))
}
