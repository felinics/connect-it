package connectit

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"
)

const sessionRefreshSkew = time.Minute

// MCPAuthHandler returns an auth.OAuthHandler for the official Go MCP SDK. It
// creates a session token on demand, caches it until shortly before expiry,
// and replaces it after a 401 or 403 response.
func (c *Client) MCPAuthHandler(config MCPSessionConfig) auth.OAuthHandler {
	return &mcpAuthHandler{
		client: c,
		config: cloneSessionConfig(config),
	}
}

type mcpAuthHandler struct {
	client *Client
	config MCPSessionConfig

	mu    sync.Mutex
	token *oauth2.Token
}

func (h *mcpAuthHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	token, err := h.currentToken(ctx)
	if err != nil {
		return nil, err
	}
	return oauth2.StaticTokenSource(token), nil
}

func (h *mcpAuthHandler) Authorize(ctx context.Context, _ *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	h.mu.Lock()
	h.token = nil
	h.mu.Unlock()
	_, err := h.currentToken(ctx)
	return err
}

func (h *mcpAuthHandler) currentToken(ctx context.Context) (*oauth2.Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.token != nil && h.token.AccessToken != "" &&
		(h.token.Expiry.IsZero() || time.Now().Add(sessionRefreshSkew).Before(h.token.Expiry)) {
		return cloneOAuthToken(h.token), nil
	}

	session, err := h.client.CreateMCPSession(ctx, h.config)
	if err != nil {
		return nil, err
	}
	h.token = &oauth2.Token{
		AccessToken: session.Token,
		TokenType:   "Bearer",
		Expiry:      session.ExpiresAt,
	}
	return cloneOAuthToken(h.token), nil
}

func cloneSessionConfig(config MCPSessionConfig) MCPSessionConfig {
	return MCPSessionConfig{
		ConnectionID:  config.ConnectionID,
		ToolAllowlist: append([]string(nil), config.ToolAllowlist...),
		TTL:           config.TTL,
	}
}

func cloneOAuthToken(token *oauth2.Token) *oauth2.Token {
	if token == nil {
		return nil
	}
	cloned := *token
	return &cloned
}
