package configsvc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

// initialGoogleAdsPublic 是 policy 集成用例的基准配置：mcp_url / client_id 是
// policy 字段，project_id 只做展示与校验，足以覆盖“该失效”与“不该失效”两侧。
func initialGoogleAdsPublic() map[string]any {
	return map[string]any{
		"client_id":  "client-a",
		"project_id": "111",
		"mcp_url":    "https://mcp-a.example/mcp",
	}
}

func putGoogleAds(
	t *testing.T,
	svc *configsvc.Service,
	public map[string]any,
	secrets map[string]string,
	ifMatch time.Time,
) configsvc.ConfigView {
	t.Helper()
	view, err := svc.Put(context.Background(), "google_ads", public, secrets, ifMatch)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func insertPolicyConnection(
	t *testing.T,
	pool *pgxpool.Pool,
	connectorType connector.Type,
) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(
		context.Background(),
		`insert into connections (
		   id, connector_type, alias, auth_method, credential,
		   secret_key_version, profile, scopes, status, created_at, updated_at
		 ) values ($1, $2, $3, 'oauth', '\x'::bytea, 1, '{}', '{}',
		           'active', now(), now())`,
		id,
		string(connectorType),
		"policy-"+id.String(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func authorizationGeneration(
	t *testing.T,
	pool *pgxpool.Pool,
	connectionID uuid.UUID,
) int64 {
	t.Helper()
	var generation int64
	if err := pool.QueryRow(
		context.Background(),
		`select authorization_generation from connections where id = $1`,
		connectionID,
	).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func policyDigests(
	t *testing.T,
	pool *pgxpool.Pool,
	connectorType connector.Type,
) (identity, definition []byte) {
	t.Helper()
	if err := pool.QueryRow(
		context.Background(),
		`select identity_digest, definition_digest
		   from connector_policy_identities
		  where connector_type = $1 and initialized`,
		string(connectorType),
	).Scan(&identity, &definition); err != nil {
		t.Fatal(err)
	}
	return identity, definition
}

func policyIdentityFingerprint(
	t *testing.T,
	pool *pgxpool.Pool,
	connectorType connector.Type,
) string {
	t.Helper()
	var fingerprint string
	if err := pool.QueryRow(
		t.Context(),
		`select coalesce(
		     (select to_jsonb(policy)::text
		        from connector_policy_identities as policy
		       where connector_type = $1),
		     'missing'
		   )`,
		string(connectorType),
	).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

func TestResolvedPolicyDriftFailsClosedUntilStartupReconcile(
	t *testing.T,
) {
	// 只保留两条真正需要 Postgres 的路径：整行缺失（pgx.ErrNoRows 分支）与
	// 一条存在但不匹配的行。version / definition / initialized 各种不匹配组合
	// 由纯函数测试 TestMatchesPolicyIdentityRequiresExactPersistedState 覆盖。
	drifts := []struct {
		name string
		sql  string
	}{
		{
			name: "missing",
			sql: `delete from connector_policy_identities
			       where connector_type = 'google_ads'`,
		},
		{
			name: "identity digest mismatch",
			sql: `update connector_policy_identities
			        set identity_digest = decode(repeat('aa', 32), 'hex')
			      where connector_type = 'google_ads'`,
		},
	}
	for _, drift := range drifts {
		t.Run(drift.name, func(t *testing.T) {
			svc, pool := newRWService(
				t,
				configsvc.Fixture("google_ads", "https://tokens.example/token"),
			)
			putGoogleAds(
				t,
				svc,
				initialGoogleAdsPublic(),
				map[string]string{"client_secret": "secret-a"},
				time.Time{},
			)
			connectionID := insertPolicyConnection(t, pool, "google_ads")
			if _, err := pool.Exec(t.Context(), drift.sql); err != nil {
				t.Fatal(err)
			}
			beforePolicy := policyIdentityFingerprint(t, pool, "google_ads")
			beforeGeneration := authorizationGeneration(t, pool, connectionID)

			if _, _, err := svc.ResolvedWithPolicy(
				t.Context(),
				"google_ads",
			); !errors.Is(err, configsvc.ErrPolicyDrift) {
				t.Fatalf(
					"ResolvedWithPolicy error = %v, want ErrPolicyDrift",
					err,
				)
			}
			if _, err := svc.Resolved(
				t.Context(),
				"google_ads",
			); !errors.Is(err, configsvc.ErrPolicyDrift) {
				t.Fatalf("Resolved error = %v, want ErrPolicyDrift", err)
			}
			if after := policyIdentityFingerprint(
				t,
				pool,
				"google_ads",
			); after != beforePolicy {
				t.Fatalf(
					"ordinary read changed policy identity:\nbefore %s\nafter  %s",
					beforePolicy,
					after,
				)
			}
			if got := authorizationGeneration(
				t,
				pool,
				connectionID,
			); got != beforeGeneration {
				t.Fatalf(
					"ordinary read generation = %d, want unchanged %d",
					got,
					beforeGeneration,
				)
			}

			results, err := svc.ReconcilePolicyIdentities(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 ||
				!results[0].Changed ||
				results[0].ConnectionsBumped != 1 {
				t.Fatalf("startup reconcile result = %+v", results)
			}
			if _, _, err := svc.ResolvedWithPolicy(
				t.Context(),
				"google_ads",
			); err != nil {
				t.Fatalf("read after startup reconcile: %v", err)
			}
			if got := authorizationGeneration(
				t,
				pool,
				connectionID,
			); got != beforeGeneration+1 {
				t.Fatalf(
					"startup reconcile generation = %d, want %d",
					got,
					beforeGeneration+1,
				)
			}
		})
	}
}

func TestGoogleAdsPolicyPutDeleteBumpsOnlyAuthorizationSemanticChanges(t *testing.T) {
	svc, pool := newRWService(
		t,
		configsvc.Fixture("google_ads", "https://tokens.example/token"),
	)
	ctx := context.Background()
	first := putGoogleAds(
		t,
		svc,
		initialGoogleAdsPublic(),
		map[string]string{"client_secret": "secret-a"},
		time.Time{},
	)
	if first.Public["allow_insecure_http"] != "false" {
		t.Fatalf("normalized default was not persisted: %+v", first.Public)
	}
	connectionID := insertPolicyConnection(t, pool, "google_ads")

	if err := store.New(pool).SetConnectorConfigVerified(
		ctx,
		store.SetConnectorConfigVerifiedParams{
			ConnectorType: "google_ads",
			Endpoint:      stringPointer("https://mcp-a.example:443/mcp"),
		},
	); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Get(ctx, "google_ads")
	if err != nil {
		t.Fatal(err)
	}

	// A display/validation-only project field and client-secret rotation are
	// not connector-wide endpoint/account policy changes.
	nonPolicy := initialGoogleAdsPublic()
	nonPolicy["project_id"] = "222"
	second := putGoogleAds(
		t,
		svc,
		nonPolicy,
		map[string]string{"client_secret": "secret-b"},
		first.UpdatedAt,
	)
	if got := authorizationGeneration(t, pool, connectionID); got != 1 {
		t.Fatalf("non-policy update generation = %d, want 1", got)
	}
	var verifiedEndpoint *string
	if err := pool.QueryRow(
		ctx,
		`select mcp_verified_endpoint from connector_configs
		  where connector_type = 'google_ads'`,
	).Scan(&verifiedEndpoint); err != nil {
		t.Fatal(err)
	}
	if verifiedEndpoint == nil || *verifiedEndpoint != "https://mcp-a.example:443/mcp" {
		t.Fatal("non-policy update unexpectedly cleared MCP verification")
	}

	endpointUpdate := initialGoogleAdsPublic()
	endpointUpdate["project_id"] = "222"
	endpointUpdate["mcp_url"] = "https://mcp-b.example/mcp"
	third := putGoogleAds(t, svc, endpointUpdate, nil, second.UpdatedAt)
	if got := authorizationGeneration(t, pool, connectionID); got != 2 {
		t.Fatalf("mcp_url update generation = %d, want 2", got)
	}
	if err := pool.QueryRow(
		ctx,
		`select mcp_verified_endpoint from connector_configs
		  where connector_type = 'google_ads'`,
	).Scan(&verifiedEndpoint); err != nil {
		t.Fatal(err)
	}
	if verifiedEndpoint != nil {
		t.Fatal("endpoint policy update did not clear MCP verification")
	}

	networkUpdate := map[string]any{}
	for key, value := range endpointUpdate {
		networkUpdate[key] = value
	}
	networkUpdate["allow_insecure_http"] = "true"
	fourth := putGoogleAds(t, svc, networkUpdate, nil, third.UpdatedAt)
	if got := authorizationGeneration(t, pool, connectionID); got != 3 {
		t.Fatalf("network flag update generation = %d, want 3", got)
	}

	clientUpdate := map[string]any{}
	for key, value := range networkUpdate {
		clientUpdate[key] = value
	}
	clientUpdate["client_id"] = "client-b"
	putGoogleAds(t, svc, clientUpdate, nil, fourth.UpdatedAt)
	if got := authorizationGeneration(t, pool, connectionID); got != 4 {
		t.Fatalf("OAuth client update generation = %d, want 4", got)
	}

	if err := svc.Delete(ctx, "google_ads"); err != nil {
		t.Fatal(err)
	}
	if got := authorizationGeneration(t, pool, connectionID); got != 5 {
		t.Fatalf("policy-bearing delete generation = %d, want 5", got)
	}
	if _, err := svc.Get(ctx, "google_ads"); !errors.Is(err, configsvc.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}

	identity, definition := policyDigests(t, pool, "google_ads")
	if len(identity) != 32 || len(definition) != 32 {
		t.Fatalf("persisted digest lengths = %d/%d", len(identity), len(definition))
	}
	if bytes.Contains(identity, []byte("secret-b")) ||
		bytes.Contains(definition, []byte("secret-b")) {
		t.Fatal("policy persistence contains secret plaintext")
	}
}

func TestPolicyPutIfMatchAndFailureRollbackConfigIdentityAndGeneration(t *testing.T) {
	svc, pool := newRWService(
		t,
		configsvc.Fixture("google_ads", "https://tokens.example/token"),
	)
	ctx := context.Background()
	first := putGoogleAds(
		t,
		svc,
		initialGoogleAdsPublic(),
		map[string]string{"client_secret": "secret-a"},
		time.Time{},
	)
	connectionID := insertPolicyConnection(t, pool, "google_ads")
	beforeIdentity, beforeDefinition := policyDigests(t, pool, "google_ads")

	stale := first.UpdatedAt.Add(-time.Second)
	changed := initialGoogleAdsPublic()
	changed["mcp_url"] = "https://mcp-stale.example/mcp"
	if _, err := svc.Put(ctx, "google_ads", changed, nil, stale); !errors.Is(
		err,
		configsvc.ErrConflict,
	) {
		t.Fatalf("stale If-Match error = %v, want ErrConflict", err)
	}
	if got := authorizationGeneration(t, pool, connectionID); got != 1 {
		t.Fatalf("If-Match conflict generation = %d, want 1", got)
	}

	if _, err := pool.Exec(ctx, `
create function reject_policy_generation_bump() returns trigger
language plpgsql as $$
begin
  if new.authorization_generation <> old.authorization_generation then
    raise exception 'injected generation bump failure';
  end if;
  return new;
end
$$;
create trigger reject_policy_generation_bump
before update on connections
for each row execute function reject_policy_generation_bump()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Put(ctx, "google_ads", changed, nil, first.UpdatedAt); err == nil {
		t.Fatal("injected generation failure unexpectedly committed")
	}

	view, err := svc.Get(ctx, "google_ads")
	if err != nil {
		t.Fatal(err)
	}
	if view.Public["mcp_url"] != "https://mcp-a.example:443/mcp" ||
		!view.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("failed transaction leaked config update: %+v", view)
	}
	if got := authorizationGeneration(t, pool, connectionID); got != 1 {
		t.Fatalf("failed transaction generation = %d, want 1", got)
	}
	afterIdentity, afterDefinition := policyDigests(t, pool, "google_ads")
	if !bytes.Equal(beforeIdentity, afterIdentity) ||
		!bytes.Equal(beforeDefinition, afterDefinition) {
		t.Fatal("failed transaction leaked policy identity update")
	}
}

func TestPolicyPutInterleavingNeverPublishesNewEndpointWithOldGeneration(t *testing.T) {
	svc, pool := newRWService(
		t,
		configsvc.Fixture("google_ads", "https://tokens.example/token"),
	)
	ctx := context.Background()
	first := putGoogleAds(
		t,
		svc,
		initialGoogleAdsPublic(),
		map[string]string{"client_secret": "secret-a"},
		time.Time{},
	)
	connectionID := insertPolicyConnection(t, pool, "google_ads")

	const gateLock int64 = 73100421
	const signalLock int64 = 73100422
	gateTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gateTx.Rollback(ctx)
	if _, err := gateTx.Exec(ctx, `select pg_advisory_xact_lock($1)`, gateLock); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
create function block_policy_config_update() returns trigger
language plpgsql as $$
begin
  perform pg_advisory_lock(%d);
  perform pg_advisory_xact_lock(%d);
  perform pg_advisory_unlock(%d);
  return new;
end
$$;
create trigger block_policy_config_update
before update on connector_configs
for each row execute function block_policy_config_update()`,
		signalLock,
		gateLock,
		signalLock,
	)); err != nil {
		t.Fatal(err)
	}

	updated := initialGoogleAdsPublic()
	updated["mcp_url"] = "https://mcp-b.example/mcp"
	putResult := make(chan error, 1)
	go func() {
		_, err := svc.Put(ctx, "google_ads", updated, nil, first.UpdatedAt)
		putResult <- err
	}()

	probe, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Release()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var acquired bool
		if err := probe.QueryRow(
			ctx,
			`select pg_try_advisory_lock($1)`,
			signalLock,
		).Scan(&acquired); err != nil {
			t.Fatal(err)
		}
		if !acquired {
			blocked = true
			break
		}
		if _, err := probe.Exec(ctx, `select pg_advisory_unlock($1)`, signalLock); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("Put did not reach deterministic transaction gate")
	}

	staleUpdate := initialGoogleAdsPublic()
	staleUpdate["mcp_url"] = "https://mcp-stale.example/mcp"
	staleResult := make(chan error, 1)
	go func() {
		_, err := svc.Put(ctx, "google_ads", staleUpdate, nil, first.UpdatedAt)
		staleResult <- err
	}()
	select {
	case err := <-staleResult:
		t.Fatalf("stale writer escaped policy/config lock before winner commit: %v", err)
	case <-time.After(100 * time.Millisecond):
		// Expected: it waits on the same connector policy sentinel.
	}

	var visibleEndpoint string
	var visibleGeneration int64
	if err := pool.QueryRow(ctx, `
select cc.public_config->>'mcp_url', c.authorization_generation
from connector_configs cc
join connections c on c.connector_type = cc.connector_type
where cc.connector_type = 'google_ads' and c.id = $1`,
		connectionID,
	).Scan(&visibleEndpoint, &visibleGeneration); err != nil {
		t.Fatal(err)
	}
	if visibleEndpoint != "https://mcp-a.example:443/mcp" || visibleGeneration != 1 {
		t.Fatalf(
			"during blocked writer saw endpoint/generation %q/%d, want old/1",
			visibleEndpoint,
			visibleGeneration,
		)
	}

	if err := gateTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-putResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Put did not complete after gate release")
	}
	select {
	case err := <-staleResult:
		if !errors.Is(err, configsvc.ErrConflict) {
			t.Fatalf("serialized stale writer error = %v, want ErrConflict", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale writer did not resolve after winner commit")
	}
	if err := pool.QueryRow(ctx, `
select cc.public_config->>'mcp_url', c.authorization_generation
from connector_configs cc
join connections c on c.connector_type = cc.connector_type
where cc.connector_type = 'google_ads' and c.id = $1`,
		connectionID,
	).Scan(&visibleEndpoint, &visibleGeneration); err != nil {
		t.Fatal(err)
	}
	if visibleEndpoint != "https://mcp-b.example:443/mcp" || visibleGeneration != 2 {
		t.Fatalf(
			"after commit saw endpoint/generation %q/%d, want new/2",
			visibleEndpoint,
			visibleGeneration,
		)
	}
}

func TestPolicyReconcileAuditsStaticDefinitionAndLegacyCutover(t *testing.T) {
	definitionV1 := configsvc.Fixture("google_ads", "https://tokens-a.example/token")
	svcV1, pool := newRWService(t, definitionV1)
	putGoogleAds(
		t,
		svcV1,
		initialGoogleAdsPublic(),
		map[string]string{"client_secret": "secret-a"},
		time.Time{},
	)
	connectionID := insertPolicyConnection(t, pool, "google_ads")

	definitionV2 := configsvc.Fixture("google_ads", "https://tokens-b.example/token")
	registryV2 := registry.New()
	registryV2.MustRegister(definitionV2)
	svcV2 := configsvc.New(store.New(pool), registryV2, testKeyring(t))
	results, err := svcV2.ReconcilePolicyIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 ||
		!results[0].Changed ||
		!results[0].PreviousKnown ||
		!results[0].DefinitionChanged ||
		results[0].ConnectionsBumped != 1 {
		t.Fatalf("static reconcile result = %+v", results)
	}
	if got := authorizationGeneration(t, pool, connectionID); got != 2 {
		t.Fatalf("static Definition reconcile generation = %d, want 2", got)
	}
	results, err = svcV2.ReconcilePolicyIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Changed || results[0].ConnectionsBumped != 0 {
		t.Fatalf("idempotent reconcile result = %+v", results)
	}

	// Simulate a legacy installation upgraded to 0006: no persisted identity.
	if _, err := pool.Exec(
		context.Background(),
		`delete from connector_policy_identities where connector_type = 'google_ads'`,
	); err != nil {
		t.Fatal(err)
	}
	results, err = svcV2.ReconcilePolicyIdentities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 ||
		!results[0].Changed ||
		results[0].PreviousKnown ||
		results[0].ConnectionsBumped != 1 {
		t.Fatalf("legacy reconcile result = %+v", results)
	}
	if got := authorizationGeneration(t, pool, connectionID); got != 3 {
		t.Fatalf("legacy reconcile generation = %d, want 3", got)
	}
}

func TestDeleteWithoutPolicyFieldsDoesNotInvalidateSessions(t *testing.T) {
	definition := connector.Definition{
		Type:                "display_config",
		Name:                "Display Config",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{{
			Key:       "display_name",
			Label:     "Display name",
			InputType: connector.InputText,
		}},
	}
	svc, pool := newRWService(t, definition)
	putGoogleAdsLike, err := svc.Put(
		context.Background(),
		"display_config",
		map[string]any{"display_name": "before"},
		nil,
		time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if putGoogleAdsLike.Public["display_name"] != "before" {
		t.Fatal("display config setup failed")
	}
	connectionID := insertPolicyConnection(t, pool, "display_config")
	if err := svc.Delete(context.Background(), "display_config"); err != nil {
		t.Fatal(err)
	}
	if got := authorizationGeneration(t, pool, connectionID); got != 1 {
		t.Fatalf("non-policy delete generation = %d, want 1", got)
	}
}

func stringPointer(value string) *string { return &value }
