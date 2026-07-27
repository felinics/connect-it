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
		AuthMethods: []connector.AuthMethod{{
			Key: "oauth", Label: "OAuth", Type: connector.AuthOAuth2,
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			},
		}},
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
	return catalogsvc.New(q, reg, cfg), ctx, exec
}

func TestListStatuses(t *testing.T) {
	svc, ctx, exec := newCatalog(t)
	// 孤儿配置行（代码不认识的 type）
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
		t.Fatalf("应有 4 条: %+v", items)
	}
	// 排序按 type 升序
	if items[0].Type != "ghost_app" || items[3].Type != "shelf_app" {
		t.Fatalf("排序不符: %+v", items)
	}
}

func TestGet(t *testing.T) {
	svc, ctx, exec := newCatalog(t)

	item, err := svc.Get(ctx, "ready_app")
	if err != nil || item.Status != status.Ready || item.Name != "Ready" ||
		item.Mode != connector.ModeRemoteMCP {
		t.Fatalf("ready_app: %+v err=%v", item, err)
	}
	if len(item.AuthMethods) != 1 || item.AuthMethods[0].Key != "oauth" ||
		item.AuthMethods[0].Type != connector.AuthOAuth2 {
		t.Fatalf("ready_app auth methods: %+v", item.AuthMethods)
	}

	exec(`insert into connector_configs
	      (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	      values ('ghost_app', 1, '{}', '\x'::bytea, 1, now(), now())`)
	item, err = svc.Get(ctx, "ghost_app")
	if err != nil || item.Status != status.DefinitionMissing {
		t.Fatalf("ghost_app: %+v err=%v", item, err)
	}

	if _, err := svc.Get(ctx, "nope"); !errors.Is(err, configsvc.ErrNotFound) {
		t.Fatalf("未知 type 应 ErrNotFound, got %v", err)
	}
}
