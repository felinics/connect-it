package dbgate

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/dbcontract"
)

// configuredDevIdentity is the identity scripts/configure-dev-database.sql is
// supposed to produce.
func configuredDevIdentity(t *testing.T) (ExpectedDatabaseIdentity, string) {
	t.Helper()
	databaseURL := os.Getenv("TEST_CONFIGURED_DEV_DATABASE_URL")
	systemIdentifier := os.Getenv(
		"TEST_CONFIGURED_DEV_DATABASE_SYSTEM_IDENTIFIER",
	)
	if databaseURL == "" || systemIdentifier == "" {
		t.Skip(
			"set TEST_CONFIGURED_DEV_DATABASE_URL and " +
				"TEST_CONFIGURED_DEV_DATABASE_SYSTEM_IDENTIFIER",
		)
	}
	return ExpectedDatabaseIdentity{
		Database:         "connect_it",
		Schema:           "public",
		SystemIdentifier: systemIdentifier,
		ApplicationRole:  "connect_it_app",
		OwnerRole:        "connect_it_owner",
	}, databaseURL
}

func connectTestDatabase(
	t *testing.T,
	ctx context.Context,
	databaseURL string,
) *pgx.Conn {
	t.Helper()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Close(context.Background())
	})
	return connection
}

// contractViolationCodes runs the contract query itself so a failure names the
// violated codes instead of only the first one the gate reports.
func contractViolationCodes(
	t *testing.T,
	ctx context.Context,
	conn *pgx.Conn,
	identity ExpectedDatabaseIdentity,
) []string {
	t.Helper()
	tables, grantTables, grants := privilegeArguments()
	var violations []string
	if err := conn.QueryRow(
		ctx,
		productionContractQuery,
		identity.Database,
		identity.Schema,
		identity.SystemIdentifier,
		identity.ApplicationRole,
		identity.OwnerRole,
		tables,
		grantTables,
		grants,
		dbcontract.CurrentSchemaVersion,
	).Scan(&violations); err != nil {
		t.Fatalf("run production contract query: %v", err)
	}
	return violations
}

func TestConfiguredDevDatabasePassesProductionGate(t *testing.T) {
	identity, databaseURL := configuredDevIdentity(t)
	ctx := context.Background()
	application := connectTestDatabase(t, ctx, databaseURL)

	gate, err := NewProductionDatabaseGate(identity)
	if err != nil {
		t.Fatalf("NewProductionDatabaseGate: %v", err)
	}
	if err := gate(ctx, application); err != nil {
		t.Fatalf(
			"scripts/configure-dev-database.sql does not satisfy the gate "+
				"(violations %v): %s",
			contractViolationCodes(t, ctx, application, identity),
			SafeStartupFailure(err),
		)
	}
}

// Each case breaks exactly one clause of the contract and must produce exactly
// that violation code — and the gate must pass again once it is restored, which
// is what proves the case isolated the clause it claims to.
func TestConfiguredDevDatabaseRejectsContractViolations(t *testing.T) {
	identity, databaseURL := configuredDevIdentity(t)
	adminURL := os.Getenv("TEST_CONFIGURED_DEV_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Skip("TEST_CONFIGURED_DEV_DATABASE_ADMIN_URL is required")
	}

	ctx := context.Background()
	application := connectTestDatabase(t, ctx, databaseURL)
	admin := connectTestDatabase(t, ctx, adminURL)
	gate, err := NewProductionDatabaseGate(identity)
	if err != nil {
		t.Fatalf("NewProductionDatabaseGate: %v", err)
	}

	for _, test := range []struct {
		name     string
		breakSQL string
		fixSQL   string
		want     string
	}{
		{
			name:     "application inherits",
			breakSQL: "alter role connect_it_app inherit",
			fixSQL:   "alter role connect_it_app noinherit",
			want:     "application_attrs",
		},
		{
			name:     "owner can login",
			breakSQL: "alter role connect_it_owner login",
			fixSQL:   "alter role connect_it_owner nologin",
			want:     "owner_attrs",
		},
		{
			name:     "public schema usage",
			breakSQL: "grant usage on schema public to public",
			fixSQL:   "revoke usage on schema public from public",
			want:     "application_grants",
		},
		{
			name:     "cross database connect",
			breakSQL: "grant connect on database postgres to connect_it_app",
			fixSQL:   "revoke connect on database postgres from connect_it_app",
			want:     "cross_database",
		},
		{
			name:     "extra table grant",
			breakSQL: "grant delete on public.tool_runs to connect_it_app",
			fixSQL:   "revoke delete on public.tool_runs from connect_it_app",
			want:     "table_privileges",
		},
		{
			name: "missing marker table",
			breakSQL: "alter table public.schema_migrations " +
				"rename to schema_migrations_hidden",
			fixSQL: "alter table public.schema_migrations_hidden " +
				"rename to schema_migrations",
			want: "table_privileges",
		},
		{
			name: "public control function",
			breakSQL: "grant execute on function " +
				"pg_catalog.pg_control_system() to public",
			fixSQL: "revoke execute on function " +
				"pg_catalog.pg_control_system() from public",
			want: "routine_privileges",
		},
		{
			name: "extra marker row",
			breakSQL: "insert into public.schema_migrations (version, dirty) " +
				"values (2, false)",
			fixSQL: "delete from public.schema_migrations where version = 2",
			want:   "schema_rows",
		},
		{
			name:     "dirty schema marker",
			breakSQL: "update public.schema_migrations set dirty = true",
			fixSQL:   "update public.schema_migrations set dirty = false",
			want:     "schema_version",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, test.breakSQL); err != nil {
				t.Fatalf("introduce violation: %v", err)
			}
			defer func() {
				if _, err := admin.Exec(ctx, test.fixSQL); err != nil {
					t.Errorf("restore contract: %v", err)
					return
				}
				if err := gate(ctx, application); err != nil {
					t.Errorf("gate still fails after restore: %v", err)
				}
			}()

			got := contractViolationCodes(t, ctx, application, identity)
			if len(got) == 0 || got[0] != test.want {
				t.Fatalf("violation codes = %v, want %q first", got, test.want)
			}
			if err := gate(ctx, application); !errors.Is(
				err,
				contractViolations[test.want],
			) {
				t.Fatalf("gate error = %v, want %v", err, contractViolations[test.want])
			}
		})
	}
}
