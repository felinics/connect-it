-- name: CreateMCPSession :exec
INSERT INTO mcp_sessions (
  id, token_hash, api_token_id, connection_id, tool_snapshot, expires_at, created_at
)
VALUES ($1, $2, $3, $4, $5, $6, now());

-- name: DeleteExpiredMCPSessions :exec
DELETE FROM mcp_sessions
WHERE expires_at <= now()
   OR api_token_id IN (SELECT id FROM api_tokens WHERE revoked_at IS NOT NULL);

-- name: GetMCPSessionByTokenHash :one
SELECT s.*
FROM mcp_sessions s
JOIN api_tokens t ON t.id = s.api_token_id
WHERE s.token_hash = $1
  AND t.revoked_at IS NULL;
