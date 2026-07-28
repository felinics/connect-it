-- name: CreateOAuthAuthorization :one
INSERT INTO oauth_authorizations (
  id, connector_type, state_hash, pkce_verifier, secret_key_version,
  auth_method, connection_id, oauth_client_id, status, expires_at, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9, now()
)
RETURNING *;

-- name: ClaimOAuthAuthorization :one
UPDATE oauth_authorizations
SET status = 'processing'
WHERE state_hash = $1
  AND status = 'pending'
  AND expires_at >= now()
RETURNING *;

-- name: DeleteClaimedOAuthAuthorization :execrows
DELETE FROM oauth_authorizations
WHERE id = $1
  AND status = 'processing';

-- name: SupersedeOpenOAuthAuthorizations :exec
DELETE FROM oauth_authorizations
WHERE connection_id = $1
  AND status IN ('pending', 'processing');

-- name: ExpireOAuthAuthorizations :exec
WITH expired AS (
  DELETE FROM oauth_authorizations
  WHERE (status = 'pending' AND expires_at < now())
     OR (status = 'processing' AND expires_at < now() - interval '1 minute')
  RETURNING connection_id
)
UPDATE connections
SET status = 'authorization_failed',
    updated_at = now()
WHERE id IN (
  SELECT connection_id
  FROM expired
)
  AND status = 'pending';
