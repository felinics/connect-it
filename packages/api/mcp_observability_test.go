package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/service/sessions"
)

type diagnosticExecutor struct {
	result *mcp.CallToolResult
	err    error
}

func (e diagnosticExecutor) CallTool(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, *mcp.CallToolParamsRaw) (*mcp.CallToolResult, error) {
	return e.result, e.err
}

func TestMCPFailureObservability(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		err        error
	}{
		{"internal", "internal", errors.New("private-error-text")},
		{"rpc", "invalid_args", &jsonrpc.Error{Code: -32602, Message: "private-error-text"}},
		{"nil_result", "internal", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			e := echo.New()
			e.Logger.SetOutput(&logs)
			host := &mcpHost{exec: diagnosticExecutor{err: tc.err}, logger: e.Logger}
			view := sessions.SessionView{ID: uuid.New(), Routes: map[string]sessions.ToolRoute{
				"github__read": {ConnectionID: uuid.New(), ToolName: "read"},
			}}
			result := host.callTool(t.Context(), view, &mcp.CallToolParamsRaw{Name: "github__read", Arguments: json.RawMessage(`{"secret":"private-arguments"}`)})
			var fields map[string]any
			encoded, _ := json.Marshal(result.StructuredContent)
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			runID, _ := fields["run_id"].(string)
			if _, err := uuid.Parse(runID); err != nil || fields["kind"] != tc.kind || !result.IsError {
				t.Fatalf("missing diagnostic fields: %s", encoded)
			}
			if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != string(encoded) {
				t.Fatalf("text and structured error differ: %+v", result)
			}
			if strings.Contains(logs.String(), "private-") || strings.Contains(string(encoded), "private-") {
				t.Fatal("private error text or arguments escaped into logs or response")
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["run_id"] != runID || record["kind"] != tc.kind || record["session_id"] != view.ID.String() {
				t.Fatalf("log and response are not correlated: %s", logs.String())
			}
		})
	}
}

func TestMCPDeniedToolLogsRequestedName(t *testing.T) {
	var logs bytes.Buffer
	e := echo.New()
	e.Logger.SetOutput(&logs)
	host := &mcpHost{logger: e.Logger}
	view := sessions.SessionView{ID: uuid.New()}
	result := host.callTool(t.Context(), view, &mcp.CallToolParamsRaw{
		Name: "github__unknown", Arguments: json.RawMessage(`{"secret":"private-arguments"}`),
	})
	if !result.IsError {
		t.Fatal("expected denied tool call to fail")
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["tool_name"] != "github__unknown" || record["stage"] != "route" || record["kind"] != "tool_unavailable" {
		t.Fatalf("missing denied tool diagnostics: %s", logs.String())
	}
	if strings.Contains(logs.String(), "private-arguments") {
		t.Fatal("tool arguments escaped into logs")
	}
}

func TestMCPPreservesToolResult(t *testing.T) {
	for _, isError := range []bool{false, true} {
		var logs bytes.Buffer
		e := echo.New()
		e.Logger.SetOutput(&logs)
		original := &mcp.CallToolResult{IsError: isError,
			Meta:              mcp.Meta{"upstream": "preserved"},
			Content:           []mcp.Content{&mcp.TextContent{Text: "private-result"}},
			StructuredContent: map[string]any{"detail": "private-result"},
		}
		host := &mcpHost{exec: diagnosticExecutor{result: original}, logger: e.Logger}
		view := sessions.SessionView{Routes: map[string]sessions.ToolRoute{"tool": {ToolName: "tool"}}}
		result := host.callTool(t.Context(), view, &mcp.CallToolParamsRaw{Name: "tool"})
		if result.Meta["connect-it.dev/diagnostics"] == nil {
			t.Fatal("missing result correlation metadata")
		}
		if original.Meta["connect-it.dev/diagnostics"] != nil || result.Meta["upstream"] != "preserved" || result.IsError != isError || result.Content[0] != original.Content[0] {
			t.Fatal("mutated upstream result")
		}
		if strings.Contains(logs.String(), "private-result") {
			t.Fatal("logged result payload")
		}
		if isError && !strings.Contains(logs.String(), "tool_error") {
			t.Fatal("tool error was not logged")
		}
	}
}
