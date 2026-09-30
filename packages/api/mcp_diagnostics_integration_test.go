package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/authsvc"
	"github.com/felinics/connect-it/packages/service/configsvc"
	execsvc "github.com/felinics/connect-it/packages/service/exec"
	"github.com/felinics/connect-it/packages/service/mcpclient"
	"github.com/felinics/connect-it/packages/service/sessions"
	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

type diagnosticLog struct {
	sync.Mutex
	bytes.Buffer
}

func (b *diagnosticLog) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *diagnosticLog) text() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

type diagnosticAuth struct {
	token string
	base  http.RoundTripper
}

func (rt diagnosticAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(r)
}

func TestMCPDiagnosticsEndToEnd(t *testing.T) {
	pool := testutil.NewDB(t)
	up := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	for _, name := range []string{"rpc", "tool_error", "ok"} {
		up.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "private-result"}}, IsError: name == "tool_error"}, nil
		})
	}
	up.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" && req.GetParams().(*mcp.CallToolParamsRaw).Name == "rpc" {
				return nil, &jsonrpc.Error{Code: -32602, Message: "private-rpc-message", Data: json.RawMessage(`"private-rpc-data"`)}
			}
			return next(ctx, method, req)
		}
	})
	upstream := httptest.NewTLSServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return up }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer upstream.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	reg := registry.New()
	reg.MustRegister(connector.Definition{Type: "fixture", Name: "Fixture", ConfigSchemaVersion: 1,
		AuthMethods:    []connector.AuthMethod{{Key: "none", Type: connector.AuthNone}},
		Implementation: connector.RemoteMCP{Endpoint: upstream.URL},
	})
	keyring, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	q := store.New(pool)
	cfg := configsvc.New(q, reg, keyring)
	engine := execsvc.New(q, reg, cfg, nil, keyring, mcpclient.Client{})
	connectionID := uuid.New()
	_, err = pool.Exec(t.Context(), `insert into connections
		(id, connector_type, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
		values ($1, 'fixture', 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	auth := authsvc.New(q)
	_, tokenID, err := auth.CreateAPIToken(t.Context(), "diagnostic-test")
	if err != nil {
		t.Fatal(err)
	}
	sess := sessions.New(q, engine)
	issued, err := sess.Create(t.Context(), tokenID, map[string]uuid.UUID{"fixture": connectionID}, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	view, err := sess.Resolve(t.Context(), issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	app := New(Deps{Registry: reg, Store: q, Config: cfg, Auth: auth, Exec: engine, Sessions: sess})
	logs := new(diagnosticLog)
	app.Logger.SetOutput(logs)
	server := httptest.NewServer(app)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "diagnostic-client", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: diagnosticAuth{token: issued.Token, base: previous}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	seen := map[string]bool{}
	for _, tc := range []struct{ name, kind string }{{"rpc", "invalid_args"}, {"tool_error", "tool_error"}, {"ok", ""}} {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "fixture__" + tc.name, Arguments: map[string]any{"private": "private-argument"}})
		if err != nil {
			t.Fatal(err)
		}
		meta, ok := result.Meta["connect-it.dev/diagnostics"].(map[string]any)
		if !ok {
			t.Fatalf("missing metadata: %+v", result)
		}
		runID, _ := meta["run_id"].(string)
		if _, err := uuid.Parse(runID); err != nil || seen[runID] {
			t.Fatalf("invalid or reused run id: %q", runID)
		}
		seen[runID] = true
		var status string
		var kind *string
		if err := pool.QueryRow(t.Context(), `select status, error_kind from tool_runs where id=$1 and session_id=$2 and connection_id=$3`, runID, view.ID, connectionID).Scan(&status, &kind); err != nil {
			t.Fatal(err)
		}
		if tc.kind == "" {
			if result.IsError || status != "ok" || kind != nil {
				t.Fatalf("unexpected success audit: %s %v", status, kind)
			}
		} else {
			if !result.IsError || status != "error" || kind == nil || *kind != tc.kind {
				t.Fatalf("inconsistent audit for %s: status=%s kind=%s result=%+v", tc.name, status, *kind, result.StructuredContent)
			}
			if !strings.Contains(logs.text(), runID) {
				t.Fatal("run id absent from failure log")
			}
		}
		if tc.name == "rpc" {
			fields := result.StructuredContent.(map[string]any)
			if fields["run_id"] != runID || fields["rpc_code"] != float64(-32602) || fields["stage"] != "tools/call" {
				t.Fatalf("lost RPC diagnostics: %+v", fields)
			}
		} else if result.Content[0].(*mcp.TextContent).Text != "private-result" {
			t.Fatal("modified upstream content")
		}
	}
	if strings.Contains(logs.text(), "private-") {
		t.Fatal("private data reached logs")
	}
	t.Run("canceled_call_is_audited", func(t *testing.T) {
		ctx, runID := execsvc.WithRunID(t.Context())
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := engine.CallTool(ctx, view.ID, tokenID, connectionID, &mcp.CallToolParamsRaw{Name: "ok"})
		if err == nil {
			t.Fatal("expected cancellation")
		}
		if f := execsvc.DescribeError(err); f.Stage != "connection" || f.Kind != "canceled" {
			t.Fatalf("cancellation diagnostics=%+v", f)
		}
		var kind string
		if err := pool.QueryRow(t.Context(), `select error_kind from tool_runs where id=$1`, runID).Scan(&kind); err != nil || kind != "canceled" {
			t.Fatalf("canceled audit kind=%q err=%v", kind, err)
		}
	})
	t.Run("empty_result_is_audited_as_failure", func(t *testing.T) {
		empty := execsvc.New(q, reg, cfg, nil, keyring, emptyMCPClient{})
		ctx, runID := execsvc.WithRunID(t.Context())
		_, err := empty.CallTool(ctx, view.ID, tokenID, connectionID, &mcp.CallToolParamsRaw{Name: "ok"})
		if err == nil {
			t.Fatal("empty result was not an error")
		}
		if f := execsvc.DescribeError(err); f.Stage != "dispatch" || f.Kind != "internal" {
			t.Fatalf("empty result diagnostics=%+v", f)
		}
		var status, kind string
		if err := pool.QueryRow(t.Context(), `select status, error_kind from tool_runs where id=$1`, runID).Scan(&status, &kind); err != nil || kind != "internal" || status != "error" {
			t.Fatalf("empty result audit status=%q kind=%q err=%v", status, kind, err)
		}
	})
	t.Run("disabled_connection_preserves_audit_identity", func(t *testing.T) {
		if err := cfg.SetEnabled(t.Context(), "fixture", false); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := cfg.SetEnabled(t.Context(), "fixture", true); err != nil {
				t.Error(err)
			}
		}()
		ctx, runID := execsvc.WithRunID(t.Context())
		_, err := engine.CallTool(ctx, view.ID, tokenID, connectionID, &mcp.CallToolParamsRaw{Name: "ok"})
		if err == nil {
			t.Fatal("disabled connector was executed")
		}
		var connectorType, kind string
		if err := pool.QueryRow(t.Context(), `select connector_type, error_kind from tool_runs where id=$1`, runID).Scan(&connectorType, &kind); err != nil || connectorType != "fixture" || kind != "tool_unavailable" {
			t.Fatalf("disabled audit type=%q kind=%q err=%v", connectorType, kind, err)
		}
	})
	t.Run("audit_failure_is_observable", func(t *testing.T) {
		var auditLog diagnosticLog
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&auditLog, nil)))
		defer slog.SetDefault(previous)
		if _, err := pool.Exec(t.Context(), `drop table tool_runs`); err != nil {
			t.Fatal(err)
		}
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "fixture__ok"})
		if err != nil || result.IsError {
			t.Fatalf("audit failure changed tool outcome: %v", err)
		}
		runID := result.Meta["connect-it.dev/diagnostics"].(map[string]any)["run_id"].(string)
		if !strings.Contains(auditLog.text(), runID) || !strings.Contains(auditLog.text(), "tool audit write failed") || strings.Contains(auditLog.text(), "private-") {
			t.Fatalf("audit failure was not safely correlated: %s", auditLog.text())
		}
	})
}

type emptyMCPClient struct{ mcpclient.Client }

func (emptyMCPClient) CallTool(context.Context, string, string, string, time.Duration, *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	return nil, nil
}
