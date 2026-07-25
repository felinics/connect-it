package smoketest

import (
	"encoding/json"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// Registry compiles definition's Tool schemas for the smoke run. handlerKeys
// are the Managed Tool IDs; a Remote-MCP-only connector passes none.
func Registry(
	t *testing.T,
	definition connector.Definition,
	handlerKeys ...string,
) *registry.Registry {
	t.Helper()
	definitionRegistry := registry.New()
	if err := definitionRegistry.Register(
		definition,
		handlerKeys...,
	); err != nil {
		t.Fatalf("register %s definition", definition.Type)
	}
	return definitionRegistry
}

// ToolIDs lists every published Tool ID, which is the handler-key set of a
// fully Managed connector.
func ToolIDs(definition connector.Definition) []string {
	ids := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		ids = append(ids, tool.ID)
	}
	return ids
}

// Run executes one Managed Tool end to end against the real Provider: compiled
// input schema, handler, then compiled output schema. A Tool whose arguments or
// structured output no longer match its published Definition is a contract
// break, so both directions are validated even though only the call is real.
//
// Every diagnostic here is fixed text plus a public failure code: neither the
// handler's error nor the Provider's body may enter smoke output.
func Run(
	t *testing.T,
	definitionRegistry *registry.Registry,
	handlers connector.HandlerMap,
	call connector.ToolCallContext,
) json.RawMessage {
	t.Helper()
	schemas, exists := definitionRegistry.ToolSchemas(
		call.ConnectorType,
		call.ToolID,
	)
	if !exists {
		t.Fatalf("%s has no registered schemas", call.ToolID)
	}
	if err := schemas.Input.Validate(call.Arguments); err != nil {
		t.Fatalf("%s smoke input violates its compiled schema", call.ToolID)
	}
	handler, exists := handlers[call.ToolID]
	if !exists {
		t.Fatalf("%s has no registered handler", call.ToolID)
	}
	result, err := handler(t.Context(), call)
	if err != nil {
		t.Fatalf("%s returned an internal error", call.ToolID)
	}
	if result.Failure != nil {
		t.Fatalf(
			"%s failed: code=%s upstream_status=%d",
			call.ToolID,
			result.Failure.Code,
			result.Failure.UpstreamStatus,
		)
	}
	var output any
	if err := json.Unmarshal(result.Structured, &output); err != nil {
		t.Fatalf("%s returned invalid structured JSON", call.ToolID)
	}
	if err := schemas.Output.Validate(output); err != nil {
		t.Fatalf("%s output violates its compiled schema", call.ToolID)
	}
	return append(json.RawMessage(nil), result.Structured...)
}

// FieldCall builds a custom-credential or API-key Tool call.
func FieldCall(
	definition connector.Definition,
	connectionID string,
	toolID string,
	arguments map[string]any,
	fields map[string]string,
) connector.ToolCallContext {
	credential := make(map[string]any, len(fields))
	for key, value := range fields {
		credential[key] = value
	}
	return connector.ToolCallContext{
		ConnectorType: definition.Type,
		ConnectionID:  connectionID,
		ToolID:        toolID,
		Arguments:     arguments,
		Credential:    credential,
	}
}

// TokenCall builds an OAuth bearer Tool call.
func TokenCall(
	definition connector.Definition,
	connectionID string,
	toolID string,
	arguments map[string]any,
	accessToken string,
) connector.ToolCallContext {
	return connector.ToolCallContext{
		ConnectorType: definition.Type,
		ConnectionID:  connectionID,
		ToolID:        toolID,
		Arguments:     arguments,
		AccessToken:   accessToken,
		TokenType:     "Bearer",
	}
}
