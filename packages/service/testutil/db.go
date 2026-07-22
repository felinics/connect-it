// Package testutil 为集成测试提供隔离的数据库环境。
// 约定：读 TEST_DATABASE_URL，未设置则 t.Skip；
// 每个测试创建随机 schema、在其中跑全量 migration，结束后 DROP SCHEMA CASCADE。
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

	service "github.com/memohai/connect-it/packages/service"
)

// NewDB 返回一个连接到独立随机 schema、已跑完全部 migration 的连接池。
// 清理（DROP SCHEMA、关闭连接）通过 t.Cleanup 自动完成。
func NewDB(t *testing.T) *pgxpool.Pool {
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

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("连接 TEST_DATABASE_URL: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("创建 schema: %v", err)
	}

	schemaURL, err := WithSearchPath(base, schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MigrateUp(schemaURL); err != nil {
		t.Fatalf("migration 失败: %v", err)
	}
	pool, err := pgxpool.New(ctx, schemaURL)
	if err != nil {
		t.Fatalf("连接测试 schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

// WithSearchPath 给数据库 URL 追加 search_path 参数（pgx 将其作为运行时参数下发）。
func WithSearchPath(databaseURL, schema string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("解析数据库 URL: %w", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
