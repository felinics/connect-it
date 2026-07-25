package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/api"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	execsvc "github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type execCall struct {
	connID        uuid.UUID
	toolID        string
	args          string
	authorization execsvc.ExecutionGrant
}

type fakeExecutor struct {
	mu    sync.Mutex
	calls []execCall
	res   connector.ToolResultData
	err   error
}

func (f *fakeExecutor) Execute(
	ctx context.Context,
	request execsvc.ExecuteRequest,
) (connector.ToolResultData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, execCall{
		connID:        request.ConnectionID,
		toolID:        request.ToolID,
		args:          string(request.Arguments),
		authorization: request.Authorization,
	})
	return f.res, f.err
}

type mcpEnv struct {
	srv      *httptest.Server
	exec     *fakeExecutor
	sess     *sessions.Service
	pool     *pgxpool.Pool
	logs     *bytes.Buffer
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
	tool := func(id, desc string, risk connector.ToolRisk) connector.Tool {
		return connector.Tool{ID: id, Name: id, Description: desc, Risk: risk,
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			OutputSchema: json.RawMessage(
				`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"],"additionalProperties":false}`,
			),
			Backend: connector.ManagedBackend{HandlerKey: id}}
	}
	reg.MustRegister(connector.Definition{
		Type: "github", Name: "GitHub", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{Key: "none", Type: connector.AuthNone, Label: "None"}},
		Tools: []connector.Tool{
			tool("list_issues", "列 issue", connector.RiskRead),
			tool("create_issue", "建 issue", connector.RiskWrite),
		},
	}, "list_issues", "create_issue")
	reg.MustRegister(connector.Definition{
		Type: "gmail", Name: "Gmail", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{Key: "none", Type: connector.AuthNone, Label: "None"}},
		Tools:       []connector.Tool{tool("send_message", "发邮件", connector.RiskWrite)},
	}, "send_message")

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	auth := authsvc.New(q)
	cat := catalogsvc.New(q, reg, cfg)
	sess := sessions.New(q, reg)
	fx := &fakeExecutor{res: connector.ToolResultData{Text: "done", Structured: json.RawMessage(`{"n":1}`)}}

	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}

	env := &mcpEnv{
		exec: fx, sess: sess, pool: pool, registry: reg, logs: new(bytes.Buffer),
	}
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
		Registry: reg, Config: cfg, Catalog: cat, Auth: auth,
		Exec: fx, Sessions: sess,
		CookieSecret: []byte("test-cookie-secret"),
	})
	e.Logger.SetOutput(env.logs)
	env.srv = httptest.NewServer(e)
	t.Cleanup(env.srv.Close)
	return env
}

// mcpConnect 用官方 go-sdk client 连上聚合 /mcp。
func mcpConnect(t *testing.T, env *mcpEnv, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   env.srv.URL + "/mcp",
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
	input := sessions.AllowlistInput{}
	if allowlist != nil {
		input = sessions.AllowlistInput{Tools: append([]string{}, allowlist...)}
	}
	created, err := env.sess.CreateWithExpiry(
		context.Background(),
		bindings,
		input,
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	return created.Token
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

func TestMCPInvalidSessionIsUnauthorized(t *testing.T) {
	env := newMCPEnv(t)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer invalid-session-token")
	resp, body := doReq(t, http.MethodPost, env.srv.URL+"/mcp", `{}`, headers)
	if resp.StatusCode != http.StatusUnauthorized ||
		!strings.Contains(body, `"error":"invalid_session"`) {
		t.Fatalf("invalid Session response = %d %s", resp.StatusCode, body)
	}
}

func TestMCPResolveInfrastructureFailureIsStableInternalError(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(
		t,
		env,
		map[string]uuid.UUID{"gh-main": env.connGH},
		nil,
	)
	env.pool.Close()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	resp, body := doReq(t, http.MethodPost, env.srv.URL+"/mcp", `{}`, headers)
	if resp.StatusCode != http.StatusInternalServerError ||
		!strings.Contains(body, `"error":"internal"`) ||
		!strings.Contains(body, `"message":"failed to validate mcp session"`) {
		t.Fatalf("closed DB response = %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "closed pool") ||
		strings.Contains(body, "database") ||
		!strings.Contains(env.logs.String(), "mcp session resolution failed") {
		t.Fatalf("infra error visibility body=%q logs=%q", body, env.logs.String())
	}
}

func TestMCPListToolsScopedBySession(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, map[string]uuid.UUID{
		"gh-main": env.connGH, "gm-main": env.connGM,
	}, nil)
	session := mcpConnect(t, env, token)

	var names []string
	var readOutputSchema any
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
		if tool.Name == "gh-main__list_issues" {
			readOutputSchema = tool.OutputSchema
		}
	}
	want := []string{"gh-main__list_issues"}
	if len(names) != 1 {
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
	outputJSON, err := json.Marshal(readOutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if readOutputSchema == nil || !strings.Contains(string(outputJSON), `"n"`) {
		t.Fatalf("published output schema = %s", outputJSON)
	}
}

func TestMCPExplicitAllowlistCanGrantWriteTool(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(
		t,
		env,
		map[string]uuid.UUID{"gh-main": env.connGH},
		[]string{"gh-main__create_issue"},
	)
	session := mcpConnect(t, env, token)

	var names []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if len(names) != 1 || names[0] != "gh-main__create_issue" {
		t.Fatalf("explicit write grant tools = %v", names)
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
	view, err := env.sess.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
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
	grant := env.exec.calls[0].authorization
	if grant.SessionID != view.ID ||
		grant.ExposedToolName != "gh-main__list_issues" ||
		grant.ConnectionID != env.connGH ||
		grant.ToolID != "list_issues" {
		t.Fatalf("executor grant = %+v", grant)
	}
}

func TestMCPLegalBodyAboveOneMiBReachesSDK(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(
		t,
		env,
		map[string]uuid.UUID{"gh-main": env.connGH},
		nil,
	)
	session := mcpConnect(t, env, token)

	argumentValue := map[string]string{
		"padding": strings.Repeat("x", (1<<20)+4096),
	}
	arguments, err := json.Marshal(argumentValue)
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) <= 1<<20 {
		t.Fatalf("test arguments = %d bytes, want above 1 MiB", len(arguments))
	}
	result, err := session.CallTool(
		context.Background(),
		&mcp.CallToolParams{
			Name:      "gh-main__list_issues",
			Arguments: argumentValue,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("large legal MCP request returned error: %+v", result)
	}

	env.exec.mu.Lock()
	defer env.exec.mu.Unlock()
	gotArgumentBytes := 0
	if len(env.exec.calls) > 0 {
		gotArgumentBytes = len(env.exec.calls[0].args)
	}
	if len(env.exec.calls) != 1 ||
		gotArgumentBytes != len(arguments) {
		t.Fatalf(
			"executor calls = %d args bytes = %d, want %d",
			len(env.exec.calls),
			gotArgumentBytes,
			len(arguments),
		)
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
	if tc, _ := res.Content[0].(*mcp.TextContent); !strings.Contains(tc.Text, "internal_error") ||
		strings.Contains(tc.Text, context.DeadlineExceeded.Error()) {
		t.Fatalf("text: %+v", res.Content[0])
	}
}

func TestMCPToolUnavailable(t *testing.T) {
	env := newMCPEnv(t)
	token := createSession(t, env, map[string]uuid.UUID{"gh-main": env.connGH}, nil)
	view, err := env.sess.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(
		context.Background(),
		`update mcp_sessions
		 set tool_allowlist = '{"version":1,"tools":[{"name":"gh-main__removed_tool","risk":"read"}]}'::jsonb
		 where id = $1`,
		view.ID,
	); err != nil {
		t.Fatal(err)
	}

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
	var omitted struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(body), &omitted); err != nil {
		t.Fatal(err)
	}
	omittedView, err := env.sess.Resolve(context.Background(), omitted.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(omittedView.Grants) != 1 ||
		omittedView.Grants["gh__list_issues"].Risk != connector.RiskRead {
		t.Fatalf("omitted allowlist grants = %+v", omittedView.Grants)
	}
	responseExpiry, err := time.Parse(time.RFC3339Nano, omitted.ExpiresAt)
	if err != nil {
		t.Fatalf("response expires_at %q: %v", omitted.ExpiresAt, err)
	}
	if !responseExpiry.Equal(omittedView.ExpiresAt) {
		t.Fatalf(
			"response expiry = %s, database expiry = %s",
			responseExpiry,
			omittedView.ExpiresAt,
		)
	}

	// 显式 [] 是零 Tool，不会折叠成 omitted。
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"},"tool_allowlist":[]}`, bh)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("explicit []: %d %s", resp.StatusCode, body)
	}
	var empty struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &empty); err != nil {
		t.Fatal(err)
	}
	emptyView, err := env.sess.Resolve(context.Background(), empty.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyView.Grants) != 0 {
		t.Fatalf("explicit [] grants = %+v", emptyView.Grants)
	}

	// 显式 null 必须拒绝。
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"},"tool_allowlist":null}`, bh)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_allowlist") {
		t.Fatalf("explicit null: %d %s", resp.StatusCode, body)
	}

	// write Tool 只有显式列出时才会进入 grant。
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"},"tool_allowlist":["gh__create_issue"]}`, bh)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("explicit write: %d %s", resp.StatusCode, body)
	}
	var writeGrant struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &writeGrant); err != nil {
		t.Fatal(err)
	}
	writeView, err := env.sess.Resolve(context.Background(), writeGrant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if grant := writeView.Grants["gh__create_issue"]; grant.Risk != connector.RiskWrite {
		t.Fatalf("explicit write grant = %+v", grant)
	}

	// 校验失败样例
	resp, body = doReq(t, http.MethodPost, env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"not-a-uuid"}}`, bh)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid_connection_id") {
		t.Fatalf("非法 uuid 应 400: %d %s", resp.StatusCode, body)
	}
	var sessionsBeforeInvalidTTL int
	if err := env.pool.QueryRow(
		context.Background(),
		`select count(*) from mcp_sessions`,
	).Scan(&sessionsBeforeInvalidTTL); err != nil {
		t.Fatal(err)
	}
	for _, ttl := range []string{
		"-9223372036854775808",
		"-1",
		"86401",
		"90000",
		"9223372036854775807",
	} {
		resp, body = doReq(
			t,
			http.MethodPost,
			env.srv.URL+"/v1/mcp-sessions",
			`{"connections":{"gh":"`+env.connGH.String()+`"},"ttl_seconds":`+ttl+`}`,
			bh,
		)
		if resp.StatusCode != http.StatusBadRequest ||
			!strings.Contains(body, `"error":"invalid_ttl"`) {
			t.Fatalf("ttl_seconds=%s response = %d %s", ttl, resp.StatusCode, body)
		}
	}
	var sessionsAfterInvalidTTL int
	if err := env.pool.QueryRow(
		context.Background(),
		`select count(*) from mcp_sessions`,
	).Scan(&sessionsAfterInvalidTTL); err != nil {
		t.Fatal(err)
	}
	if sessionsAfterInvalidTTL != sessionsBeforeInvalidTTL {
		t.Fatalf(
			"invalid TTLs created %d sessions",
			sessionsAfterInvalidTTL-sessionsBeforeInvalidTTL,
		)
	}

	resp, body = doReq(
		t,
		http.MethodPost,
		env.srv.URL+"/v1/mcp-sessions",
		`{"connections":{"gh":"`+env.connGH.String()+`"},"ttl_seconds":86400}`,
		bh,
	)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("maximum TTL response = %d %s", resp.StatusCode, body)
	}
}
