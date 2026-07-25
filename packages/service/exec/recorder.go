package exec

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

const (
	auditWriteTimeout          = 2 * time.Second
	maxAuditInputMetadataBytes = 4 << 10
)

type auditBackendKind uint8

const (
	auditBackendUnknown auditBackendKind = iota
	auditBackendManaged
	auditBackendRemote
)

// auditFailure 是把任意执行失败折叠成的稳定投影：只有失败码和上游状态码，
// 从不携带 Provider body 或调用方参数。
type auditFailure struct {
	Code           connector.FailureCode
	UpstreamStatus int
}

type auditInputMetadata struct {
	RawBytes                   int      `json:"raw_bytes"`
	DeclaredTopLevelFields     []string `json:"declared_top_level_fields"`
	DeclaredTopLevelFieldCount int      `json:"declared_top_level_field_count"`
	FieldNamesTruncated        bool     `json:"field_names_truncated,omitempty"`
}

type auditOutputMetadata struct {
	ResultTypes     []string `json:"result_types"`
	TextBytes       int      `json:"text_bytes"`
	StructuredBytes int      `json:"structured_bytes"`
	Outcome         string   `json:"outcome"`
	FailureCode     string   `json:"failure_code,omitempty"`
}

type auditErrorMetadata struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// recorder writes only bounded, content-free metadata to tool_runs and, when
// the backend was reached, the stable health projection.
type recorder struct {
	engine        *Engine
	started       time.Time
	connectorType string
	connectionID  uuid.UUID
	sessionID     uuid.UUID
	toolID        string
	input         json.RawMessage
	inputSchema   json.RawMessage
	backend       auditBackendKind
}

func (r *recorder) record(
	ctx context.Context,
	result connector.ToolResultData,
	execErr error,
	backendTouched bool,
) {
	if r.engine.q == nil {
		return
	}

	failure := classifyAuditFailure(
		ctx,
		result,
		execErr,
		r.backend,
		backendTouched,
	)
	status := "ok"
	if failure != nil {
		status = "error"
	}
	errText, errorCode, upstreamStatus := projectAuditFailure(failure)
	input := projectAuditInput(r.input, r.inputSchema)
	summaryValue := projectAuditOutput(result, failure)
	duration := int32(time.Since(r.started).Milliseconds())
	connID := r.connectionID
	writeCtx, cancel := detachedAuditContext(ctx)
	defer cancel()
	if err := r.engine.q.InsertToolRun(writeCtx, store.InsertToolRunParams{
		ID:             uuid.New(),
		ConnectorType:  r.connectorType,
		ConnectionID:   &connID,
		ToolID:         r.toolID,
		SessionID:      &r.sessionID,
		Status:         status,
		Error:          errText,
		ErrorCode:      errorCode,
		UpstreamStatus: upstreamStatus,
		Input:          input,
		OutputSummary:  &summaryValue,
		DurationMs:     &duration,
	}); err != nil {
		// Audit persistence remains best-effort and must not replace the Tool
		// result with a secondary recorder failure.
		_ = err
	}

	if !backendTouched {
		return
	}
	if auditFailureImpactsHealth(failure) {
		_ = r.engine.q.UpsertConnectorHealthFailure(
			writeCtx,
			store.UpsertConnectorHealthFailureParams{
				ConnectorType: r.connectorType,
				LastError:     errText,
			},
		)
		return
	}
	if execErr != nil {
		// An unknown local/handler error does not establish either Provider
		// health or unhealthiness.
		return
	}
	// A successful result or a health-neutral typed Provider failure proves
	// that the backend was reachable.
	_ = r.engine.q.UpsertConnectorHealthSuccess(writeCtx, r.connectorType)
}

// projectAuditInput records only request shape known by the Tool's static
// schema. Values and input-only dynamic keys are deliberately never copied.
func projectAuditInput(
	raw json.RawMessage,
	inputSchema json.RawMessage,
) []byte {
	declared := declaredTopLevelProperties(inputSchema)
	var supplied map[string]json.RawMessage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &supplied)
	}

	fields := make([]string, 0, len(declared))
	for field := range declared {
		if _, present := supplied[field]; present {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)

	metadata := auditInputMetadata{
		RawBytes:                   len(raw),
		DeclaredTopLevelFields:     fields,
		DeclaredTopLevelFieldCount: len(fields),
	}
	encoded, _ := json.Marshal(metadata)
	if len(encoded) <= maxAuditInputMetadataBytes {
		return encoded
	}

	metadata.FieldNamesTruncated = true
	for len(metadata.DeclaredTopLevelFields) > 0 {
		metadata.DeclaredTopLevelFields =
			metadata.DeclaredTopLevelFields[:len(metadata.DeclaredTopLevelFields)-1]
		encoded, _ = json.Marshal(metadata)
		if len(encoded) <= maxAuditInputMetadataBytes {
			return encoded
		}
	}
	// Fixed field names plus integer counters are always comfortably below the
	// limit. Keep the defensive fallback metadata-only even if that changes.
	encoded, _ = json.Marshal(metadata)
	return encoded
}

func declaredTopLevelProperties(
	inputSchema json.RawMessage,
) map[string]struct{} {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if len(inputSchema) == 0 ||
		json.Unmarshal(inputSchema, &schema) != nil {
		return nil
	}
	declared := make(map[string]struct{}, len(schema.Properties))
	for field := range schema.Properties {
		declared[field] = struct{}{}
	}
	return declared
}

func projectAuditOutput(
	result connector.ToolResultData,
	failure *auditFailure,
) string {
	resultTypes := make([]string, 0, 2)
	if result.Text != "" {
		resultTypes = append(resultTypes, "text")
	}
	if len(result.Structured) > 0 {
		resultTypes = append(resultTypes, "structured")
	}
	if len(resultTypes) == 0 {
		resultTypes = append(resultTypes, "none")
	}
	metadata := auditOutputMetadata{
		ResultTypes:     resultTypes,
		TextBytes:       len(result.Text),
		StructuredBytes: len(result.Structured),
		Outcome:         "success",
	}
	if failure != nil {
		metadata.Outcome = "failure"
		metadata.FailureCode = string(normalizeAuditFailureCode(failure.Code))
	}
	encoded, _ := json.Marshal(metadata)
	return string(encoded)
}

// projectAuditFailure 生成 tool_runs 的三个失败列：安全 error 文本、稳定失败码
// 和已归一化的上游状态码。原始 error 文本从不落库。
func projectAuditFailure(failure *auditFailure) (
	errText *string,
	errorCode *string,
	upstreamStatus *int32,
) {
	if failure == nil {
		return nil, nil, nil
	}
	code := normalizeAuditFailureCode(failure.Code)
	encoded, _ := json.Marshal(auditErrorMetadata{
		Code:    string(code),
		Message: connector.DefaultFailureMessage(code),
	})
	text := string(encoded)
	name := string(code)
	if normalized := normalizeUpstreamStatus(failure.UpstreamStatus); normalized != 0 {
		value := int32(normalized)
		upstreamStatus = &value
	}
	return &text, &name, upstreamStatus
}

func classifyAuditFailure(
	ctx context.Context,
	result connector.ToolResultData,
	execErr error,
	backend auditBackendKind,
	backendTouched bool,
) *auditFailure {
	if execErr == nil {
		if result.Failure != nil {
			return &auditFailure{
				Code: normalizeAuditFailureCode(result.Failure.Code),
				UpstreamStatus: normalizeUpstreamStatus(
					result.Failure.UpstreamStatus,
				),
			}
		}
		return nil
	}

	if ctx != nil && ctx.Err() != nil {
		return &auditFailure{Code: connector.FailureCanceled}
	}

	var coded interface {
		Code() connector.FailureCode
	}
	if errors.As(execErr, &coded) {
		failure := &auditFailure{
			Code: normalizeAuditFailureCode(coded.Code()),
		}
		var withStatus interface {
			UpstreamStatus() int
		}
		if errors.As(execErr, &withStatus) {
			failure.UpstreamStatus = normalizeUpstreamStatus(
				withStatus.UpstreamStatus(),
			)
		}
		return failure
	}

	var validationError *connector.CredentialValidationError
	if errors.As(execErr, &validationError) && validationError != nil {
		return &auditFailure{
			Code: normalizeAuditFailureCode(validationError.Code),
			UpstreamStatus: normalizeUpstreamStatus(
				validationError.UpstreamStatus,
			),
		}
	}

	switch {
	case errors.Is(execErr, context.Canceled):
		return &auditFailure{Code: connector.FailureCanceled}
	case errors.Is(execErr, context.DeadlineExceeded):
		return &auditFailure{Code: connector.FailureTimeout}
	case errors.Is(execErr, ErrToolUnavailable):
		return &auditFailure{Code: connector.FailureToolUnavailable}
	case errors.Is(execErr, ErrInvalidToolResult):
		return &auditFailure{Code: connector.FailureInvalidResponse}
	case errors.Is(execErr, errCredentialState):
		return &auditFailure{Code: connector.FailureInternalError}
	case errors.Is(execErr, ErrConnectionNotFound),
		errors.Is(execErr, tokens.ErrNotFound):
		return &auditFailure{Code: connector.FailureNotFound}
	case errors.Is(execErr, tokens.ErrReauthRequired):
		return &auditFailure{Code: connector.FailureAuthorizationFailed}
	case backendTouched && backend == auditBackendRemote:
		return &auditFailure{Code: connector.FailureUpstreamUnavailable}
	default:
		return &auditFailure{Code: connector.FailureInternalError}
	}
}

func normalizeAuditFailureCode(
	code connector.FailureCode,
) connector.FailureCode {
	if code.Valid() {
		return code
	}
	return connector.FailureInternalError
}

func normalizeUpstreamStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func detachedAuditContext(ctx context.Context) (
	context.Context,
	context.CancelFunc,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
}

func auditFailureImpactsHealth(failure *auditFailure) bool {
	if failure == nil {
		return false
	}
	// A Provider-originated 4xx response proves reachability but represents a
	// caller, credential, permission, conflict, or rate-limit outcome. The
	// stable code must not accidentally override that HTTP fact (for example,
	// a Provider's HTTP 408 may be exposed as timeout while remaining
	// transport-health neutral).
	if failure.UpstreamStatus >= 400 && failure.UpstreamStatus <= 499 {
		return false
	}
	if failure.UpstreamStatus >= 500 {
		return true
	}
	switch failure.Code {
	case connector.FailureInvalidResponse,
		connector.FailureUpstreamUnavailable,
		connector.FailureTimeout:
		return true
	default:
		return false
	}
}
