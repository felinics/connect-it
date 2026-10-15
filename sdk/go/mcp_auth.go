package connectit

import (
	"context"
	"io"
	"net/http"
	"strings"
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
	token, created, err := h.currentToken(ctx)
	if err != nil {
		return nil, err
	}
	h.observe(created)
	return oauth2.StaticTokenSource(token), nil
}

func (h *mcpAuthHandler) Authorize(ctx context.Context, request *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	failedToken := requestBearerToken(request)
	h.mu.Lock()
	if failedToken == "" || h.token == nil || h.token.AccessToken == failedToken {
		h.token = nil
	}
	h.mu.Unlock()
	_, created, err := h.currentToken(ctx)
	if err != nil {
		return err
	}
	h.observe(created)
	return nil
}

// observe reports a freshly issued session to the configured observer. It
// runs outside the handler lock so the observer may block or log freely.
func (h *mcpAuthHandler) observe(created *MCPSession) {
	if created == nil || h.config.OnSessionCreated == nil {
		return
	}
	h.config.OnSessionCreated(*created)
}

func requestBearerToken(request *http.Request) string {
	if request == nil {
		return ""
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(request.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// currentToken returns the cached token, or issues a new session. The second
// result is the freshly issued session, nil when the cached token was reused.
func (h *mcpAuthHandler) currentToken(ctx context.Context) (*oauth2.Token, *MCPSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.token != nil && h.token.AccessToken != "" &&
		(h.token.Expiry.IsZero() || time.Now().Add(sessionRefreshSkew).Before(h.token.Expiry)) {
		return cloneOAuthToken(h.token), nil, nil
	}

	session, err := h.client.CreateMCPSession(ctx, h.config)
	if err != nil {
		return nil, nil, err
	}
	h.token = &oauth2.Token{
		AccessToken: session.Token,
		TokenType:   "Bearer",
		Expiry:      session.ExpiresAt,
	}
	return cloneOAuthToken(h.token), &session, nil
}

func cloneSessionConfig(config MCPSessionConfig) MCPSessionConfig {
	return MCPSessionConfig{
		Connections:      cloneConnections(config.Connections),
		ToolAllowlist:    append([]string(nil), config.ToolAllowlist...),
		TTL:              config.TTL,
		OnSessionCreated: config.OnSessionCreated,
	}
}

func cloneOAuthToken(token *oauth2.Token) *oauth2.Token {
	if token == nil {
		return nil
	}
	cloned := *token
	return &cloned
}
