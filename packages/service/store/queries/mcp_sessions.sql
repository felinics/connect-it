-- name: CreateMCPSession :exec
INSERT INTO mcp_sessions (id, token_hash, tool_allowlist, status, expires_at, created_at)
VALUES ($1, $2, $3, 'active', $4, now());

-- name: AddMCPSessionConnection :exec
INSERT INTO mcp_session_connections (session_id, alias, connection_id)
VALUES ($1, $2, $3);

-- name: GetMCPSessionByTokenHash :one
SELECT * FROM mcp_sessions WHERE token_hash = $1;

-- name: ListMCPSessionConnections :many
SELECT * FROM mcp_session_connections WHERE session_id = $1;

-- name: ConnectionExistsByID :one
SELECT EXISTS (SELECT 1 FROM connections WHERE id = $1) AS found;

-- name: GetConnectionConnectorTypes :many
SELECT id, connector_type FROM connections WHERE id = ANY(@ids::uuid[]);
