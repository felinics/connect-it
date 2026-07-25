// Package exec 是两种 Tool backend 的统一执行引擎（spec §11）：
// 装配 Definition＋配置＋credential，分派 Managed handler 或 Remote MCP，
// 结果 piggyback 写入 tool_runs 与 connector_health。
//
// health 只记录 backend 真正被调用后的成败：配置类拒绝（tool/handler 不存在、
// mapper 未实现、self_hosted 未 verify）不污染健康数据；health-neutral 的 typed
// Provider failure 视为连通成功（health 记成功、tool_runs 记 error）。
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

var (
	ErrConnectionNotFound = errors.New("exec: connection 不存在")
	ErrToolUnavailable    = errors.New("exec: tool 不存在或已下线")
	ErrInvalidToolResult  = errors.New("exec: backend 返回了矛盾的 tool result")
	errCredentialState    = errors.New("exec: 更新 credential 状态失败")
)

const (
	messagePolicyDenied   = "session is not authorized to execute this tool"
	messageReauthRequired = "connection requires authorization"
	messageInputTooLarge  = "input exceeds the allowed size"
)

type MCPCaller interface {
	CallTool(context.Context, mcpclient.CallRequest) (connector.ToolResultData, error)
}

type connectionReader interface {
	GetConnection(context.Context, uuid.UUID) (store.Connection, error)
}

type configResolver interface {
	Resolved(context.Context, connector.Type) (map[string]any, error)
}

type accessTokenRefresher interface {
	AccessToken(context.Context, uuid.UUID, int64) (tokens.OAuthToken, error)
}

// Deps 是 Engine 的全部协作者。composition root 用具名字段装配，
// 避免一串位置参数在调用点失去含义。
type Deps struct {
	Store      *store.Queries
	Registry   *registry.Registry
	Configs    *configsvc.Service
	Refresher  *tokens.Refresher
	Keyring    *crypto.Keyring
	Handlers   map[connector.Type]connector.HandlerMap
	MCP        MCPCaller
	Matchers   connector.ScopeMatcherMap
	Authorizer SessionExecutionAuthorizer
}

type Engine struct {
	q           *store.Queries
	connections connectionReader
	reg         *registry.Registry
	configs     configResolver
	refresher   accessTokenRefresher
	kr          *crypto.Keyring
	handlers    map[connector.Type]connector.HandlerMap
	mcp         MCPCaller
	matchers    connector.ScopeMatcherMap
	authorizer  SessionExecutionAuthorizer
}

func New(deps Deps) *Engine {
	engine := &Engine{
		q:          deps.Store,
		reg:        deps.Registry,
		kr:         deps.Keyring,
		handlers:   deps.Handlers,
		mcp:        deps.MCP,
		matchers:   deps.Matchers,
		authorizer: deps.Authorizer,
	}
	// 每个可选依赖单独判空后再装进接口字段，避免 nil 指针被包成非 nil 接口。
	if deps.Store != nil {
		engine.connections = deps.Store
	}
	if deps.Configs != nil {
		engine.configs = deps.Configs
	}
	if deps.Refresher != nil {
		engine.refresher = deps.Refresher
	}
	return engine
}

// stop 是流水线某一阶段要求提前结束时交给调用方的结果；nil 表示继续。
type stop = *connector.ToolResultData

// Execute 执行一次 Tool 调用。返回的 error 表示装配/传输层失败；
// 预期的输入或 Provider 失败经 ToolResultData.Failure 表达。
func (e *Engine) Execute(ctx context.Context, request ExecuteRequest) (connector.ToolResultData, error) {
	// This must remain the first execution boundary: no JSON decoding, Registry
	// lookup, or database call may happen before the protocol-wide hard cap.
	if exceedsInputLimit(request.Arguments, connector.AbsoluteMaxInputBytes) {
		return failureResult(connector.FailureInputTooLarge, messageInputTooLarge), nil
	}
	if !validExecutionGrant(request) {
		return policyDeniedResult(), nil
	}

	r := &run{engine: e, request: request, started: time.Now()}
	if halt, err := r.prepare(ctx); halt != nil || err != nil {
		return stopped(halt), err
	}
	result, halt, err := r.dispatch(ctx)
	if halt != nil || (err != nil && !r.backendTouched) {
		return stopped(halt), err
	}
	return r.finish(ctx, result, err)
}

// run 收拢一次 Execute 的全部中间状态。它不是抽象层：只是给原先那个线性
// 函数的局部变量一个名字，好让每个阶段能以方法形式独立命名。
type run struct {
	engine  *Engine
	request ExecuteRequest
	started time.Time
	rec     *recorder

	connectorType connector.Type
	def           connector.Definition
	conn          store.Connection
	tool          *connector.Tool
	schemas       registry.CompiledToolSchemas
	method        connector.AuthMethod

	arguments  map[string]any
	normalized json.RawMessage
	config     map[string]any
	oauth      tokens.OAuthToken
	credential map[string]any

	// 这两个版本号必须是 backend 真正使用的那份快照：OAuth 惰性刷新会覆盖
	// 首次读到的 Connection 行。
	credentialVersion       int64
	authorizationGeneration int64

	backendTouched bool
}

// prepare 是 backend 之前的全部装配与准入检查，按执行顺序排列。
func (r *run) prepare(ctx context.Context) (stop, error) {
	for _, phase := range []func(context.Context) (stop, error){
		r.resolveTool,
		r.checkToolInputLimit,
		r.authorizeRequest,
		r.validateInput,
		r.authorizeConnection,
		r.resolveConfig,
		r.resolveCredential,
	} {
		if halt, err := phase(ctx); halt != nil || err != nil {
			return halt, err
		}
	}
	return nil, nil
}

// resolveTool 读取 Connection、Definition、Tool 与已编译 schema。
// recorder 一旦建立，后续每一次失败都会进审计。
func (r *run) resolveTool(ctx context.Context) (stop, error) {
	if r.engine.connections == nil {
		return nil, fmt.Errorf("exec: connection reader 未配置")
	}
	row, err := r.engine.connections.GetConnection(ctx, r.request.ConnectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}
		return nil, err
	}
	r.conn = row
	r.connectorType = connector.Type(row.ConnectorType)
	r.credentialVersion = row.CredentialVersion
	r.authorizationGeneration = row.AuthorizationGeneration

	def, ok := r.engine.reg.Get(r.connectorType)
	if !ok {
		return nil, fmt.Errorf("exec: 未知 connector type %s", r.connectorType)
	}
	r.def = def
	r.rec = &recorder{
		engine:        r.engine,
		started:       r.started,
		connectorType: row.ConnectorType,
		connectionID:  r.request.ConnectionID,
		sessionID:     r.request.Authorization.SessionID,
		toolID:        r.request.ToolID,
		input:         r.request.Arguments,
	}

	if r.tool = findTool(def, r.request.ToolID); r.tool == nil {
		return nil, r.fail(ctx, fmt.Errorf("%w: %s", ErrToolUnavailable, r.request.ToolID))
	}
	r.rec.inputSchema = r.tool.InputSchema

	schemas, ok := r.engine.reg.ToolSchemas(r.connectorType, r.request.ToolID)
	if !ok || schemas.Input == nil {
		return nil, r.fail(ctx, fmt.Errorf("exec: tool %q 缺少已编译 schema", r.request.ToolID))
	}
	r.schemas = schemas
	return nil, nil
}

func (r *run) checkToolInputLimit(context.Context) (stop, error) {
	if !exceedsInputLimit(r.request.Arguments, r.schemas.MaxInputBytes) {
		return nil, nil
	}
	// Do not record or decode over-limit input. In particular, never persist
	// even a truncated prefix of attacker-controlled arguments.
	result := failureResult(connector.FailureInputTooLarge, messageInputTooLarge)
	return &result, nil
}

func (r *run) authorizeRequest(ctx context.Context) (stop, error) {
	return r.authorize(ctx, r.conn.AuthorizationGeneration)
}

// validateInput 是请求时的 instance 校验：schema 在注册时已编译，这里校验的是
// 本次调用的实参并应用 accepted defaults。失败信息绝不回显调用方内容。
func (r *run) validateInput(context.Context) (stop, error) {
	arguments, normalized, err := normalizeToolArguments(r.request.Arguments, r.schemas.Input)
	if err != nil {
		result := failureResult(
			connector.FailureInvalidInput,
			"input does not match the tool schema",
		)
		return &result, nil
	}
	r.arguments, r.normalized = arguments, normalized
	return nil, nil
}

// authorizeConnection 解析 auth method，再检查 credential 授予的 scope 与
// Connection 自身状态。
func (r *run) authorizeConnection(ctx context.Context) (stop, error) {
	method, err := findAuthMethod(r.def, r.conn.AuthMethod)
	if err != nil {
		return nil, r.fail(ctx, err)
	}
	r.method = method
	if !r.engine.scopesAuthorize(
		r.connectorType,
		method.Key,
		r.conn.Scopes,
		r.conn.ScopesKnown,
		r.tool.RequiredScopes,
	) {
		return r.deny(ctx, connector.FailurePermissionDenied,
			"credential does not grant the permission required by this tool"), nil
	}
	if r.conn.Status != "active" {
		return r.deny(ctx, connector.FailureAuthorizationFailed, messageReauthRequired), nil
	}
	return nil, nil
}

func (r *run) resolveConfig(ctx context.Context) (stop, error) {
	if r.engine.configs == nil {
		return nil, fmt.Errorf("exec: config resolver 未配置")
	}
	resolved, err := r.engine.configs.Resolved(ctx, r.connectorType)
	if err != nil {
		return nil, r.fail(ctx, err)
	}
	r.config = resolved
	return nil, nil
}

func (r *run) resolveCredential(ctx context.Context) (stop, error) {
	switch r.method.Type {
	case connector.AuthNone:
		return nil, nil
	case connector.AuthOAuth2:
		return r.resolveOAuthToken(ctx)
	case connector.AuthAPIKey, connector.AuthCustomCredential:
		return r.resolveCredentialFields(ctx)
	default:
		return nil, r.fail(ctx, fmt.Errorf("exec: auth method %q 类型不支持", r.method.Key))
	}
}

func (r *run) resolveOAuthToken(ctx context.Context) (stop, error) {
	if r.engine.refresher == nil {
		return nil, r.fail(ctx, errors.New("exec: OAuth token refresher 未配置"))
	}
	token, err := r.engine.refresher.AccessToken(
		ctx,
		r.request.ConnectionID,
		r.conn.AuthorizationGeneration,
	)
	if err != nil {
		return r.classifyRefreshFailure(ctx, err)
	}
	if token.CredentialVersion < 1 ||
		token.AuthorizationGeneration != r.conn.AuthorizationGeneration {
		return nil, r.fail(ctx, errors.New("exec: OAuth token snapshot version 不完整"))
	}
	r.oauth = token
	r.credentialVersion = token.CredentialVersion
	r.authorizationGeneration = token.AuthorizationGeneration
	return nil, nil
}

// classifyRefreshFailure 的分支顺序是有意的：typed Provider failure 必须先于
// context 判定，因为它常常把 ctx.Err() 当作 cause 包在里面。
func (r *run) classifyRefreshFailure(ctx context.Context, err error) (stop, error) {
	switch {
	case errors.Is(err, tokens.ErrAuthorizationChanged):
		return r.deny(ctx, connector.FailurePolicyDenied, messagePolicyDenied), nil
	case errors.Is(err, tokens.ErrReauthRequired):
		return r.deny(ctx, connector.FailureAuthorizationFailed, messageReauthRequired), nil
	}
	if failure, ok := publicToolFailure(err); ok {
		// 一个 typed Provider failure 说明刷新端点确实被访问到了。
		result := connector.ToolResultData{Failure: &failure}
		r.rec.record(ctx, result, nil, true)
		return &result, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return r.deny(ctx, connector.FailureCanceled,
			connector.DefaultFailureMessage(connector.FailureCanceled)), nil
	}
	return nil, r.fail(ctx, err)
}

func (r *run) resolveCredentialFields(ctx context.Context) (stop, error) {
	if r.engine.kr == nil {
		return nil, r.fail(ctx, errors.New("exec: credential keyring 未配置"))
	}
	plain, err := r.engine.kr.Decrypt(
		r.conn.Credential,
		int(r.conn.SecretKeyVersion),
		[]byte(r.conn.ID.String()),
	)
	if err != nil {
		return nil, r.fail(ctx, err)
	}
	fields, err := credential.UnmarshalFields(plain)
	if err != nil {
		return nil, r.fail(ctx, err)
	}
	r.credential = make(map[string]any, len(fields.Fields))
	for key, value := range fields.Fields {
		r.credential[key] = value
	}
	return nil, nil
}

// dispatch 把已装配好的调用交给具体 backend。返回的 error 在 backendTouched
// 为 true 时是 backend 自身的失败，否则是已记账的装配错误。
func (r *run) dispatch(ctx context.Context) (connector.ToolResultData, stop, error) {
	none := connector.ToolResultData{}
	switch backend := r.tool.Backend.(type) {
	case connector.ManagedBackend:
		return r.callManaged(ctx, backend)
	case connector.RemoteMCPBackend:
		return r.callRemote(ctx, backend)
	default:
		return none, nil, r.fail(ctx, fmt.Errorf(
			"exec: tool %q 缺少可执行 backend", r.request.ToolID))
	}
}

func (r *run) callManaged(
	ctx context.Context,
	backend connector.ManagedBackend,
) (connector.ToolResultData, stop, error) {
	none := connector.ToolResultData{}
	r.rec.backend = auditBackendManaged
	handler, exists := r.engine.handlers[r.connectorType][backend.HandlerKey]
	if !exists {
		return none, nil, r.fail(ctx, fmt.Errorf(
			"exec: managed handler %q 未注册", backend.HandlerKey))
	}
	if halt, err := r.authorize(ctx, r.authorizationGeneration); halt != nil || err != nil {
		return none, halt, err
	}
	r.backendTouched = true
	result, err := handler(ctx, connector.ToolCallContext{
		ConnectorType: r.connectorType,
		ConnectionID:  r.request.ConnectionID.String(),
		ToolID:        r.request.ToolID,
		Arguments:     r.arguments,
		Config:        r.config,
		Credential:    r.credential,
		AccessToken:   r.oauth.AccessToken,
		TokenType:     r.oauth.TokenType,
	})
	return result, nil, err
}

func (r *run) callRemote(
	ctx context.Context,
	backend connector.RemoteMCPBackend,
) (connector.ToolResultData, stop, error) {
	none := connector.ToolResultData{}
	r.rec.backend = auditBackendRemote
	if backend.InputMapperKey != "" || backend.OutputMapperKey != "" {
		return none, nil, r.fail(ctx, fmt.Errorf(
			"exec: tool %q 的 mapper 未实现（第一期仅支持参数直通）", r.request.ToolID))
	}
	server := findServer(r.def, backend.ServerKey)
	if server == nil {
		return none, nil, r.fail(ctx, fmt.Errorf(
			"exec: MCP server %q 不存在", backend.ServerKey))
	}
	endpoint, err := r.engine.resolveEndpoint(ctx, r.connectorType, server, r.config)
	if err != nil {
		return none, nil, r.fail(ctx, err)
	}
	if r.engine.mcp == nil {
		return none, nil, r.fail(ctx, fmt.Errorf("exec: MCP caller 未配置"))
	}
	bearerToken, err := remoteMCPBearerToken(
		r.method,
		server.AuthBinding,
		r.oauth,
		r.credential,
	)
	if err != nil {
		return none, nil, r.fail(ctx, err)
	}
	if halt, err := r.authorize(ctx, r.authorizationGeneration); halt != nil || err != nil {
		return none, halt, err
	}
	r.backendTouched = true
	result, err := r.engine.mcp.CallTool(ctx, mcpclient.CallRequest{
		ConnectorType:     r.connectorType,
		ConnectionID:      r.request.ConnectionID,
		ToolID:            r.request.ToolID,
		Server:            *server,
		Endpoint:          endpoint,
		BearerToken:       bearerToken,
		AllowInsecureHTTP: remoteMCPAllowInsecure(server, r.config),
		RemoteToolName:    backend.RemoteToolName,
		Arguments:         r.normalized,
	})
	return result, nil, err
}

// finish 把 backend 结果收敛成公开契约：typed failure 归一化、结构化输出做
// instance 校验、显式 credential-invalid 用精确快照做 CAS，最后写审计。
func (r *run) finish(
	ctx context.Context,
	result connector.ToolResultData,
	execErr error,
) (connector.ToolResultData, error) {
	if execErr != nil {
		failure, ok := publicToolFailure(execErr)
		if !ok {
			r.rec.record(ctx, result, execErr, r.backendTouched)
			return result, execErr
		}
		result = connector.ToolResultData{Failure: &failure}
	}
	result, err := validateBackendResult(result, r.schemas.Output)
	if err == nil &&
		result.Failure.IndicatesCredentialInvalid() &&
		r.method.Type != connector.AuthNone {
		err = r.markCredentialInvalid(ctx)
	}
	r.rec.record(ctx, result, err, r.backendTouched)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	return result, nil
}

func (r *run) markCredentialInvalid(ctx context.Context) error {
	if r.engine.q == nil {
		return fmt.Errorf("%w: store 未配置", errCredentialState)
	}
	if _, err := r.engine.q.MarkConnectionCredentialInvalidCAS(
		ctx,
		store.MarkConnectionCredentialInvalidCASParams{
			ConnectionID:                    r.request.ConnectionID,
			ExpectedCredentialVersion:       r.credentialVersion,
			ExpectedAuthorizationGeneration: r.authorizationGeneration,
		},
	); err != nil {
		return fmt.Errorf("%w: %w", errCredentialState, err)
	}
	return nil
}

// authorize 每一次都对 backend 将要使用的 authorization generation 复检 session
// 授权：显式拒绝 fail-closed 成 policy_denied，基础设施错误原样上抛。
func (r *run) authorize(ctx context.Context, generation int64) (stop, error) {
	err := r.engine.executionAuthorizationError(ctx, r.request, r.tool.Risk, generation)
	if err == nil {
		return nil, nil
	}
	if !isExecutionUnauthorized(err) {
		return nil, r.fail(ctx, err)
	}
	return r.deny(ctx, connector.FailurePolicyDenied, messagePolicyDenied), nil
}

// fail 记录一次装配失败并原样返回该 error。
func (r *run) fail(ctx context.Context, err error) error {
	r.rec.record(ctx, connector.ToolResultData{}, err, false)
	return err
}

// deny 记录一次未触达 backend 的拒绝，并返回要交给调用方的结果。
func (r *run) deny(ctx context.Context, code connector.FailureCode, message string) stop {
	denied := failureResult(code, message)
	r.rec.record(ctx, denied, nil, false)
	return &denied
}

func stopped(halt stop) connector.ToolResultData {
	if halt == nil {
		return connector.ToolResultData{}
	}
	return *halt
}

// publicToolFailure 提取 backend 错误里已归一化的公开 ToolFailure 投影。
func publicToolFailure(err error) (connector.ToolFailure, bool) {
	var public interface{ ToolFailure() *connector.ToolFailure }
	if !errors.As(err, &public) {
		return connector.ToolFailure{}, false
	}
	return connector.NormalizeToolFailure(public.ToolFailure()), true
}

func remoteMCPAllowInsecure(server *connector.RemoteMCPServer, resolved map[string]any) string {
	if server == nil || server.Endpoint.Source != connector.EndpointConfigField {
		return "false"
	}
	value, _ := resolved["allow_insecure_http"].(string)
	if value == "" {
		return "false"
	}
	return value
}

func (e *Engine) scopesAuthorize(
	t connector.Type,
	authMethod string,
	granted []string,
	scopesKnown bool,
	required []string,
) bool {
	if !scopesKnown {
		return true
	}
	matcher := connector.ExactScopeMatcher
	if byMethod := e.matchers[t]; byMethod != nil && byMethod[authMethod] != nil {
		matcher = byMethod[authMethod]
	}
	for _, scope := range required {
		if !matcher(granted, scope) {
			return false
		}
	}
	return true
}

func remoteMCPBearerToken(
	method connector.AuthMethod,
	binding connector.MCPAuthBinding,
	oauthToken tokens.OAuthToken,
	fields map[string]any,
) (string, error) {
	if method.Type == connector.AuthNone {
		return "", nil
	}
	if binding.Scheme != "bearer" {
		return "", fmt.Errorf("exec: Remote MCP auth scheme 不支持")
	}
	switch method.Type {
	case connector.AuthOAuth2:
		if oauthToken.TokenType != "Bearer" || oauthToken.AccessToken == "" {
			return "", fmt.Errorf("exec: OAuth bearer token 不完整")
		}
		return oauthToken.AccessToken, nil
	case connector.AuthAPIKey, connector.AuthCustomCredential:
		fieldKey := binding.CredentialFieldByAuthMethod[method.Key]
		if fieldKey == "" {
			return "", fmt.Errorf(
				"exec: Remote MCP auth method %q 缺少显式 credential binding", method.Key)
		}
		value, ok := fields[fieldKey].(string)
		if !ok || value == "" {
			return "", fmt.Errorf(
				"exec: Remote MCP credential binding %q 不可用", fieldKey)
		}
		return value, nil
	default:
		return "", fmt.Errorf("exec: Remote MCP auth method 不支持")
	}
}

func validExecutionGrant(request ExecuteRequest) bool {
	grant := request.Authorization
	return request.ConnectionID != uuid.Nil &&
		request.ToolID != "" &&
		grant.SessionID != uuid.Nil &&
		grant.ExposedToolName != "" &&
		grant.ConnectionID == request.ConnectionID &&
		grant.ToolID == request.ToolID
}

func (e *Engine) executionAuthorizationError(
	ctx context.Context,
	request ExecuteRequest,
	currentRisk connector.ToolRisk,
	currentAuthorizationGeneration int64,
) error {
	if e.authorizer == nil || currentAuthorizationGeneration < 1 {
		return ErrExecutionUnauthorized
	}
	grant := request.Authorization
	return e.authorizer.AuthorizeExecution(
		ctx,
		grant.SessionID,
		grant.ExposedToolName,
		grant.ConnectionID,
		grant.ToolID,
		currentRisk,
		currentAuthorizationGeneration,
	)
}

func policyDeniedResult() connector.ToolResultData {
	return failureResult(connector.FailurePolicyDenied, messagePolicyDenied)
}

// resolveEndpoint 求解 MCP endpoint：固定 URL 直接用；来自配置的必须已通过
// mcp:verify 且与验证时一致（spec §13 规则 3），并做 https 检查（规则 2）。
func (e *Engine) resolveEndpoint(ctx context.Context, t connector.Type, server *connector.RemoteMCPServer, resolved map[string]any) (string, error) {
	if server.Endpoint.Source == connector.EndpointFixed {
		return server.Endpoint.URL, nil
	}
	endpoint, _ := resolved[server.Endpoint.ConfigFieldKey].(string)
	if endpoint == "" {
		return "", fmt.Errorf("exec: 配置字段 %q 未填写 MCP endpoint", server.Endpoint.ConfigFieldKey)
	}
	allowInsecure, _ := resolved["allow_insecure_http"].(string)
	if err := mcpclient.CheckEndpoint(endpoint, allowInsecure == "true"); err != nil {
		return "", err
	}
	v, err := e.q.GetConnectorConfigVerification(ctx, string(t))
	if err != nil {
		return "", err
	}
	if v.McpVerifiedAt == nil || v.McpVerifiedEndpoint == nil || *v.McpVerifiedEndpoint != endpoint {
		return "", fmt.Errorf("exec: self_hosted endpoint 未通过 mcp:verify 或已变更，须先验证")
	}
	return endpoint, nil
}

func findTool(def connector.Definition, id string) *connector.Tool {
	for i := range def.Tools {
		if def.Tools[i].ID == id {
			return &def.Tools[i]
		}
	}
	return nil
}

func findServer(def connector.Definition, key string) *connector.RemoteMCPServer {
	for i := range def.RemoteMCPServers {
		if def.RemoteMCPServers[i].Key == key {
			return &def.RemoteMCPServers[i]
		}
	}
	return nil
}

func findAuthMethod(def connector.Definition, key string) (connector.AuthMethod, error) {
	for _, m := range def.AuthMethods {
		if m.Key == key {
			return m, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf("exec: 未知 auth method %s", key)
}
