// Package dbmigrate initializes a completely empty database schema from the
// current baseline. It deliberately has no upgrade, down, force, or recovery
// operations.
package dbmigrate

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/dbcontract"
	"github.com/memohai/connect-it/packages/service/migrations"
)

var (
	ErrDatabaseURLRequired = errors.New(
		"database initialization requires DATABASE_URL",
	)
	ErrDatabaseConnection = errors.New(
		"database initialization could not connect",
	)
	ErrSchemaSelection = errors.New(
		"database initialization requires one explicit writable schema",
	)
	ErrSchemaInspection = errors.New(
		"database initialization could not inspect the schema",
	)
	ErrNonEmptySchema = errors.New(
		"database initialization requires a completely empty schema",
	)
	ErrInvalidSchemaState = errors.New(
		"database schema is not the current clean version",
	)
	ErrBaselineApplication = errors.New(
		"database baseline could not be applied",
	)
)

// Initialize applies the current baseline only when the connection's sole
// effective schema is completely empty. An exact current clean schema is a
// no-op; every other state fails closed.
func Initialize(ctx context.Context, databaseURL string) error {
	if strings.TrimSpace(databaseURL) == "" {
		return ErrDatabaseURLRequired
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return ErrDatabaseConnection
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return ErrDatabaseConnection
	}
	defer func() {
		_ = conn.Close(context.Background())
	}()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ErrSchemaInspection
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	if _, err := tx.Exec(
		ctx,
		`select pg_catalog.pg_advisory_xact_lock(
		   pg_catalog.hashtext(pg_catalog.current_database()),
		   pg_catalog.hashtext(pg_catalog.current_schema())
		 )`,
	); err != nil {
		return ErrSchemaInspection
	}

	state, err := inspectSchema(ctx, tx)
	if err != nil {
		return err
	}
	if state.markerExists {
		if !state.markerIsTable {
			return ErrInvalidSchemaState
		}
		if err := requireCurrentMarker(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return ErrSchemaInspection
		}
		return nil
	}
	if state.objectCount != 0 {
		return ErrNonEmptySchema
	}

	if _, err := tx.Exec(ctx, migrations.BaselineSQL()); err != nil {
		return ErrBaselineApplication
	}
	if err := requireCurrentMarker(ctx, tx); err != nil {
		return ErrBaselineApplication
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrBaselineApplication
	}
	return nil
}

type schemaState struct {
	markerExists  bool
	markerIsTable bool
	objectCount   int64
}

func inspectSchema(ctx context.Context, tx pgx.Tx) (schemaState, error) {
	var (
		schema             string
		singleSearchSchema bool
		safeSchema         bool
		state              schemaState
	)
	err := tx.QueryRow(
		ctx,
		`
			select coalesce(pg_catalog.current_schema(), ''),
			       coalesce(
			         pg_catalog.current_schemas(false)::pg_catalog.text[] =
			           array[
			             pg_catalog.current_schema()::pg_catalog.text
			           ]::pg_catalog.text[],
			         false
			       ),
			       coalesce(
			         pg_catalog.current_schema() <> 'information_schema'
			         and pg_catalog.current_schema() !~ '^pg_',
			         false
			       ),
			       pg_catalog.to_regclass(
			         pg_catalog.format(
			           '%I.%I',
			           pg_catalog.current_schema(),
			           'schema_migrations'
			         )
			       ) is not null,
			       coalesce(
			         (
			           select marker.relkind in ('r', 'p')
			           from pg_catalog.pg_class as marker
			           where marker.oid = pg_catalog.to_regclass(
			             pg_catalog.format(
			               '%I.%I',
			               pg_catalog.current_schema(),
			               'schema_migrations'
			             )
			           )
			         ),
			         false
			       ),
			       (
			         select pg_catalog.count(*)
			         from pg_catalog.pg_depend as dependency
			         join pg_catalog.pg_namespace as namespace
			           on namespace.oid = dependency.refobjid
			         where dependency.refclassid =
			           'pg_catalog.pg_namespace'::pg_catalog.regclass
			           and namespace.nspname = pg_catalog.current_schema()
			       )
		`,
	).Scan(
		&schema,
		&singleSearchSchema,
		&safeSchema,
		&state.markerExists,
		&state.markerIsTable,
		&state.objectCount,
	)
	if err != nil {
		return schemaState{}, ErrSchemaInspection
	}
	if schema == "" || !singleSearchSchema || !safeSchema {
		return schemaState{}, ErrSchemaSelection
	}
	return state, nil
}

func requireCurrentMarker(ctx context.Context, tx pgx.Tx) error {
	var (
		rows       int64
		minVersion int64
		maxVersion int64
		dirty      bool
	)
	if err := tx.QueryRow(
		ctx,
		`
			select pg_catalog.count(*),
			       coalesce(pg_catalog.min(version), -1),
			       coalesce(pg_catalog.max(version), -1),
			       coalesce(pg_catalog.bool_or(dirty), true)
			from schema_migrations
		`,
	).Scan(&rows, &minVersion, &maxVersion, &dirty); err != nil {
		return ErrInvalidSchemaState
	}
	if rows != 1 ||
		minVersion != dbcontract.CurrentSchemaVersion ||
		maxVersion != dbcontract.CurrentSchemaVersion ||
		dirty {
		return ErrInvalidSchemaState
	}
	return nil
}
