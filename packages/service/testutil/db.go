// Package testutil provides an isolated database environment for integration
// tests. It reads TEST_DATABASE_URL and skips when the variable is unset.
// Each test gets its own random schema, runs the full migration set inside
// it, and drops that schema with CASCADE when finished.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	service "github.com/felinics/connect-it/packages/service"
)

// NewDB returns a pool connected to a dedicated random schema with every
// migration applied. Cleanup, meaning DROP SCHEMA and closing the pool, runs
// automatically through t.Cleanup.
func NewDB(t *testing.T) *pgxpool.Pool {
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

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("connect to TEST_DATABASE_URL: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}

	schemaURL, err := WithSearchPath(base, schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MigrateUp(schemaURL); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	pool, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatalf("connect to the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

// WithSearchPath appends a search_path parameter to a database URL; pgx sends
// it as a runtime parameter.
func WithSearchPath(databaseURL, schema string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
