-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, name, token_hash, created_at)
VALUES ($1, $2, $3, now());

-- name: InsertAPITokenIfAbsent :execrows
INSERT INTO api_tokens (id, name, token_hash, created_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (token_hash) DO NOTHING;

-- name: GetAPITokenByHash :one
SELECT * FROM api_tokens WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: ListAPITokens :many
SELECT * FROM api_tokens ORDER BY created_at DESC;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;
