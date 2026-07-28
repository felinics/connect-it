package configsvc_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
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

// rwDefinition 是读写测试的主 connector：必填公开＋必填 secret＋默认值＋可选 secret。
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
		t.Fatalf("public 回显失败: %+v", view.Public)
	}
	if !slices.Equal(view.SecretKeysSet, []string{"api_key", "client_secret"}) {
		t.Fatalf("secret_keys_set 不符: %v", view.SecretKeysSet)
	}
	if view.SchemaVersion != 1 || view.UpdatedAt.IsZero() {
		t.Fatalf("视图字段不符: %+v", view)
	}
}

func TestPutMergesSecrets(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh", "api_key": "k2"})
	// 第二次不带 client_secret（保留）、api_key 传空串（删除）
	mustPut(t, s, "example_app",
		map[string]any{"client_id": "new"},
		map[string]string{"api_key": ""})

	view, err := s.Get(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.SecretKeysSet, []string{"client_secret"}) {
		t.Fatalf("合并语义不符: %v", view.SecretKeysSet)
	}
	if view.Public["client_id"] != "new" {
		t.Fatalf("public 应全量替换: %+v", view.Public)
	}
}

func TestPutIfMatchConflict(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	first := mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "shh"})

	// 推进 updated_at
	if _, err := s.Put(ctx, "example_app", map[string]any{"client_id": "abc"},
		map[string]string{}, first.UpdatedAt); err != nil {
		t.Fatalf("匹配的 if_match 应成功: %v", err)
	}
	// 旧 if_match → 冲突
	if _, err := s.Put(ctx, "example_app", map[string]any{"client_id": "x"},
		map[string]string{}, first.UpdatedAt); !errors.Is(err, configsvc.ErrConflict) {
		t.Fatalf("过期 if_match 应 ErrConflict, got %v", err)
	}
	// 新建时带 if_match → 冲突
	if _, err := s.Put(ctx, "example_app2", nil, nil, time.Now()); !errors.Is(err, configsvc.ErrUnknownConnector) {
		t.Fatalf("未知 type 应 ErrUnknownConnector, got %v", err)
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
		t.Fatalf("行版本比代码新应 ErrIncompatible, got %v", err)
	}
}

func TestPutPrunesRemovedSecretFields(t *testing.T) {
	// 模拟代码升级删除字段：v1 定义含 legacy secret，写入后换用不含 legacy 的
	// v2 服务实例再保存一次，legacy 应被清理。
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
		t.Fatalf("已删除字段应被清理: %v", view.SecretKeysSet)
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
		t.Fatalf("删除后应 ErrNotFound, got %v", err)
	}
	if err := s.Delete(ctx, "example_app"); !errors.Is(err, configsvc.ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound, got %v", err)
	}
}

func TestResolvedDefaultsAndSecrets(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	// 未配置：只有默认值
	resolved, err := s.Resolved(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if resolved["tenant"] != "common" || len(resolved) != 1 {
		t.Fatalf("未配置时应仅默认值: %v", resolved)
	}

	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc", "tenant": "org1"},
		map[string]string{"client_secret": "shh"})
	resolved, err = s.Resolved(ctx, "example_app")
	if err != nil {
		t.Fatal(err)
	}
	if resolved["client_id"] != "abc" || resolved["client_secret"] != "shh" || resolved["tenant"] != "org1" {
		t.Fatalf("合并结果不符: %v", resolved)
	}
}

func TestResolvedAppliesUpgrader(t *testing.T) {
	pool := testutil.NewDB(t)
	kr := testKeyring(t)

	// v1 定义：old_name
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

	// v2 定义：new_name＋upgrader
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
		t.Fatalf("upgrader 未生效: %v", resolved)
	}
	if _, exists := resolved["old_name"]; exists {
		t.Fatalf("旧字段应被改名: %v", resolved)
	}
	// DB 行保持 v1 原样（升级不落盘）
	row, err := store.New(pool).GetConnectorConfig(context.Background(), "upg_app")
	if err != nil || row.ConfigSchemaVersion != 1 {
		t.Fatalf("升级不应落盘: %+v err=%v", row, err)
	}
}

func TestConfigStateMapping(t *testing.T) {
	s, _ := newRWService(t, rwDefinition())
	ctx := context.Background()

	st, err := s.ConfigState(ctx, "example_app")
	if err != nil || st.Exists {
		t.Fatalf("无行时 Exists 应为 false: %+v err=%v", st, err)
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
		t.Fatalf("映射不符: %+v", st)
	}
}
