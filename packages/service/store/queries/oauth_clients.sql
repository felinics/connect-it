-- name: LockOAuthClientRegistration :exec
SELECT pg_advisory_xact_lock(hashtextextended($1, 0));

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients WHERE id = $1;

-- name: GetOAuthClientByRegistration :one
SELECT *
FROM oauth_clients
WHERE connector_type = $1
  AND resource = $2
  AND issuer = $3
  AND redirect_uri = $4;

-- name: CreateOAuthClient :one
INSERT INTO oauth_clients (
  id, connector_type, resource, issuer, redirect_uri,
  client_id, client_secret, secret_key_version,
  token_endpoint, token_endpoint_auth_method, client_secret_expires_at,
  created_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5,
  $6, $7, $8,
  $9, $10, $11,
  now(), now()
)
RETURNING *;

-- name: UpdateOAuthClientEndpoint :one
UPDATE oauth_clients
SET token_endpoint = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ReplaceOAuthClientRegistration :one
UPDATE oauth_clients
SET client_id = $2,
    client_secret = $3,
    secret_key_version = $4,
    token_endpoint = $5,
    token_endpoint_auth_method = $6,
    client_secret_expires_at = $7,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ExpireOAuthClientRegistration :exec
UPDATE oauth_clients
SET client_secret_expires_at = now(),
    updated_at = now()
WHERE id = $1;
