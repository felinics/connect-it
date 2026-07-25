package api

import (
	"net/http"
	"time"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 30 * time.Second
	// Stateless MCP rejects standalone GET streams, but a POST response may
	// still use SSE while a Tool runs. Keep this above Nginx's existing
	// 300-second proxy read timeout so Go does not become the earlier cutoff.
	serverWriteTimeout   = 310 * time.Second
	serverIdleTimeout    = 120 * time.Second
	serverMaxHeaderBytes = 64 << 10
)

// NewHTTPServer returns the production server boundary. In particular,
// ReadTimeout is wider than the MCP middleware's 25-second body deadline but
// narrower than Nginx's 35-second client_body_timeout.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
}
