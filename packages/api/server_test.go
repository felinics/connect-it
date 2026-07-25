package api

import (
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProductionHTTPServerLimits(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := NewHTTPServer(":1234", handler)

	if server.Addr != ":1234" || server.Handler == nil {
		t.Fatalf("server address/handler not configured: %#v", server)
	}
	if server.ReadHeaderTimeout != 5*time.Second ||
		server.ReadTimeout != 30*time.Second ||
		server.WriteTimeout != 310*time.Second ||
		server.IdleTimeout != 120*time.Second ||
		server.MaxHeaderBytes != 64<<10 {
		t.Fatalf(
			"unexpected server limits: header=%s read=%s write=%s idle=%s headers=%d",
			server.ReadHeaderTimeout,
			server.ReadTimeout,
			server.WriteTimeout,
			server.IdleTimeout,
			server.MaxHeaderBytes,
		)
	}
}

func TestMCPIngressAndNginxTimeoutOrdering(t *testing.T) {
	config, err := os.ReadFile("../../docker/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	nginxBodyTimeout := nginxSecondsDirective(
		t,
		string(config),
		"client_body_timeout",
	)
	if !(mcpBodyReadTimeout < serverReadTimeout &&
		serverReadTimeout < nginxBodyTimeout) {
		t.Fatalf(
			"timeout order = app %s, server %s, nginx %s",
			mcpBodyReadTimeout,
			serverReadTimeout,
			nginxBodyTimeout,
		)
	}

	nginxProxyReadTimeout := nginxSecondsDirective(
		t,
		string(config),
		"proxy_read_timeout",
	)
	if serverWriteTimeout <= nginxProxyReadTimeout {
		t.Fatalf(
			"server WriteTimeout %s must remain above nginx proxy_read_timeout %s",
			serverWriteTimeout,
			nginxProxyReadTimeout,
		)
	}
}

func TestNginxHasExactStreamingMCPIngressBoundary(t *testing.T) {
	configBytes, err := os.ReadFile("../../docker/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	config := string(configBytes)
	for _, required := range []string{
		"location = /mcp {",
		"client_max_body_size 37m;",
		"client_body_timeout 35s;",
		"proxy_request_buffering off;",
		"proxy_buffering off;",
		"error_page 413 = @mcp_body_too_large;",
		`"error":"input_too_large"`,
		"location ~ ^/(v1|admin|healthz|swagger)(/|$) {",
	} {
		if !strings.Contains(config, required) {
			t.Errorf("nginx.conf missing %q", required)
		}
	}
}

func nginxSecondsDirective(
	t *testing.T,
	config string,
	name string,
) time.Duration {
	t.Helper()
	pattern := regexp.MustCompile(
		`(?m)^\s*` + regexp.QuoteMeta(name) + `\s+([0-9]+)s;\s*$`,
	)
	match := pattern.FindStringSubmatch(config)
	if len(match) != 2 {
		t.Fatalf("nginx directive %s not found", name)
	}
	seconds, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	return time.Duration(seconds) * time.Second
}
