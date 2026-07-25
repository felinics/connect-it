-- Create only if the connector-wide policy still exactly matches the
-- configuration used by the slow credential validator. FOR SHARE linearizes
-- this INSERT against configsvc's FOR UPDATE writer: a writer that commits
-- first makes the snapshot mismatch; a writer that commits second sees and
-- bumps this newly inserted Connection.
-- name: CreateConnectionAtPolicyIdentity :one
WITH matched_policy AS (
  SELECT policy.connector_type
  FROM connector_policy_identities AS policy
  WHERE policy.connector_type = @connector_type
    AND policy.initialized
    AND policy.identity_version = @expected_identity_version
    AND policy.identity_digest = @expected_identity_digest
    AND policy.definition_digest = @expected_definition_digest
  FOR SHARE
)
INSERT INTO connections (
  id, connector_type, alias, auth_method, credential, secret_key_version,
  profile, scopes, scopes_known, status, access_token_expires_at,
  created_at, updated_at
)
SELECT
  @id, @connector_type, sqlc.narg(alias), @auth_method, @credential,
  @secret_key_version, @profile, @scopes, @scopes_known, @status,
  sqlc.narg(access_token_expires_at), now(), now()
FROM matched_policy
RETURNING *;

-- name: GetConnection :one
SELECT * FROM connections WHERE id = $1;

-- name: ListConnections :many
SELECT * FROM connections ORDER BY created_at DESC;

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE id = $1;
