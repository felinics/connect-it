// Runtime types for managed tools.
package connector

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ManagedCall struct {
	Arguments json.RawMessage
	// Config is the administrator config with defaults applied, including
	// decrypted secrets. It comes from configsvc.Resolved.
	Config map[string]any
	// Credential holds the decrypted credential fields of an api_key or
	// custom_credential connection. It is nil for OAuth and AuthNone.
	Credential map[string]any
	// AccessToken is the valid access token of an OAuth connection after lazy
	// refresh. For api_key connections it is the credential value; otherwise
	// it is empty.
	AccessToken string
}

// ManagedHandler is the execution entry point of a managed tool.
type ManagedHandler func(ctx context.Context, call ManagedCall) (*mcp.CallToolResult, error)
