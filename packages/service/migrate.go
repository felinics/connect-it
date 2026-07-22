// Package service 是数据库层与业务服务的宿主 module 根包，
// 负责 schema migration：migrations/ 经 go:embed 编译进二进制，启动时执行。
package service

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrateUp 把数据库升级到最新版本；已在最新版本时不做任何事。
func MigrateUp(databaseURL string) error {
	m, db, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// newMigrator 用 pgx stdlib 打开连接（URL 中的 search_path 等运行时参数生效），
// schema_migrations 表建在 CURRENT_SCHEMA 下，因此测试的随机 schema 天然隔离。
func newMigrator(databaseURL string) (*migrate.Migrate, *sql.DB, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, nil, fmt.Errorf("migrate source: %w", err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate init: %w", err)
	}
	return m, db, nil
}
