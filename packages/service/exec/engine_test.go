package exec

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

func newDBHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, harnessOpt{database: true})
}

func TestManagedSuccess(t *testing.T) {
	h := newDBHarness(t)
	res, err := h.execute("managed_echo", json.RawMessage(`{"x":1}`))
	if err != nil || res.Text != "managed-ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(h.managedCalls) != 1 {
		t.Fatal("handler 未被调用")
	}
	call := h.managedCalls[0]
	if call.ToolID != "managed_echo" || call.Arguments["x"] != float64(1) ||
		call.Config["mcp_url"] != "https://self.internal/mcp" ||
		call.AccessToken != "" {
		t.Fatalf("ToolCallContext 不符: %+v", call)
	}
	if found, failures, _ := h.health(); !found || failures != 0 {
		t.Fatalf("health 应记成功: found=%v failures=%d", found, failures)
	}
	run := h.lastRun()
	if run.Status != "ok" || run.SessionID != h.sessionID {
		t.Fatalf("tool_runs = %+v, want ok/%s", run, h.sessionID)
	}
}

func TestManagedHandlerError(t *testing.T) {
	h := newDBHarness(t)
	h.managedErr = errors.New("handler exploded")
	if _, err := h.execute("managed_echo", nil); err == nil {
		t.Fatal("handler 报错应透传")
	}
	if found, _, _ := h.health(); found {
		t.Fatal("未知 managed 内部错误不应改变 Provider health")
	}
	run := h.lastRun()
	if run.Status != "error" ||
		run.Error == nil ||
		strings.Contains(*run.Error, "handler exploded") ||
		!strings.Contains(*run.Error, `"code":"internal_error"`) {
		t.Fatalf("tool_runs 应只记录安全 internal marker: %+v", run)
	}
}

func TestManagedHandlerMissingIsConfigError(t *testing.T) {
	h := newDBHarness(t)
	_, err := h.execute("managed_missing", nil)
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("应为 handler 未注册错误: %v", err)
	}
	if found, _, _ := h.health(); found {
		t.Fatal("配置类拒绝不应写 health")
	}
	if run := h.lastRun(); run.Status != "error" {
		t.Fatal("tool_runs 仍应记录")
	}
}

func TestRemoteFixed(t *testing.T) {
	h := newDBHarness(t)
	res, err := h.execute("remote_fixed", json.RawMessage(`{"m":"hi"}`))
	if err != nil || res.Text != "remote-ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(h.mcp.calls) != 1 {
		t.Fatal("MCPCaller 未被调用")
	}
	c := h.mcp.calls[0]
	if c.Endpoint != "https://mcp.example.com/mcp" ||
		c.RemoteToolName != "upstream_echo" ||
		c.Server.RequestTimeout != 5*time.Second ||
		string(c.Arguments) != `{"m":"hi"}` ||
		c.ConnectorType != "exec_test" ||
		c.ConnectionID != h.connID ||
		c.ToolID != "remote_fixed" ||
		c.Server.Key != "fixed" ||
		c.AllowInsecureHTTP != "false" {
		t.Fatalf("MCP 调用/policy identity 不完整: %+v", c)
	}
	if found, failures, _ := h.health(); !found || failures != 0 {
		t.Fatal("health 应记成功")
	}
}

func TestRemoteBusinessFailureCountsAsHealthy(t *testing.T) {
	h := newDBHarness(t)
	h.mcp.result = connector.ToolResultData{
		Failure: &connector.ToolFailure{
			Code: connector.FailureProviderError,
			Message: connector.DefaultFailureMessage(
				connector.FailureProviderError,
			),
		},
	}
	res, err := h.execute("remote_fixed", nil)
	if err != nil || !res.Failed() {
		t.Fatalf("业务失败应透传: %+v err=%v", res, err)
	}
	if found, failures, _ := h.health(); !found || failures != 0 {
		t.Fatal("业务失败算连通成功，health 记成功")
	}
	if run := h.lastRun(); run.Status != "error" {
		t.Fatalf("tool_runs 应记 error: %+v", run)
	}
}

func TestRemoteTransportErrorFailsHealth(t *testing.T) {
	h := newDBHarness(t)
	h.mcp.err = errors.New("connection refused")
	if _, err := h.execute("remote_fixed", nil); err == nil {
		t.Fatal("传输错误应返回")
	}
	found, failures, lastError := h.health()
	if !found || failures != 1 || lastError == nil {
		t.Fatal("传输错误应写 health 失败和安全错误")
	}
	if strings.Contains(*lastError, "connection refused") ||
		!strings.Contains(*lastError, `"code":"upstream_unavailable"`) {
		t.Fatalf("health 不得保存原始 transport error: %s", *lastError)
	}
}

func TestExplicitCredentialInvalidMarksExactSnapshotReauthRequired(
	t *testing.T,
) {
	h := newDBHarness(t)
	h.useAPIKeyCredential()
	h.managed = connector.ToolResultData{
		Failure: mustCredentialInvalidFailure(t, 401),
	}

	result, err := h.execute("managed_echo", nil)
	if err != nil ||
		result.Failure == nil ||
		!result.Failure.IndicatesCredentialInvalid() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	connection, err := h.q.GetConnection(h.ctx, h.connID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != "reauth_required" {
		t.Fatalf("connection status = %q, want reauth_required", connection.Status)
	}
}

func TestCredentialFailureWithoutExplicitSignalDoesNotChangeState(
	t *testing.T,
) {
	for _, test := range []struct {
		name    string
		failure connector.ToolFailure
	}{
		{
			name: "ordinary authorization failed",
			failure: connector.ToolFailure{
				Code:           connector.FailureAuthorizationFailed,
				Message:        "provider authorization failed",
				UpstreamStatus: 401,
			},
		},
		{
			name: "forbidden",
			failure: connector.ToolFailure{
				Code:           connector.FailurePermissionDenied,
				Message:        "provider permission denied",
				UpstreamStatus: 403,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newDBHarness(t)
			h.useAPIKeyCredential()
			failure := test.failure
			h.managed = connector.ToolResultData{Failure: &failure}

			result, err := h.execute("managed_echo", nil)
			if err != nil || result.Failure == nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			connection, err := h.q.GetConnection(h.ctx, h.connID)
			if err != nil {
				t.Fatal(err)
			}
			if connection.Status != "active" {
				t.Fatalf("connection status = %q, want active", connection.Status)
			}
		})
	}
}

func TestStaleCredentialInvalidFailureCannotMarkNewSnapshot(t *testing.T) {
	h := newDBHarness(t)
	h.useAPIKeyCredential()
	h.managed = connector.ToolResultData{
		Failure: mustCredentialInvalidFailure(t, 401),
	}
	h.beforeReturn = func() {
		h.mustExec(`update connections
		     set credential_version = credential_version + 1,
		         authorization_generation = authorization_generation + 1
		   where id = $1`, h.connID)
	}

	result, err := h.execute("managed_echo", nil)
	if err != nil ||
		result.Failure == nil ||
		!result.Failure.IndicatesCredentialInvalid() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	connection, err := h.q.GetConnection(h.ctx, h.connID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != "active" ||
		connection.CredentialVersion != 2 ||
		connection.AuthorizationGeneration != 2 {
		t.Fatalf("new snapshot was polluted: %+v", connection)
	}
}

func TestCredentialInvalidStateWriteErrorIsReturned(t *testing.T) {
	h := newDBHarness(t)
	h.useAPIKeyCredential()
	h.managed = connector.ToolResultData{
		Failure: mustCredentialInvalidFailure(t, 401),
	}
	h.beforeReturn = h.pool.Close

	result, err := h.execute("managed_echo", nil)
	if err == nil || result.Failure != nil {
		t.Fatalf("state write failure was hidden: result=%+v err=%v", result, err)
	}
}

func TestRemoteTypedCredentialInvalidFailureIsNormalizedAndCASApplied(
	t *testing.T,
) {
	h := newDBHarness(t)
	h.useAPIKeyCredential()
	h.mcp.err = &fakeToolFailureError{
		failure: *mustCredentialInvalidFailure(t, http.StatusUnauthorized),
	}

	result, err := h.execute("remote_fixed", nil)
	if err != nil ||
		result.Failure == nil ||
		!result.Failure.IndicatesCredentialInvalid() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	connection, err := h.q.GetConnection(h.ctx, h.connID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Status != "reauth_required" {
		t.Fatalf("connection status = %q", connection.Status)
	}
	if found, failures, _ := h.health(); !found || failures != 0 {
		t.Fatalf(
			"typed Provider 401 proves reachability: found=%v failures=%d",
			found,
			failures,
		)
	}
}

func TestSelfHostedRequiresVerify(t *testing.T) {
	h := newDBHarness(t)
	_, err := h.execute("remote_self", nil)
	if err == nil || !strings.Contains(err.Error(), "mcp:verify") {
		t.Fatalf("未验证应拒绝并提示 mcp:verify: %v", err)
	}
	if found, _, _ := h.health(); found {
		t.Fatal("配置类拒绝不应写 health")
	}

	endpoint := "https://self.internal/mcp"
	if err := h.q.SetConnectorConfigVerified(
		h.ctx,
		store.SetConnectorConfigVerifiedParams{
			ConnectorType: "exec_test",
			Endpoint:      &endpoint,
		},
	); err != nil {
		t.Fatal(err)
	}
	res, err := h.execute("remote_self", nil)
	if err != nil || res.Text != "remote-ok" {
		t.Fatalf("验证后应可执行: %+v err=%v", res, err)
	}
	if h.mcp.calls[len(h.mcp.calls)-1].Endpoint != endpoint {
		t.Fatalf("应打配置的 endpoint: %+v", h.mcp.calls)
	}

	// endpoint 变更后重新拒绝
	h.mustExec(`update connector_configs
	     set public_config='{"mcp_url":"https://other.internal/mcp"}'
	   where connector_type='exec_test'`)
	if _, err := h.execute("remote_self", nil); err == nil {
		t.Fatal("endpoint 变更后应重新要求验证")
	}
}

func TestMapperUnimplemented(t *testing.T) {
	h := newDBHarness(t)
	_, err := h.execute("remote_mapped", nil)
	if err == nil || !strings.Contains(err.Error(), "mapper 未实现") {
		t.Fatalf("mapper 应报未实现: %v", err)
	}
	if found, _, _ := h.health(); found {
		t.Fatal("配置类拒绝不应写 health")
	}
}

func TestUnknownTool(t *testing.T) {
	h := newDBHarness(t)
	if _, err := h.execute("nope", nil); !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("未知 tool 应 ErrToolUnavailable: %v", err)
	}
}

func TestUnknownConnection(t *testing.T) {
	h := newDBHarness(t)
	_, err := h.engine.Execute(
		h.ctx,
		h.request(uuid.New(), "managed_echo", nil),
	)
	if !errors.Is(err, ErrConnectionNotFound) {
		t.Fatalf("want ErrConnectionNotFound, got %v", err)
	}
}

func TestOversizeInputRecordsOnlyBoundedMetadata(t *testing.T) {
	h := newDBHarness(t)
	const secretMarker = "oversize-raw-argument-secret"
	oversize, _ := json.Marshal(map[string]string{
		"query": strings.Repeat(secretMarker, 4*1024),
	})
	if _, err := h.execute("managed_echo", oversize); err != nil {
		t.Fatal(err)
	}
	run := h.lastRun()
	if len(run.Input) > 4<<10 {
		t.Fatalf("input metadata = %d bytes, want <= 4 KiB", len(run.Input))
	}
	if strings.Contains(string(run.Input), secretMarker) {
		t.Fatalf("input metadata leaked argument content: %s", run.Input)
	}
	metadata := decodeInputMetadata(t, run.Input)
	if metadata.RawBytes != len(oversize) ||
		metadata.DeclaredTopLevelFieldCount != 1 ||
		strings.Join(metadata.DeclaredTopLevelFields, ",") != "query" {
		t.Fatalf("input metadata = %+v", metadata)
	}
}

// The recorder tests own the classification matrix. This integration test only
// proves that Engine persists those projections, never provider/user content.
func TestAuditPersistenceStoresOnlyStableMetadata(t *testing.T) {
	h := newDBHarness(t)
	const (
		argumentSecret = "raw-argument-value-secret"
		dynamicKey     = "customer@example.com"
		bodySecret     = "raw-provider-body-secret"
	)
	h.managed = connector.ToolResultData{
		Failure: &connector.ToolFailure{
			Code:           connector.FailureTimeout,
			Message:        bodySecret,
			UpstreamStatus: http.StatusGatewayTimeout,
		},
	}
	arguments := json.RawMessage(
		`{"query":"raw-argument-value-secret","customer@example.com":"raw-argument-value-secret"}`,
	)
	result, err := h.execute("managed_echo", arguments)
	if err != nil || result.Failure == nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	run := h.lastRun()
	if run.Status != "error" ||
		run.Error == nil ||
		run.ErrorCode == nil ||
		*run.ErrorCode != string(connector.FailureTimeout) ||
		run.UpstreamStatus == nil ||
		*run.UpstreamStatus != http.StatusGatewayTimeout ||
		run.OutputSummary == nil {
		t.Fatalf("persisted run = %+v", run)
	}
	found, failures, healthError := h.health()
	if !found || failures != 1 || healthError == nil {
		t.Fatalf("health = found:%v failures:%d error:%v", found, failures, healthError)
	}
	persisted := string(run.Input) + *run.Error + *run.OutputSummary + *healthError
	for _, forbidden := range []string{argumentSecret, dynamicKey, bodySecret} {
		if strings.Contains(persisted, forbidden) {
			t.Fatalf("database leaked %q: %s", forbidden, persisted)
		}
	}

	metadata := decodeInputMetadata(t, run.Input)
	if metadata.RawBytes != len(arguments) ||
		metadata.DeclaredTopLevelFieldCount != 1 ||
		strings.Join(metadata.DeclaredTopLevelFields, ",") != "query" {
		t.Fatalf("input metadata = %+v", metadata)
	}

	var outputMetadata auditOutputMetadata
	if err := json.Unmarshal([]byte(*run.OutputSummary), &outputMetadata); err != nil {
		t.Fatal(err)
	}
	if outputMetadata.Outcome != "failure" ||
		outputMetadata.FailureCode != string(connector.FailureTimeout) {
		t.Fatalf("output metadata = %+v", outputMetadata)
	}
}

// CAS 必须用 backend 真正使用的那份快照：OAuth 惰性刷新后 Engine 首次读到的
// Connection 行已经过期，只有刷新返回的 version 才能标记 credential 失效。
func TestCredentialInvalidAfterOAuthRefreshUsesReturnedSnapshotVersion(
	t *testing.T,
) {
	h := newHarness(t, harnessOpt{
		def:        oauthSnapshotDefinition(),
		registered: []string{"read"},
		database:   true,
		authMethod: "oauth",
	})
	h.mustExec(
		`update connections set credential_version = 2 where id = $1`,
		h.connID,
	)
	// Engine 首次读到的仍然是 version 1，token resolver 返回 version 2。
	h.engine.connections = &fakeConnections{row: store.Connection{
		ID:                      h.connID,
		ConnectorType:           "oauth_snapshot",
		AuthMethod:              "oauth",
		Status:                  "active",
		CredentialVersion:       1,
		AuthorizationGeneration: 1,
	}}
	h.engine.refresher = &fakeRefresher{token: tokens.OAuthToken{
		AccessToken:             "refreshed-access-token",
		TokenType:               "Bearer",
		CredentialVersion:       2,
		AuthorizationGeneration: 1,
	}}
	h.managed = connector.ToolResultData{
		Failure: mustCredentialInvalidFailure(t, 401),
	}

	result, err := h.execute("read", json.RawMessage(`{}`))
	if err != nil ||
		len(h.managedCalls) != 1 ||
		h.managedCalls[0].AccessToken != "refreshed-access-token" ||
		result.Failure == nil ||
		!result.Failure.IndicatesCredentialInvalid() {
		t.Fatalf("calls=%+v result=%+v err=%v", h.managedCalls, result, err)
	}
	stored, err := h.q.GetConnection(h.ctx, h.connID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "reauth_required" ||
		stored.CredentialVersion != 2 ||
		stored.AuthorizationGeneration != 1 {
		t.Fatalf("stored snapshot = %+v", stored)
	}
}

func oauthSnapshotDefinition() connector.Definition {
	return connector.Definition{
		Type:                "oauth_snapshot",
		Name:                "OAuth Snapshot",
		ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{
			Key:   "oauth",
			Type:  connector.AuthOAuth2,
			Label: "OAuth",
			OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://oauth.example/authorize",
				TokenEndpoint:         "https://oauth.example/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://oauth.example:443"},
					TokenOrigins:         []string{"https://oauth.example:443"},
				},
			},
		}},
		Tools: []connector.Tool{{
			ID:          "read",
			Name:        "Read",
			Risk:        connector.RiskRead,
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Backend:     connector.ManagedBackend{HandlerKey: "read"},
		}},
	}
}

func TestRemoteMCPUsesExplicitCredentialBindingNotDeclarationOrder(
	t *testing.T,
) {
	method := connector.AuthMethod{
		Key:  "multi",
		Type: connector.AuthCustomCredential,
		CredentialFields: []connector.ConfigField{
			{Key: "username"},
			{Key: "api_token", Secret: true},
		},
	}
	got, err := remoteMCPBearerToken(
		method,
		connector.MCPAuthBinding{
			Scheme:                      "bearer",
			CredentialFieldByAuthMethod: map[string]string{"multi": "api_token"},
		},
		tokens.OAuthToken{},
		map[string]any{
			"username":  "not-a-token",
			"api_token": "actual-token",
		},
	)
	if err != nil || got != "actual-token" {
		t.Fatalf("bearer = %q, err=%v", got, err)
	}

	if _, err := remoteMCPBearerToken(
		method,
		connector.MCPAuthBinding{Scheme: "bearer"},
		tokens.OAuthToken{},
		map[string]any{"api_token": "actual-token"},
	); err == nil {
		t.Fatal("missing explicit auth-method binding was accepted")
	}
}

func TestRemoteMCPOAuthRequiresTypedBearerToken(t *testing.T) {
	method := connector.AuthMethod{Key: "oauth", Type: connector.AuthOAuth2}
	got, err := remoteMCPBearerToken(
		method,
		connector.MCPAuthBinding{Scheme: "bearer"},
		tokens.OAuthToken{AccessToken: "oauth-token", TokenType: "Bearer"},
		nil,
	)
	if err != nil || got != "oauth-token" {
		t.Fatalf("oauth bearer = %q, err=%v", got, err)
	}
	if _, err := remoteMCPBearerToken(
		method,
		connector.MCPAuthBinding{Scheme: "bearer"},
		tokens.OAuthToken{AccessToken: "oauth-token", TokenType: "DPoP"},
		nil,
	); err == nil {
		t.Fatal("unsupported token scheme was accepted")
	}
}

func TestEngineScopePolicyUsesKnownFlagAndProviderMatcher(t *testing.T) {
	engine := &Engine{
		matchers: connector.ScopeMatcherMap{
			"example": {
				"oauth": func(granted []string, required string) bool {
					for _, scope := range granted {
						if scope == required ||
							(scope == "user" && required == "read:user") {
							return true
						}
					}
					return false
				},
			},
		},
	}
	if !engine.scopesAuthorize("example", "oauth", nil, false, []string{"anything"}) {
		t.Fatal("unknown scopes must defer to Provider")
	}
	if !engine.scopesAuthorize(
		"example",
		"oauth",
		[]string{"user"},
		true,
		[]string{"read:user"},
	) {
		t.Fatal("Provider matcher implication was ignored")
	}
	if engine.scopesAuthorize(
		"example",
		"oauth",
		[]string{"read:user"},
		true,
		[]string{"repo"},
	) {
		t.Fatal("known missing scope was accepted")
	}
}

func mustCredentialInvalidFailure(
	t *testing.T,
	status int,
) *connector.ToolFailure {
	t.Helper()
	failure, err := connector.NewCredentialInvalidFailure(
		"provider credential is invalid",
		status,
	)
	if err != nil {
		t.Fatal(err)
	}
	return failure
}

func decodeInputMetadata(t *testing.T, raw []byte) auditInputMetadata {
	t.Helper()
	var metadata auditInputMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}
