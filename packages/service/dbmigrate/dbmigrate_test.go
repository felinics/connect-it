package dbmigrate_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/dbcontract"
	"github.com/memohai/connect-it/packages/service/dbmigrate"
)

func TestInitializeCreatesCurrentBaselineAndIsIdempotent(t *testing.T) {
	t.Parallel()

	schemaURL, connect := newEmptyTestSchema(t)
	if err := dbmigrate.Initialize(context.Background(), schemaURL); err != nil {
		t.Fatalf("initialize empty schema: %v", err)
	}
	if err := dbmigrate.Initialize(context.Background(), schemaURL); err != nil {
		t.Fatalf("initialize current schema: %v", err)
	}

	conn := connect()
	defer conn.Close(context.Background())

	var (
		rows    int64
		version int64
		dirty   bool
	)
	if err := conn.QueryRow(
		context.Background(),
		"select count(*), min(version), bool_or(dirty) from schema_migrations",
	).Scan(&rows, &version, &dirty); err != nil {
		t.Fatalf("read schema marker: %v", err)
	}
	if rows != 1 || version != dbcontract.CurrentSchemaVersion || dirty {
		t.Fatalf(
			"schema marker = rows %d version %d dirty %t",
			rows,
			version,
			dirty,
		)
	}

	for _, table := range []string{
		"admin_account",
		"api_tokens",
		"connector_configs",
		"connections",
		"oauth_authorizations",
		"mcp_sessions",
		"mcp_session_connections",
		"connector_health",
		"tool_runs",
		"connector_policy_identities",
		"schema_migrations",
	} {
		var exists bool
		if err := conn.QueryRow(
			context.Background(),
			"select to_regclass($1) is not null",
			table,
		).Scan(&exists); err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if !exists {
			t.Errorf("baseline table %s is missing", table)
		}
	}

}

func TestInitializeSerializesConcurrentInitializers(t *testing.T) {
	t.Parallel()

	schemaURL, _ := newEmptyTestSchema(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			results <- dbmigrate.Initialize(context.Background(), schemaURL)
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("concurrent initialize: %v", err)
		}
	}
}

func TestInitializeRejectsUnknownNonEmptySchemaWithoutMutation(t *testing.T) {
	t.Parallel()

	schemaURL, connect := newEmptyTestSchema(t)
	conn := connect()
	if _, err := conn.Exec(
		context.Background(),
		"create table existing_data (id bigint primary key)",
	); err != nil {
		t.Fatalf("create unknown table: %v", err)
	}
	conn.Close(context.Background())

	err := dbmigrate.Initialize(context.Background(), schemaURL)
	if !errors.Is(err, dbmigrate.ErrNonEmptySchema) {
		t.Fatalf("initialize unknown schema error = %v", err)
	}

	conn = connect()
	defer conn.Close(context.Background())
	var (
		existing bool
		marker   bool
	)
	if err := conn.QueryRow(
		context.Background(),
		`select to_regclass('existing_data') is not null,
		        to_regclass('schema_migrations') is not null`,
	).Scan(&existing, &marker); err != nil {
		t.Fatalf("inspect rejected schema: %v", err)
	}
	if !existing || marker {
		t.Fatalf(
			"rejected schema mutated: existing=%t marker=%t",
			existing,
			marker,
		)
	}
}

func TestInitializeRejectsEveryInvalidMarker(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		rows string
	}{
		{name: "missing row"},
		{name: "wrong version", rows: "(2, false)"},
		{name: "dirty", rows: "(1, true)"},
		{name: "multiple rows", rows: "(1, false), (2, false)"},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			schemaURL, connect := newEmptyTestSchema(t)
			conn := connect()
			if _, err := conn.Exec(
				context.Background(),
				"create table schema_migrations (version bigint, dirty boolean)",
			); err != nil {
				t.Fatalf("create marker: %v", err)
			}
			if testCase.rows != "" {
				if _, err := conn.Exec(
					context.Background(),
					"insert into schema_migrations values "+testCase.rows,
				); err != nil {
					t.Fatalf("seed marker: %v", err)
				}
			}
			conn.Close(context.Background())

			err := dbmigrate.Initialize(context.Background(), schemaURL)
			if !errors.Is(err, dbmigrate.ErrInvalidSchemaState) {
				t.Fatalf("initialize invalid marker error = %v", err)
			}

			conn = connect()
			defer conn.Close(context.Background())
			var rows int64
			if err := conn.QueryRow(
				context.Background(),
				"select count(*) from schema_migrations",
			).Scan(&rows); err != nil {
				t.Fatalf("read marker after rejection: %v", err)
			}
			wantRows := int64(0)
			if testCase.rows != "" {
				wantRows = int64(strings.Count(testCase.rows, "("))
			}
			if rows != wantRows {
				t.Fatalf("marker rows after rejection = %d, want %d", rows, wantRows)
			}
		})
	}
}

func TestInitializeRejectsAmbiguousSearchPath(t *testing.T) {
	t.Parallel()

	schemaURL, _ := newEmptyTestSchema(t)
	parsed, err := url.Parse(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", query.Get("search_path")+",public")
	parsed.RawQuery = query.Encode()

	err = dbmigrate.Initialize(context.Background(), parsed.String())
	if !errors.Is(err, dbmigrate.ErrSchemaSelection) {
		t.Fatalf("initialize ambiguous search_path error = %v", err)
	}
}

func TestInitializeErrorsNeverEchoDatabaseURL(t *testing.T) {
	t.Parallel()

	const secret = "DO_NOT_LEAK_DATABASE_SECRET"
	err := dbmigrate.Initialize(
		context.Background(),
		"://"+secret+"@invalid",
	)
	if !errors.Is(err, dbmigrate.ErrDatabaseConnection) {
		t.Fatalf("invalid URL error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("database error leaked URL: %q", err)
	}
}

func newEmptyTestSchema(
	t *testing.T,
) (string, func() *pgx.Conn) {
	t.Helper()

	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect TEST_DATABASE_URL: %v", err)
	}

	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	schema := "test_dbmigrate_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "create schema "+identifier); err != nil {
		admin.Close(ctx)
		t.Fatalf("create random schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(
			context.Background(),
			"drop schema "+identifier+" cascade",
		)
		_ = admin.Close(context.Background())
	})

	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	schemaURL := parsed.String()

	connect := func() *pgx.Conn {
		t.Helper()
		conn, err := pgx.Connect(context.Background(), schemaURL)
		if err != nil {
			t.Fatalf("connect random schema: %v", err)
		}
		return conn
	}
	return schemaURL, connect
}
