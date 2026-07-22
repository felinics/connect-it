package api_test

import (
	"context"
	"encoding/json"
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
	connID uuid.UUID
	toolID string
	args   string
}

type fakeExecutor struct {
	mu    sync.Mutex
	calls []execCall
	res   connector.ToolResultData
	err   error
}

func (f *fakeExecutor) Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, execCall{connectionID, toolID, string(args)})
	return f.res, f.err
}

type mcpEnv struct {
	srv      *httptest.Server
	exec     *fakeExecutor
	sess     *sessions.Service
	connGH   uuid.UUID
	connGM   uuid.UUID
	registry *registry.Registry
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ff", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	tool := func(id, desc string) connector.Tool {
		return connector.Tool{ID: id, Name: id, Description: desc, Risk: connector.RiskRead,
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			Backend:     connector.ManagedBackend{HandlerKey: id}}
	}
	reg.MustRegister(connector.Definition{
		Type: "github", Name: "GitHub", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{Key: "none", Type: connector.AuthNone, Label: "None"}},
		Tools:       []connector.Tool{tool("list_issues", "列 issue"), tool("create_issue", "建 issue")},
	}, "list_issues", "create_issue")
	reg.MustRegister(connector.Definition{
		Type: "gmail", Name: "Gmail", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{Key: "none", Type: connector.AuthNone, Label: "None"}},
		Tools:       []connector.Tool{tool("send_message", "发邮件")},
	}, "send_message")

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	auth := authsvc.New(q)
	cat := catalogsvc.New(q, reg, cfg)
	sess := sessions.New(q)
	fx := &fakeExecutor{res: connector.ToolResultData{Text: "done", Structured: json.RawMessage(`{"n":1}`)}}

	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}

	env := &mcpEnv{exec: fx, sess: sess, registry: reg}
	mkConn := func(typ, alias string) uuid.UUID {
		id := uuid.New()
		if _, err := pool.Exec(context.Background(), `insert into connections
		  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
		  values ($1, $2, $3, 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`, id, typ, alias); err != nil {
			t.Fatal(err)
		}
		return id
	}
	env.connGH = mkConn("github", "gh-main")
	env.connGM = mkConn("gmail", "gm-main")

	e := api.New(api.Deps{
		Registry: reg, Store: q, Config: cfg, Catalog: cat, Auth: auth,
		Exec: fx, Sessions: sess,
		CookieSecret: []byte("test-cookie-secret"),
	})
	env.srv = httptest.NewServer(e)
	t.Cleanup(env.srv.Close)
	return env
}

// mcpConnect 用官方 go-sdk client 连上聚合 /mcp。
func mcpConnect(t *testing.T, env *mcpEnv, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: env.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authHeaderRT{token: token, base: http.DefaultTransport}},
	}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type authHeaderRT struct {
	token string
	base  http.RoundTripper
}

func (rt authHeaderRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(req)
}

func createSession(t *testing.T, env *mcpEnv, bindings map[string]uuid.UUID, allowlist []string) string {
	t.Helper()
	token, err := env.sess.Create(context.Background(), bindings, allowlist, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMCPRequiresSessionToken(t *testing.T) {
	env := newMCPEnv(t)
	resp, err := http.Post(env.srv.URL+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401: %d", resp.StatusCode)
	}
}

func TestMCPListToolsScopedBySession(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, map[string]uuid.UUID{
		"gh-main": env.connGH, "gm-main": env.connGM,
	}, nil)
	session := mcpConnect(t, env, token)

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	want := []string{"gh-main__create_issue", "gh-main__list_issues", "gm-main__send_message"}
	if len(names) != 3 {
		t.Fatalf("names: %v", names)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("缺少 %s: %v", w, names)
		}
	}
}

func TestMCPAllowlistFiltersTools(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, map[string]uuid.UUID{"gh-main": env.connGH},
		[]string{"gh-main__list_issues"})
	session := mcpConnect(t, env, token)

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != "gh-main__list_issues" {
		t.Fatalf("allowlist 过滤失败: %v", names)
	}
}

func TestMCPCallToolDispatchesToExecutor(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, map[string]uuid.UUID{"gh-main": env.connGH}, nil)
	session := mcpConnect(t, env, token)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gh-main__list_issues", Arguments: json.RawMessage(`{"repo":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("不应 IsError: %+v", res)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content: %+v", res.Content)
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || tc.Text != "done" {
		t.Fatalf("text: %+v", res.Content[0])
	}
	env.exec.mu.Lock()
	defer env.exec.mu.Unlock()
	if len(env.exec.calls) != 1 || env.exec.calls[0].connID != env.connGH ||
		env.exec.calls[0].toolID != "list_issues" || env.exec.calls[0].args != `{"repo":"x"}` {
		t.Fatalf("executor 调用不符: %+v", env.exec.calls)
	}
}

func TestMCPCallExecutorErrorBecomesIsError(t *testing.T) {
	env := newMCPEnv(t)
	env.exec.err = context.DeadlineExceeded
	token := createSession(t, env, map[string]uuid.UUID{"gh-main": env.connGH}, nil)
	session := mcpConnect(t, env, token)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gh-main__list_issues", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("执行失败应 IsError: %+v", res)
	}
	if tc, _ := res.Content[0].(*mcp.TextContent); !strings.Contains(tc.Text, "execution_failed") {
		t.Fatalf("text: %+v", res.Content[0])
	}
}

func TestMCPToolUnavailable(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, map[string]uuid.UUID{"gh-main": env.connGH}, nil)

	// 模拟 Definition 移除 Tool：换一个没有 create_issue 的 registry 重建 env 不可行
	//（server 每请求动态构建），直接调用一个 alias 合法但 Definition 不存在的 tool。
	session := mcpConnect(t, env, token)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "gh-main__removed_tool", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("已删除 tool 应 IsError: %+v", res)
	}
	if tc, _ := res.Content[0].(*mcp.TextContent); !strings.Contains(tc.Text, "tool_unavailable") {
		t.Fatalf("text: %+v", res.Content[0])
	}
}

func TestCreateMCPSessionEndpoint(t *testing.T) {
	env := newMCPEnv(t)
	// 造一个 api token
	h := adminLogin(t, env.srv)
	resp, body := doReq(t, http.MethodPost, env.srv.URL+"/admin/api-tokens", `{"name":"bot"}`, h)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建 api token: %d %s", resp.StatusCode, body)
	}
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatal(err)
	}
	bh := http.Header{}
	bh.Set("Authorization", "Bearer "+created.Token)

	// 无鉴权 → 401
	resp, _ = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"}}`, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 应 401: %d", resp.StatusCode)
	}

	// 成功
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"},"ttl_seconds":600}`, bh)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(body, "token") {
		t.Fatalf("创建 session: %d %s", resp.StatusCode, body)
	}

	// 校验失败样例
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"not-a-uuid"}}`, bh)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_connection_id") {
		t.Fatalf("非法 uuid 应 400: %d %s", resp.StatusCode, body)
	}
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"},"ttl_seconds":90000}`, bh)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_ttl") {
		t.Fatalf("超限 ttl 应 400: %d %s", resp.StatusCode, body)
	}
}
