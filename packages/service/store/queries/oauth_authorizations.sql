-- name: CreateOAuthAuthorization :one
INSERT INTO oauth_authorizations (
  id, connector_type, state_hash, pkce_verifier, secret_key_version,
  auth_method, alias, connection_id, status, expires_at, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now()
)
RETURNING *;

-- name: GetOAuthAuthorizationByStateHash :one
SELECT * FROM oauth_authorizations WHERE state_hash = $1;

-- name: CompleteOAuthAuthorization :exec
UPDATE oauth_authorizations
SET status = $2,
    connection_id = $3
WHERE id = $1;

-- name: DeleteExpiredOAuthAuthorizations :exec
DELETE FROM oauth_authorizations
WHERE expires_at < now() AND status = 'pending';
