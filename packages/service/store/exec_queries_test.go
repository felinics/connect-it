package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func ptr[T any](v T) *T { return &v }

func TestConnectorHealthUpsert(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	fail := func(msg string) {
		t.Helper()
		if err := q.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{
			ConnectorType: "test_health", LastError: ptr(msg),
		}); err != nil {
			t.Fatal(err)
		}
	}
	fail("boom1")
	fail("boom2")
	h, err := q.GetConnectorHealth(ctx, "test_health")
	if err != nil {
		t.Fatal(err)
	}
	if h.ConsecutiveFailures != 2 {
		t.Fatalf("两次失败后 consecutive_failures 应为 2, got %d", h.ConsecutiveFailures)
	}
	if h.LastError == nil || *h.LastError != "boom2" || h.LastErrorAt == nil {
		t.Fatalf("last_error 应为最后一次错误: %+v", h)
	}

	if err := q.UpsertConnectorHealthSuccess(ctx, "test_health"); err != nil {
		t.Fatal(err)
	}
	h, err = q.GetConnectorHealth(ctx, "test_health")
	if err != nil {
		t.Fatal(err)
	}
	if h.ConsecutiveFailures != 0 || h.LastOkAt == nil {
		t.Fatalf("成功应清零并记 last_ok_at: %+v", h)
	}
}

func TestInsertToolRun(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()
	id := uuid.New()
	connID := uuid.New()
	err := q.InsertToolRun(ctx, store.InsertToolRunParams{
		ID:            id,
		ConnectorType: "test_runs",
		ConnectionID:  &connID,
		ToolID:        "list_items",
		SessionID:     nil,
		Status:        "ok",
		Error:         nil,
		Input:         []byte(`{"a":1}`),
		OutputSummary: ptr("done"),
		DurationMs:    ptr(int32(12)),
	})
	if err != nil {
		t.Fatal(err)
	}
	var status, toolID string
	var sessionID *uuid.UUID
	if err := pool.QueryRow(ctx,
		`select status, tool_id, session_id from tool_runs where id = $1`, id).
		Scan(&status, &toolID, &sessionID); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || toolID != "list_items" || sessionID != nil {
		t.Fatalf("行内容不符: %s %s %v", status, toolID, sessionID)
	}
}

func TestSetConnectorConfigVerified(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `insert into connector_configs
	  (connector_type, config_schema_version, public_config, secret_config, secret_key_version, created_at, updated_at)
	  values ('test_verify', 1, '{}', '\x'::bytea, 1, now(), now())`); err != nil {
		t.Fatal(err)
	}
	if err := q.SetConnectorConfigVerified(ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: "test_verify",
		Endpoint:      ptr("https://mcp.internal/mcp"),
	}); err != nil {
		t.Fatal(err)
	}
	v, err := q.GetConnectorConfigVerification(ctx, "test_verify")
	if err != nil {
		t.Fatal(err)
	}
	if v.McpVerifiedAt == nil || v.McpVerifiedEndpoint == nil || *v.McpVerifiedEndpoint != "https://mcp.internal/mcp" {
		t.Fatalf("verify 字段未写入: %+v", v)
	}
}
