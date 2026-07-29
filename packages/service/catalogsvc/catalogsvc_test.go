package catalogsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/core/status"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func newCatalog(t *testing.T) (*catalogsvc.Service, context.Context, func(sql string)) {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	remote := connector.RemoteMCP{Endpoint: "https://mcp.example.com"}
	reg.MustRegister(connector.Definition{
		Type: "ready_app", Name: "Ready", ConfigSchemaVersion: 1, Implementation: remote,
		AuthMethods: []connector.AuthMethod{
			{
				Key: "oauth", Label: "OAuth", Type: connector.AuthOAuth2,
				OAuth: &connector.OAuthConfig{
					AuthorizationEndpoint: "https://example.com/authorize",
					TokenEndpoint:         "https://example.com/token",
				},
			},
			{
				Key: "token", Label: "API token", Type: connector.AuthAPIKey,
				CredentialFields: []connector.ConfigField{{
					Key:         "token",
					Label:       "Token",
					InputType:   connector.InputText,
					Required:    true,
					Secret:      true,
					Description: "Provider API token",
					Validation: connector.FieldValidation{
						Pattern: `^token_`,
					},
				}},
			},
		},
	})
	reg.MustRegister(connector.Definition{
		Type: "needs_app", Name: "Needs", ConfigSchemaVersion: 1, Implementation: remote,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
		},
	})
	reg.MustRegister(connector.Definition{
		Type: "shelf_app", Name: "Shelf", ConfigSchemaVersion: 1, Implementation: remote,
	})
	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	ctx := context.Background()
	exec := func(sql string) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	return catalogsvc.New(reg, cfg), ctx, exec
}

func TestListStatuses(t *testing.T) {
	svc, ctx, exec := newCatalog(t)
	// Orphaned config row: a type the code does not know.
	exec(`insert into connector_configs
	      (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	      values ('ghost_app', 1, '{}', '\x'::bytea, 1, now(), now())`)
	items, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]status.Status{}
	for _, it := range items {
		got[it.Type] = it.Status
	}
	want := map[string]status.Status{
		"ready_app": status.Ready,
		"needs_app": status.NeedsConfig,
		"shelf_app": status.Ready,
		"ghost_app": status.DefinitionMissing,
	}
	for typ, st := range want {
		if got[typ] != st {
			t.Errorf("%s: got %s want %s", typ, got[typ], st)
		}
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 items: %+v", items)
	}
	// Sorted by type, ascending.
	if items[0].Type != "ghost_app" || items[3].Type != "shelf_app" {
		t.Fatalf("unexpected order: %+v", items)
	}
}

func TestGet(t *testing.T) {
	svc, ctx, exec := newCatalog(t)

	item, err := svc.Get(ctx, "ready_app")
	if err != nil || item.Status != status.Ready || item.Name != "Ready" ||
		item.Mode != connector.ModeRemoteMCP {
		t.Fatalf("ready_app: %+v err=%v", item, err)
	}
	if len(item.AuthMethods) != 2 || item.AuthMethods[0].Key != "oauth" ||
		item.AuthMethods[0].Type != connector.AuthOAuth2 {
		t.Fatalf("ready_app auth methods: %+v", item.AuthMethods)
	}
	if item.AuthMethods[0].CredentialFields == nil {
		t.Fatalf("oauth credential fields must be an empty array: %+v", item.AuthMethods[0])
	}
	tokenMethod := item.AuthMethods[1]
	if tokenMethod.Key != "token" || tokenMethod.Type != connector.AuthAPIKey ||
		len(tokenMethod.CredentialFields) != 1 {
		t.Fatalf("ready_app token method: %+v", tokenMethod)
	}
	field := tokenMethod.CredentialFields[0]
	if field.Key != "token" || !field.Required || !field.Secret ||
		field.InputType != connector.InputText || field.Pattern != `^token_` ||
		field.Description != "Provider API token" || field.Options == nil {
		t.Fatalf("ready_app token field: %+v", field)
	}

	exec(`insert into connector_configs
	      (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	      values ('ghost_app', 1, '{}', '\x'::bytea, 1, now(), now())`)
	item, err = svc.Get(ctx, "ghost_app")
	if err != nil || item.Status != status.DefinitionMissing {
		t.Fatalf("ghost_app: %+v err=%v", item, err)
	}

	if _, err := svc.Get(ctx, "nope"); !errors.Is(err, configsvc.ErrNotFound) {
		t.Fatalf("an unknown type should yield ErrNotFound, got %v", err)
	}
}
