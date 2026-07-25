package exec

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
)

// ExecutionGrant is the exact session-bound capability that will replace the
// legacy scattered Execute arguments at the authorization-generation cutover.
// A grant is not independently sufficient authorization; the session
// authorizer must re-check persisted state for every execution.
type ExecutionGrant struct {
	SessionID       uuid.UUID
	ExposedToolName string
	ConnectionID    uuid.UUID
	ToolID          string
}

// ExecuteRequest is the execution boundary used by the upcoming atomic
// session-authorizer cutover.
type ExecuteRequest struct {
	ConnectionID  uuid.UUID
	ToolID        string
	Arguments     json.RawMessage
	Authorization ExecutionGrant
}

// SessionExecutionAuthorizer re-authorizes a session grant against current
// persisted binding, risk, allowlist, and authorization-generation state.
//
// This contract is intentionally exact even before activation. The legacy
// Execute path must not infer, synthesize, or accept a weaker grant.
type SessionExecutionAuthorizer interface {
	AuthorizeExecution(
		ctx context.Context,
		sessionID uuid.UUID,
		exposedToolName string,
		connectionID uuid.UUID,
		toolID string,
		currentRisk connector.ToolRisk,
		currentAuthorizationGeneration int64,
	) error
}

// ExecutionUnauthorizedMarker is implemented only by errors that mean a
// persisted session capability was explicitly denied. Infrastructure,
// database, and context errors must not implement this marker: Engine
// propagates those errors instead of disguising them as policy decisions.
type ExecutionUnauthorizedMarker interface {
	error
	ExecutionUnauthorized()
}

type executionUnauthorizedError struct{}

func (executionUnauthorizedError) Error() string {
	return "exec: session execution is not authorized"
}

func (executionUnauthorizedError) ExecutionUnauthorized() {}

// ErrExecutionUnauthorized is the canonical marker for a fail-closed,
// explicitly unauthorized execution. Session implementations may wrap this
// sentinel or provide their own ExecutionUnauthorizedMarker.
var ErrExecutionUnauthorized error = executionUnauthorizedError{}

func isExecutionUnauthorized(err error) bool {
	var marker ExecutionUnauthorizedMarker
	return errors.As(err, &marker)
}
