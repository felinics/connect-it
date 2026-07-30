// Package service is the root package of the module hosting the database
// layer and the business services. It owns schema migration: migrations/ is
// embedded with go:embed and applied at startup.
package service

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
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
	if err := ensureSearchPathSchema(db); err != nil {
		db.Close()
		return nil, nil, err
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

// ensureSearchPathSchema creates the first schema named by search_path when
// it does not exist yet. The service owns its schema even when DATABASE_URL
// shares a database with a host application (search_path=connect_it), so
// startup must not depend on the host having provisioned the schema first.
//
// The first non-$user entry is checked directly against the catalog rather
// than through current_schema(): with a fallback entry such as
// search_path=connect_it,public, current_schema() silently resolves to the
// fallback while the intended schema is missing, and migrations would land in
// the wrong schema. When the entry already exists nothing is executed, which
// keeps restricted roles without CREATE privilege working.
func ensureSearchPathSchema(db *sql.DB) error {
	var searchPath string
	if err := db.QueryRow("SHOW search_path").Scan(&searchPath); err != nil {
		return fmt.Errorf("read search_path: %w", err)
	}
	target := ""
	for _, entry := range strings.Split(searchPath, ",") {
		name := strings.Trim(strings.TrimSpace(entry), `"`)
		if name == "" || name == "$user" {
			continue
		}
		target = name
		break
	}
	if target == "" {
		// search_path names only $user (or nothing). There is no schema this
		// service could own; accept the session default when it resolves.
		var current sql.NullString
		if err := db.QueryRow("SELECT current_schema()").Scan(&current); err != nil {
			return fmt.Errorf("resolve current schema: %w", err)
		}
		if current.Valid {
			return nil
		}
		return errors.New("search_path names no schema to create")
	}
	exists, err := schemaExists(db, target)
	if err != nil {
		return fmt.Errorf("check schema %s: %w", target, err)
	}
	if exists {
		return nil
	}
	if _, err := db.Exec("CREATE SCHEMA IF NOT EXISTS " + pgx.Identifier{target}.Sanitize()); err != nil {
		// IF NOT EXISTS only guards a pre-check: replicas racing through
		// first start can all pass it, and the losers fail on the catalog's
		// unique index. The schema exists either way, so only a boot where
		// it is truly still missing may fail. This runs before golang-migrate
		// takes its advisory lock, so no lock covers the race here.
		if exists, checkErr := schemaExists(db, target); checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("create schema %s: %w", target, err)
	}
	return nil
}

func schemaExists(db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", name,
	).Scan(&exists)
	return exists, err
}
