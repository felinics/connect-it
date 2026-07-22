package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func TestConnectorConfigCRUD(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	first, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "github",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{"client_id":"abc"}`),
		SecretConfig:        []byte{1, 2, 3},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.UpdatedAt.IsZero() || first.CreatedAt.IsZero() {
		t.Fatal("created_at/updated_at 未写入")
	}

	got, err := q.GetConnectorConfig(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	var pub map[string]any
	if err := json.Unmarshal(got.PublicConfig, &pub); err != nil {
		t.Fatal(err)
	}
	if pub["client_id"] != "abc" || got.SecretKeyVersion != 1 {
		t.Fatalf("roundtrip 失败: %+v", got)
	}
	if got.McpVerifiedAt != nil || got.McpVerifiedEndpoint != nil {
		t.Fatal("verify 字段应为 NULL")
	}

	// upsert 更新已有行
	second, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "github",
		ConfigSchemaVersion: 2,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ConfigSchemaVersion != 2 || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("upsert 应更新版本且保留 created_at: %+v", second)
	}

	n, err := q.DeleteConnectorConfig(ctx, "github")
	if err != nil || n != 1 {
		t.Fatalf("删除应影响 1 行: n=%d err=%v", n, err)
	}
	if n, _ := q.DeleteConnectorConfig(ctx, "github"); n != 0 {
		t.Fatal("重复删除应影响 0 行")
	}
	if _, err := q.GetConnectorConfig(ctx, "github"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("删除后应 ErrNoRows, got %v", err)
	}
}

func TestUpdateConnectorConfigIfMatch(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	row, err := q.UpsertConnectorConfig(ctx, store.UpsertConnectorConfigParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{"a":"b"}`),
		SecretConfig:        []byte{9},
		SecretKeyVersion:    1,
		UpdatedAt:           row.UpdatedAt,
	})
	if err != nil {
		t.Fatalf("匹配的 updated_at 应更新成功: %v", err)
	}
	if updated.UpdatedAt.Equal(row.UpdatedAt) {
		t.Fatal("updated_at 应被推进")
	}

	// 用过期的 updated_at 再更新 → ErrNoRows
	_, err = q.UpdateConnectorConfigIfMatch(ctx, store.UpdateConnectorConfigIfMatchParams{
		ConnectorType:       "gmail",
		ConfigSchemaVersion: 1,
		PublicConfig:        []byte(`{}`),
		SecretConfig:        []byte{},
		SecretKeyVersion:    1,
		UpdatedAt:           row.UpdatedAt,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("过期 If-Match 应 ErrNoRows, got %v", err)
	}
}

func TestAdminAccountQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	if _, err := q.GetAdminAccount(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("空表应 ErrNoRows, got %v", err)
	}
	n, err := q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: "hash1",
	})
	if err != nil || n != 1 {
		t.Fatalf("首次插入应影响 1 行: n=%d err=%v", n, err)
	}
	n, err = q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: "hash2",
	})
	if err != nil || n != 0 {
		t.Fatalf("已存在时应影响 0 行: n=%d err=%v", n, err)
	}
	acct, err := q.GetAdminAccount(ctx)
	if err != nil || acct.PasswordHash != "hash1" || acct.ID != 1 {
		t.Fatalf("已有账号不应被覆盖: %+v err=%v", acct, err)
	}
	if n, _ := q.UpdateAdminPassword(ctx, "hash3"); n != 1 {
		t.Fatal("改密应影响 1 行")
	}
	acct, _ = q.GetAdminAccount(ctx)
	if acct.PasswordHash != "hash3" {
		t.Fatalf("密码未更新: %+v", acct)
	}
}

func TestAPITokenQueries(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	id := uuid.New()
	if err := q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: id, Name: "ci", TokenHash: "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := q.GetAPITokenByHash(ctx, "deadbeef")
	if err != nil || tok.ID != id || tok.RevokedAt != nil {
		t.Fatalf("查询失败: %+v err=%v", tok, err)
	}
	list, err := q.ListAPITokens(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list 应有 1 行: %v err=%v", list, err)
	}
	if n, _ := q.RevokeAPIToken(ctx, id); n != 1 {
		t.Fatal("撤销应影响 1 行")
	}
	if _, err := q.GetAPITokenByHash(ctx, "deadbeef"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("已撤销 token 不应命中: %v", err)
	}
	if n, _ := q.RevokeAPIToken(ctx, id); n != 0 {
		t.Fatal("重复撤销应影响 0 行")
	}
}

func TestGetConnectorHealthNoRows(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	if _, err := q.GetConnectorHealth(context.Background(), "github"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("无行应 ErrNoRows, got %v", err)
	}
}
