package service

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/catalogsvc"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/store"
)

func TestMigrateUpFromV020(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "isolated"
		if fallback {
			name = "with_fallback"
		}
		t.Run(name, func(t *testing.T) {
			dbURL, conn := versionOneDatabase(t, true)
			before := storedUpgradeRows(t, conn)
			u, err := url.Parse(dbURL)
			if err != nil {
				t.Fatal(err)
			}
			target := u.Query().Get("search_path")
			var fallbackConn *pgx.Conn
			if fallback {
				fallbackURL := newTestSchema(t)
				fallbackConn, err = pgx.Connect(t.Context(), fallbackURL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = fallbackConn.Close(context.Background()) })
				_, err = fallbackConn.Exec(t.Context(), `CREATE TABLE connector_settings
					(connector_type text PRIMARY KEY, enabled boolean NOT NULL DEFAULT true, updated_at timestamptz NOT NULL);
					INSERT INTO connector_settings VALUES ('fixture',false,'2026-01-01Z');`)
				if err != nil {
					t.Fatal(err)
				}
				fallbackU, _ := url.Parse(fallbackURL)
				q := u.Query()
				q.Set("search_path", target+","+fallbackU.Query().Get("search_path"))
				u.RawQuery = q.Encode()
				dbURL = u.String()
			}
			for range 2 {
				if err := MigrateUp(dbURL); err != nil {
					t.Fatal(err)
				}
				if !tableExists(t, dbURL, target+".connector_settings") {
					t.Fatal("upgrade did not create connector_settings in the owning schema")
				}
				assertUpgradeVersion(t, conn, 2)
				if !reflect.DeepEqual(storedUpgradeRows(t, conn), before) {
					t.Fatal("upgrade changed existing stored rows")
				}
				assertUpgradeCatalog(t, conn, true)
			}
			if fallbackConn != nil {
				var enabled bool
				if err := fallbackConn.QueryRow(t.Context(), "SELECT enabled FROM connector_settings WHERE connector_type='fixture'").Scan(&enabled); err != nil || enabled {
					t.Fatalf("fallback settings changed: enabled=%v err=%v", enabled, err)
				}
				if tableExists(t, fallbackConn.Config().ConnString(), "schema_migrations") {
					t.Fatal("migration metadata leaked into fallback schema")
				}
			}
		})
	}
}

func TestMigrateUpPreservesVersionOneSettings(t *testing.T) {
	dbURL, conn := versionOneDatabase(t, false)
	if _, err := conn.Exec(t.Context(), "INSERT INTO connector_settings VALUES ('fixture',false,'2026-01-01Z')"); err != nil {
		t.Fatal(err)
	}
	before := storedUpgradeRows(t, conn)
	var setting string
	if err := conn.QueryRow(t.Context(), "SELECT row_to_json(s)::text FROM connector_settings s").Scan(&setting); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := MigrateUp(dbURL); err != nil {
			t.Fatal(err)
		}
		assertUpgradeVersion(t, conn, 2)
		assertUpgradeCatalog(t, conn, false)
	}
	m, db, err := newMigrator(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(-1); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	assertUpgradeVersion(t, conn, 1)
	var after string
	if err := conn.QueryRow(t.Context(), "SELECT row_to_json(s)::text FROM connector_settings s").Scan(&after); err != nil || after != setting {
		t.Fatalf("rollback changed existing settings: %v", err)
	}
	if err := MigrateUp(dbURL); err != nil {
		t.Fatal(err)
	}
	assertUpgradeVersion(t, conn, 2)
	assertUpgradeCatalog(t, conn, false)
	if err := conn.QueryRow(t.Context(), "SELECT row_to_json(s)::text FROM connector_settings s").Scan(&after); err != nil || after != setting {
		t.Fatalf("reapplying upgrade changed existing settings: %v", err)
	}
	if !reflect.DeepEqual(storedUpgradeRows(t, conn), before) {
		t.Fatal("upgrade/rollback changed existing stored rows")
	}
}

func TestMigrateUpConcurrentLegacyStart(t *testing.T) {
	dbURL, conn := versionOneDatabase(t, true)
	before := storedUpgradeRows(t, conn)
	errs := make(chan error, 4)
	for range 4 {
		go func() { errs <- MigrateUp(dbURL) }()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	assertUpgradeVersion(t, conn, 2)
	assertUpgradeCatalog(t, conn, true)
	if !reflect.DeepEqual(storedUpgradeRows(t, conn), before) {
		t.Fatal("concurrent upgrade changed existing stored rows")
	}
}

func versionOneDatabase(t *testing.T, legacy bool) (string, *pgx.Conn) {
	t.Helper()
	dbURL := newTestSchema(t)
	conn, err := pgx.Connect(t.Context(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	var baseline []byte
	if legacy {
		baseline, err = os.ReadFile("testdata/v0.2.0_init.sql")
	} else {
		baseline, err = migrationsFS.ReadFile("migrations/0001_init.up.sql")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), string(baseline)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `CREATE TABLE schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL);
		INSERT INTO schema_migrations VALUES (1,false);
		INSERT INTO api_tokens VALUES ('00000000-0000-0000-0000-000000000001','fixture','stored-token-hash','2026-01-01Z',NULL);
		INSERT INTO connections (id,connector_type,alias,auth_method,credential,secret_key_version,profile,scopes,status,created_at,updated_at)
		VALUES ('00000000-0000-0000-0000-000000000002','fixture','saved','oauth','\x01020304',1,'{"name":"stored"}','{repo}','active','2026-01-01Z','2026-01-01Z');`); err != nil {
		t.Fatal(err)
	}
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, version, err := kr.Encrypt([]byte(`{"client_secret":"fixture-value"}`), []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO connector_configs
		(connector_type,config_schema_version,public_config,secret_config,secret_key_version,created_at,updated_at)
		VALUES ('fixture',1,'{"client_id":"stored"}',$1,$2,'2026-01-01Z','2026-01-01Z')`, ciphertext, version); err != nil {
		t.Fatal(err)
	}
	return dbURL, conn
}

func storedUpgradeRows(t *testing.T, conn *pgx.Conn) map[string]json.RawMessage {
	t.Helper()
	rows := map[string]json.RawMessage{}
	for _, table := range []string{"api_tokens", "connections", "connector_configs"} {
		var data []byte
		if err := conn.QueryRow(t.Context(), "SELECT coalesce(json_agg(t), '[]') FROM "+table+" t").Scan(&data); err != nil {
			t.Fatal(err)
		}
		rows[table] = data
	}
	return rows
}

func assertUpgradeVersion(t *testing.T, conn *pgx.Conn, want int) {
	t.Helper()
	var version int
	var dirty bool
	if err := conn.QueryRow(t.Context(), "SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil || version != want || dirty {
		t.Fatalf("migration version=%d dirty=%v err=%v; want %d clean", version, dirty, err, want)
	}
}

func assertUpgradeCatalog(t *testing.T, conn *pgx.Conn, enabled bool) {
	t.Helper()
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{Type: "fixture", Name: "Fixture", ConfigSchemaVersion: 1,
		Implementation: connector.RemoteMCP{Endpoint: "https://example.test/mcp"}})
	cat := catalogsvc.New(reg, configsvc.New(store.New(conn), reg, kr))
	items, err := cat.List(t.Context())
	if err != nil || len(items) != 1 || items[0].Type != "fixture" || items[0].Enabled != enabled {
		t.Fatalf("catalog unavailable or settings changed: items=%+v err=%v", items, err)
	}
}
