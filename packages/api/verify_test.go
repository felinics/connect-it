package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/api"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

type fakeLister struct {
	mu        sync.Mutex
	names     []string
	err       error
	endpoints []string
}

func (f *fakeLister) ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints = append(f.endpoints, endpoint)
	return f.names, f.err
}

func newVerifyServer(t *testing.T) (*httptest.Server, *fakeLister, *store.Queries, *configsvc.Service) {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("dd", 32))
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	reg.MustRegister(connector.Definition{
		Type: "self_app", Name: "SelfHosted", ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "mcp_url", Label: "MCP URL", InputType: connector.InputURL, Required: true},
		},
		RemoteMCPServers: []connector.RemoteMCPServer{
			{Key: "self",
				Endpoint:   connector.Endpoint{Source: connector.EndpointConfigField, ConfigFieldKey: "mcp_url"},
				Provenance: connector.Provenance{Kind: connector.ProvenanceSelfHosted}},
		},
		Tools: []connector.Tool{
			{ID: "search", Name: "Search", Risk: connector.RiskRead,
				Backend: connector.RemoteMCPBackend{ServerKey: "self", RemoteToolName: "upstream_search"}},
		},
	})

	q := store.New(pool)
	cfg := configsvc.New(q, reg, kr)
	auth := authsvc.New(q)
	cat := catalogsvc.New(q, reg, cfg)
	lister := &fakeLister{names: []string{"upstream_search", "other"}}

	t.Setenv(authsvc.EnvAdminPassword, adminPassword)
	if err := auth.EnsureAdminFromEnv(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := api.New(api.Deps{
		Registry: reg, Store: q, Config: cfg, Catalog: cat, Auth: auth,
		MCPTools:     lister,
		CookieSecret: []byte("test-cookie-secret"),
	})
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv, lister, q, cfg
}

func TestVerifySelfHostedFlow(t *testing.T) {
	srv, lister, q, cfg := newVerifyServer(t)
	h := adminLogin(t, srv)
	ctx := context.Background()

	// endpoint 未配置 → 422
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/connectors/self_app/mcp:verify", "", h)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "mcp_url") {
		t.Fatalf("未配置应 422: %d %s", resp.StatusCode, body)
	}

	// 配置 endpoint
	if _, err := cfg.Put(ctx, "self_app",
		map[string]any{"mcp_url": "https://self.internal/mcp"}, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}

	// 上游缺 tool → 422＋health 失败
	lister.mu.Lock()
	lister.names = []string{"unrelated"}
	lister.mu.Unlock()
	resp, body = doReq(t, http.MethodPost, srv.URL+"/admin/connectors/self_app/mcp:verify", "", h)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "upstream_search") {
		t.Fatalf("缺 tool 应 422: %d %s", resp.StatusCode, body)
	}
	if hrow, err := q.GetConnectorHealth(ctx, "self_app"); err != nil || hrow.ConsecutiveFailures != 1 {
		t.Fatalf("verify 失败应写 health: %+v err=%v", hrow, err)
	}

	// 上游齐全 → 204＋verified 落库＋health 清零
	lister.mu.Lock()
	lister.names = []string{"upstream_search"}
	lister.mu.Unlock()
	resp, body = doReq(t, http.MethodPost, srv.URL+"/admin/connectors/self_app/mcp:verify", "", h)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("verify 成功应 204: %d %s", resp.StatusCode, body)
	}
	v, err := q.GetConnectorConfigVerification(ctx, "self_app")
	if err != nil || v.McpVerifiedAt == nil || v.McpVerifiedEndpoint == nil ||
		*v.McpVerifiedEndpoint != "https://self.internal/mcp" {
		t.Fatalf("verified 未落库: %+v err=%v", v, err)
	}
	if hrow, _ := q.GetConnectorHealth(ctx, "self_app"); hrow.ConsecutiveFailures != 0 {
		t.Fatalf("verify 成功应清零 health: %+v", hrow)
	}
	if lister.endpoints[len(lister.endpoints)-1] != "https://self.internal/mcp" {
		t.Fatalf("应探测配置的 endpoint: %v", lister.endpoints)
	}
}

func TestVerifyUnknownConnector(t *testing.T) {
	srv, _, _, _ := newVerifyServer(t)
	h := adminLogin(t, srv)
	resp, _ := doReq(t, http.MethodPost, srv.URL+"/admin/connectors/nope/mcp:verify", "", h)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知 type 应 404: %d", resp.StatusCode)
	}
}

func TestVerifyListerError(t *testing.T) {
	srv, lister, _, cfg := newVerifyServer(t)
	h := adminLogin(t, srv)
	if _, err := cfg.Put(context.Background(), "self_app",
		map[string]any{"mcp_url": "https://self.internal/mcp"}, nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	lister.mu.Lock()
	lister.err = errors.New("connection refused")
	lister.mu.Unlock()
	resp, body := doReq(t, http.MethodPost, srv.URL+"/admin/connectors/self_app/mcp:verify", "", h)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "握手失败") {
		t.Fatalf("握手失败应 422: %d %s", resp.StatusCode, body)
	}
}
