package exec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
	"github.com/memohai/connect-it/packages/service/tokens"
)

// 本文件是 exec 唯一的测试装配：所有 fake 协作者、两份测试 Definition 和一个
// harness。内存模式用 fake reader/resolver 断言"某一步之前不得越界"，
// database 模式换成真库与真 configsvc，用来断言审计与 credential 状态落库。

type fakeConnections struct {
	calls int
	row   store.Connection
	err   error
}

func (f *fakeConnections) GetConnection(
	context.Context,
	uuid.UUID,
) (store.Connection, error) {
	f.calls++
	return f.row, f.err
}

type fakeConfigs struct {
	calls int
	value map[string]any
	err   error
}

func (f *fakeConfigs) Resolved(
	context.Context,
	connector.Type,
) (map[string]any, error) {
	f.calls++
	return f.value, f.err
}

type fakeRefresher struct {
	calls int
	token tokens.OAuthToken
	err   error
}

func (f *fakeRefresher) AccessToken(
	context.Context,
	uuid.UUID,
	int64,
) (tokens.OAuthToken, error) {
	f.calls++
	return f.token, f.err
}

type fakeMCP struct {
	calls  []mcpclient.CallRequest
	result connector.ToolResultData
	err    error
}

func (f *fakeMCP) CallTool(
	_ context.Context,
	request mcpclient.CallRequest,
) (connector.ToolResultData, error) {
	request.Arguments = append(json.RawMessage(nil), request.Arguments...)
	f.calls = append(f.calls, request)
	if f.err != nil {
		return connector.ToolResultData{}, f.err
	}
	return f.result, nil
}

type authorizationCall struct {
	sessionID  uuid.UUID
	exposed    string
	connection uuid.UUID
	tool       string
	risk       connector.ToolRisk
	generation int64
}

// fakeAuthorizer 按调用顺序返回 results；用尽后一律放行。
type fakeAuthorizer struct {
	calls   []authorizationCall
	results []error
}

func (a *fakeAuthorizer) AuthorizeExecution(
	_ context.Context,
	sessionID uuid.UUID,
	exposed string,
	connectionID uuid.UUID,
	toolID string,
	risk connector.ToolRisk,
	generation int64,
) error {
	a.calls = append(a.calls, authorizationCall{
		sessionID:  sessionID,
		exposed:    exposed,
		connection: connectionID,
		tool:       toolID,
		risk:       risk,
		generation: generation,
	})
	if len(a.results) < len(a.calls) {
		return nil
	}
	return a.results[len(a.calls)-1]
}

var _ SessionExecutionAuthorizer = (*fakeAuthorizer)(nil)

// fakeToolFailureError 是一个把 typed ToolFailure 投影出来的 backend 错误。
type fakeToolFailureError struct {
	failure connector.ToolFailure
}

func (e *fakeToolFailureError) Error() string { return e.failure.Message }

func (e *fakeToolFailureError) ToolFailure() *connector.ToolFailure {
	failure := e.failure
	return &failure
}

// execDefinition 覆盖全部分派路径：managed 成功、managed 缺 handler、
// remote 固定 endpoint、remote self_hosted、remote 带 mapper。
func execDefinition() connector.Definition {
	bearer := connector.MCPAuthBinding{
		Scheme:                      "bearer",
		CredentialFieldByAuthMethod: map[string]string{"api_key": "token"},
	}
	passthrough := json.RawMessage(
		`{"type":"object","additionalProperties":true}`,
	)
	return connector.Definition{
		Type:                "exec_test",
		Name:                "Exec Test",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "mcp_url", Label: "MCP URL", InputType: connector.InputURL},
		},
		AuthMethods: []connector.AuthMethod{
			{Key: "none", Type: connector.AuthNone, Label: "None"},
			{
				Key:   "api_key",
				Type:  connector.AuthAPIKey,
				Label: "API key",
				CredentialFields: []connector.ConfigField{{
					Key:       "token",
					Label:     "Token",
					InputType: connector.InputText,
					Secret:    true,
					Required:  true,
				}},
			},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{
			{
				Key: "fixed",
				Endpoint: connector.Endpoint{
					Source: connector.EndpointFixed,
					URL:    "https://mcp.example.com/mcp",
				},
				AuthBinding: bearer,
				Provenance: connector.Provenance{
					Kind: connector.ProvenanceOfficial,
				},
				RequestTimeout: 5 * time.Second,
			},
			{
				Key: "self",
				Endpoint: connector.Endpoint{
					Source:         connector.EndpointConfigField,
					ConfigFieldKey: "mcp_url",
				},
				AuthBinding: bearer,
				Provenance: connector.Provenance{
					Kind: connector.ProvenanceSelfHosted,
				},
				RequestTimeout: 5 * time.Second,
			},
		},
		Tools: []connector.Tool{
			{
				ID:   "managed_echo",
				Name: "Managed echo",
				Risk: connector.RiskRead,
				InputSchema: json.RawMessage(`{
					"type":"object",
					"properties":{
						"query":{"type":"string"},
						"payload":{"type":"object","additionalProperties":true}
					},
					"additionalProperties":true
				}`),
				Backend: connector.ManagedBackend{HandlerKey: "managed_echo"},
			},
			{
				ID:          "managed_missing",
				Name:        "Managed missing handler",
				Risk:        connector.RiskRead,
				InputSchema: passthrough,
				Backend:     connector.ManagedBackend{HandlerKey: "ghost"},
			},
			{
				ID:          "remote_fixed",
				Name:        "Remote fixed",
				Risk:        connector.RiskRead,
				InputSchema: passthrough,
				Backend: connector.RemoteMCPBackend{
					ServerKey:      "fixed",
					RemoteToolName: "upstream_echo",
				},
			},
			{
				ID:          "remote_self",
				Name:        "Remote self hosted",
				Risk:        connector.RiskRead,
				InputSchema: passthrough,
				Backend: connector.RemoteMCPBackend{
					ServerKey:      "self",
					RemoteToolName: "upstream_echo",
				},
			},
			{
				ID:          "remote_mapped",
				Name:        "Remote mapped",
				Risk:        connector.RiskRead,
				InputSchema: passthrough,
				Backend: connector.RemoteMCPBackend{
					ServerKey:      "fixed",
					RemoteToolName: "upstream_echo",
					InputMapperKey: "in",
				},
			},
		},
	}
}

// schemaDefinition 造一对 schema 完全相同的 managed / remote tool，
// 用来证明两条分派路径共享同一份请求时 instance 校验。
func schemaDefinition(
	input string,
	output string,
	maxInputBytes int64,
) connector.Definition {
	tool := func(id string, backend connector.ToolBackend) connector.Tool {
		return connector.Tool{
			ID:            id,
			Name:          id,
			InputSchema:   json.RawMessage(input),
			OutputSchema:  json.RawMessage(output),
			MaxInputBytes: maxInputBytes,
			Risk:          connector.RiskRead,
			Backend:       backend,
		}
	}
	return connector.Definition{
		Type:                "schema_test",
		Name:                "Schema Test",
		ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{
			{Key: "none", Type: connector.AuthNone, Label: "None"},
			{
				Key:   "oauth",
				Type:  connector.AuthOAuth2,
				Label: "OAuth",
				OAuth: &connector.OAuthConfig{
					AuthorizationEndpoint: "https://provider.example/authorize",
					TokenEndpoint:         "https://provider.example/token",
					Egress: connector.OAuthEgressConfig{
						AuthorizationOrigins: []string{
							"https://provider.example:443",
						},
						TokenOrigins: []string{
							"https://provider.example:443",
						},
					},
				},
			},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{{
			Key: "fixed",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    "https://mcp.example.com/mcp",
			},
			Provenance: connector.Provenance{
				Kind: connector.ProvenanceOfficial,
			},
		}},
		Tools: []connector.Tool{
			tool("managed", connector.ManagedBackend{HandlerKey: "managed"}),
			tool("remote", connector.RemoteMCPBackend{
				ServerKey:      "fixed",
				RemoteToolName: "remote",
			}),
		},
	}
}

// harnessOpt 描述一次测试装配。零值表示"内存 fakes ＋ execDefinition"。
type harnessOpt struct {
	def          connector.Definition
	registered   []string // Registry 认可的 handler key
	runtime      []string // 运行时 HandlerMap 里真正存在的 key
	database     bool
	authMethod   string
	publicConfig string
}

type harness struct {
	t         *testing.T
	ctx       context.Context
	pool      *pgxpool.Pool
	q         *store.Queries
	kr        *crypto.Keyring
	engine    *Engine
	connID    uuid.UUID
	sessionID uuid.UUID

	connections *fakeConnections
	configs     *fakeConfigs
	authorizer  *fakeAuthorizer
	mcp         *fakeMCP

	managed      connector.ToolResultData
	managedErr   error
	managedCalls []connector.ToolCallContext
	beforeReturn func()
}

func newHarness(t *testing.T, opt harnessOpt) *harness {
	t.Helper()
	if opt.def.Type == "" {
		opt.def = execDefinition()
		opt.registered = []string{"managed_echo", "ghost"}
		opt.runtime = []string{"managed_echo"}
		opt.publicConfig = `{"mcp_url":"https://self.internal/mcp"}`
	}
	if opt.runtime == nil {
		opt.runtime = opt.registered
	}
	if opt.authMethod == "" {
		opt.authMethod = "none"
	}
	if opt.publicConfig == "" {
		opt.publicConfig = "{}"
	}

	h := &harness{
		t:          t,
		ctx:        context.Background(),
		connID:     uuid.New(),
		sessionID:  uuid.New(),
		configs:    &fakeConfigs{value: map[string]any{}},
		authorizer: &fakeAuthorizer{},
		mcp:        &fakeMCP{result: connector.ToolResultData{Text: "remote-ok"}},
		managed:    connector.ToolResultData{Text: "managed-ok"},
	}

	reg := registry.New()
	reg.MustRegister(opt.def, opt.registered...)
	runtime := connector.HandlerMap{}
	for _, key := range opt.runtime {
		runtime[key] = func(
			_ context.Context,
			call connector.ToolCallContext,
		) (connector.ToolResultData, error) {
			h.managedCalls = append(h.managedCalls, call)
			if h.beforeReturn != nil {
				h.beforeReturn()
			}
			return h.managed, h.managedErr
		}
	}
	h.engine = &Engine{
		reg:        reg,
		handlers:   map[connector.Type]connector.HandlerMap{opt.def.Type: runtime},
		mcp:        h.mcp,
		authorizer: h.authorizer,
	}

	if !opt.database {
		h.connections = &fakeConnections{row: store.Connection{
			ID:                      h.connID,
			ConnectorType:           string(opt.def.Type),
			AuthMethod:              opt.authMethod,
			Status:                  "active",
			AuthorizationGeneration: 1,
		}}
		h.engine.connections = h.connections
		h.engine.configs = h.configs
		return h
	}

	h.pool = testutil.NewDB(t)
	h.q = store.New(h.pool)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	h.kr = kr
	cfg := configsvc.New(h.q, reg, kr)
	h.engine.q = h.q
	h.engine.connections = h.q
	h.engine.configs = cfg
	h.engine.kr = kr

	h.mustExec(`insert into connector_configs
	  (connector_type, config_schema_version, public_config, secret_config,
	   secret_key_version, created_at, updated_at)
	  values ($1, $2, $3, '\x'::bytea, 1, now(), now())`,
		string(opt.def.Type), int32(opt.def.ConfigSchemaVersion), opt.publicConfig)
	// Production startup establishes the current policy identity before
	// serving reads or creating Connections.
	if _, err := cfg.ReconcilePolicyIdentities(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.mustExec(`insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version,
	   profile, scopes, status, created_at, updated_at)
	  values ($1, $2, $3, $4, '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`,
		h.connID, string(opt.def.Type), "alias-"+h.connID.String()[:8], opt.authMethod)
	return h
}

func (h *harness) mustExec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.ctx, sql, args...); err != nil {
		h.t.Fatalf("%s: %v", sql, err)
	}
}

func (h *harness) request(
	connectionID uuid.UUID,
	toolID string,
	args json.RawMessage,
) ExecuteRequest {
	return ExecuteRequest{
		ConnectionID: connectionID,
		ToolID:       toolID,
		Arguments:    args,
		Authorization: ExecutionGrant{
			SessionID:       h.sessionID,
			ExposedToolName: "alias__" + toolID,
			ConnectionID:    connectionID,
			ToolID:          toolID,
		},
	}
}

func (h *harness) execute(
	toolID string,
	args json.RawMessage,
) (connector.ToolResultData, error) {
	return h.engine.Execute(h.ctx, h.request(h.connID, toolID, args))
}

// useAPIKeyCredential 把 database 模式的 Connection 换成一份可解密的 api_key。
func (h *harness) useAPIKeyCredential() {
	h.t.Helper()
	plain, err := (credential.Fields{
		Fields: map[string]string{"token": "test-api-token"},
	}).Marshal()
	if err != nil {
		h.t.Fatal(err)
	}
	ciphertext, keyVersion, err := h.kr.Encrypt(plain, []byte(h.connID.String()))
	if err != nil {
		h.t.Fatal(err)
	}
	h.mustExec(`update connections
	     set auth_method = 'api_key', credential = $2, secret_key_version = $3
	   where id = $1`, h.connID, ciphertext, keyVersion)
}

type persistedRun struct {
	Status         string
	Error          *string
	ErrorCode      *string
	UpstreamStatus *int32
	SessionID      uuid.UUID
	Input          []byte
	OutputSummary  *string
}

func (h *harness) lastRun() persistedRun {
	h.t.Helper()
	var run persistedRun
	if err := h.pool.QueryRow(
		h.ctx,
		`select status, error, error_code, upstream_status, session_id,
		        input, output_summary
		   from tool_runs
		  order by created_at desc
		  limit 1`,
	).Scan(
		&run.Status,
		&run.Error,
		&run.ErrorCode,
		&run.UpstreamStatus,
		&run.SessionID,
		&run.Input,
		&run.OutputSummary,
	); err != nil {
		h.t.Fatal(err)
	}
	return run
}

func (h *harness) health() (found bool, failures int32, lastError *string) {
	h.t.Helper()
	row, err := h.q.GetConnectorHealth(h.ctx, "exec_test")
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return true, row.ConsecutiveFailures, row.LastError
}

func assertFailureCode(
	t *testing.T,
	result connector.ToolResultData,
	want connector.FailureCode,
) {
	t.Helper()
	if result.Failure == nil || result.Failure.Code != want || !result.Failed() {
		t.Fatalf("result failure = %#v, want %q", result.Failure, want)
	}
}
