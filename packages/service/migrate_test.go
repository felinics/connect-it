package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// newTestSchema 创建随机 schema，返回带 search_path 的数据库 URL。
// 未设置 TEST_DATABASE_URL 时 t.Skip。
func newTestSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过集成测试")
	}
	ctx := context.Background()
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(buf[:])
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("连接 TEST_DATABASE_URL: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("创建 schema: %v", err)
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
	"admin_account", "api_tokens", "connector_configs", "connections",
	"oauth_authorizations", "mcp_sessions", "mcp_session_connections",
	"connector_health", "tool_runs",
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

func TestMigrateUpCreatesAllTables(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, tbl := range wantTables {
		if !tableExists(t, dbURL, tbl) {
			t.Errorf("表 %s 未创建", tbl)
		}
	}
}

func TestMigrateUpIdempotent(t *testing.T) {
	dbURL := newTestSchema(t)
	if err := MigrateUp(dbURL); err != nil {
		t.Fatal(err)
	}
	if err := MigrateUp(dbURL); err != nil {
		t.Fatalf("第二次 MigrateUp 应为 no-op: %v", err)
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
			t.Errorf("表 %s 未被 down migration 删除", tbl)
		}
	}
}
