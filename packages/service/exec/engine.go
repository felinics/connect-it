// Package exec assembles a connection and dispatches native MCP tools.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

const (
	defaultMCPTimeout = 30 * time.Second
	auditWriteTimeout = 2 * time.Second
)

var (
	ErrConnectionNotFound = errors.New("exec: connection does not exist")
	ErrConnectionInactive = errors.New("exec: connection is not usable")
	ErrToolUnavailable    = errors.New("exec: tool does not exist or was withdrawn")
)

const (
	errorKindAuth            = "auth"
	errorKindCanceled        = "canceled"
	errorKindInternal        = "internal"
	errorKindInvalidArgs     = "invalid_args"
	errorKindTimeout         = "timeout"
	errorKindToolError       = "tool_error"
	errorKindToolUnavailable = "tool_unavailable"
	errorKindTransport       = "transport"
	errorKindUpstream4xx     = "upstream_4xx"
	errorKindUpstream5xx     = "upstream_5xx"
)

type MCPClient interface {
	ListTools(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration) ([]*mcp.Tool, error)
	CallTool(ctx context.Context, endpoint, token, authorizationScheme string, timeout time.Duration, params *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error)
}

type Engine struct {
	q         *store.Queries
	reg       *registry.Registry
	cfg       *configsvc.Service
	refresher *tokens.Refresher
	kr        *crypto.Keyring
	mcp       MCPClient
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, refresher *tokens.Refresher, kr *crypto.Keyring, mcpClient MCPClient) *Engine {
	return &Engine{q: q, reg: reg, cfg: cfg, refresher: refresher, kr: kr, mcp: mcpClient}
}

// ListTools returns static Managed tools or dynamically discovers the native
// tool definitions exposed by a Remote MCP connector.
func (e *Engine) ListTools(ctx context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error) {
	row, def, err := e.connection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	switch impl := def.Implementation.(type) {
	case connector.Managed:
		tools := make([]*mcp.Tool, 0, len(impl.Tools))
		for _, managedTool := range impl.Tools {
			tool := managedTool.Tool
			tools = append(tools, &tool)
		}
		return tools, nil
	case connector.RemoteMCP:
		prepared, err := e.prepare(ctx, row, def)
		if err != nil {
			return nil, err
		}
		endpoint, err := remoteEndpoint(impl, prepared.config)
		if err != nil {
			return nil, err
		}
		return e.mcp.ListTools(ctx, endpoint, prepared.accessToken,
			authorizationScheme(impl), requestTimeout(impl))
	default:
		return nil, fmt.Errorf("exec: connector %s has an invalid implementation", def.Type)
	}
}

// CallTool dispatches one native MCP call. The caller must authorize and route
// the original tool name against the immutable session snapshot first.
func (e *Engine) CallTool(
	ctx context.Context,
	sessionID, apiTokenID, connectionID uuid.UUID,
	params *mcp.CallToolParamsRaw,
) (*mcp.CallToolResult, error) {
	row, def, err := e.connection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	toolName := ""
	if params != nil {
		toolName = params.Name
	}
	rec := &recorder{
		engine:        e,
		started:       time.Now(),
		connectorType: row.ConnectorType,
		connectionID:  connectionID,
		sessionID:     sessionID,
		apiTokenID:    apiTokenID,
		toolName:      toolName,
	}
	if params == nil || params.Name == "" {
		err := fmt.Errorf("%w: tool name must not be empty", ErrToolUnavailable)
		rec.record(ctx, nil, err)
		return nil, err
	}

	switch impl := def.Implementation.(type) {
	case connector.Managed:
		tool := findManagedTool(impl, params.Name)
		if tool == nil {
			err := fmt.Errorf("%w: %s", ErrToolUnavailable, params.Name)
			rec.record(ctx, nil, err)
			return nil, err
		}
		arguments, err := validateManagedArguments(tool.Tool.InputSchema, params.Arguments)
		if err != nil {
			result := invalidArgumentsResult(err)
			rec.record(ctx, result, nil)
			return result, nil
		}
		prepared, err := e.prepare(ctx, row, def)
		if err != nil {
			rec.record(ctx, nil, err)
			return nil, err
		}
		result, err := tool.Handler(ctx, connector.ManagedCall{
			Arguments:   arguments,
			Config:      prepared.config,
			Credential:  prepared.credential,
			AccessToken: prepared.accessToken,
		})
		if result == nil && err == nil {
			err = fmt.Errorf("exec: managed tool %q returned an empty result", params.Name)
		}
		rec.record(ctx, result, err)
		return result, err
	case connector.RemoteMCP:
		prepared, err := e.prepare(ctx, row, def)
		if err != nil {
			rec.record(ctx, nil, err)
			return nil, err
		}
		endpoint, err := remoteEndpoint(impl, prepared.config)
		if err != nil {
			rec.record(ctx, nil, err)
			return nil, err
		}
		result, err := e.mcp.CallTool(ctx, endpoint, prepared.accessToken,
			authorizationScheme(impl), requestTimeout(impl), params)
		rec.record(ctx, result, err)
		return result, err
	default:
		err := fmt.Errorf("exec: connector %s has an invalid implementation", def.Type)
		rec.record(ctx, nil, err)
		return nil, err
	}
}

func (e *Engine) connection(ctx context.Context, connectionID uuid.UUID) (store.Connection, connector.Definition, error) {
	row, err := e.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.Connection{}, connector.Definition{}, ErrConnectionNotFound
		}
		return store.Connection{}, connector.Definition{}, err
	}
	if row.Status == "reauth_required" {
		return store.Connection{}, connector.Definition{},
			fmt.Errorf("%w (current status %s)", tokens.ErrReauthRequired, row.Status)
	}
	if row.Status != "active" {
		return store.Connection{}, connector.Definition{},
			fmt.Errorf("%w (current status %s)", ErrConnectionInactive, row.Status)
	}
	def, ok := e.reg.Get(connector.Type(row.ConnectorType))
	if !ok {
		return store.Connection{}, connector.Definition{},
			fmt.Errorf("exec: unknown connector type %s", row.ConnectorType)
	}
	return row, def, nil
}

type preparedCall struct {
	config      map[string]any
	credential  map[string]any
	accessToken string
}

func (e *Engine) prepare(ctx context.Context, row store.Connection, def connector.Definition) (preparedCall, error) {
	resolved, err := e.cfg.Resolved(ctx, def.Type)
	if err != nil {
		return preparedCall{}, err
	}
	if err := validateResolvedConfig(def, resolved); err != nil {
		return preparedCall{}, err
	}
	method, err := findAuthMethod(def, row.AuthMethod)
	if err != nil {
		return preparedCall{}, err
	}
	if method.Type == connector.AuthNone {
		return preparedCall{config: resolved}, nil
	}

	if method.Type == connector.AuthAPIKey || method.Type == connector.AuthCustomCredential {
		plain, err := e.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(row.ID.String()))
		if err != nil {
			return preparedCall{}, err
		}
		fields, err := credential.UnmarshalFields(plain)
		if err != nil {
			return preparedCall{}, err
		}
		credentialFields := make(map[string]any, len(fields.Fields))
		for key, value := range fields.Fields {
			credentialFields[key] = value
		}
		accessToken := fields.Fields[method.CredentialFields[0].Key]
		if accessToken == "" {
			return preparedCall{}, fmt.Errorf("exec: connection %s has an empty credential", row.ID)
		}
		return preparedCall{
			config:      resolved,
			credential:  credentialFields,
			accessToken: accessToken,
		}, nil
	}

	accessToken, err := e.refresher.AccessToken(ctx, row.ID)
	if err != nil {
		return preparedCall{}, err
	}
	if accessToken == "" {
		return preparedCall{}, fmt.Errorf("exec: connection %s has an empty credential", row.ID)
	}
	return preparedCall{
		config:      resolved,
		accessToken: accessToken,
	}, nil
}

func requestTimeout(remote connector.RemoteMCP) time.Duration {
	if remote.RequestTimeout > 0 {
		return remote.RequestTimeout
	}
	return defaultMCPTimeout
}

func authorizationScheme(remote connector.RemoteMCP) string {
	if remote.AuthorizationScheme != "" {
		return remote.AuthorizationScheme
	}
	return "Bearer"
}

func remoteEndpoint(remote connector.RemoteMCP, config map[string]any) (string, error) {
	if remote.EndpointSelector == nil {
		return remote.Endpoint, nil
	}
	field := remote.EndpointSelector.ConfigField
	option, _ := config[field].(string)
	endpoint, ok := remote.EndpointSelector.Endpoints[option]
	if !ok {
		return "", fmt.Errorf("exec: invalid remote MCP endpoint option %q", option)
	}
	return endpoint, nil
}

func findManagedTool(managed connector.Managed, name string) *connector.ManagedTool {
	for i := range managed.Tools {
		if managed.Tools[i].Tool.Name == name {
			return &managed.Tools[i]
		}
	}
	return nil
}

func validateManagedArguments(schema any, arguments json.RawMessage) (json.RawMessage, error) {
	var inputSchema *jsonschema.Schema
	if typed, ok := schema.(*jsonschema.Schema); ok {
		inputSchema = typed
	} else {
		data, err := json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("InputSchema is not valid JSON: %w", err)
		}
		inputSchema = new(jsonschema.Schema)
		if err := json.Unmarshal(data, inputSchema); err != nil {
			return nil, fmt.Errorf("InputSchema could not be parsed: %w", err)
		}
	}
	resolved, err := inputSchema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return nil, fmt.Errorf("InputSchema could not be parsed: %w", err)
	}

	value := make(map[string]any)
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &value); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
		if value == nil {
			return nil, fmt.Errorf("arguments must be a JSON object")
		}
	}
	if err := resolved.ApplyDefaults(&value); err != nil {
		return nil, fmt.Errorf("apply argument defaults: %w", err)
	}
	if err := resolved.Validate(&value); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal arguments: %w", err)
	}
	return normalized, nil
}

func invalidArgumentsResult(err error) *mcp.CallToolResult {
	message := "arguments do not conform to InputSchema: " + err.Error()
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: map[string]any{"error": "invalid_arguments", "message": message},
	}
}

func validateResolvedConfig(def connector.Definition, resolved map[string]any) error {
	for _, field := range def.ConfigFields {
		if !field.Required {
			continue
		}
		value, _ := resolved[field.Key].(string)
		if value == "" {
			return fmt.Errorf("exec: connector %s is missing required config %q", def.Type, field.Key)
		}
	}
	return nil
}

func findAuthMethod(def connector.Definition, key string) (connector.AuthMethod, error) {
	for _, method := range def.AuthMethods {
		if method.Key == key {
			return method, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf("exec: unknown auth method %s", key)
}

// recorder writes metadata-only audit rows for attempted tool calls.
type recorder struct {
	engine        *Engine
	started       time.Time
	connectorType string
	connectionID  uuid.UUID
	sessionID     uuid.UUID
	apiTokenID    uuid.UUID
	toolName      string
}

func (r *recorder) record(ctx context.Context, result *mcp.CallToolResult, callErr error) {
	runStatus := "ok"
	var errorKind *string
	var upstreamStatus *int32
	switch {
	case callErr != nil:
		runStatus = "error"
		kind, statusCode := classifyCallError(callErr)
		errorKind = &kind
		upstreamStatus = statusCode
	case result != nil && result.IsError:
		runStatus = "error"
		kind := classifyToolError(result)
		errorKind = &kind
	}

	duration := int32(time.Since(r.started).Milliseconds())
	connectionID := r.connectionID
	var sessionID *uuid.UUID
	if r.sessionID != uuid.Nil {
		sessionID = &r.sessionID
	}
	var apiTokenID *uuid.UUID
	if r.apiTokenID != uuid.Nil {
		apiTokenID = &r.apiTokenID
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()
	_ = r.engine.q.InsertToolRun(auditCtx, store.InsertToolRunParams{
		ID:             uuid.New(),
		ConnectorType:  r.connectorType,
		ConnectionID:   &connectionID,
		ToolID:         r.toolName,
		SessionID:      sessionID,
		ApiTokenID:     apiTokenID,
		Status:         runStatus,
		ErrorKind:      errorKind,
		UpstreamStatus: upstreamStatus,
		DurationMs:     &duration,
	})
}

type upstreamStatusError interface {
	error
	UpstreamStatusCode() int
}

func classifyCallError(err error) (string, *int32) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return errorKindTimeout, nil
	case errors.Is(err, context.Canceled):
		return errorKindCanceled, nil
	case errors.Is(err, ErrToolUnavailable):
		return errorKindToolUnavailable, nil
	case errors.Is(err, tokens.ErrReauthRequired):
		return errorKindAuth, nil
	}
	var upstreamErr upstreamStatusError
	if errors.As(err, &upstreamErr) {
		statusCode := upstreamErr.UpstreamStatusCode()
		var storedStatus *int32
		if statusCode >= 100 && statusCode <= 599 {
			value := int32(statusCode)
			storedStatus = &value
		}
		switch {
		case statusCode == 401 || statusCode == 403:
			return errorKindAuth, storedStatus
		case statusCode >= 400 && statusCode < 500:
			return errorKindUpstream4xx, storedStatus
		case statusCode >= 500:
			return errorKindUpstream5xx, storedStatus
		default:
			return errorKindTransport, storedStatus
		}
	}
	var transportErr *url.Error
	if errors.As(err, &transportErr) {
		return errorKindTransport, nil
	}
	return errorKindInternal, nil
}

func classifyToolError(result *mcp.CallToolResult) string {
	if fields, ok := result.StructuredContent.(map[string]any); ok &&
		fields["error"] == "invalid_arguments" {
		return errorKindInvalidArgs
	}
	return errorKindToolError
}
