package configsvc_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

func strPtr(s string) *string { return &s }

func testKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	k, err := crypto.ParseKeyring("1:" + strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// rwDefinition is the main connector for read/write tests: a required public
// field, a required secret, a default value, and an optional secret.
func rwDefinition() connector.Definition {
	return connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
			{Key: "tenant", Label: "Tenant", InputType: connector.InputText, Required: true, DefaultValue: strPtr("common")},
			{Key: "api_key", Label: "API Key", InputType: connector.InputText, Secret: true},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	}
}

func newRWService(t *testing.T, defs ...connector.Definition) (*configsvc.Service, *pgxpool.Pool) {
	t.Helper()
	pool := testutil.NewDB(t)
	r := registry.New()
	for _, d := range defs {
		r.MustRegister(d)
	}
	return configsvc.New(store.New(pool), r, testKeyring(t)), pool
}

func mustPut(t *testing.T, s *configsvc.Service, typ connector.Type, public map[string]any, secrets map[string]string) configsvc.ConfigView {
	t.Helper()
	view, err := s.Put(context.Background(), typ, public, secrets, time.Time{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return view
}

func TestPutGetRoundtrip(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh", "api_key": "k2"})

	view, err := s.Get(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if view.Public["client_id"] != "abc" {
		t.Fatalf("public was not echoed back: %+v", view.Public)
	}
	if !slices.Equal(view.SecretKeysSet, []string{"api_key", "client_secret"}) {
		t.Fatalf("unexpected secret_keys_set: %v", view.SecretKeysSet)
	}
	if view.SchemaVersion != 1 || view.UpdatedAt.IsZero() {
		t.Fatalf("unexpected view fields: %+v", view)
	}
}

func TestPutMergesSecrets(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh", "api_key": "k2"})
	// Second write: client_secret absent (kept), api_key empty (deleted).
	mustPut(t, s, "example_app",
		map[string]any{"client_id": "new"},
		map[string]string{"api_key": ""})

	view, err := s.Get(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.SecretKeysSet, []string{"client_secret"}) {
		t.Fatalf("unexpected merge semantics: %v", view.SecretKeysSet)
	}
	if view.Public["client_id"] != "new" {
		t.Fatalf("public should be replaced wholesale: %+v", view.Public)
	}
}

func TestPutIfMatchConflict(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	first := mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh"})

	// Advance updated_at.
	if _, err := s.Put(ctx, "example_app", map[string]any{"client_id": "abc"},
		map[string]string{}, first.UpdatedAt); err != nil {
		t.Fatalf("a matching if_match should succeed: %v", err)
	}
	// A stale if_match must conflict.
	if _, err := s.Put(ctx, "example_app", map[string]any{"client_id": "x"},
		map[string]string{}, first.UpdatedAt); !errors.Is(err, configsvc.ErrConflict) {
		t.Fatalf("a stale if_match should yield ErrConflict, got %v", err)
	}
	// Sending if_match on a create must conflict.
	if _, err := s.Put(ctx, "example_app2", nil, nil, time.Now()); !errors.Is(err, configsvc.ErrUnknownConnector) {
		t.Fatalf("an unknown type should yield ErrUnknownConnector, got %v", err)
	}
}

func TestPutIncompatible(t *testing.T) {
	s, pool := newRWService(t, rwDefinition())
	ctx := context.Background()

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh"})
	if _, err := pool.Exec(ctx,
		"update connector_configs set config_schema_version = 99 where connector_type = 'example_app'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "example_app", map[string]any{"client_id": "x"},
		map[string]string{}, time.Time{}); !errors.Is(err, configsvc.ErrIncompatible) {
		t.Fatalf("a row newer than the code should yield ErrIncompatible, got %v", err)
	}
}

func TestPutPrunesRemovedSecretFields(t *testing.T) {
	// Simulate a code upgrade that drops a field: the v1 definition carries a
	// legacy secret; after writing it, a v2 service instance without legacy
	// saves again and legacy must be cleaned up.
	oldDef := rwDefinition()
	oldDef.ConfigFields = append(oldDef.ConfigFields, connector.ConfigField{
		Key: "legacy", Label: "Legacy", InputType: connector.InputText, Secret: true,
	})
	pool := testutil.NewDB(t)
	kr := testKeyring(t)

	oldReg := registry.New()
	oldReg.MustRegister(oldDef)
	oldSvc := configsvc.New(store.New(pool), oldReg, kr)
	if _, err := oldSvc.Put(context.Background(), "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh", "legacy": "zombie"}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	newReg := registry.New()
	newReg.MustRegister(rwDefinition())
	newSvc := configsvc.New(store.New(pool), newReg, kr)
	if _, err := newSvc.Put(context.Background(), "example_app",
		map[string]any{"client_id": "abc"}, map[string]string{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	view, err := newSvc.Get(context.Background(), "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(view.SecretKeysSet, "legacy") {
		t.Fatalf("a removed field should be cleaned up: %v", view.SecretKeysSet)
	}
}

func TestDelete(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh"})
	if err := s.Delete(ctx, "example_app"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "example_app"); !errors.Is(err, configsvc.ErrNotFound) {
		t.Fatalf("after delete it should yield ErrNotFound, got %v", err)
	}
	if err := s.Delete(ctx, "example_app"); !errors.Is(err, configsvc.ErrNotFound) {
		t.Fatalf("deleting twice should yield ErrNotFound, got %v", err)
	}
}

func TestResolvedDefaultsAndSecrets(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	// Unconfigured: defaults only.
	resolved, err := s.Resolved(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if resolved["tenant"] != "common" || len(resolved) != 1 {
		t.Fatalf("an unconfigured connector should resolve to defaults only: %v", resolved)
	}

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc", "tenant": "org1"},
		map[string]string{"client_secret": "shh"})
	resolved, err = s.Resolved(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if resolved["client_id"] != "abc" || resolved["client_secret"] != "shh" || resolved["tenant"] != "org1" {
		t.Fatalf("unexpected merge result: %v", resolved)
	}
}

func TestResolvedAppliesUpgrader(t *testing.T) {
	pool := testutil.NewDB(t)
	kr := testKeyring(t)

	// v1 definition: old_name.
	v1 := connector.Definition{
		Type: "upg_app", Name: "Upg", ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "old_name", Label: "Old", InputType: connector.InputText},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	}
	regV1 := registry.New()
	regV1.MustRegister(v1)
	svcV1 := configsvc.New(store.New(pool), regV1, kr)
	if _, err := svcV1.Put(context.Background(), "upg_app",
		map[string]any{"old_name": "kept"}, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}

	// v2 definition: new_name plus an upgrader.
	v2 := connector.Definition{
		Type: "upg_app", Name: "Upg", ConfigSchemaVersion: 2,
		ConfigFields: []connector.ConfigField{
			{Key: "new_name", Label: "New", InputType: connector.InputText},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
		ConfigUpgraders: []connector.ConfigUpgrader{{
			FromVersion: 1,
			Upgrade: func(public, secret map[string]any) (map[string]any, map[string]any, error) {
				if v, ok := public["old_name"]; ok {
					public["new_name"] = v
					delete(public, "old_name")
				}
				return public, secret, nil
			},
		}},
	}
	regV2 := registry.New()
	regV2.MustRegister(v2)
	svcV2 := configsvc.New(store.New(pool), regV2, kr)

	resolved, err := svcV2.Resolved(context.Background(), "upg_app")
	if err != nil {
		t.Fatal(err)
	}
	if resolved["new_name"] != "kept" {
		t.Fatalf("the upgrader did not take effect: %v", resolved)
	}
	if _, exists := resolved["old_name"]; exists {
		t.Fatalf("the old field should have been renamed: %v", resolved)
	}
	// The DB row stays at v1: upgrades are in-memory only.
	row, err := store.New(pool).GetConnectorConfig(context.Background(), "upg_app")
	if err != nil || row.ConfigSchemaVersion != 1 {
		t.Fatalf("an upgrade must not be persisted: %+v err=%v", row, err)
	}
}

func TestConfigStateMapping(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	st, err := s.ConfigState(ctx, "example_app")
	if err != nil || st.Exists {
		t.Fatalf("Exists should be false when no row exists: %+v err=%v", st, err)
	}

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh"})
	st, err = s.ConfigState(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exists || st.SchemaVersion != 1 || st.PublicValues["client_id"] != "abc" ||
		!st.SecretKeysSet["client_secret"] {
		t.Fatalf("unexpected mapping: %+v", st)
	}
}

func TestConnectorEnabledState(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	enabled, err := s.Enabled(ctx, "example_app")
	if err != nil || !enabled {
		t.Fatalf("a connector without a stored override should be enabled: enabled=%v err=%v", enabled, err)
	}
	if err := s.SetEnabled(ctx, "example_app", false); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireEnabled(ctx, "example_app"); !errors.Is(err, configsvc.ErrConnectorDisabled) {
		t.Fatalf("a disabled connector should be rejected, got %v", err)
	}
	states, err := s.EnabledStates(ctx)
	if err != nil || states["example_app"] {
		t.Fatalf("unexpected enabled states: %+v err=%v", states, err)
	}
	if err := s.SetEnabled(ctx, "example_app", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RequireEnabled(ctx, "example_app"); err != nil {
		t.Fatalf("a re-enabled connector should be usable: %v", err)
	}
	if err := s.SetEnabled(ctx, "unknown", false); !errors.Is(err, configsvc.ErrUnknownConnector) {
		t.Fatalf("an unknown connector should be rejected, got %v", err)
	}
}
