package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestProjectAuditInputIsMetadataOnlyAndBounded(t *testing.T) {
	const (
		argumentSecret = "raw-argument-secret"
		fileNameSecret = "private-customer-file.pdf"
		dynamicKey     = "dynamic-secret-map-key"
	)
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"query":{"type":"string"},
			"payload":{"type":"object","additionalProperties":true}
		},
		"additionalProperties":true
	}`)
	raw := json.RawMessage(`{
		"query":"raw-argument-secret",
		"payload":{"private-customer-file.pdf":"raw-argument-secret"},
		"dynamic-secret-map-key":"private-customer-file.pdf"
	}`)
	projected := projectAuditInput(raw, schema)
	if len(projected) > maxAuditInputMetadataBytes {
		t.Fatalf("input metadata bytes = %d", len(projected))
	}
	for _, forbidden := range []string{
		argumentSecret,
		fileNameSecret,
		dynamicKey,
	} {
		if strings.Contains(string(projected), forbidden) {
			t.Fatalf("input metadata leaked %q: %s", forbidden, projected)
		}
	}
	var metadata auditInputMetadata
	if err := json.Unmarshal(projected, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.RawBytes != len(raw) ||
		metadata.DeclaredTopLevelFieldCount != 2 ||
		!reflect.DeepEqual(
			metadata.DeclaredTopLevelFields,
			[]string{"payload", "query"},
		) {
		t.Fatalf("input metadata = %+v", metadata)
	}

	const fieldCount = 256
	properties := make(map[string]any, fieldCount)
	arguments := make(map[string]any, fieldCount+1)
	for index := 0; index < fieldCount; index++ {
		name := fmt.Sprintf(
			"declared_field_%03d_%s",
			index,
			strings.Repeat("x", 64),
		)
		properties[name] = map[string]any{"type": "string"}
		arguments[name] = argumentSecret
	}
	arguments[dynamicKey] = fileNameSecret
	largeSchema, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	largeInput, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	projected = projectAuditInput(largeInput, largeSchema)
	if len(projected) > maxAuditInputMetadataBytes {
		t.Fatalf("bounded input metadata bytes = %d", len(projected))
	}
	if err := json.Unmarshal(projected, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.DeclaredTopLevelFieldCount != fieldCount ||
		!metadata.FieldNamesTruncated ||
		len(metadata.DeclaredTopLevelFields) >= fieldCount {
		t.Fatalf("bounded input metadata = %+v", metadata)
	}
	if strings.Contains(string(projected), dynamicKey) ||
		strings.Contains(string(projected), argumentSecret) ||
		strings.Contains(string(projected), fileNameSecret) {
		t.Fatalf("bounded input metadata leaked dynamic content: %s", projected)
	}
}

func TestProjectAuditOutputAndErrorExcludeResultContent(t *testing.T) {
	const (
		textSecret       = "raw-text-secret"
		structuredSecret = "raw-structured-secret"
		messageSecret    = "raw-provider-body-secret"
	)
	result := connector.ToolResultData{
		Text:       textSecret,
		Structured: json.RawMessage(`{"secret":"raw-structured-secret"}`),
		Failure: &connector.ToolFailure{
			Code:    connector.FailureTimeout,
			Message: messageSecret,
		},
	}
	failure := classifyAuditFailure(
		context.Background(),
		result,
		nil,
		auditBackendManaged,
		true,
	)
	summary := projectAuditOutput(result, failure)
	errText, _, _ := projectAuditFailure(failure)
	if errText == nil {
		t.Fatal("failed result must project a stable error")
	}
	for _, forbidden := range []string{
		textSecret,
		structuredSecret,
		messageSecret,
	} {
		if strings.Contains(summary, forbidden) ||
			strings.Contains(*errText, forbidden) {
			t.Fatalf("output projection leaked %q: %s / %s", forbidden, summary, *errText)
		}
	}

	var output auditOutputMetadata
	if err := json.Unmarshal([]byte(summary), &output); err != nil {
		t.Fatal(err)
	}
	if output.Outcome != "failure" ||
		output.FailureCode != string(connector.FailureTimeout) ||
		output.TextBytes != len(textSecret) ||
		output.StructuredBytes != len(result.Structured) ||
		!reflect.DeepEqual(output.ResultTypes, []string{"text", "structured"}) {
		t.Fatalf("output metadata = %+v", output)
	}
	var projectedError auditErrorMetadata
	if err := json.Unmarshal([]byte(*errText), &projectedError); err != nil {
		t.Fatal(err)
	}
	if projectedError.Code != string(connector.FailureTimeout) ||
		projectedError.Message != "provider request timed out" {
		t.Fatalf("error metadata = %+v", projectedError)
	}
}

func TestProjectAuditFailureColumnsAreNormalized(t *testing.T) {
	_, code, status := projectAuditFailure(&auditFailure{
		Code:           connector.FailureRateLimited,
		UpstreamStatus: 429,
	})
	if code == nil ||
		*code != string(connector.FailureRateLimited) ||
		status == nil ||
		*status != 429 {
		t.Fatalf("projected columns = %v/%v", code, status)
	}

	_, code, status = projectAuditFailure(&auditFailure{
		Code:           "raw-secret-invalid-code",
		UpstreamStatus: 999,
	})
	if code == nil ||
		*code != string(connector.FailureInternalError) ||
		status != nil {
		t.Fatalf("normalized columns = %v/%v", code, status)
	}

	errText, code, status := projectAuditFailure(nil)
	if errText != nil || code != nil || status != nil {
		t.Fatalf("success columns = %v/%v/%v, want all nil", errText, code, status)
	}
}

type codedAuditTestError struct {
	code   connector.FailureCode
	status int
}

func (e *codedAuditTestError) Error() string {
	return "typed-error-raw-secret"
}

func (e *codedAuditTestError) Code() connector.FailureCode {
	return e.code
}

func (e *codedAuditTestError) UpstreamStatus() int {
	return e.status
}

func TestClassifyAuditFailureIsStable(t *testing.T) {
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name           string
		ctx            context.Context
		result         connector.ToolResultData
		err            error
		backend        auditBackendKind
		backendTouched bool
		wantCode       connector.FailureCode
		wantStatus     int
		impactsHealth  bool
	}{
		{
			name:           "caller canceled wins",
			ctx:            canceledContext,
			err:            errors.New("raw-secret-after-cancel"),
			backend:        auditBackendRemote,
			backendTouched: true,
			wantCode:       connector.FailureCanceled,
		},
		{
			name:           "wrapped canceled",
			ctx:            context.Background(),
			err:            fmt.Errorf("raw-secret: %w", context.Canceled),
			backend:        auditBackendRemote,
			backendTouched: true,
			wantCode:       connector.FailureCanceled,
		},
		{
			name:           "provider timeout",
			ctx:            context.Background(),
			err:            fmt.Errorf("raw-secret: %w", context.DeadlineExceeded),
			backend:        auditBackendRemote,
			backendTouched: true,
			wantCode:       connector.FailureTimeout,
			impactsHealth:  true,
		},
		{
			name:           "typed provider failure",
			ctx:            context.Background(),
			err:            &codedAuditTestError{code: connector.FailureProviderError, status: 503},
			backend:        auditBackendManaged,
			backendTouched: true,
			wantCode:       connector.FailureProviderError,
			wantStatus:     503,
			impactsHealth:  true,
		},
		{
			name:           "provider HTTP timeout is health neutral",
			ctx:            context.Background(),
			err:            &codedAuditTestError{code: connector.FailureTimeout, status: 408},
			backend:        auditBackendManaged,
			backendTouched: true,
			wantCode:       connector.FailureTimeout,
			wantStatus:     408,
		},
		{
			name: "tool failure",
			ctx:  context.Background(),
			result: connector.ToolResultData{Failure: &connector.ToolFailure{
				Code:           connector.FailurePermissionDenied,
				Message:        "raw-provider-body-secret",
				UpstreamStatus: 403,
			}},
			backend:        auditBackendManaged,
			backendTouched: true,
			wantCode:       connector.FailurePermissionDenied,
			wantStatus:     403,
		},
		{
			name: "invalid tool failure code",
			ctx:  context.Background(),
			result: connector.ToolResultData{Failure: &connector.ToolFailure{
				Code:    "raw-secret-code",
				Message: "raw-provider-body-secret",
			}},
			backend:        auditBackendManaged,
			backendTouched: true,
			wantCode:       connector.FailureInternalError,
		},
		{
			name:           "remote transport",
			ctx:            context.Background(),
			err:            errors.New("raw-provider-body-secret"),
			backend:        auditBackendRemote,
			backendTouched: true,
			wantCode:       connector.FailureUpstreamUnavailable,
			impactsHealth:  true,
		},
		{
			name:           "unknown managed error",
			ctx:            context.Background(),
			err:            errors.New("raw-provider-body-secret"),
			backend:        auditBackendManaged,
			backendTouched: true,
			wantCode:       connector.FailureInternalError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAuditFailure(
				tc.ctx,
				tc.result,
				tc.err,
				tc.backend,
				tc.backendTouched,
			)
			if got == nil ||
				got.Code != tc.wantCode ||
				got.UpstreamStatus != tc.wantStatus {
				t.Fatalf("failure = %+v, want code=%q status=%d", got, tc.wantCode, tc.wantStatus)
			}
			if auditFailureImpactsHealth(got) != tc.impactsHealth {
				t.Fatalf(
					"health impact = %v, want %v",
					auditFailureImpactsHealth(got),
					tc.impactsHealth,
				)
			}
			projected, _, _ := projectAuditFailure(got)
			if projected == nil ||
				strings.Contains(*projected, "raw-secret") ||
				strings.Contains(*projected, "raw-provider") {
				t.Fatalf("unsafe error projection = %v", projected)
			}
		})
	}
}
