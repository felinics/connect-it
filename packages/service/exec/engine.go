// Package exec 是两种 Tool backend 的统一执行引擎（spec §11）：
// 装配 Definition＋配置＋credential，分派 Managed handler 或 Remote MCP，
// 结果 piggyback 写入 tool_runs 与 connector_health。
//
// health 只记录 backend 真正被调用后的成败：配置类拒绝（tool/handler 不存在、
// mapper 未实现、self_hosted 未 verify）不污染健康数据；上游 IsError=true
// 视为连通成功（health 记成功、tool_runs 记 error）。
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

const (
	maxInputBytes    = 64 * 1024
	maxSummaryBytes  = 2048
	defaultMCPWindow = 30 * time.Second
)

var (
	ErrConnectionNotFound = errors.New("exec: connection 不存在")
	ErrToolUnavailable    = errors.New("exec: tool 不存在或已下线")
)

type MCPCaller interface {
	CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error)
}

type Engine struct {
	q         *store.Queries
	reg       *registry.Registry
	cfg       *configsvc.Service
	refresher *tokens.Refresher
	kr        *crypto.Keyring
	handlers  map[connector.Type]connector.HandlerMap
	mcp       MCPCaller
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, refresher *tokens.Refresher, kr *crypto.Keyring, handlers map[connector.Type]connector.HandlerMap, mcp MCPCaller) *Engine {
	return &Engine{q: q, reg: reg, cfg: cfg, refresher: refresher, kr: kr, handlers: handlers, mcp: mcp}
}

// Execute 执行一次 Tool 调用。返回的 error 表示装配/传输层失败；
// 业务失败经 ToolResultData.IsError 表达。
func (e *Engine) Execute(ctx context.Context, connectionID uuid.UUID, toolID string, args json.RawMessage) (connector.ToolResultData, error) {
	started := time.Now()

	row, err := e.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return connector.ToolResultData{}, ErrConnectionNotFound
		}
		return connector.ToolResultData{}, err
	}
	t := connector.Type(row.ConnectorType)
	def, ok := e.reg.Get(t)
	if !ok {
		return connector.ToolResultData{}, fmt.Errorf("exec: 未知 connector type %s", t)
	}

	rec := &recorder{engine: e, started: started, connectorType: string(t), connectionID: connectionID, toolID: toolID, input: args}

	tool := findTool(def, toolID)
	if tool == nil {
		err := fmt.Errorf("%w: %s", ErrToolUnavailable, toolID)
		rec.record(ctx, connector.ToolResultData{}, err, false)
		return connector.ToolResultData{}, err
	}

	var argsMap map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			err = fmt.Errorf("exec: arguments 不是合法 JSON 对象: %w", err)
			rec.record(ctx, connector.ToolResultData{}, err, false)
			return connector.ToolResultData{}, err
		}
	}
	resolved, err := e.cfg.Resolved(ctx, t)
	if err != nil {
		rec.record(ctx, connector.ToolResultData{}, err, false)
		return connector.ToolResultData{}, err
	}

	method, err := findAuthMethod(def, row.AuthMethod)
	if err != nil {
		rec.record(ctx, connector.ToolResultData{}, err, false)
		return connector.ToolResultData{}, err
	}
	var accessToken string
	var credFields map[string]any
	if method.Type != connector.AuthNone {
		accessToken, err = e.refresher.AccessToken(ctx, connectionID)
		if err != nil {
			rec.record(ctx, connector.ToolResultData{}, err, false)
			return connector.ToolResultData{}, err
		}
		if method.Type == connector.AuthAPIKey || method.Type == connector.AuthCustomCredential {
			plain, err := e.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(row.ID.String()))
			if err != nil {
				rec.record(ctx, connector.ToolResultData{}, err, false)
				return connector.ToolResultData{}, err
			}
			fields, err := credential.UnmarshalFields(plain)
			if err != nil {
				rec.record(ctx, connector.ToolResultData{}, err, false)
				return connector.ToolResultData{}, err
			}
			credFields = map[string]any{}
			for k, v := range fields.Fields {
				credFields[k] = v
			}
		}
	}

	call := connector.ToolCallContext{
		ConnectorType: t,
		ToolID:        toolID,
		Arguments:     argsMap,
		Config:        resolved,
		Credential:    credFields,
		AccessToken:   accessToken,
	}

	switch b := tool.Backend.(type) {
	case connector.ManagedBackend:
		handler, ok := e.handlers[t][b.HandlerKey]
		if !ok {
			err := fmt.Errorf("exec: managed handler %q 未注册", b.HandlerKey)
			rec.record(ctx, connector.ToolResultData{}, err, false)
			return connector.ToolResultData{}, err
		}
		res, err := handler(ctx, call)
		rec.record(ctx, res, err, true)
		return res, err
	case connector.RemoteMCPBackend:
		if b.InputMapperKey != "" || b.OutputMapperKey != "" {
			err := fmt.Errorf("exec: tool %q 的 mapper 未实现（第一期仅支持参数直通）", toolID)
			rec.record(ctx, connector.ToolResultData{}, err, false)
			return connector.ToolResultData{}, err
		}
		server := findServer(def, b.ServerKey)
		if server == nil {
			err := fmt.Errorf("exec: MCP server %q 不存在", b.ServerKey)
			rec.record(ctx, connector.ToolResultData{}, err, false)
			return connector.ToolResultData{}, err
		}
		endpoint, err := e.resolveEndpoint(ctx, t, server, resolved)
		if err != nil {
			rec.record(ctx, connector.ToolResultData{}, err, false)
			return connector.ToolResultData{}, err
		}
		timeout := server.RequestTimeout
		if timeout <= 0 {
			timeout = defaultMCPWindow
		}
		res, err := e.mcp.CallTool(ctx, endpoint, accessToken, timeout, b.RemoteToolName, args)
		rec.record(ctx, res, err, true)
		return res, err
	default:
		err := fmt.Errorf("exec: tool %q 缺少可执行 backend", toolID)
		rec.record(ctx, connector.ToolResultData{}, err, false)
		return connector.ToolResultData{}, err
	}
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

// recorder 统一写 tool_runs 与（仅当 backend 被真正调用时）connector_health。
type recorder struct {
	engine        *Engine
	started       time.Time
	connectorType string
	connectionID  uuid.UUID
	toolID        string
	input         json.RawMessage
}

func (r *recorder) record(ctx context.Context, res connector.ToolResultData, execErr error, backendTouched bool) {
	status := "ok"
	var errText *string
	switch {
	case execErr != nil:
		status = "error"
		msg := execErr.Error()
		errText = &msg
	case res.IsError:
		status = "error"
	}

	input := r.input
	if len(input) > maxInputBytes {
		// 截断原文会破坏 jsonb 合法性，改存标记对象。
		input, _ = json.Marshal(map[string]any{"truncated": true, "original_bytes": len(r.input)})
	}
	if len(input) == 0 {
		input = nil
	}
	var summary *string
	if res.Text != "" {
		s := res.Text
		if len(s) > maxSummaryBytes {
			s = s[:maxSummaryBytes]
		}
		summary = &s
	}
	duration := int32(time.Since(r.started).Milliseconds())
	connID := r.connectionID
	if err := r.engine.q.InsertToolRun(ctx, store.InsertToolRunParams{
		ID:            uuid.New(),
		ConnectorType: r.connectorType,
		ConnectionID:  &connID,
		ToolID:        r.toolID,
		Status:        status,
		Error:         errText,
		Input:         input,
		OutputSummary: summary,
		DurationMs:    &duration,
	}); err != nil {
		// 审计写入失败不阻断执行结果，只能吞掉（记录层自身无处上报）。
		_ = err
	}

	if !backendTouched {
		return
	}
	if execErr != nil {
		msg := execErr.Error()
		_ = r.engine.q.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{
			ConnectorType: r.connectorType, LastError: &msg,
		})
		return
	}
	_ = r.engine.q.UpsertConnectorHealthSuccess(ctx, r.connectorType)
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
