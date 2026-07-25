package store_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/service/store"
)

func ptr[T any](value T) *T { return &value }

// connectionFixture is an active Connection inserted directly, bypassing the
// primitives under test. Only the fields a case actually cares about are set;
// the rest fall back to what an OAuth Connection normally starts with.
type connectionFixture struct {
	ID          uuid.UUID
	Alias       *string
	AuthMethod  string
	Credential  []byte
	Scopes      []string
	ScopesKnown bool
}

func insertConnection(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture connectionFixture,
) store.Connection {
	t.Helper()
	if fixture.ID == uuid.Nil {
		fixture.ID = uuid.New()
	}
	if fixture.AuthMethod == "" {
		fixture.AuthMethod = "oauth"
	}
	if fixture.Credential == nil {
		fixture.Credential = []byte{}
	}
	if fixture.Scopes == nil {
		fixture.Scopes = []string{}
	}
	if _, err := pool.Exec(
		t.Context(),
		`insert into connections (
		   id, connector_type, alias, auth_method, credential,
		   secret_key_version, profile, scopes, scopes_known, status,
		   created_at, updated_at
		 ) values ($1, 'github', $2, $3, $4, 1, '{}',
		           $5, $6, 'active', current_timestamp, current_timestamp)`,
		fixture.ID,
		fixture.Alias,
		fixture.AuthMethod,
		fixture.Credential,
		fixture.Scopes,
		fixture.ScopesKnown,
	); err != nil {
		t.Fatal(err)
	}
	connection, err := store.New(pool).GetConnection(t.Context(), fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func requireNoRows(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("error = %v, want pgx.ErrNoRows", err)
	}
}
