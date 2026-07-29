package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
	"github.com/memohai/connect-it/packages/service/tokens"
)

type remoteCall struct {
	endpoint string
	token    string
	scheme   string
	timeout  time.Duration
	params   *mcp.CallToolParamsRaw
}

type fakeMCP struct {
	tools     []*mcp.Tool
	result    *mcp.CallToolResult
	callErr   error
	listCalls int
	calls     []remoteCall
}

func (f *fakeMCP) ListTools(context.Context, string, string, string, time.Duration) ([]*mcp.Tool, error) {
	f.listCalls++
	return f.tools, nil
}

func (f *fakeMCP) CallTool(_ context.Context, endpoint, token, scheme string, timeout time.Duration, params *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	f.calls = append(f.calls, remoteCall{
		endpoint: endpoint, token: token, scheme: scheme, timeout: timeout, params: params,
	})
	return f.result, f.callErr
}

type fakeUpstreamError struct{ statusCode int }

func (e *fakeUpstreamError) Error() string           { return "upstream failed" }
func (e *fakeUpstreamError) UpstreamStatusCode() int { return e.statusCode }

type harness struct {
	engine      *exec.Engine
	managedID   uuid.UUID
	remoteID    uuid.UUID
	managedCall connector.ManagedCall
	mcp         *fakeMCP
	pool        *pgxpool.Pool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testutil.NewDB(t)
	q := store.New(pool)
	keyring, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{pool: pool}
	managedHandler := func(_ context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
		h.managedCall = call
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "managed-ok"}},
			StructuredContent: map[string]any{"source": "managed"},
		}, nil
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type:                "managed_app",
		Name:                "Managed",
		ConfigSchemaVersion: 1,
		AuthMethods:         []connector.AuthMethod{{Key: "none", Type: connector.AuthNone}},
		Implementation: connector.Managed{Tools: []connector.ManagedTool{{
			Tool: mcp.Tool{
				Name: "echo",
				InputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"x": map[string]any{"type": "integer"}},
					"required":             []string{"x"},
					"additionalProperties": false,
				},
			},
			Handler: managedHandler,
		}}},
	})
	reg.MustRegister(connector.Definition{
		Type:                "remote_app",
		Name:                "Remote",
		ConfigSchemaVersion: 1,
		AuthMethods:         []connector.AuthMethod{{Key: "none", Type: connector.AuthNone}},
		Implementation: connector.RemoteMCP{
			Endpoint:       "https://mcp.example.com",
			RequestTimeout: 5 * time.Second,
		},
	})

	h.mcp = &fakeMCP{
		tools:  []*mcp.Tool{{Name: "dynamic-tool", InputSchema: map[string]any{"type": "object"}}},
		result: &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "remote-ok"}}},
	}
	h.engine = exec.New(q, reg, configsvc.New(q, reg, keyring), nil, keyring, h.mcp)
	h.managedID = uuid.New()
	h.remoteID = uuid.New()
	for id, connectorType := range map[uuid.UUID]string{
		h.managedID: "managed_app",
		h.remoteID:  "remote_app",
	} {
		if _, err := pool.Exec(t.Context(), `insert into connections
		  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
		  values ($1, $2, null, 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`,
			id, connectorType); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestManagedAndRemoteDispatch(t *testing.T) {
	h := newHarness(t)
	sessionID := uuid.New()
	apiTokenID := uuid.New()

	managedTools, err := h.engine.ListTools(t.Context(), h.managedID)
	if err != nil || len(managedTools) != 1 || managedTools[0].Name != "echo" || h.mcp.listCalls != 0 {
		t.Fatalf("managed tools=%+v listCalls=%d err=%v", managedTools, h.mcp.listCalls, err)
	}
	managedResult, err := h.engine.CallTool(t.Context(), sessionID, apiTokenID, h.managedID,
		&mcp.CallToolParamsRaw{Name: "echo", Arguments: json.RawMessage(`{"x":1}`)})
	if err != nil || managedResult.Content[0].(*mcp.TextContent).Text != "managed-ok" ||
		string(h.managedCall.Arguments) != `{"x":1}` {
		t.Fatalf("managed result=%+v call=%+v err=%v", managedResult, h.managedCall, err)
	}

	remoteTools, err := h.engine.ListTools(t.Context(), h.remoteID)
	if err != nil || len(remoteTools) != 1 || remoteTools[0].Name != "dynamic-tool" {
		t.Fatalf("remote tools=%+v err=%v", remoteTools, err)
	}
	params := &mcp.CallToolParamsRaw{Name: "upstream.echo", Arguments: json.RawMessage(`{"message":"hi"}`)}
	remoteResult, err := h.engine.CallTool(t.Context(), sessionID, apiTokenID, h.remoteID, params)
	if err != nil || remoteResult != h.mcp.result {
		t.Fatalf("remote result=%+v err=%v", remoteResult, err)
	}
	if h.mcp.listCalls != 1 {
		t.Fatalf("tools/call must not perform tools/list: listCalls=%d", h.mcp.listCalls)
	}
	if len(h.mcp.calls) != 1 {
		t.Fatalf("calls=%+v", h.mcp.calls)
	}
	call := h.mcp.calls[0]
	if call.endpoint != "https://mcp.example.com" || call.scheme != "Bearer" ||
		call.timeout != 5*time.Second ||
		call.params != params {
		t.Fatalf("remote call=%+v", call)
	}
	var auditCount int
	if err := h.pool.QueryRow(t.Context(),
		`select count(*) from tool_runs where session_id = $1 and api_token_id = $2`,
		sessionID, apiTokenID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("session audit rows=%d", auditCount)
	}
}

func TestManagedValidatesInputSchemaBeforeHandler(t *testing.T) {
	h := newHarness(t)
	result, err := h.engine.CallTool(t.Context(), uuid.Nil, uuid.Nil, h.managedID,
		&mcp.CallToolParamsRaw{Name: "echo", Arguments: json.RawMessage(`{"x":"wrong"}`)})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if h.managedCall.Arguments != nil {
		t.Fatalf("the handler must not be called: %+v", h.managedCall)
	}
	var errorKind string
	if err := h.pool.QueryRow(t.Context(),
		`select error_kind from tool_runs where connection_id = $1 order by created_at desc limit 1`,
		h.managedID).Scan(&errorKind); err != nil {
		t.Fatal(err)
	}
	if errorKind != "invalid_args" {
		t.Fatalf("error_kind=%q", errorKind)
	}

	h.mcp.result = nil
	h.mcp.callErr = &fakeUpstreamError{statusCode: 503}
	_, err = h.engine.CallTool(t.Context(), uuid.Nil, uuid.Nil, h.remoteID,
		&mcp.CallToolParamsRaw{Name: "upstream.echo", Arguments: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("the remote call should return an error")
	}
	var upstreamStatus int
	if err := h.pool.QueryRow(t.Context(),
		`select error_kind, upstream_status from tool_runs
		 where connection_id = $1 order by created_at desc limit 1`,
		h.remoteID).Scan(&errorKind, &upstreamStatus); err != nil {
		t.Fatal(err)
	}
	if errorKind != "upstream_5xx" || upstreamStatus != 503 {
		t.Fatalf("error_kind=%q upstream_status=%d", errorKind, upstreamStatus)
	}
}

func TestUnavailableAndInactive(t *testing.T) {
	h := newHarness(t)
	_, err := h.engine.CallTool(t.Context(), uuid.Nil, uuid.Nil, h.managedID,
		&mcp.CallToolParamsRaw{Name: "missing"})
	if !errors.Is(err, exec.ErrToolUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if _, err := h.engine.ListTools(t.Context(), uuid.New()); !errors.Is(err, exec.ErrConnectionNotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, err := h.pool.Exec(t.Context(),
		`update connections set status = 'reauth_required' where id = $1`,
		h.managedID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.ListTools(t.Context(), h.managedID); !errors.Is(err, tokens.ErrReauthRequired) {
		t.Fatalf("err=%v", err)
	}
}
