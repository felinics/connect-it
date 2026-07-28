-- name: CreateConnection :one
INSERT INTO connections (
  id, connector_type, alias, auth_method, credential, secret_key_version,
  profile, scopes, status, access_token_expires_at, created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, '{}', $7, $8, $9, now(), now()
)
RETURNING *;

-- name: GetConnection :one
SELECT * FROM connections WHERE id = $1;

-- name: GetConnectionForUpdate :one
SELECT * FROM connections WHERE id = $1 FOR UPDATE;

-- name: ListConnections :many
SELECT * FROM connections ORDER BY created_at DESC;

-- name: UpdateConnectionCredential :exec
UPDATE connections
SET credential = $2,
    secret_key_version = $3,
    status = $4,
    access_token_expires_at = $5,
    updated_at = now()
WHERE id = $1;

-- name: ActivateOAuthConnection :exec
UPDATE connections
SET credential = $2,
    secret_key_version = $3,
    status = 'active',
    access_token_expires_at = $4,
    oauth_client_id = $5,
    updated_at = now()
WHERE id = $1;

-- name: UpdateConnectionStatus :exec
UPDATE connections
SET status = $2,
    updated_at = now()
WHERE id = $1;

-- name: MarkPendingConnectionAuthorizationFailed :exec
UPDATE connections
SET status = 'authorization_failed',
    updated_at = now()
WHERE id = $1
  AND status = 'pending';

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE id = $1;
