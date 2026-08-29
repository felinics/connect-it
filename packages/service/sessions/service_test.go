package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/service/sessions"
	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

type fakeToolLister map[uuid.UUID][]*mcp.Tool

func (f fakeToolLister) ListTools(_ context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error) {
	tools, ok := f[connectionID]
	if !ok {
		return nil, errors.New("upstream unavailable")
	}
	return tools, nil
}

func TestAggregateSessionSnapshotAndRevocation(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()

	apiTokenID := uuid.New()
	if err := q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: apiTokenID, Name: "memoh", TokenHash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	githubID := insertConnection(t, pool, "github")
	notionID := insertConnection(t, pool, "notion")
	lister := fakeToolLister{
		githubID: {
			{Name: "issues", Description: "GitHub issues"},
		},
		notionID: {
			{Name: "search", Description: "Notion search"},
		},
	}
	svc := sessions.New(q, lister)

	result, err := svc.Create(ctx, apiTokenID, map[string]uuid.UUID{
		"github": githubID,
		"notion": notionID,
	}, nil, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || result.ExpiresAt.IsZero() {
		t.Fatalf("invalid create result: %+v", result)
	}
	view, err := svc.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Tools) != 2 || view.Tools[0].Name != "github__issues" ||
		view.Tools[1].Name != "notion__search" {
		t.Fatalf("unexpected tools: %+v", view.Tools)
	}
	route := view.Routes["github__issues"]
	if route.ConnectionID != githubID || route.ToolName != "issues" {
		t.Fatalf("unexpected route: %+v", route)
	}

	if _, err := q.RevokeAPIToken(ctx, apiTokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Resolve(ctx, result.Token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("revoked API token must invalidate session, got %v", err)
	}
}

func TestAggregateSessionRejectsUnknownAllowlistTool(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	ctx := context.Background()
	apiTokenID := uuid.New()
	if err := q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: apiTokenID, Name: "memoh", TokenHash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	connectionID := insertConnection(t, pool, "github")
	svc := sessions.New(q, fakeToolLister{
		connectionID: {{Name: "issues"}},
	})

	_, err := svc.Create(ctx, apiTokenID,
		map[string]uuid.UUID{"github": connectionID},
		[]string{"github__missing"}, time.Hour)
	var validationErr *sessions.ValidationError
	if !errors.As(err, &validationErr) || validationErr.Code != "invalid_allowlist" {
		t.Fatalf("expected invalid_allowlist, got %v", err)
	}
}

func insertConnection(t *testing.T, pool *pgxpool.Pool, connectorType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version,
	   profile, scopes, status, created_at, updated_at)
	  values ($1, $2, null, 'oauth', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`,
		id, connectorType); err != nil {
		t.Fatal(err)
	}
	return id
}
