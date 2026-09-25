package exec

import (
	"context"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type runIDKey struct{}

type callError struct {
	err   error
	stage string
}

func (e *callError) Error() string        { return e.err.Error() }
func (e *callError) Unwrap() error        { return e.err }
func (e *callError) FailureStage() string { return e.stage }

func WithRunID(ctx context.Context) (context.Context, uuid.UUID) {
	if id, ok := ctx.Value(runIDKey{}).(uuid.UUID); ok {
		return ctx, id
	}
	id := uuid.New()
	return context.WithValue(ctx, runIDKey{}, id), id
}

func DescribeToolError(result *mcp.CallToolResult) Failure {
	f := Failure{Code: "tool_error", Message: "tool returned an error", Kind: classifyToolError(result)}
	if f.Kind == errorKindInvalidArgs {
		f.Code, f.Message = "invalid_arguments", "arguments do not conform to the tool input schema"
	}
	return f
}
