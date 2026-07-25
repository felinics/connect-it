package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	execsvc "github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

var _ execsvc.SessionExecutionAuthorizer = (*sessions.Service)(nil)

func newSvc(t *testing.T) (*sessions.Service, *store.Queries, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	pool := testutil.NewDB(t)
	reg := registry.New()
	tool := func(id string, risk connector.ToolRisk) connector.Tool {
		return connector.Tool{
			ID:          id,
			Name:        id,
			Risk:        risk,
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
			Backend:     connector.ManagedBackend{HandlerKey: id},
		}
	}
	reg.MustRegister(connector.Definition{
		Type:                "github",
		Name:                "GitHub",
		ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{{
			Key: "none", Type: connector.AuthNone, Label: "None",
		}},
		Tools: []connector.Tool{
			tool("list_issues", connector.RiskRead),
			tool("create_issue", connector.RiskWrite),
			tool("delete_issue", connector.RiskDestructive),
		},
	}, "list_issues", "create_issue", "delete_issue")

	q := store.New(pool)
	connID := uuid.New()
	if _, err := pool.Exec(context.Background(), `insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'github', 'gh-main', 'none', '\x'::bytea, 1, '{}', '{}', 'active', now(), now())`, connID); err != nil {
		t.Fatal(err)
	}
	return sessions.New(q, reg), q, pool, connID
}

func explicit(names ...string) sessions.AllowlistInput {
	return sessions.AllowlistInput{Tools: append([]string{}, names...)}
}

func createToken(
	svc *sessions.Service,
	ctx context.Context,
	bindings map[string]uuid.UUID,
	allowlist sessions.AllowlistInput,
	ttl time.Duration,
) (string, error) {
	created, err := svc.CreateWithExpiry(ctx, bindings, allowlist, ttl)
	return created.Token, err
}

func TestSplitExposedName(t *testing.T) {
	cases := []struct {
		name        string
		alias, tool string
		ok          bool
	}{
		{"gh-main__list_issues", "gh-main", "list_issues", true},
		{"a__b__c", "a", "b__c", true},
		{"noseparator", "", "", false},
		{"Bad__tool", "", "", false},
		{"gh__Bad-Tool", "", "", false},
	}
	for _, tc := range cases {
		alias, tool, ok := sessions.SplitExposedName(tc.name)
		if ok != tc.ok || alias != tc.alias || tool != tc.tool {
			t.Fatalf("%s: got %q %q %v", tc.name, alias, tool, ok)
		}
	}
}

func TestCreateOmittedExpandsOnlyCurrentReadTools(t *testing.T) {
	svc, _, pool, connID := newSvc(t)
	token, err := createToken(svc,
		context.Background(),
		map[string]uuid.UUID{"gh-main": connID},
		sessions.AllowlistInput{},
		2*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Fatalf("token length = %d, want 64", len(token))
	}

	view, err := svc.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	binding := view.Bindings["gh-main"]
	if binding.ConnectionID != connID ||
		binding.AuthorizationGeneration != 1 ||
		binding.ActiveConnectorType != "github" {
		t.Fatalf("binding = %+v", binding)
	}
	if len(view.Grants) != 1 {
		t.Fatalf("grants = %+v, want only read grant", view.Grants)
	}
	grant := view.Grants["gh-main__list_issues"]
	if grant.Risk != connector.RiskRead {
		t.Fatalf("read grant = %+v", grant)
	}
	if until := time.Until(view.ExpiresAt); until < time.Hour || until > 2*time.Hour {
		t.Fatalf("ttl = %v", until)
	}
	if _, err := pool.Exec(
		context.Background(),
		`update connections set status = 'revoked' where id = $1`,
		connID,
	); err != nil {
		t.Fatal(err)
	}
	inactive, err := svc.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if inactive.Bindings["gh-main"].ActiveConnectorType != "" {
		t.Fatalf("inactive binding exposed connector type: %+v", inactive.Bindings["gh-main"])
	}
}

func TestCreateWithExpiryReturnsExactDatabaseTimestamp(t *testing.T) {
	svc, _, pool, connID := newSvc(t)
	created, err := svc.CreateWithExpiry(
		context.Background(),
		map[string]uuid.UUID{"gh-main": connID},
		sessions.AllowlistInput{},
		37*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Resolve(context.Background(), created.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !created.ExpiresAt.Equal(view.ExpiresAt) {
		t.Fatalf(
			"CreateWithExpiry = %s, Resolve = %s",
			created.ExpiresAt,
			view.ExpiresAt,
		)
	}
	var expiresAt, createdAt time.Time
	if err := pool.QueryRow(
		context.Background(),
		`select expires_at, created_at from mcp_sessions where id = $1`,
		view.ID,
	).Scan(&expiresAt, &createdAt); err != nil {
		t.Fatal(err)
	}
	if !created.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("returned expiry = %s, stored expiry = %s", created.ExpiresAt, expiresAt)
	}
	if ttl := expiresAt.Sub(createdAt); ttl != 37*time.Minute {
		t.Fatalf("database-calculated TTL = %s, want 37m", ttl)
	}
}

func TestCreateExplicitRiskAndEmptySemantics(t *testing.T) {
	svc, _, _, connID := newSvc(t)
	ctx := context.Background()

	token, err := createToken(svc, ctx, map[string]uuid.UUID{"gh-main": connID}, explicit(
		"gh-main__create_issue",
		"gh-main__delete_issue",
	), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Grants) != 2 {
		t.Fatalf("grants = %+v", view.Grants)
	}

	emptyToken, err := createToken(svc,
		ctx,
		map[string]uuid.UUID{"gh-main": connID},
		explicit(),
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	emptyView, err := svc.Resolve(ctx, emptyToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyView.Grants) != 0 {
		t.Fatalf("explicit [] grants = %+v, want zero tools", emptyView.Grants)
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _, pool, connID := newSvc(t)
	ctx := context.Background()
	inactiveID := uuid.New()
	if _, err := pool.Exec(ctx, `insert into connections
	  (id, connector_type, alias, auth_method, credential, secret_key_version, profile, scopes, status, created_at, updated_at)
	  values ($1, 'github', 'inactive', 'none', '\x'::bytea, 1, '{}', '{}', 'revoked', now(), now())`, inactiveID); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		bindings map[string]uuid.UUID
		input    sessions.AllowlistInput
		ttl      time.Duration
		wantCode string
	}{
		{"empty bindings", map[string]uuid.UUID{}, sessions.AllowlistInput{}, 0, "empty_bindings"},
		{"invalid alias", map[string]uuid.UUID{"Bad_Alias": connID}, sessions.AllowlistInput{}, 0, "invalid_alias"},
		{"unknown connection", map[string]uuid.UUID{"gh": uuid.New()}, sessions.AllowlistInput{}, 0, "unknown_connection"},
		{"inactive connection", map[string]uuid.UUID{"gh": inactiveID}, sessions.AllowlistInput{}, 0, "inactive_connection"},
		{"negative ttl", map[string]uuid.UUID{"gh-main": connID}, sessions.AllowlistInput{}, -time.Second, "invalid_ttl"},
		{"ttl too large", map[string]uuid.UUID{"gh-main": connID}, sessions.AllowlistInput{}, 25 * time.Hour, "invalid_ttl"},
		{"fractional-second ttl", map[string]uuid.UUID{"gh-main": connID}, sessions.AllowlistInput{}, 1500 * time.Millisecond, "invalid_ttl"},
		{"null allowlist", map[string]uuid.UUID{"gh-main": connID}, sessions.AllowlistInput{Null: true}, 0, "invalid_allowlist"},
		{"invalid exposed name", map[string]uuid.UUID{"gh-main": connID}, explicit("nounderscore"), 0, "invalid_allowlist"},
		{"unbound alias", map[string]uuid.UUID{"gh-main": connID}, explicit("other__list_issues"), 0, "invalid_allowlist"},
		{"unknown tool", map[string]uuid.UUID{"gh-main": connID}, explicit("gh-main__missing"), 0, "invalid_allowlist"},
		{"duplicate tool", map[string]uuid.UUID{"gh-main": connID}, explicit("gh-main__list_issues", "gh-main__list_issues"), 0, "invalid_allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := createToken(svc, ctx, tc.bindings, tc.input, tc.ttl)
			var validation *sessions.ValidationError
			if !errors.As(err, &validation) || validation.Code != tc.wantCode {
				t.Fatalf("want %s, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestResolveRejectsUnknownExpiredRevokedAndLegacyEnvelope(t *testing.T) {
	svc, _, pool, connID := newSvc(t)
	ctx := context.Background()

	if _, err := svc.Resolve(ctx, "deadbeef"); !errors.Is(err, sessions.ErrInvalidSession) {
		t.Fatalf("unknown token error = %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate string
	}{
		{"expired", `update mcp_sessions set expires_at = now() - interval '1 minute'`},
		{"revoked", `update mcp_sessions set status = 'revoked'`},
		{"legacy array", `update mcp_sessions set tool_allowlist = '["gh-main__list_issues"]'::jsonb`},
		{"unknown envelope field", `update mcp_sessions set tool_allowlist = '{"version":1,"tools":[],"extra":true}'::jsonb`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := createToken(svc,
				ctx,
				map[string]uuid.UUID{"gh-main": connID},
				sessions.AllowlistInput{},
				time.Hour,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, tc.mutate+` where token_hash = encode(sha256($1::bytea), 'hex')`, token); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Resolve(ctx, token); !errors.Is(err, sessions.ErrInvalidSession) {
				t.Fatalf("Resolve error = %v, want ErrInvalidSession", err)
			}
		})
	}
}

func TestAuthorizeExecutionAdversarialMatrix(t *testing.T) {
	type authCall struct {
		sessionID  uuid.UUID
		exposed    string
		connection uuid.UUID
		tool       string
		risk       connector.ToolRisk
		generation int64
	}
	for _, tc := range []struct {
		name   string
		input  sessions.AllowlistInput
		mutate func(context.Context, *pgxpool.Pool, sessions.SessionView)
		change func(*authCall)
	}{
		{name: "revoked", input: explicit("gh-main__list_issues"), mutate: execMutation(`update mcp_sessions set status = 'revoked' where id = $1`)},
		{name: "expired", input: explicit("gh-main__list_issues"), mutate: execMutation(`update mcp_sessions set expires_at = now() - interval '1 second' where id = $1`)},
		{name: "connection inactive", input: explicit("gh-main__list_issues"), mutate: func(ctx context.Context, pool *pgxpool.Pool, view sessions.SessionView) {
			_, err := pool.Exec(ctx, `update connections set status = 'revoked' where id = $1`, view.Bindings["gh-main"].ConnectionID)
			if err != nil {
				panic(err)
			}
		}},
		{name: "binding generation drift", input: explicit("gh-main__list_issues"), mutate: execMutation(`update mcp_session_connections set authorization_generation = authorization_generation + 1 where session_id = $1`)},
		{name: "current generation drift", input: explicit("gh-main__list_issues"), mutate: func(ctx context.Context, pool *pgxpool.Pool, view sessions.SessionView) {
			_, err := pool.Exec(ctx, `update connections set authorization_generation = authorization_generation + 1 where id = $1`, view.Bindings["gh-main"].ConnectionID)
			if err != nil {
				panic(err)
			}
		}},
		{name: "legacy array", input: explicit("gh-main__list_issues"), mutate: execMutation(`update mcp_sessions set tool_allowlist = '["gh-main__list_issues"]'::jsonb where id = $1`)},
		{name: "grant absent", input: explicit()},
		{name: "wrong session", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.sessionID = uuid.New() }},
		{name: "wrong alias", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.exposed = "other__list_issues" }},
		{name: "wrong connection", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.connection = uuid.New() }},
		{name: "wrong tool", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.tool = "create_issue" }},
		{name: "malformed exposed", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.exposed = "bad" }},
		{name: "risk drift", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.risk = connector.RiskWrite }},
		{name: "generation argument drift", input: explicit("gh-main__list_issues"), change: func(call *authCall) { call.generation = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, pool, connID := newSvc(t)
			ctx := context.Background()
			token, err := createToken(svc, ctx, map[string]uuid.UUID{"gh-main": connID}, tc.input, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			view, err := svc.Resolve(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(ctx, pool, view)
			}
			call := authCall{
				sessionID:  view.ID,
				exposed:    "gh-main__list_issues",
				connection: connID,
				tool:       "list_issues",
				risk:       connector.RiskRead,
				generation: 1,
			}
			if tc.change != nil {
				tc.change(&call)
			}
			err = svc.AuthorizeExecution(
				ctx,
				call.sessionID,
				call.exposed,
				call.connection,
				call.tool,
				call.risk,
				call.generation,
			)
			if !errors.Is(err, sessions.ErrExecutionUnauthorized) {
				t.Fatalf("AuthorizeExecution error = %v, want ErrExecutionUnauthorized", err)
			}
		})
	}
}

func execMutation(sql string) func(context.Context, *pgxpool.Pool, sessions.SessionView) {
	return func(ctx context.Context, pool *pgxpool.Pool, view sessions.SessionView) {
		if _, err := pool.Exec(ctx, sql, view.ID); err != nil {
			panic(err)
		}
	}
}

func TestAuthorizeExecutionAcceptsExactCurrentGrant(t *testing.T) {
	svc, _, _, connID := newSvc(t)
	ctx := context.Background()
	token, err := createToken(svc,
		ctx,
		map[string]uuid.UUID{"gh-main": connID},
		explicit("gh-main__create_issue"),
		time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.Resolve(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AuthorizeExecution(
		ctx,
		view.ID,
		"gh-main__create_issue",
		connID,
		"create_issue",
		connector.RiskWrite,
		1,
	); err != nil {
		t.Fatalf("AuthorizeExecution exact grant: %v", err)
	}
}

func TestCreateRollsBackSessionWhenBindingInsertFails(t *testing.T) {
	svc, _, pool, connID := newSvc(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		create function fail_test_session_binding() returns trigger language plpgsql as $$
		begin
			raise exception 'injected binding failure';
		end
		$$;
		create trigger fail_test_session_binding
		before insert on mcp_session_connections
		for each row execute function fail_test_session_binding()
	`); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := pool.QueryRow(ctx, `select count(*) from mcp_sessions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := createToken(svc,
		ctx,
		map[string]uuid.UUID{"gh-main": connID},
		sessions.AllowlistInput{},
		time.Hour,
	); err == nil {
		t.Fatal("Create succeeded despite injected binding failure")
	}
	var after int
	if err := pool.QueryRow(ctx, `select count(*) from mcp_sessions`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("session rows after failed create = %d, want %d", after, before)
	}
}
