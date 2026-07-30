package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// newTestSchema creates a random schema and returns a database URL carrying
// the matching search_path. It skips the test when TEST_DATABASE_URL is unset.
func newTestSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set, skipping integration test")
	}
	ctx := context.Background()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(buf[:])
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

var wantTables = []string{
	"admin_account", "api_tokens", "connector_configs", "oauth_clients", "connections",
	"oauth_authorizations", "mcp_sessions", "tool_runs",
}

func tableExists(t *testing.T, dbURL, table string) bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// newMissingSchemaURL returns a database URL whose search_path starts with a
// schema that does not exist yet (followed by any extra entries), the schema
// name, and cleanup for whatever MigrateUp creates.
func newMissingSchemaURL(t *testing.T, extra ...string) (string, string) {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set, skipping integration test")
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(buf[:])
	t.Cleanup(func() {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, base)
		if err != nil {
			return
		}
		defer conn.Close(ctx)
		_, _ = conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", strings.Join(append([]string{schema}, extra...), ","))
	u.RawQuery = q.Encode()
	return u.String(), schema
}

func TestMigrateUpCreatesMissingSchema(t *testing.T) {
	dbURL, _ := newMissingSchemaURL(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("MigrateUp should create the search_path schema itself: %v", err)
	}
	for _, tbl := range wantTables {
		if !tableExists(t, dbURL, tbl) {
			t.Errorf("table %s was not created", tbl)
		}
	}
}

// publicTables returns the table names currently present in public. The
// shared test database may carry pre-existing tables there, so leak checks
// compare snapshots instead of asserting absence.
func publicTables(t *testing.T, dbURL string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = 'public'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	return tables
}

// A fallback entry must not swallow the intended schema: with
// search_path=missing,public, current_schema() resolves to public while the
// first entry is missing, and migrations would silently land in public.
func TestMigrateUpPrefersFirstSearchPathEntry(t *testing.T) {
	dbURL, schema := newMissingSchemaURL(t, "public")
	before := publicTables(t, dbURL)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("MigrateUp should create the first search_path schema: %v", err)
	}
	for _, tbl := range wantTables {
		if !tableExists(t, dbURL, schema+"."+tbl) {
			t.Errorf("table %s was not created in schema %s", tbl, schema)
		}
	}
	for tbl := range publicTables(t, dbURL) {
		if !before[tbl] {
			t.Errorf("table %s leaked into the public fallback schema", tbl)
		}
	}
}

func TestMigrateUpCreatesAllTables(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, tbl := range wantTables {
		if !tableExists(t, dbURL, tbl) {
			t.Errorf("table %s was not created", tbl)
		}
	}
}

func TestMigrateUpIdempotent(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatal(err)
	}
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("a second MigrateUp should be a no-op: %v", err)
	}
}

func TestMigrateDownDropsAllTables(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatal(err)
	}
	m, db, err := newMigrator(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := m.Down(); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	for _, tbl := range wantTables {
		if tableExists(t, dbURL, tbl) {
			t.Errorf("table %s was not dropped by the down migration", tbl)
		}
	}
}
