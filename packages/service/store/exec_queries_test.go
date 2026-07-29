package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func ptr[T any](v T) *T { return &v }

func TestInsertToolRun(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()
	id := uuid.New()
	connID := uuid.New()
	err := q.InsertToolRun(ctx, store.InsertToolRunParams{
		ID:             id,
		ConnectorType:  "test_runs",
		ConnectionID:   &connID,
		ToolID:         "list_items",
		SessionID:      nil,
		Status:         "ok",
		ErrorKind:      nil,
		UpstreamStatus: nil,
		DurationMs:     ptr(int32(12)),
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
		t.Fatalf("unexpected row contents: %s %s session=%v", status, toolID, sessionID)
	}
}
