package connectit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// MCPSessionConfig binds one aggregate MCP session to the selected persistent
// Connect-It Connections. Map keys become the tool namespace, for example
// "github" exposes "github__search_repositories".
// An empty tool allowlist snapshots every tool discovered at issuance.
type MCPSessionConfig struct {
	Connections   map[string]string
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
		Connections   map[string]string `json:"connections"`
		ToolAllowlist []string          `json:"tool_allowlist,omitempty"`
		TTLSeconds    int64             `json:"ttl_seconds,omitempty"`
	}{
		Connections:   cloneConnections(config.Connections),
		ToolAllowlist: config.ToolAllowlist,
		TTLSeconds:    int64(config.TTL / time.Second),
	}
	var session MCPSession
	if err := c.doJSON(ctx, http.MethodPost, "/v1/mcp-sessions", request, &session); err != nil {
		return MCPSession{}, err
	}
	session.Token = strings.TrimSpace(session.Token)
	if session.Token == "" {
		return MCPSession{}, errors.New("connectit: MCP session response is missing token")
	}
	if session.ExpiresAt.IsZero() {
		return MCPSession{}, errors.New("connectit: MCP session response is missing expiry")
	}
	return session, nil
}

func cloneConnections(connections map[string]string) map[string]string {
	if connections == nil {
		return nil
	}
	cloned := make(map[string]string, len(connections))
	for alias, connectionID := range connections {
		cloned[alias] = connectionID
	}
	return cloned
}
