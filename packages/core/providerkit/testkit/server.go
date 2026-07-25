package testkit

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/providerkit"
)

const testOrigin = "http://provider.test:80"

// Server exposes an httptest handler through a policy-bound providerkit
// Client without making public network requests.
type Server struct {
	HTTP   *httptest.Server
	Client *providerkit.Client
}

// ServerOptions customizes only non-secret test policy identity and limits.
// Zero values preserve NewServer's historical defaults.
type ServerOptions struct {
	Provider         string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
}

// NewServer starts a deterministic local server and constructs a guarded
// Client whose validated test IP is dialed into that listener.
func NewServer(t testing.TB, handler http.Handler) *Server {
	t.Helper()
	return NewServerWithOptions(t, handler, ServerOptions{})
}

// NewServerWithOptions is NewServer with explicit test-only policy limits.
func NewServerWithOptions(
	t testing.TB,
	handler http.Handler,
	options ServerOptions,
) *Server {
	t.Helper()
	if handler == nil {
		t.Fatal("testkit: handler must not be nil")
	}
	provider := options.Provider
	if provider == "" {
		provider = "test_provider"
	}

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target := server.Listener.Addr().String()
	if _, _, err := net.SplitHostPort(target); err != nil {
		t.Fatalf("testkit: invalid listener address: %v", err)
	}

	factory := providerkit.NewFactory(
		providerkit.WithInsecureProviderHTTP(true),
		providerkit.WithResolver(Resolver{}),
		providerkit.WithDialContext(DialTarget(target)),
	)
	client, err := factory.NewDynamicClient(providerkit.DynamicPolicyInput{
		Provider:          provider,
		BaseURL:           testOrigin,
		AllowInsecureHTTP: "true",
		RequestTimeout:    options.RequestTimeout,
		MaxResponseBytes:  options.MaxResponseBytes,
		Retry: providerkit.RetryPolicy{
			Disabled: true,
		},
	})
	if err != nil {
		server.Close()
		t.Fatalf("testkit: construct client: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return &Server{HTTP: server, Client: client}
}

// URL returns a relative request target suitable for providerkit.Request.URL.
func (server *Server) URL(path string) string {
	return path
}
