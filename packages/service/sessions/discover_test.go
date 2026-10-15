package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type partialLister struct {
	tools map[uuid.UUID][]*mcp.Tool
	errs  map[uuid.UUID]error
}

func (l partialLister) ListTools(_ context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error) {
	if err, failed := l.errs[connectionID]; failed {
		return nil, err
	}
	return l.tools[connectionID], nil
}

func decodeSnapshot(t *testing.T, encoded []byte) toolSnapshot {
	t.Helper()
	var snapshot toolSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestDiscoverToolsSkipsFailedAliasAndReportsIt(t *testing.T) {
	githubID, gmailID := uuid.New(), uuid.New()
	svc := &Service{lister: partialLister{
		tools: map[uuid.UUID][]*mcp.Tool{githubID: {{Name: "issues"}}},
		errs:  map[uuid.UUID]error{gmailID: errors.New("tools/list: Forbidden")},
	}}

	encoded, failed, err := svc.discoverTools(context.Background(),
		map[string]uuid.UUID{"github": githubID, "gmail": gmailID}, nil)
	if err != nil {
		t.Fatalf("partial failure must not fail discovery: %v", err)
	}
	snapshot := decodeSnapshot(t, encoded)
	if len(snapshot.Tools) != 1 || snapshot.Tools[0].Name != "github__issues" {
		t.Fatalf("snapshot must keep the healthy alias only, got %+v", snapshot.Tools)
	}
	if _, routed := snapshot.Routes["github__issues"]; !routed {
		t.Fatalf("routes=%+v", snapshot.Routes)
	}
	if len(failed) != 1 || failed[0].Alias != "gmail" || failed[0].ConnectionID != gmailID ||
		failed[0].Error != "tools/list: Forbidden" {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestDiscoverToolsFailsWhenEveryAliasFails(t *testing.T) {
	githubID, gmailID := uuid.New(), uuid.New()
	svc := &Service{lister: partialLister{errs: map[uuid.UUID]error{
		githubID: errors.New("initialize: Forbidden"),
		gmailID:  errors.New("tools/list: Forbidden"),
	}}}

	_, failed, err := svc.discoverTools(context.Background(),
		map[string]uuid.UUID{"github": githubID, "gmail": gmailID}, nil)
	if !errors.Is(err, ErrToolDiscovery) {
		t.Fatalf("err=%v", err)
	}
	if failed != nil {
		t.Fatalf("failures must not accompany an error, got %+v", failed)
	}
	// The first failing alias (sorted) names the error so log lines stay
	// pairable with the downstream's own warning.
	if got := err.Error(); got != `sessions: tool discovery failed for alias "github": initialize: Forbidden` {
		t.Fatalf("err=%q", got)
	}
}

func TestDiscoverToolsFailsWhenAllowlistNeedsFailedAlias(t *testing.T) {
	githubID, gmailID := uuid.New(), uuid.New()
	svc := &Service{lister: partialLister{
		tools: map[uuid.UUID][]*mcp.Tool{githubID: {{Name: "issues"}}},
		errs:  map[uuid.UUID]error{gmailID: errors.New("tools/list: Forbidden")},
	}}
	bindings := map[string]uuid.UUID{"github": githubID, "gmail": gmailID}

	_, _, err := svc.discoverTools(context.Background(), bindings, map[string]bool{"gmail__send": true})
	if !errors.Is(err, ErrToolDiscovery) {
		t.Fatalf("an allowlisted tool of a failed alias must fail discovery, got %v", err)
	}

	encoded, failed, err := svc.discoverTools(context.Background(), bindings, map[string]bool{"github__issues": true})
	if err != nil {
		t.Fatalf("allowlist on the healthy alias must still succeed: %v", err)
	}
	if snapshot := decodeSnapshot(t, encoded); len(snapshot.Tools) != 1 {
		t.Fatalf("snapshot=%+v", snapshot.Tools)
	}
	if len(failed) != 1 || failed[0].Alias != "gmail" {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestDiscoverToolsSucceedsWithoutFailures(t *testing.T) {
	githubID := uuid.New()
	svc := &Service{lister: partialLister{
		tools: map[uuid.UUID][]*mcp.Tool{githubID: {{Name: "issues"}}},
	}}

	encoded, failed, err := svc.discoverTools(context.Background(),
		map[string]uuid.UUID{"github": githubID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 0 {
		t.Fatalf("failed=%+v", failed)
	}
	if snapshot := decodeSnapshot(t, encoded); len(snapshot.Tools) != 1 {
		t.Fatalf("snapshot=%+v", snapshot.Tools)
	}
}
