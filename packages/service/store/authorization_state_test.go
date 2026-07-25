package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

var (
	fixtureIdentityDigest   = []byte("test-policy-identity-00000000000")
	fixtureDefinitionDigest = []byte("test-definition-digest-000000000")
)

// oauthDB bundles the pool, the Store primitives and the must-helpers every
// authorization-state case needs, so each case shows only the inputs and
// fences that make it different.
type oauthDB struct {
	*store.Queries
	t    *testing.T
	ctx  context.Context
	pool *pgxpool.Pool
}

func newOAuthDB(t *testing.T) *oauthDB {
	pool := testutil.NewDB(t)
	return &oauthDB{
		Queries: store.New(pool),
		t:       t,
		ctx:     context.Background(),
		pool:    pool,
	}
}

// exec reaches states no Store primitive exposes (concurrent writers, clock
// travel, schema-constraint probes).
func (db *oauthDB) exec(sql string, args ...any) {
	db.t.Helper()
	if _, err := db.pool.Exec(db.ctx, sql, args...); err != nil {
		db.t.Fatal(err)
	}
}

func (db *oauthDB) connection(id uuid.UUID) store.Connection {
	db.t.Helper()
	connection, err := db.GetConnection(db.ctx, id)
	if err != nil {
		db.t.Fatal(err)
	}
	return connection
}

func (db *oauthDB) authorization(stateHash string) store.OauthAuthorization {
	db.t.Helper()
	authorization, err := db.GetOAuthAuthorizationByStateHash(db.ctx, stateHash)
	if err != nil {
		db.t.Fatal(err)
	}
	return authorization
}

// activeConnection is an already-authorized OAuth Connection sitting at
// credential_version 1 / authorization_generation 1.
func (db *oauthDB) activeConnection() store.Connection {
	db.t.Helper()
	return insertConnection(db.t, db.pool, connectionFixture{
		Alias:      ptr("github-" + uuid.NewString()),
		Credential: []byte("credential-v1"),
		Scopes:     []string{"repo:read"},
	})
}

// beginInitial starts an initial flow on a brand-new pending Connection at the
// connector policy identity the fixture pins.
func (db *oauthDB) beginInitial(
	connectionID, authorizationID uuid.UUID,
	stateHash string,
	expiresAt time.Time,
) (store.BeginInitialOAuthAuthorizationAtPolicyIdentityRow, error) {
	db.t.Helper()
	db.exec(
		`insert into connector_policy_identities (
		   connector_type, identity_version, identity_digest,
		   definition_digest, initialized, created_at, updated_at
		 ) values ('github', 1, $1, $2, true, current_timestamp, current_timestamp)
		 on conflict (connector_type) do nothing`,
		fixtureIdentityDigest,
		fixtureDefinitionDigest,
	)
	alias := "github-" + connectionID.String()
	return db.BeginInitialOAuthAuthorizationAtPolicyIdentity(
		db.ctx,
		store.BeginInitialOAuthAuthorizationAtPolicyIdentityParams{
			ConnectionID:                  connectionID,
			ConnectorType:                 "github",
			ExpectedIdentityVersion:       1,
			ExpectedIdentityDigest:        fixtureIdentityDigest,
			ExpectedDefinitionDigest:      fixtureDefinitionDigest,
			ConnectionAlias:               &alias,
			AuthMethod:                    "oauth",
			EmptyCredential:               []byte{},
			ConnectionSecretKeyVersion:    1,
			RequestedScopes:               []string{"repo:read"},
			AuthorizationID:               authorizationID,
			StateHash:                     stateHash,
			ContextCiphertext:             []byte("encrypted-context"),
			ExpiresAt:                     expiresAt,
			AuthorizationSecretKeyVersion: 1,
			RedirectUrl:                   "https://app.example/oauth/done",
		},
	)
}

func (db *oauthDB) mustBeginInitial(
	connectionID uuid.UUID,
	stateHash string,
	expiresAt time.Time,
) uuid.UUID {
	db.t.Helper()
	authorizationID := uuid.New()
	if _, err := db.beginInitial(
		connectionID,
		authorizationID,
		stateHash,
		expiresAt,
	); err != nil {
		db.t.Fatal(err)
	}
	return authorizationID
}

// beginReauth reads the Connection's current fences first, the way the service
// layer does, so a case only names the attempt it is starting.
func (db *oauthDB) beginReauth(
	connectionID, authorizationID uuid.UUID,
	stateHash string,
	expiresAt time.Time,
) (store.BeginOAuthReauthorizationRow, error) {
	db.t.Helper()
	current := db.connection(connectionID)
	return db.BeginOAuthReauthorization(
		db.ctx,
		store.BeginOAuthReauthorizationParams{
			ConnectionID:                           connectionID,
			ExpectedCurrentAttemptVersion:          current.AuthorizationAttemptVersion,
			ExpectedCurrentAuthorizationGeneration: current.AuthorizationGeneration,
			AuthorizationID:                        authorizationID,
			StateHash:                              stateHash,
			ContextCiphertext:                      []byte("encrypted-context"),
			ExpiresAt:                              expiresAt,
			RequestedScopes:                        []string{"repo:read", "repo:write"},
			AuthorizationSecretKeyVersion:          1,
			RedirectUrl:                            "https://app.example/oauth/done",
		},
	)
}

func (db *oauthDB) mustBeginReauth(
	connectionID, authorizationID uuid.UUID,
	stateHash string,
	expiresAt time.Time,
) store.BeginOAuthReauthorizationRow {
	db.t.Helper()
	row, err := db.beginReauth(connectionID, authorizationID, stateHash, expiresAt)
	if err != nil {
		db.t.Fatal(err)
	}
	return row
}

func (db *oauthDB) mustClaim(stateHash string) {
	db.t.Helper()
	if _, err := db.ClaimOAuthAuthorization(db.ctx, stateHash); err != nil {
		db.t.Fatal(err)
	}
}

func (db *oauthDB) acquireLeaseFor(
	connectionID, owner uuid.UUID,
	generation int64,
	leaseSeconds int32,
) (store.AcquireRefreshLeaseRow, error) {
	return db.AcquireRefreshLease(db.ctx, store.AcquireRefreshLeaseParams{
		ConnectionID:                    connectionID,
		Owner:                           &owner,
		LeaseSeconds:                    leaseSeconds,
		ExpectedAuthorizationGeneration: generation,
	})
}

func (db *oauthDB) acquireLease(
	connectionID, owner uuid.UUID,
	generation int64,
) (store.AcquireRefreshLeaseRow, error) {
	return db.acquireLeaseFor(connectionID, owner, generation, 30)
}

func (db *oauthDB) mustAcquireLease(
	connectionID, owner uuid.UUID,
	generation int64,
) store.AcquireRefreshLeaseRow {
	db.t.Helper()
	lease, err := db.acquireLease(connectionID, owner, generation)
	if err != nil {
		db.t.Fatal(err)
	}
	return lease
}

func (db *oauthDB) markRequesting(
	connectionID, owner uuid.UUID,
	credentialVersion, generation int64,
) error {
	_, err := db.MarkRefreshLeaseRequesting(
		db.ctx,
		store.MarkRefreshLeaseRequestingParams{
			ConnectionID:                    connectionID,
			Owner:                           &owner,
			ExpectedCredentialVersion:       credentialVersion,
			ExpectedAuthorizationGeneration: generation,
		},
	)
	return err
}

const bumpAuthorizationGeneration = `update connections
	set authorization_generation = authorization_generation + 1
	where id = $1`

func TestConnectionVersionSnapshotAndWholeGroupRecredentialCAS(t *testing.T) {
	db := newOAuthDB(t)
	connection := db.activeConnection()

	if connection.CredentialVersion != 1 ||
		connection.AuthorizationGeneration != 1 ||
		connection.AuthorizationAttemptVersion != 0 ||
		connection.ScopesKnown {
		t.Fatalf("new Connection versions/defaults = %+v", connection)
	}

	snapshot := db.connection(connection.ID)
	if snapshot.CredentialVersion != 1 ||
		snapshot.AuthorizationGeneration != 1 ||
		snapshot.AuthorizationAttemptVersion != 0 ||
		snapshot.Status != "active" {
		t.Fatalf("version snapshot = %+v", snapshot)
	}
	if _, err := db.GetConnectionAtAuthorizationGeneration(
		db.ctx,
		store.GetConnectionAtAuthorizationGenerationParams{
			ConnectionID:                    connection.ID,
			ExpectedAuthorizationGeneration: 1,
		},
	); err != nil {
		t.Fatal(err)
	}

	expiresAt := time.Now().Add(time.Hour).UTC()
	updated, err := db.RecredentialConnection(db.ctx, store.RecredentialConnectionParams{
		ConnectionID:                    connection.ID,
		ExpectedCredentialVersion:       snapshot.CredentialVersion,
		ExpectedAuthorizationGeneration: snapshot.AuthorizationGeneration,
		Credential:                      []byte("credential-v2"),
		SecretKeyVersion:                2,
		Profile:                         []byte(`{"account":"new"}`),
		Scopes:                          []string{"repo:read", "repo:write"},
		ScopesKnown:                     true,
		AccessTokenExpiresAt:            &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CredentialVersion != 2 || updated.AuthorizationGeneration != 2 {
		t.Fatalf("recredential versions = %+v, want 2/2", updated)
	}

	// Replaying the same expected versions must not overwrite the new snapshot.
	_, err = db.RecredentialConnection(db.ctx, store.RecredentialConnectionParams{
		ConnectionID:                    connection.ID,
		ExpectedCredentialVersion:       snapshot.CredentialVersion,
		ExpectedAuthorizationGeneration: snapshot.AuthorizationGeneration,
		Credential:                      []byte("stale"),
		SecretKeyVersion:                9,
		Profile:                         []byte(`{"account":"stale"}`),
		Scopes:                          []string{"admin"},
		ScopesKnown:                     true,
	})
	requireNoRows(t, err)

	got := db.connection(connection.ID)
	if string(got.Credential) != "credential-v2" ||
		string(got.Profile) != `{"account": "new"}` &&
			string(got.Profile) != `{"account":"new"}` ||
		len(got.Scopes) != 2 ||
		!got.ScopesKnown ||
		got.SecretKeyVersion != 2 ||
		got.CredentialVersion != 2 ||
		got.AuthorizationGeneration != 2 {
		t.Fatalf("whole credential snapshot = %+v", got)
	}
	_, err = db.GetConnectionAtAuthorizationGeneration(
		db.ctx,
		store.GetConnectionAtAuthorizationGenerationParams{
			ConnectionID:                    connection.ID,
			ExpectedAuthorizationGeneration: 1,
		},
	)
	requireNoRows(t, err)
}

func TestConnectionOAuthSchemaRejectsInvalidVersionsAndAuthorizationLinks(t *testing.T) {
	db := newOAuthDB(t)
	connection := db.activeConnection()

	for _, statement := range []string{
		`update connections set credential_version = 0 where id = $1`,
		`update connections set authorization_generation = 0 where id = $1`,
		`update connections set authorization_attempt_version = -1 where id = $1`,
		`update connections
		 set refresh_owner = '00000000-0000-0000-0000-000000000001'::uuid,
		     refresh_lease_until = current_timestamp + interval '1 minute',
		     refresh_state = 'unknown'
		 where id = $1`,
	} {
		if _, err := db.pool.Exec(db.ctx, statement, connection.ID); err == nil {
			t.Fatalf("constraint unexpectedly accepted statement: %s", statement)
		}
	}

	for _, invalid := range []struct {
		name         string
		connectionID uuid.UUID
		attempt      int64
		generation   int64
		flowKind     string
	}{
		{"zero attempt", connection.ID, 0, 1, "initial"},
		{"zero generation", connection.ID, 1, 0, "initial"},
		{"invalid flow", connection.ID, 1, 1, "other"},
		{"missing connection", uuid.New(), 1, 1, "initial"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			_, err := db.pool.Exec(db.ctx, `insert into oauth_authorizations (
				id, connector_type, state_hash, context_ciphertext, connection_id,
				status, expires_at, created_at, auth_method, alias,
				secret_key_version, redirect_url, attempt_version,
				expected_authorization_generation, flow_kind, requested_scopes
			) values (
				$1, 'github', $2, '\x01'::bytea, $3, 'pending',
				current_timestamp + interval '1 hour', current_timestamp,
				'oauth', '', 1, 'https://app.example/oauth/done', $4, $5, $6,
				'{}'::text[]
			)`,
				uuid.New(),
				"invalid-"+uuid.NewString(),
				invalid.connectionID,
				invalid.attempt,
				invalid.generation,
				invalid.flowKind,
			)
			if err == nil {
				t.Fatal("invalid authorization unexpectedly succeeded")
			}
		})
	}
}

func TestInitialOAuthBeginAndCallbackClaimAreAtomicAndFenced(t *testing.T) {
	db := newOAuthDB(t)

	connectionID := uuid.New()
	authorizationID := uuid.New()
	stateHash := "state-" + uuid.NewString()
	begin, err := db.beginInitial(
		connectionID,
		authorizationID,
		stateHash,
		time.Now().Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if begin.ConnectionID != connectionID ||
		begin.AuthorizationID != authorizationID ||
		begin.AttemptVersion != 1 ||
		begin.ExpectedAuthorizationGeneration != 1 {
		t.Fatalf("initial begin = %+v", begin)
	}

	connection := db.connection(connectionID)
	authorization := db.authorization(stateHash)
	if connection.Status != "pending" ||
		connection.AuthorizationAttemptVersion != 1 ||
		connection.AuthorizationGeneration != 1 ||
		authorization.ConnectionID != connectionID ||
		authorization.FlowKind != "initial" ||
		authorization.Status != "pending" ||
		authorization.AttemptVersion != 1 ||
		authorization.ExpectedAuthorizationGeneration != 1 {
		t.Fatalf("initial rows connection=%+v authorization=%+v", connection, authorization)
	}
	if len(authorization.RequestedScopes) != 1 ||
		authorization.RequestedScopes[0] != "repo:read" {
		t.Fatalf("initial requested scopes = %v", authorization.RequestedScopes)
	}

	// A failure in the authorization INSERT must roll back the Connection
	// INSERT from the same statement.
	rolledBackConnectionID := uuid.New()
	if _, err := db.beginInitial(
		rolledBackConnectionID,
		uuid.New(),
		stateHash,
		time.Now().Add(time.Hour),
	); err == nil {
		t.Fatal("duplicate state unexpectedly succeeded")
	}
	if _, err := db.GetConnection(db.ctx, rolledBackConnectionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rolled-back Connection lookup error = %v", err)
	}

	// Concurrent callbacks race on one conditional pending -> claimed update.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimErr := db.ClaimOAuthAuthorization(db.ctx, stateHash)
			results <- claimErr
		}()
	}
	wg.Wait()
	close(results)
	successes, noRows := 0, 0
	for claimErr := range results {
		switch {
		case claimErr == nil:
			successes++
		case errors.Is(claimErr, pgx.ErrNoRows):
			noRows++
		default:
			t.Fatalf("claim error = %v", claimErr)
		}
	}
	if successes != 1 || noRows != 1 {
		t.Fatalf("concurrent claim successes=%d no_rows=%d, want 1/1", successes, noRows)
	}
	_, err = db.ClaimOAuthAuthorization(db.ctx, stateHash)
	requireNoRows(t, err)

	// Each remaining case ruins exactly one claim precondition.
	expiredState := "state-expired-" + uuid.NewString()
	db.mustBeginInitial(uuid.New(), expiredState, time.Now().Add(-time.Minute))
	_, err = db.ClaimOAuthAuthorization(db.ctx, expiredState)
	requireNoRows(t, err)

	wrongStatusConnectionID := uuid.New()
	wrongStatusState := "state-status-" + uuid.NewString()
	db.mustBeginInitial(wrongStatusConnectionID, wrongStatusState, time.Now().Add(time.Hour))
	db.exec(`update connections set status = 'active' where id = $1`, wrongStatusConnectionID)
	_, err = db.ClaimOAuthAuthorization(db.ctx, wrongStatusState)
	requireNoRows(t, err)

	staleConnectionID := uuid.New()
	staleState := "state-stale-" + uuid.NewString()
	db.mustBeginInitial(staleConnectionID, staleState, time.Now().Add(time.Hour))
	db.exec(bumpAuthorizationGeneration, staleConnectionID)
	_, err = db.ClaimOAuthAuthorization(db.ctx, staleState)
	requireNoRows(t, err)
	if changed, err := db.MarkSupersededOAuthAuthorizations(db.ctx); err != nil || changed < 1 {
		t.Fatalf("mark superseded rows=%d err=%v", changed, err)
	}
	if status := db.authorization(staleState).Status; status != "superseded" {
		t.Fatalf("stale authorization status = %q", status)
	}
}

func TestOAuthReauthAttemptsCompletionAndFailureCleanup(t *testing.T) {
	db := newOAuthDB(t)
	connection := db.activeConnection()
	future := time.Now().Add(time.Hour)

	firstState := "reauth-first-" + uuid.NewString()
	first := db.mustBeginReauth(connection.ID, uuid.New(), firstState, future)
	if first.AttemptVersion != 1 || first.ExpectedAuthorizationGeneration != 2 {
		t.Fatalf("first reauth = %+v, want attempt/generation 1/2", first)
	}

	secondID := uuid.New()
	secondState := "reauth-second-" + uuid.NewString()
	second := db.mustBeginReauth(connection.ID, secondID, secondState, future)
	if second.AttemptVersion != 2 || second.ExpectedAuthorizationGeneration != 3 {
		t.Fatalf("second reauth = %+v, want attempt/generation 2/3", second)
	}
	secondAuthorization := db.authorization(secondState)
	if len(secondAuthorization.RequestedScopes) != 2 ||
		secondAuthorization.RequestedScopes[0] != "repo:read" ||
		secondAuthorization.RequestedScopes[1] != "repo:write" {
		t.Fatalf("reauth requested scopes = %v", secondAuthorization.RequestedScopes)
	}
	// The superseded first attempt can no longer be claimed.
	_, err := db.ClaimOAuthAuthorization(db.ctx, firstState)
	requireNoRows(t, err)

	// If authorization INSERT fails, the attempt/generation bump is part of
	// the same statement and must be rolled back.
	beforeConflict := db.connection(connection.ID)
	if _, err := db.beginReauth(connection.ID, uuid.New(), secondState, future); err == nil {
		t.Fatal("duplicate reauth state unexpectedly succeeded")
	}
	afterConflict := db.connection(connection.ID)
	if afterConflict.AuthorizationAttemptVersion != beforeConflict.AuthorizationAttemptVersion ||
		afterConflict.AuthorizationGeneration != beforeConflict.AuthorizationGeneration {
		t.Fatalf("failed BeginReauth changed versions: before=%+v after=%+v", beforeConflict, afterConflict)
	}

	db.mustClaim(secondState)
	// A normal refresh can write credential_version while reauth is in flight.
	db.exec(`update connections
		set credential = 'refresh-v2'::bytea,
		    credential_version = credential_version + 1
		where id = $1`, connection.ID)
	completed, err := db.CompleteOAuthAuthorizationCAS(
		db.ctx,
		store.CompleteOAuthAuthorizationCASParams{
			AuthorizationID:      secondID,
			Credential:           []byte("reauth-v3"),
			SecretKeyVersion:     3,
			Profile:              []byte(`{"account":"reauthorized"}`),
			Scopes:               []string{"repo:read", "repo:write"},
			ScopesKnown:          true,
			AccessTokenExpiresAt: nil,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// Begin moved generation 1 -> 2 -> 3. Completion moves 3 -> 4 to
	// invalidate Sessions that may have been created after Begin while the old
	// credential was still present. Credential is 1 -> refresh 2 -> completion 3.
	if completed.CredentialVersion != 3 || completed.AuthorizationGeneration != 4 {
		t.Fatalf("completion versions = %+v, want credential/generation 3/4", completed)
	}
	got := db.connection(connection.ID)
	completedAuthorization := db.authorization(secondState)
	if got.Status != "active" ||
		string(got.Credential) != "reauth-v3" ||
		!got.ScopesKnown ||
		got.SecretKeyVersion != 3 ||
		completedAuthorization.Status != "completed" {
		t.Fatalf("completed connection=%+v authorization=%+v", got, completedAuthorization)
	}

	// A completion whose generation moved on must change nothing at all.
	staleID := uuid.New()
	staleState := "reauth-stale-" + uuid.NewString()
	db.mustBeginReauth(connection.ID, staleID, staleState, future)
	db.mustClaim(staleState)
	db.exec(bumpAuthorizationGeneration, connection.ID)
	beforeStaleCompletion := db.connection(connection.ID)
	_, err = db.CompleteOAuthAuthorizationCAS(
		db.ctx,
		store.CompleteOAuthAuthorizationCASParams{
			AuthorizationID:  staleID,
			Credential:       []byte("must-not-win"),
			SecretKeyVersion: 9,
			Profile:          []byte(`{"account":"wrong"}`),
			Scopes:           []string{"admin"},
			ScopesKnown:      true,
		},
	)
	requireNoRows(t, err)
	afterStaleCompletion := db.connection(connection.ID)
	staleAuthorization := db.authorization(staleState)
	if string(afterStaleCompletion.Credential) != string(beforeStaleCompletion.Credential) ||
		afterStaleCompletion.CredentialVersion != beforeStaleCompletion.CredentialVersion ||
		staleAuthorization.Status != "claimed" {
		t.Fatalf(
			"failed completion was not atomic: before=%+v after=%+v authz=%+v",
			beforeStaleCompletion, afterStaleCompletion, staleAuthorization,
		)
	}

	failedID := uuid.New()
	db.mustBeginReauth(connection.ID, failedID, "reauth-failed-"+uuid.NewString(), future)
	if rows, err := db.FailReauthOAuthAuthorization(db.ctx, failedID); err != nil || rows != 1 {
		t.Fatalf("fail reauth rows=%d err=%v", rows, err)
	}

	expiredState := "reauth-expired-" + uuid.NewString()
	db.mustBeginReauth(connection.ID, uuid.New(), expiredState, time.Now().Add(-time.Minute))
	if rows, err := db.ExpireReauthOAuthAuthorizations(db.ctx); err != nil || rows < 1 {
		t.Fatalf("expire reauth rows=%d err=%v", rows, err)
	}
	if status := db.connection(connection.ID).Status; status != "active" {
		t.Fatalf("reauth cleanup changed active Connection status to %q", status)
	}
	if status := db.authorization(expiredState).Status; status != "expired" {
		t.Fatalf("expired reauth status = %q", status)
	}
}

func TestInitialOAuthFailureAndExpiryDeleteOnlyMatchingPendingConnection(t *testing.T) {
	db := newOAuthDB(t)

	connectionID := uuid.New()
	stateHash := "cleanup-" + uuid.NewString()
	authorizationID := db.mustBeginInitial(connectionID, stateHash, time.Now().Add(time.Hour))
	if rows, err := db.CleanupInitialOAuthAuthorization(db.ctx, authorizationID); err != nil || rows != 1 {
		t.Fatalf("cleanup initial rows=%d err=%v", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, connectionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cleaned Connection lookup error = %v", err)
	}
	if _, err := db.GetOAuthAuthorizationByStateHash(db.ctx, stateHash); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cascaded authorization lookup error = %v", err)
	}

	expiredConnectionID := uuid.New()
	db.mustBeginInitial(
		expiredConnectionID,
		"cleanup-expired-"+uuid.NewString(),
		time.Now().Add(-time.Minute),
	)
	if rows, err := db.CleanupExpiredInitialOAuthAuthorizations(db.ctx); err != nil || rows != 1 {
		t.Fatalf("cleanup expired initial rows=%d err=%v", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, expiredConnectionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired Connection lookup error = %v", err)
	}

	// A stale cleanup cannot delete a Connection whose version/status has
	// advanced since the attempt was created.
	protectedConnectionID := uuid.New()
	protectedAuthorizationID := db.mustBeginInitial(
		protectedConnectionID,
		"cleanup-protected-"+uuid.NewString(),
		time.Now().Add(time.Hour),
	)
	db.exec(`update connections
		set status = 'active',
		    authorization_generation = authorization_generation + 1
		where id = $1`, protectedConnectionID)
	if rows, err := db.CleanupInitialOAuthAuthorization(
		db.ctx,
		protectedAuthorizationID,
	); err != nil || rows != 0 {
		t.Fatalf("stale cleanup rows=%d err=%v, want 0", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, protectedConnectionID); err != nil {
		t.Fatalf("protected Connection missing: %v", err)
	}
}

func TestClaimedOAuthRecoveryGraceAndVersionFences(t *testing.T) {
	db := newOAuthDB(t)
	const recoveryGraceSeconds int32 = 120
	future := time.Now().Add(time.Hour)

	// Expiring an authorization by hand is how the grace boundary is probed.
	expireAuthorization := func(authorizationID uuid.UUID, secondsAgo int) {
		db.t.Helper()
		db.exec(
			`update oauth_authorizations
			 set expires_at = CURRENT_TIMESTAMP
			   - make_interval(secs => $2::double precision)
			 where id = $1`,
			authorizationID,
			secondsAgo,
		)
	}

	initialConnectionID := uuid.New()
	initialState := "claimed-initial-" + uuid.NewString()
	initialID := db.mustBeginInitial(initialConnectionID, initialState, future)
	db.mustClaim(initialState)

	expireAuthorization(initialID, 119)
	if rows, err := db.CleanupStaleClaimedInitialOAuthAuthorizations(
		db.ctx,
		recoveryGraceSeconds,
	); err != nil || rows != 0 {
		t.Fatalf("within-grace claimed initial cleanup rows=%d err=%v", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, initialConnectionID); err != nil {
		t.Fatalf("within-grace callback lost its pending Connection: %v", err)
	}

	expireAuthorization(initialID, 121)
	if rows, err := db.CleanupStaleClaimedInitialOAuthAuthorizations(
		db.ctx,
		recoveryGraceSeconds,
	); err != nil || rows != 1 {
		t.Fatalf("stale claimed initial cleanup rows=%d err=%v", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, initialConnectionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale claimed initial Connection lookup error = %v", err)
	}

	// Recovering a claimed reauth must never touch the live credential.
	active := db.activeConnection()
	credentialBefore := string(active.Credential)
	reauthID := uuid.New()
	reauthState := "claimed-reauth-" + uuid.NewString()
	db.mustBeginReauth(active.ID, reauthID, reauthState, future)
	db.mustClaim(reauthState)
	expireAuthorization(reauthID, 121)
	if rows, err := db.ExpireStaleClaimedReauthOAuthAuthorizations(
		db.ctx,
		recoveryGraceSeconds,
	); err != nil || rows != 1 {
		t.Fatalf("stale claimed reauth expiry rows=%d err=%v", rows, err)
	}
	afterRecovery := db.connection(active.ID)
	if afterRecovery.Status != "active" ||
		string(afterRecovery.Credential) != credentialBefore {
		t.Fatalf("reauth recovery changed active credential: %+v", afterRecovery)
	}
	if status := db.authorization(reauthState).Status; status != "expired" {
		t.Fatalf("recovered reauth status = %q", status)
	}

	// A newer attempt fences the janitor off the older expired one.
	staleID := uuid.New()
	staleState := "claimed-stale-version-" + uuid.NewString()
	db.mustBeginReauth(active.ID, staleID, staleState, future)
	db.mustClaim(staleState)
	expireAuthorization(staleID, 121)
	currentState := "current-version-" + uuid.NewString()
	db.mustBeginReauth(active.ID, uuid.New(), currentState, future)
	if rows, err := db.ExpireStaleClaimedReauthOAuthAuthorizations(
		db.ctx,
		recoveryGraceSeconds,
	); err != nil || rows != 0 {
		t.Fatalf("stale version recovery rows=%d err=%v", rows, err)
	}
	stale, current := db.authorization(staleState), db.authorization(currentState)
	if stale.Status != "claimed" || current.Status != "pending" {
		t.Fatalf(
			"version fence changed attempts: stale=%q current=%q",
			stale.Status,
			current.Status,
		)
	}

	supersededConnectionID := uuid.New()
	supersededState := "superseded-initial-" + uuid.NewString()
	db.mustBeginInitial(supersededConnectionID, supersededState, future)
	db.mustClaim(supersededState)
	db.exec(bumpAuthorizationGeneration, supersededConnectionID)
	if _, err := db.MarkSupersededOAuthAuthorizations(db.ctx); err != nil {
		t.Fatal(err)
	}
	// A defensively simulated current live attempt protects the Connection,
	// even though application code cannot start another initial flow on the
	// same pending Connection.
	currentInitialID := uuid.New()
	db.exec(
		`insert into oauth_authorizations (
		   id, connector_type, state_hash, context_ciphertext, connection_id,
		   status, expires_at, created_at, auth_method, alias,
		   secret_key_version, redirect_url, attempt_version,
		   expected_authorization_generation, flow_kind, requested_scopes
		 ) values (
		   $1, 'github', $2, '\x01'::bytea, $3, 'pending',
		   CURRENT_TIMESTAMP + interval '1 hour', CURRENT_TIMESTAMP,
		   'oauth', '', 1, '', 1, 2, 'initial', '{}'::text[]
		 )`,
		currentInitialID,
		"current-initial-"+uuid.NewString(),
		supersededConnectionID,
	)
	if rows, err := db.CleanupSupersededInitialOAuthAuthorizations(
		db.ctx,
	); err != nil || rows != 0 {
		t.Fatalf("live-attempt protected cleanup rows=%d err=%v", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, supersededConnectionID); err != nil {
		t.Fatalf("current attempt did not protect Connection: %v", err)
	}
	db.exec(`delete from oauth_authorizations where id = $1`, currentInitialID)
	if rows, err := db.CleanupSupersededInitialOAuthAuthorizations(
		db.ctx,
	); err != nil || rows != 1 {
		t.Fatalf("superseded initial cleanup rows=%d err=%v", rows, err)
	}
	if _, err := db.GetConnection(db.ctx, supersededConnectionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("superseded initial Connection lookup error = %v", err)
	}
}

func TestRefreshLeaseStateMachineAndCASFences(t *testing.T) {
	db := newOAuthDB(t)
	connection := db.activeConnection()
	owner1, owner2 := uuid.New(), uuid.New()

	// The lease window is clamped inside SQL, not by the caller.
	for _, leaseSeconds := range []int32{29, 601} {
		_, err := db.acquireLeaseFor(connection.ID, owner1, 1, leaseSeconds)
		requireNoRows(t, err)
	}

	lease := db.mustAcquireLease(connection.ID, owner1, 1)
	if lease.CredentialVersion != 1 ||
		lease.AuthorizationGeneration != 1 ||
		lease.RefreshOwner == nil ||
		*lease.RefreshOwner != owner1 ||
		lease.RefreshState == nil ||
		*lease.RefreshState != "leased" ||
		lease.RefreshLeaseUntil == nil {
		t.Fatalf("acquired lease = %+v", lease)
	}
	var leaseIsFuture bool
	if err := db.pool.QueryRow(db.ctx,
		`select refresh_lease_until > current_timestamp from connections where id = $1`,
		connection.ID,
	).Scan(&leaseIsFuture); err != nil {
		t.Fatal(err)
	}
	if !leaseIsFuture {
		t.Fatal("lease deadline is not in the future according to database time")
	}

	// A live lease excludes every other owner from every lease primitive.
	_, err := db.acquireLease(connection.ID, owner2, 1)
	requireNoRows(t, err)
	if _, err := db.GetRefreshLeaseCredential(
		db.ctx,
		store.GetRefreshLeaseCredentialParams{
			ConnectionID:                    connection.ID,
			Owner:                           &owner1,
			ExpectedCredentialVersion:       1,
			ExpectedAuthorizationGeneration: 1,
		},
	); err != nil {
		t.Fatal(err)
	}
	_, err = db.GetRefreshLeaseCredential(
		db.ctx,
		store.GetRefreshLeaseCredentialParams{
			ConnectionID:                    connection.ID,
			Owner:                           &owner1,
			ExpectedCredentialVersion:       2,
			ExpectedAuthorizationGeneration: 1,
		},
	)
	requireNoRows(t, err)

	requireNoRows(t, db.markRequesting(connection.ID, owner2, 1, 1))
	if err := db.markRequesting(connection.ID, owner1, 1, 1); err != nil {
		t.Fatal(err)
	}

	_, err = db.CompleteRefreshLeaseCAS(db.ctx, store.CompleteRefreshLeaseCASParams{
		ConnectionID:                    connection.ID,
		Owner:                           &owner2,
		ExpectedCredentialVersion:       1,
		ExpectedAuthorizationGeneration: 1,
		Credential:                      []byte("wrong-owner"),
		SecretKeyVersion:                2,
		Scopes:                          []string{"repo:read"},
		ScopesKnown:                     true,
	})
	requireNoRows(t, err)

	// A token-only refresh advances the credential but not the authorization.
	refreshed, err := db.CompleteRefreshLeaseCAS(db.ctx, store.CompleteRefreshLeaseCASParams{
		ConnectionID:                    connection.ID,
		Owner:                           &owner1,
		ExpectedCredentialVersion:       1,
		ExpectedAuthorizationGeneration: 1,
		Credential:                      []byte("credential-v2"),
		SecretKeyVersion:                2,
		Scopes:                          []string{"repo:read"},
		ScopesKnown:                     true,
		AuthorizationFactsChanged:       false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.CredentialVersion != 2 || refreshed.AuthorizationGeneration != 1 {
		t.Fatalf("token-only refresh versions = %+v, want 2/1", refreshed)
	}
	afterRefresh := db.connection(connection.ID)
	if afterRefresh.RefreshOwner != nil ||
		afterRefresh.RefreshLeaseUntil != nil ||
		afterRefresh.RefreshState != nil {
		t.Fatalf("completed refresh retained lease = %+v", afterRefresh)
	}

	// A refresh that changed authorization facts also bumps the generation.
	lease = db.mustAcquireLease(connection.ID, owner2, 1)
	if err := db.markRequesting(
		connection.ID,
		owner2,
		lease.CredentialVersion,
		lease.AuthorizationGeneration,
	); err != nil {
		t.Fatal(err)
	}
	refreshed, err = db.CompleteRefreshLeaseCAS(db.ctx, store.CompleteRefreshLeaseCASParams{
		ConnectionID:                    connection.ID,
		Owner:                           &owner2,
		ExpectedCredentialVersion:       lease.CredentialVersion,
		ExpectedAuthorizationGeneration: lease.AuthorizationGeneration,
		Credential:                      []byte("credential-v3"),
		SecretKeyVersion:                3,
		Scopes:                          []string{"repo:read", "repo:write"},
		ScopesKnown:                     true,
		AuthorizationFactsChanged:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.CredentialVersion != 3 || refreshed.AuthorizationGeneration != 2 {
		t.Fatalf("authorization-changing refresh versions = %+v, want 3/2", refreshed)
	}

	// Clear only releases the state the caller proved it observed.
	owner3 := uuid.New()
	lease = db.mustAcquireLease(connection.ID, owner3, 2)
	for _, clear := range []struct {
		state string
		rows  int64
	}{{"requesting", 0}, {"leased", 1}} {
		rows, err := db.ClearRefreshLeaseCAS(db.ctx, store.ClearRefreshLeaseCASParams{
			ConnectionID:                    connection.ID,
			Owner:                           &owner3,
			ExpectedRefreshState:            ptr(clear.state),
			ExpectedCredentialVersion:       lease.CredentialVersion,
			ExpectedAuthorizationGeneration: lease.AuthorizationGeneration,
		})
		if err != nil || rows != clear.rows {
			t.Fatalf("clear %q rows=%d err=%v, want %d", clear.state, rows, err, clear.rows)
		}
	}

	// An expired *leased* lease may be taken over; an expired *requesting* one
	// may not, because a Provider request may already be in flight.
	db.mustAcquireLease(connection.ID, owner3, 2)
	db.exec(`update connections
		set refresh_lease_until = current_timestamp - interval '1 second'
		where id = $1`, connection.ID)
	owner4 := uuid.New()
	lease, err = db.acquireLease(connection.ID, owner4, 2)
	if err != nil {
		t.Fatalf("expired leased takeover: %v", err)
	}
	if lease.RefreshOwner == nil || *lease.RefreshOwner != owner4 {
		t.Fatalf("takeover owner = %+v, want %s", lease.RefreshOwner, owner4)
	}

	db.exec(`update connections
		set refresh_state = 'requesting',
		    refresh_lease_until = current_timestamp - interval '1 second'
		where id = $1`, connection.ID)
	_, err = db.acquireLease(connection.ID, uuid.New(), 2)
	requireNoRows(t, err)
	if rows, err := db.ResolveExpiredRequestingRefreshLeases(db.ctx); err != nil || rows != 1 {
		t.Fatalf("resolve uncertain requesting rows=%d err=%v", rows, err)
	}
	uncertain := db.connection(connection.ID)
	if uncertain.Status != "reauth_required" ||
		uncertain.RefreshOwner != nil ||
		uncertain.RefreshLeaseUntil != nil ||
		uncertain.RefreshState != nil {
		t.Fatalf("uncertain requesting resolution = %+v", uncertain)
	}

	// The schema rejects partial or unrecognized lease triples even if a
	// future caller bypasses the Store primitive.
	if _, err := db.pool.Exec(db.ctx, `update connections
		set refresh_state = 'leased'
		where id = $1`, connection.ID); err == nil {
		t.Fatal("partial refresh lease unexpectedly satisfied schema constraint")
	}
}

func TestRefreshInvalidGrantAndConcurrentLeaseAcquisition(t *testing.T) {
	db := newOAuthDB(t)

	invalidGrantConnection := db.activeConnection()
	owner := uuid.New()
	lease := db.mustAcquireLease(invalidGrantConnection.ID, owner, 1)
	if err := db.markRequesting(
		invalidGrantConnection.ID,
		owner,
		lease.CredentialVersion,
		lease.AuthorizationGeneration,
	); err != nil {
		t.Fatal(err)
	}
	markReauthRequired := func(owner uuid.UUID, state string) int64 {
		t.Helper()
		rows, err := db.MarkRefreshLeaseReauthRequiredCAS(
			db.ctx,
			store.MarkRefreshLeaseReauthRequiredCASParams{
				ConnectionID:                    invalidGrantConnection.ID,
				Owner:                           &owner,
				ExpectedRefreshState:            ptr(state),
				ExpectedCredentialVersion:       lease.CredentialVersion,
				ExpectedAuthorizationGeneration: lease.AuthorizationGeneration,
			},
		)
		if err != nil {
			t.Fatalf("invalid_grant %q: %v", state, err)
		}
		return rows
	}
	if rows := markReauthRequired(uuid.New(), "requesting"); rows != 0 {
		t.Fatalf("wrong-owner invalid_grant rows=%d, want 0", rows)
	}
	if rows := markReauthRequired(owner, "leased"); rows != 0 {
		t.Fatalf("wrong-state invalid_grant rows=%d, want 0", rows)
	}
	if rows := markReauthRequired(owner, "requesting"); rows != 1 {
		t.Fatalf("invalid_grant rows=%d, want 1", rows)
	}
	afterInvalidGrant := db.connection(invalidGrantConnection.ID)
	if afterInvalidGrant.Status != "reauth_required" ||
		afterInvalidGrant.RefreshState != nil {
		t.Fatalf("invalid_grant result = %+v", afterInvalidGrant)
	}

	concurrentConnection := db.activeConnection()
	owners := []uuid.UUID{uuid.New(), uuid.New()}
	type acquireResult struct {
		owner uuid.UUID
		err   error
	}
	results := make(chan acquireResult, len(owners))
	var wg sync.WaitGroup
	for _, currentOwner := range owners {
		wg.Add(1)
		go func(ownerID uuid.UUID) {
			defer wg.Done()
			_, acquireErr := db.acquireLease(concurrentConnection.ID, ownerID, 1)
			results <- acquireResult{owner: ownerID, err: acquireErr}
		}(currentOwner)
	}
	wg.Wait()
	close(results)

	var winner uuid.UUID
	successes, noRows := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result.owner
		case errors.Is(result.err, pgx.ErrNoRows):
			noRows++
		default:
			t.Fatalf("concurrent acquire error = %v", result.err)
		}
	}
	if successes != 1 || noRows != 1 {
		t.Fatalf("concurrent acquire successes=%d no_rows=%d, want 1/1", successes, noRows)
	}
	got := db.connection(concurrentConnection.ID)
	if got.RefreshOwner == nil || *got.RefreshOwner != winner {
		t.Fatalf("persisted refresh owner = %+v, winner = %s", got.RefreshOwner, winner)
	}
}
