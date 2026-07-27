package connectit

import (
	"context"
	"net/http"
	"time"
)

// MCPSessionConfig binds a session to one persistent Connect-It Connection.
// An empty tool allowlist snapshots every tool discovered at issuance.
type MCPSessionConfig struct {
	ConnectionID  string
	ToolAllowlist []string
	TTL           time.Duration
}

// MCPSession is a short-lived credential for the /mcp endpoint.
type MCPSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateMCPSession signs a short-lived MCP credential. Downstreams normally
// use MCPAuthHandler instead of calling this method directly.
func (c *Client) CreateMCPSession(ctx context.Context, config MCPSessionConfig) (MCPSession, error) {
	request := struct {
		ConnectionID  string   `json:"connection_id"`
		ToolAllowlist []string `json:"tool_allowlist,omitempty"`
		TTLSeconds    int64    `json:"ttl_seconds,omitempty"`
	}{
		ConnectionID:  config.ConnectionID,
		ToolAllowlist: config.ToolAllowlist,
		TTLSeconds:    int64(config.TTL / time.Second),
	}
	var session MCPSession
	err := c.doJSON(ctx, http.MethodPost, "/v1/mcp-sessions", request, &session)
	return session, err
}
