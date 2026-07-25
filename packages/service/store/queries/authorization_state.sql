-- Connection credential/authorization version primitives.

-- name: GetConnectionAtAuthorizationGeneration :one
SELECT *
FROM connections
WHERE id = @connection_id
  AND authorization_generation = @expected_authorization_generation;

-- name: RecredentialConnection :one
UPDATE connections
SET credential = @credential,
    secret_key_version = @secret_key_version,
    profile = @profile,
    scopes = @scopes,
    scopes_known = @scopes_known,
    status = 'active',
    access_token_expires_at = sqlc.narg(access_token_expires_at),
    credential_version = credential_version + 1,
    authorization_generation = authorization_generation + 1,
    refresh_owner = NULL,
    refresh_lease_until = NULL,
    refresh_state = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation
  AND status <> 'pending'
RETURNING id, credential_version, authorization_generation;

-- Initial authorization preparation may perform inbound verification and
-- build a provider URL before touching the database. Bind its insert to the
-- exact connector policy used for that work. The shared row lock has the same
-- ordering guarantee as CreateConnectionAtPolicyIdentity.
-- name: BeginInitialOAuthAuthorizationAtPolicyIdentity :one
WITH matched_policy AS (
  SELECT policy.connector_type
  FROM connector_policy_identities AS policy
  WHERE policy.connector_type = @connector_type
    AND policy.initialized
    AND policy.identity_version = @expected_identity_version
    AND policy.identity_digest = @expected_identity_digest
    AND policy.definition_digest = @expected_definition_digest
  FOR SHARE
),
created_connection AS (
  INSERT INTO connections (
    id,
    connector_type,
    alias,
    auth_method,
    credential,
    secret_key_version,
    profile,
    scopes,
    scopes_known,
    status,
    access_token_expires_at,
    credential_version,
    authorization_generation,
    authorization_attempt_version,
    created_at,
    updated_at
  )
  SELECT
    @connection_id,
    @connector_type,
    sqlc.narg(connection_alias),
    @auth_method,
    @empty_credential,
    @connection_secret_key_version,
    '{}'::jsonb,
    @requested_scopes,
    false,
    'pending',
    NULL,
    1,
    1,
    1,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
  FROM matched_policy
  RETURNING
    id,
    authorization_attempt_version,
    authorization_generation
),
created_authorization AS (
  INSERT INTO oauth_authorizations (
    id,
    connector_type,
    state_hash,
    context_ciphertext,
    context_version,
    connection_id,
    status,
    expires_at,
    created_at,
    auth_method,
    alias,
    secret_key_version,
    redirect_url,
    attempt_version,
    expected_authorization_generation,
    flow_kind,
    requested_scopes
  )
  SELECT
    @authorization_id,
    @connector_type,
    @state_hash,
    @context_ciphertext,
    1,
    c.id,
    'pending',
    @expires_at,
    CURRENT_TIMESTAMP,
    @auth_method,
    COALESCE(sqlc.narg(connection_alias)::text, ''),
    @authorization_secret_key_version,
    @redirect_url,
    c.authorization_attempt_version,
    c.authorization_generation,
    'initial',
    @requested_scopes
  FROM created_connection AS c
  RETURNING
    id,
    connection_id,
    attempt_version,
    expected_authorization_generation
)
SELECT
  a.connection_id,
  a.id AS authorization_id,
  a.attempt_version,
  a.expected_authorization_generation
FROM created_authorization AS a;

-- A new reauth attempt immediately supersedes older attempts by advancing
-- both the attempt and authorization generation before inserting its row.
-- name: BeginOAuthReauthorization :one
WITH bumped_connection AS (
  UPDATE connections AS target
  SET authorization_attempt_version = authorization_attempt_version + 1,
      authorization_generation = authorization_generation + 1,
      updated_at = CURRENT_TIMESTAMP
  WHERE target.id = @connection_id
    AND target.status IN ('active', 'reauth_required')
    AND target.authorization_attempt_version =
        @expected_current_attempt_version
    AND target.authorization_generation =
        @expected_current_authorization_generation
  RETURNING
    target.id,
    target.connector_type,
    target.auth_method,
    COALESCE(target.alias, '') AS authorization_alias,
    target.authorization_attempt_version,
    target.authorization_generation
),
created_authorization AS (
  INSERT INTO oauth_authorizations (
    id,
    connector_type,
    state_hash,
    context_ciphertext,
    context_version,
    connection_id,
    status,
    expires_at,
    created_at,
    auth_method,
    alias,
    secret_key_version,
    redirect_url,
    attempt_version,
    expected_authorization_generation,
    flow_kind,
    requested_scopes
  )
  SELECT
    @authorization_id,
    c.connector_type,
    @state_hash,
    @context_ciphertext,
    1,
    c.id,
    'pending',
    @expires_at,
    CURRENT_TIMESTAMP,
    c.auth_method,
    c.authorization_alias,
    @authorization_secret_key_version,
    @redirect_url,
    c.authorization_attempt_version,
    c.authorization_generation,
    'reauth',
    @requested_scopes
  FROM bumped_connection AS c
  RETURNING
    id,
    connection_id,
    attempt_version,
    expected_authorization_generation
)
SELECT
  a.connection_id,
  a.id AS authorization_id,
  a.attempt_version,
  a.expected_authorization_generation
FROM created_authorization AS a;

-- Claim is the only transition that permits network token exchange. It uses
-- database time and current Connection attempt/generation state.
-- name: ClaimOAuthAuthorization :one
UPDATE oauth_authorizations AS a
SET status = 'claimed'
FROM connections AS c
WHERE a.state_hash = @state_hash
  AND a.status = 'pending'
  AND a.expires_at > CURRENT_TIMESTAMP
  AND c.id = a.connection_id
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation
  AND (
    (a.flow_kind = 'initial' AND c.status = 'pending')
    OR
    (a.flow_kind = 'reauth' AND c.status IN ('active', 'reauth_required'))
  )
RETURNING a.*;

-- OAuth completion updates the entire Connection credential snapshot and
-- marks the claimed authorization completed in the same statement. It does
-- not compare credential_version: a token-only refresh may finish during a
-- browser reauth flow. Completion advances authorization_generation again:
-- Begin already exposed a new generation while the old credential remained,
-- so any Session created in that interval must be invalidated once the new
-- credential/account snapshot commits.
-- name: CompleteOAuthAuthorizationCAS :one
WITH candidate AS MATERIALIZED (
  SELECT
    a.id,
    a.connection_id,
    a.attempt_version,
    a.expected_authorization_generation,
    a.flow_kind
  FROM oauth_authorizations AS a
  WHERE a.id = @authorization_id
    AND a.status = 'claimed'
  FOR UPDATE
),
updated_connection AS (
  UPDATE connections AS c
  SET credential = @credential,
      secret_key_version = @secret_key_version,
      profile = @profile,
      scopes = @scopes,
      scopes_known = @scopes_known,
      status = 'active',
      access_token_expires_at = sqlc.narg(access_token_expires_at),
      credential_version = c.credential_version + 1,
      authorization_generation = c.authorization_generation + 1,
      refresh_owner = NULL,
      refresh_lease_until = NULL,
      refresh_state = NULL,
      updated_at = CURRENT_TIMESTAMP
  FROM candidate AS a
  WHERE c.id = a.connection_id
    AND c.authorization_attempt_version = a.attempt_version
    AND c.authorization_generation = a.expected_authorization_generation
    AND (
      (a.flow_kind = 'initial' AND c.status = 'pending')
      OR
      (a.flow_kind = 'reauth' AND c.status IN ('active', 'reauth_required'))
    )
  RETURNING
    c.id AS connection_id,
    c.credential_version,
    c.authorization_generation,
    a.id AS authorization_id
),
completed_authorization AS (
  UPDATE oauth_authorizations AS a
  SET status = 'completed'
  FROM updated_connection AS c
  WHERE a.id = c.authorization_id
    AND a.status = 'claimed'
  RETURNING a.id
)
SELECT
  c.connection_id,
  c.credential_version,
  c.authorization_generation
FROM updated_connection AS c
JOIN completed_authorization AS a
  ON a.id = c.authorization_id;

-- Initial failures/expiry remove only the still-matching pending Connection;
-- the authorization is removed by the FK cascade.
-- name: CleanupInitialOAuthAuthorization :execrows
DELETE FROM connections AS c
USING oauth_authorizations AS a
WHERE a.id = @authorization_id
  AND a.flow_kind = 'initial'
  AND a.status IN ('pending', 'claimed')
  AND c.id = a.connection_id
  AND c.status = 'pending'
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation;

-- name: CleanupExpiredInitialOAuthAuthorizations :execrows
DELETE FROM connections AS c
USING oauth_authorizations AS a
WHERE a.flow_kind = 'initial'
  AND a.status = 'pending'
  AND a.expires_at <= CURRENT_TIMESTAMP
  AND c.id = a.connection_id
  AND c.status = 'pending'
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation;

-- A callback claimed immediately before expires_at is allowed to finish.
-- Only recover a crashed/abandoned claim after a fixed caller-supplied grace
-- which must be wider than the bounded token-exchange + validator window.
-- The Connection version predicates fence this janitor from newer attempts.
-- name: CleanupStaleClaimedInitialOAuthAuthorizations :execrows
DELETE FROM connections AS c
USING oauth_authorizations AS a
WHERE a.flow_kind = 'initial'
  AND a.status = 'claimed'
  AND @recovery_grace_seconds::integer BETWEEN 60 AND 600
  AND a.expires_at
      + make_interval(secs => @recovery_grace_seconds::int)
      <= CURRENT_TIMESTAMP
  AND c.id = a.connection_id
  AND c.status = 'pending'
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation;

-- Reauth failure ends only the attempt; it never deletes or downgrades the
-- active Connection.
-- name: FailReauthOAuthAuthorization :execrows
UPDATE oauth_authorizations AS a
SET status = 'failed'
FROM connections AS c
WHERE a.id = @authorization_id
  AND a.flow_kind = 'reauth'
  AND a.status IN ('pending', 'claimed')
  AND c.id = a.connection_id
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation;

-- name: ExpireReauthOAuthAuthorizations :execrows
UPDATE oauth_authorizations AS a
SET status = 'expired'
FROM connections AS c
WHERE a.flow_kind = 'reauth'
  AND a.status = 'pending'
  AND a.expires_at <= CURRENT_TIMESTAMP
  AND c.id = a.connection_id
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation;

-- Claimed reauth callbacks get the same recovery grace as initial callbacks.
-- Recovery only closes the matching attempt and never changes the active
-- Connection or its credential.
-- name: ExpireStaleClaimedReauthOAuthAuthorizations :execrows
UPDATE oauth_authorizations AS a
SET status = 'expired'
FROM connections AS c
WHERE a.flow_kind = 'reauth'
  AND a.status = 'claimed'
  AND @recovery_grace_seconds::integer BETWEEN 60 AND 600
  AND a.expires_at
      + make_interval(secs => @recovery_grace_seconds::int)
      <= CURRENT_TIMESTAMP
  AND c.id = a.connection_id
  AND c.status IN ('active', 'reauth_required')
  AND c.authorization_attempt_version = a.attempt_version
  AND c.authorization_generation = a.expected_authorization_generation;

-- name: MarkSupersededOAuthAuthorizations :execrows
UPDATE oauth_authorizations AS a
SET status = 'superseded'
FROM connections AS c
WHERE a.connection_id = c.id
  AND a.status IN ('pending', 'claimed')
  AND (
    a.attempt_version <> c.authorization_attempt_version
    OR
    a.expected_authorization_generation <> c.authorization_generation
  );

-- A policy/config generation bump may supersede an already claimed initial
-- attempt. Initial Connections cannot start a newer flow while pending, so
-- the unchanged attempt_version plus absence of a current live authorization
-- safely identifies the otherwise orphaned pending Connection. Generation is
-- intentionally not compared: its drift is why this row was superseded.
-- name: CleanupSupersededInitialOAuthAuthorizations :execrows
DELETE FROM connections AS c
USING oauth_authorizations AS a
WHERE a.connection_id = c.id
  AND a.flow_kind = 'initial'
  AND a.status = 'superseded'
  AND c.status = 'pending'
  AND c.authorization_attempt_version = a.attempt_version
  AND NOT EXISTS (
    SELECT 1
    FROM oauth_authorizations AS current_attempt
    WHERE current_attempt.connection_id = c.id
      AND current_attempt.status IN ('pending', 'claimed')
      AND current_attempt.attempt_version =
          c.authorization_attempt_version
      AND current_attempt.expected_authorization_generation =
          c.authorization_generation
  );

-- Refresh lease primitives. Network I/O happens only after Acquire +
-- Read + MarkRequesting have committed.

-- name: AcquireRefreshLease :one
UPDATE connections
SET refresh_owner = @owner,
    refresh_lease_until = CURRENT_TIMESTAMP
      + make_interval(secs => @lease_seconds::int),
    refresh_state = 'leased',
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND status = 'active'
  AND authorization_generation = @expected_authorization_generation
  AND @lease_seconds BETWEEN 30 AND 600
  AND (
    refresh_state IS NULL
    OR (
      refresh_state = 'leased'
      AND refresh_lease_until <= CURRENT_TIMESTAMP
    )
  )
RETURNING
  id,
  credential_version,
  authorization_generation,
  refresh_owner,
  refresh_lease_until,
  refresh_state;

-- name: GetRefreshLeaseCredential :one
SELECT *
FROM connections
WHERE id = @connection_id
  AND refresh_owner = @owner
  AND refresh_state IN ('leased', 'requesting')
  AND refresh_lease_until > CURRENT_TIMESTAMP
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation;

-- name: MarkRefreshLeaseRequesting :one
UPDATE connections
SET refresh_state = 'requesting',
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND refresh_owner = @owner
  AND refresh_state = 'leased'
  AND refresh_lease_until > CURRENT_TIMESTAMP
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation
RETURNING refresh_lease_until;

-- name: CompleteRefreshLeaseCAS :one
UPDATE connections
SET credential = @credential,
    secret_key_version = @secret_key_version,
    scopes = @scopes,
    scopes_known = @scopes_known,
    status = 'active',
    access_token_expires_at = sqlc.narg(access_token_expires_at),
    credential_version = credential_version + 1,
    authorization_generation = authorization_generation
      + CASE WHEN @authorization_facts_changed::boolean THEN 1 ELSE 0 END,
    refresh_owner = NULL,
    refresh_lease_until = NULL,
    refresh_state = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND refresh_owner = @owner
  AND refresh_state = 'requesting'
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation
RETURNING id, credential_version, authorization_generation;

-- Only use this when the caller can prove no Provider request was sent or a
-- definitive protocol response proves that no token was issued/rotated.
-- name: ClearRefreshLeaseCAS :execrows
UPDATE connections
SET refresh_owner = NULL,
    refresh_lease_until = NULL,
    refresh_state = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND refresh_owner = @owner
  AND refresh_state = @expected_refresh_state
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation;

-- A definitive credential-invalid outcome for this exact lease: either an
-- invalid_grant answer to the requesting exchange, or an expired credential
-- with no refresh token, found while the lease is still merely leased. The
-- caller names the state it observed so a lease in the other state is fenced.
-- name: MarkRefreshLeaseReauthRequiredCAS :execrows
UPDATE connections
SET status = 'reauth_required',
    refresh_owner = NULL,
    refresh_lease_until = NULL,
    refresh_state = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND refresh_owner = @owner
  AND refresh_state = @expected_refresh_state
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation;

-- A backend may mark an active Connection reauth_required only after a
-- definitive credential-invalid response for the exact credential snapshot it
-- used. A stale response must not mutate a newer credential or authorization
-- generation.
-- name: MarkConnectionCredentialInvalidCAS :execrows
UPDATE connections
SET status = 'reauth_required',
    refresh_owner = NULL,
    refresh_lease_until = NULL,
    refresh_state = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = @connection_id
  AND status = 'active'
  AND credential_version = @expected_credential_version
  AND authorization_generation = @expected_authorization_generation;

-- Default no-replay policy: an expired lease that may have reached the
-- Provider becomes reauth_required instead of replaying a rotating token.
-- name: ResolveExpiredRequestingRefreshLeases :execrows
UPDATE connections
SET status = 'reauth_required',
    refresh_owner = NULL,
    refresh_lease_until = NULL,
    refresh_state = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE status = 'active'
  AND refresh_state = 'requesting'
  AND refresh_lease_until <= CURRENT_TIMESTAMP;
