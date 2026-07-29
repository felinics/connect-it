// Package service is the root package of the module hosting the database
// layer and the business services. It owns schema migration: migrations/ is
// embedded with go:embed and applied at startup.
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

// MigrateUp brings the database to the latest version. It does nothing when
// the database is already current.
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

// newMigrator opens the connection through pgx stdlib so runtime parameters
// in the URL, such as search_path, take effect. The schema_migrations table
// lives under CURRENT_SCHEMA, which isolates the random schemas used by tests.
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
