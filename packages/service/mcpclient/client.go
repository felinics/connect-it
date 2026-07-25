// Package mcpclient invokes upstream MCP servers through the official Go SDK
// and the platform-wide providerkit egress boundary.
//
// Every CallTool/ListTools operation creates and closes one MCP session. The
// guarded HTTP client is policy-bound to the reviewed server metadata supplied
// by a connector Definition; callers cannot supply a looser network policy.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	defaultRequestTimeout = 30 * time.Second
	maxConcurrentSessions = 32

	// OperationVerify is the only configuration-time Remote MCP operation.
	// Keeping it fixed prevents unbounded/high-cardinality observer labels.
	OperationVerify = "mcp_verify"
)

var (
	errInvalidEndpoint = errors.New("mcpclient: MCP endpoint 配置无效")
	errInvalidRequest  = errors.New("mcpclient: MCP 请求配置无效")
	errRequestTimeout  = errors.New("mcpclient: request timeout")
)

// CallRequest contains the runtime identity needed to derive both the egress
// policy and low-cardinality request labels. Server must be copied directly
// from the registered connector Definition.
type CallRequest struct {
	ConnectorType     connector.Type
	ConnectionID      uuid.UUID
	ToolID            string
	Server            connector.RemoteMCPServer
	Endpoint          string
	BearerToken       string
	AllowInsecureHTTP string
	RemoteToolName    string
	Arguments         json.RawMessage
}

// ListRequest contains the configuration-time identity used by mcp:verify.
// Operation must be OperationVerify.
type ListRequest struct {
	ConnectorType     connector.Type
	Operation         string
	AuthorizationID   string
	Server            connector.RemoteMCPServer
	Endpoint          string
	BearerToken       string
	AllowInsecureHTTP string
}

// Client is a concurrency-bounded Remote MCP runtime. A zero-value Client is
// fail-closed; production code must construct it with New.
type Client struct {
	factory *providerkit.Factory
	slots   chan struct{}
}

// New binds Remote MCP to the process-wide providerkit Factory. The same
// Client should be injected into execution and mcp:verify.
func New(factory *providerkit.Factory) (*Client, error) {
	if factory == nil {
		return nil, errInvalidRequest
	}
	return &Client{
		factory: factory,
		slots:   make(chan struct{}, maxConcurrentSessions),
	}, nil
}

// FailureError is the safe error boundary for MCP transport/protocol failures.
// Error exposes only the allowlisted failure message. Unwrap retains a typed,
// already-sanitized providerkit/context cause when one exists.
type FailureError struct {
	failure connector.ToolFailure
	cause   error
}

func (e *FailureError) Error() string {
	if e == nil {
		return connector.DefaultFailureMessage(connector.FailureInternalError)
	}
	return e.failure.Message
}

func (e *FailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ToolFailure returns a defensive public projection for errors.As users.
func (e *FailureError) ToolFailure() *connector.ToolFailure {
	if e == nil {
		fallback := connector.NormalizeToolFailure(nil)
		return &fallback
	}
	copy := e.failure
	return &copy
}

// CallTool performs one MCP handshake and tool call. Successful TextContent
// and StructuredContent retain the legacy output shape.
func (client *Client) CallTool(
	ctx context.Context,
	request CallRequest,
) (connector.ToolResultData, error) {
	if ctx == nil {
		return connector.ToolResultData{}, failureError(
			connector.FailureInvalidInput,
			0,
			nil,
			false,
		)
	}
	if request.ConnectionID == uuid.Nil ||
		request.ToolID == "" ||
		request.RemoteToolName == "" ||
		request.Server.RequestTimeout < 0 {
		return connector.ToolResultData{}, failureError(
			connector.FailureConfigurationError,
			0,
			nil,
			false,
		)
	}

	operation, err := client.openSession(
		ctx,
		request.ConnectorType,
		request.Server,
		request.Endpoint,
		request.BearerToken,
		request.AllowInsecureHTTP,
		providerkit.RequestLabels{
			ConnectorType: string(request.ConnectorType),
			ToolID:        request.ToolID,
			ConnectionID:  request.ConnectionID.String(),
		},
	)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	defer operation.close()

	response, err := operation.session.CallTool(
		operation.context,
		&mcp.CallToolParams{
			Name:      request.RemoteToolName,
			Arguments: request.Arguments,
		},
	)
	if err != nil {
		return connector.ToolResultData{}, classifyError(
			operation.context,
			err,
			operation.statuses.Last(),
			operation.hadCredential,
		)
	}
	if response == nil {
		return connector.ToolResultData{}, failureError(
			connector.FailureInvalidResponse,
			0,
			nil,
			false,
		)
	}
	if response.IsError {
		failure, failureErr := connector.NewToolFailure(
			connector.FailureProviderError,
			connector.DefaultFailureMessage(
				connector.FailureProviderError,
			),
			0,
			0,
		)
		if failureErr != nil {
			fallback := connector.NormalizeToolFailure(nil)
			failure = &fallback
		}
		// An MCP IsError payload is untrusted Provider error material. Drop all
		// Text/Structured content instead of reflecting it across the Tool
		// boundary; only the stable ToolFailure is public.
		return connector.ToolResultData{Failure: failure}, nil
	}

	texts := make([]string, 0, len(response.Content))
	for _, content := range response.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	result := connector.ToolResultData{
		Text: strings.Join(texts, "\n"),
	}
	if response.StructuredContent != nil {
		structured, marshalErr := json.Marshal(response.StructuredContent)
		if marshalErr != nil {
			return connector.ToolResultData{}, failureError(
				connector.FailureInvalidResponse,
				0,
				nil,
				false,
			)
		}
		result.Structured = structured
	}
	return result, nil
}

// ToolDefinition 是上游 tool 的完整声明快照。
//
// 新增或迁移一个 Remote MCP Provider 时，我们的 Definition 必须逐字复现上游的
// 参数契约——Remote backend 是参数直通，InputSchema 与上游不一致就会在运行时
// 失败。厂商文档普遍只描述能力、不列 tool 名与参数，所以唯一可靠的来源就是对
// 真实 endpoint 调一次 tools/list。
type ToolDefinition struct {
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
}

// ListTools performs one MCP handshake and iterates every tools/list page.
func (client *Client) ListTools(
	ctx context.Context,
	request ListRequest,
) ([]string, error) {
	definitions, err := client.ListToolDefinitions(ctx, request)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names, nil
}

// ListToolDefinitions 与 ListTools 走同一条受控管道，但保留每个 tool 的完整
// 声明（含 InputSchema），供 Provider 作者把上游契约抄进 Definition。
func (client *Client) ListToolDefinitions(
	ctx context.Context,
	request ListRequest,
) ([]ToolDefinition, error) {
	if ctx == nil {
		return nil, failureError(
			connector.FailureInvalidInput,
			0,
			nil,
			false,
		)
	}
	if request.Operation != OperationVerify ||
		request.AuthorizationID == "" ||
		request.Server.RequestTimeout < 0 {
		return nil, failureError(
			connector.FailureConfigurationError,
			0,
			nil,
			false,
		)
	}

	operation, err := client.openSession(
		ctx,
		request.ConnectorType,
		request.Server,
		request.Endpoint,
		request.BearerToken,
		request.AllowInsecureHTTP,
		providerkit.RequestLabels{
			ConnectorType:   string(request.ConnectorType),
			Operation:       request.Operation,
			AuthorizationID: request.AuthorizationID,
		},
	)
	if err != nil {
		return nil, err
	}
	defer operation.close()

	var definitions []ToolDefinition
	for tool, listErr := range operation.session.Tools(operation.context, nil) {
		if listErr != nil {
			return nil, classifyError(
				operation.context,
				listErr,
				operation.statuses.Last(),
				operation.hadCredential,
			)
		}
		if tool == nil {
			return nil, failureError(
				connector.FailureInvalidResponse,
				0,
				nil,
				false,
			)
		}
		definition := ToolDefinition{
			Name:        tool.Name,
			Description: tool.Description,
		}
		// 上游 schema 只做透传快照：这里不校验也不改写，交由抄写者用
		// registry 的 linter 判断能否直接进 Definition。
		if tool.InputSchema != nil {
			encoded, marshalErr := json.Marshal(tool.InputSchema)
			if marshalErr != nil {
				return nil, failureError(
					connector.FailureInvalidResponse,
					0,
					nil,
					false,
				)
			}
			definition.InputSchema = encoded
		}
		if tool.OutputSchema != nil {
			encoded, marshalErr := json.Marshal(tool.OutputSchema)
			if marshalErr != nil {
				return nil, failureError(
					connector.FailureInvalidResponse,
					0,
					nil,
					false,
				)
			}
			definition.OutputSchema = encoded
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

type remoteOperation struct {
	context       context.Context
	cancel        context.CancelFunc
	release       func()
	policyClient  *providerkit.Client
	session       *mcp.ClientSession
	statuses      *statusRoundTripper
	hadCredential bool
}

func (client *Client) openSession(
	ctx context.Context,
	connectorType connector.Type,
	server connector.RemoteMCPServer,
	endpoint string,
	bearerToken string,
	allowInsecureHTTP string,
	labels providerkit.RequestLabels,
) (*remoteOperation, error) {
	operation := &remoteOperation{hadCredential: bearerToken != ""}
	operation.context, operation.cancel = withTimeout(
		ctx,
		server.RequestTimeout,
	)
	fail := func(err error, status int) (*remoteOperation, error) {
		classified := classifyError(
			operation.context,
			err,
			status,
			operation.hadCredential,
		)
		operation.close()
		return nil, classified
	}

	var err error
	operation.release, err = client.acquire(operation.context)
	if err != nil {
		return fail(err, 0)
	}
	operation.policyClient, err = client.policyClient(
		connectorType,
		server,
		endpoint,
		allowInsecureHTTP,
	)
	if err != nil {
		return fail(err, 0)
	}
	authorizer := providerkit.NoAuth()
	if bearerToken != "" {
		authorizer, err = providerkit.Bearer(bearerToken)
		if err != nil {
			return fail(err, 0)
		}
	}
	httpClient, err := operation.policyClient.HTTPClient(authorizer, labels)
	if err != nil {
		return fail(err, 0)
	}
	operation.statuses = wrapStatusRecorder(httpClient, operation.context)
	mcpClient := mcp.NewClient(
		&mcp.Implementation{Name: "connect-it", Version: "0.1.0"},
		nil,
	)
	operation.session, err = mcpClient.Connect(
		operation.context,
		&mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			HTTPClient:           httpClient,
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		},
		nil,
	)
	if err != nil {
		return fail(err, operation.statuses.Last())
	}
	return operation, nil
}

func (operation *remoteOperation) close() {
	if operation == nil {
		return
	}
	// Cleanup failures do not replace a completed MCP result. providerkit
	// still bounds and observes the session DELETE request.
	if operation.session != nil {
		_ = operation.session.Close()
	}
	if operation.policyClient != nil {
		operation.policyClient.CloseIdleConnections()
	}
	if operation.release != nil {
		operation.release()
	}
	if operation.cancel != nil {
		operation.cancel()
	}
}

// CheckEndpoint performs only fail-closed configuration-shape validation. It
// never establishes a connection; the actual call still passes through the
// providerkit policy and deployment gates.
func CheckEndpoint(endpoint string, allowInsecure bool) error {
	parsed, err := providerkit.ParseAndValidateURL(endpoint)
	if err != nil ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" ||
		parsed.RawFragment != "" {
		return errInvalidEndpoint
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure {
			return nil
		}
	}
	return errInvalidEndpoint
}

func (client *Client) acquire(ctx context.Context) (func(), error) {
	if client == nil || client.factory == nil || client.slots == nil {
		return nil, errInvalidRequest
	}
	select {
	case client.slots <- struct{}{}:
		return func() { <-client.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (client *Client) policyClient(
	connectorType connector.Type,
	server connector.RemoteMCPServer,
	endpoint string,
	allowInsecureHTTP string,
) (*providerkit.Client, error) {
	if client == nil || client.factory == nil ||
		connectorType == "" ||
		server.Key == "" ||
		endpoint == "" ||
		server.RequestTimeout < 0 {
		return nil, errInvalidRequest
	}
	timeout := effectiveTimeout(server.RequestTimeout)
	retry := providerkit.RetryPolicy{Disabled: true}

	switch server.Endpoint.Source {
	case connector.EndpointFixed:
		if (server.Provenance.Kind != connector.ProvenanceOfficial &&
			server.Provenance.Kind != connector.ProvenanceThirdParty) ||
			server.Endpoint.URL == "" ||
			endpoint != server.Endpoint.URL ||
			(allowInsecureHTTP != "" && allowInsecureHTTP != "false") {
			return nil, errInvalidRequest
		}
		parsed, err := providerkit.ParseAndValidateURL(server.Endpoint.URL)
		if err != nil || parsed.Scheme != "https" ||
			!reviewedHostname(parsed, server.Provenance.AllowedHostnames) {
			return nil, errInvalidRequest
		}
		origin, err := providerkit.CanonicalOrigin(parsed)
		if err != nil {
			return nil, errInvalidRequest
		}
		return client.factory.NewStaticClient(providerkit.Policy{
			Provider:         string(connectorType),
			BaseURL:          server.Endpoint.URL,
			AllowedOrigins:   []string{origin},
			RedirectMode:     providerkit.RedirectFollowPolicy,
			NetworkMode:      providerkit.PublicOnly,
			RequestTimeout:   timeout,
			MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
			Retry:            retry,
		})

	case connector.EndpointConfigField:
		if server.Provenance.Kind != connector.ProvenanceSelfHosted ||
			server.Endpoint.ConfigFieldKey == "" ||
			server.Endpoint.URL != "" {
			return nil, errInvalidRequest
		}
		if allowInsecureHTTP == "" {
			allowInsecureHTTP = "false"
		}
		return client.factory.NewDynamicClient(providerkit.DynamicPolicyInput{
			Provider:          string(connectorType),
			BaseURL:           endpoint,
			AllowInsecureHTTP: allowInsecureHTTP,
			RedirectMode:      providerkit.RedirectFollowPolicy,
			RequestTimeout:    timeout,
			MaxResponseBytes:  providerkit.DefaultMaxResponseBytes,
			Retry:             retry,
		})
	default:
		return nil, errInvalidRequest
	}
}

func reviewedHostname(parsed *url.URL, allowed []string) bool {
	if parsed == nil || len(allowed) == 0 {
		return false
	}
	host := parsed.Hostname()
	for _, candidate := range allowed {
		if candidate == strings.ToLower(candidate) &&
			candidate == host &&
			!strings.ContainsAny(candidate, "/:@?#") {
			return true
		}
	}
	return false
}

func withTimeout(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(
		ctx,
		effectiveTimeout(timeout),
		errRequestTimeout,
	)
}

func effectiveTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultRequestTimeout
	}
	return timeout
}

type statusRoundTripper struct {
	base       http.RoundTripper
	operation  context.Context
	lastStatus atomic.Int32
}

func wrapStatusRecorder(
	client *http.Client,
	operation context.Context,
) *statusRoundTripper {
	recorder := &statusRoundTripper{
		base:      client.Transport,
		operation: operation,
	}
	client.Transport = recorder
	return recorder
}

func (recorder *statusRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	if recorder == nil || recorder.base == nil ||
		recorder.operation == nil || request == nil {
		return nil, errInvalidRequest
	}

	requestContext, cancel := context.WithCancel(recorder.operation)
	stopRequestCancellation := context.AfterFunc(
		request.Context(),
		cancel,
	)
	outbound := request.Clone(requestContext)
	response, err := recorder.base.RoundTrip(outbound)
	if err != nil {
		stopRequestCancellation()
		cancel()
		recorder.lastStatus.Store(0)
		return response, err
	}
	if response != nil {
		recorder.lastStatus.Store(int32(response.StatusCode))
		if response.Body != nil {
			response.Body = &operationBody{
				body:   response.Body,
				stop:   stopRequestCancellation,
				cancel: cancel,
			}
		} else {
			stopRequestCancellation()
			cancel()
		}
	} else {
		stopRequestCancellation()
		cancel()
	}
	return response, nil
}

func (recorder *statusRoundTripper) CloseIdleConnections() {
	if closer, ok := recorder.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (recorder *statusRoundTripper) Last() int {
	if recorder == nil {
		return 0
	}
	return int(recorder.lastStatus.Load())
}

type operationBody struct {
	body   io.ReadCloser
	stop   func() bool
	cancel context.CancelFunc
	once   sync.Once
}

func (body *operationBody) Read(buffer []byte) (int, error) {
	count, err := body.body.Read(buffer)
	if err != nil {
		body.finish()
	}
	return count, err
}

func (body *operationBody) Close() error {
	err := body.body.Close()
	body.finish()
	return err
}

func (body *operationBody) finish() {
	if body == nil {
		return
	}
	body.once.Do(func() {
		if body.stop != nil {
			body.stop()
		}
		if body.cancel != nil {
			body.cancel()
		}
	})
}

func classifyError(
	ctx context.Context,
	err error,
	status int,
	hadCredential bool,
) error {
	if err == nil {
		return nil
	}

	if ctx != nil {
		if errors.Is(context.Cause(ctx), errRequestTimeout) {
			return failureError(
				connector.FailureTimeout,
				0,
				context.DeadlineExceeded,
				false,
			)
		}
		if ctx.Err() != nil {
			return failureError(
				connector.FailureCanceled,
				0,
				ctx.Err(),
				false,
			)
		}
	}

	var providerError *providerkit.Error
	if errors.As(err, &providerError) && providerError != nil {
		failure := providerkit.AsToolFailure(providerError)
		if hadCredential &&
			failure.Code == connector.FailureAuthorizationFailed &&
			failure.UpstreamStatus == http.StatusUnauthorized {
			return failureError(
				connector.FailureAuthorizationFailed,
				http.StatusUnauthorized,
				providerError,
				true,
			)
		}
		return &FailureError{
			failure: connector.NormalizeToolFailure(failure),
			cause:   providerError,
		}
	}

	// An unclassifiable status means the failure was not an HTTP one the
	// Provider produced; treat it as an invalid protocol response.
	code := connector.FailureCodeForStatus(status)
	if code == "" {
		code = connector.FailureInvalidResponse
	}
	credentialInvalid := hadCredential && status == http.StatusUnauthorized
	return failureError(code, status, nil, credentialInvalid)
}

func failureError(
	code connector.FailureCode,
	status int,
	cause error,
	credentialInvalid bool,
) *FailureError {
	message := connector.DefaultFailureMessage(code)
	var (
		failure *connector.ToolFailure
		err     error
	)
	if credentialInvalid {
		failure, err = connector.NewCredentialInvalidFailure(message, status)
	} else {
		failure, err = connector.NewToolFailure(code, message, status, 0)
	}
	if err != nil {
		fallback := connector.NormalizeToolFailure(nil)
		failure = &fallback
		cause = nil
	}
	return &FailureError{failure: *failure, cause: cause}
}
