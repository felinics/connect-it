package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/api"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type execCall struct {
	sessionID    uuid.UUID
	apiTokenID   uuid.UUID
	connectionID uuid.UUID
	name         string
	arguments    string
}

type fakeExecutor struct {
	mu       sync.Mutex
	tools    map[uuid.UUID][]*mcp.Tool
	listErrs map[uuid.UUID]error
	calls    []execCall
	res      *mcp.CallToolResult
	err      error
}

func (f *fakeExecutor) ListTools(_ context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools[connectionID], f.listErrs[connectionID]
}

func (f *fakeExecutor) CallTool(
	_ context.Context,
	sessionID, apiTokenID, connectionID uuid.UUID,
	params *mcp.CallToolParamsRaw,
) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, execCall{
		sessionID: sessionID, apiTokenID: apiTokenID, connectionID: connectionID,
		name: params.Name, arguments: string(params.Arguments),
	})
	return f.res, f.err
}

type mcpEnv struct {
	server *httptest.Server
	exec   *fakeExecutor
	sess   *sessions.Service
	connGH uuid.UUID
	token  uuid.UUID
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	pool := testutil.NewDB(t)
	keyring, err := crypto.ParseKeyring("1:" + strings.Repeat("ff", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type:                "github",
		Name:                "github",
		ConfigSchemaVersion: 1,
		AuthMethods:         []connector.AuthMethod{{Key: "none", Type: connector.AuthNone}},
		Implementation:      connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, keyring)
	auth := authsvc.New(q)
	fake := &fakeExecutor{
		tools:    map[uuid.UUID][]*mcp.Tool{},
		listErrs: map[uuid.UUID]error{},
		res: &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "done"}},
			StructuredContent: map[string]any{"n": float64(1)},
		},
	}
	sessionService := sessions.New(q, fake)
	env := &mcpEnv{exec: fake, sess: sessionService}
	insertConnection := func(connectorType, alias string) uuid.UUID {
		id := uuid.New()
		if _, err := pool.Exec(t.Context(), `insert into connections
		  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
		  values ($1, $2, $3, 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`,
			id, connectorType, alias); err != nil {
			t.Fatal(err)
		}
		return id
	}
	env.connGH = insertConnection("github", "gh-main")
	fake.tools[env.connGH] = []*mcp.Tool{
		{Name: "list_issues", Description: "list issues", InputSchema: map[string]any{"type": "object"}},
		{Name: "create.issue", Description: "create issue", InputSchema: map[string]any{"type": "object"}},
	}

	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, env.token, err = auth.CreateAPIToken(t.Context(), "test-session-owner"); err != nil {
		t.Fatal(err)
	}
	echo := api.New(api.Deps{
		Registry:     reg,
		Store:        q,
		Config:       cfg,
		Catalog:      catalogsvc.New(q, reg, cfg),
		Auth:         auth,
		Exec:         fake,
		Sessions:     sessionService,
		CookieSecret: []byte("test-cookie-secret"),
	})
	env.server = httptest.NewServer(echo)
	t.Cleanup(env.server.Close)
	return env
}

type authHeaderRT struct {
	token string
	base  http.RoundTripper
}

func (rt authHeaderRT) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(request)
}

func mcpConnect(t *testing.T, env *mcpEnv, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: env.server.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authHeaderRT{
			token: token,
			base:  http.DefaultTransport,
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func createSession(t *testing.T, env *mcpEnv, connectionID uuid.UUID, allowlist []string) string {
	t.Helper()
	token, err := env.sess.Create(t.Context(), env.token, connectionID, allowlist, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMCPRequiresSessionToken(t *testing.T) {
	env := newMCPEnv(t)
	response, err := http.Post(env.server.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestMCPAcceptsNonLocalHost(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, env.connGH, nil)
	request, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		env.server.URL+"/mcp",
		strings.NewReader(`{
			"jsonrpc":"2.0",
			"id":1,
			"method":"initialize",
			"params":{
				"protocolVersion":"2025-06-18",
				"capabilities":{},
				"clientInfo":{"name":"test-client","version":"0.0.1"}
			}
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "host.docker.internal:8080"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
}

func TestMCPDynamicListAndCall(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, env.connGH, nil)
	view, err := env.sess.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	session := mcpConnect(t, env, token)

	var names []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	want := []string{"create.issue", "list_issues"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names=%v", names)
	}

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "list_issues",
		Arguments: json.RawMessage(`{"repo":"x"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	env.exec.mu.Lock()
	defer env.exec.mu.Unlock()
	if len(env.exec.calls) != 1 || env.exec.calls[0] != (execCall{
		sessionID:    view.ID,
		apiTokenID:   view.APITokenID,
		connectionID: env.connGH,
		name:         "list_issues",
		arguments:    `{"repo":"x"}`,
	}) {
		t.Fatalf("calls=%+v", env.exec.calls)
	}
}

func TestMCPListUsesImmutableSessionSnapshot(t *testing.T) {
	env := newMCPEnv(t)
	env.exec.tools[env.connGH] = append(env.exec.tools[env.connGH],
		&mcp.Tool{Name: "invalid:name", InputSchema: map[string]any{"type": "object"}})
	token := createSession(t, env, env.connGH, nil)
	env.exec.mu.Lock()
	env.exec.tools[env.connGH] = []*mcp.Tool{{Name: "new_tool"}}
	env.exec.listErrs[env.connGH] = context.DeadlineExceeded
	env.exec.mu.Unlock()
	session := mcpConnect(t, env, token)

	var names []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	want := []string{"create.issue", "list_issues"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names=%v", names)
	}
}

func TestSessionCreationFailsWhenToolDiscoveryFails(t *testing.T) {
	env := newMCPEnv(t)
	env.exec.listErrs[env.connGH] = context.DeadlineExceeded
	_, err := env.sess.Create(t.Context(), env.token, env.connGH, nil, time.Hour)
	if !errors.Is(err, sessions.ErrToolDiscovery) {
		t.Fatalf("err=%v", err)
	}
}

func TestMCPAllowlistAndExecutorError(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, env.connGH, []string{"list_issues"})
	session := mcpConnect(t, env, token)

	var names []string
	for tool, err := range session.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != "list_issues" {
		t.Fatalf("names=%v", names)
	}
	env.exec.err = context.DeadlineExceeded
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_issues"})
	if err != nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCreateMCPSessionEndpoint(t *testing.T) {
	env := newMCPEnv(t)
	adminHeader := adminLogin(t, env.server)
	response, body := doReq(t, http.MethodPost, env.server.URL+"/admin/api-tokens", `{"name":"bot"}`, adminHeader)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create API token: %d %s", response.StatusCode, body)
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	bearer := http.Header{}
	bearer.Set("Authorization", "Bearer "+created.Token)

	response, body = doReq(t, http.MethodPost, env.server.URL+"/v1/mcp-sessions",
		`{"connection_id":"`+env.connGH.String()+`","ttl_seconds":600}`, bearer)
	if response.StatusCode != http.StatusCreated || !strings.Contains(body, "token") {
		t.Fatalf("create session: %d %s", response.StatusCode, body)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &issued); err != nil {
		t.Fatal(err)
	}
	response, body = doReq(t, http.MethodDelete,
		env.server.URL+"/admin/api-tokens/"+created.ID, "", adminHeader)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke API token: %d %s", response.StatusCode, body)
	}
	if _, err := env.sess.Resolve(t.Context(), issued.Token); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("父 API token 撤销后 session 应失效: %v", err)
	}
}
