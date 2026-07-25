-- name: CreateMCPSession :one
WITH checked_input AS (
  SELECT
    @id::uuid AS id,
    @token_hash::text AS token_hash,
    @tool_allowlist::jsonb AS tool_allowlist,
    @ttl_seconds::bigint AS ttl_seconds,
    @bindings::jsonb AS bindings
  WHERE @ttl_seconds::bigint > 0
    AND @ttl_seconds::bigint <= 86400
    AND jsonb_typeof(@bindings::jsonb) = 'array'
    AND jsonb_array_length(@bindings::jsonb) > 0
),
inserted_session AS (
  INSERT INTO mcp_sessions (
    id, token_hash, tool_allowlist, status, expires_at, created_at
  )
  SELECT
    id,
    token_hash,
    tool_allowlist,
    'active',
    CURRENT_TIMESTAMP + make_interval(secs => ttl_seconds::double precision),
    CURRENT_TIMESTAMP
  FROM checked_input
  RETURNING id, expires_at
),
inserted_bindings AS (
  INSERT INTO mcp_session_connections (
    session_id, alias, connection_id, authorization_generation
  )
  SELECT
    inserted_session.id,
    binding.alias,
    binding.connection_id,
    binding.authorization_generation
  FROM inserted_session
  CROSS JOIN checked_input
  CROSS JOIN jsonb_to_recordset(checked_input.bindings) AS binding(
    alias text,
    connection_id uuid,
    authorization_generation bigint
  )
  RETURNING session_id
)
SELECT inserted_session.expires_at
FROM inserted_session
WHERE EXISTS (SELECT 1 FROM inserted_bindings);

-- name: GetMCPSessionByTokenHash :one
SELECT
  id,
  tool_allowlist,
  status,
  expires_at,
  expires_at > now() AS unexpired
FROM mcp_sessions
WHERE token_hash = $1;

-- name: ListMCPSessionConnections :many
SELECT
  binding.session_id,
  binding.alias,
  binding.connection_id,
  binding.authorization_generation,
  coalesce(connection.connector_type, '') AS active_connector_type
FROM mcp_session_connections AS binding
LEFT JOIN connections AS connection
  ON connection.id = binding.connection_id
 AND connection.status = 'active'
WHERE binding.session_id = $1;

-- name: GetConnectionsForMCPSession :many
SELECT id, connector_type, status, authorization_generation
FROM connections
WHERE id = ANY(@ids::uuid[])
ORDER BY id
FOR SHARE;

-- name: GetMCPExecutionAuthorization :one
SELECT
  s.status AS session_status,
  s.expires_at > now() AS session_unexpired,
  s.tool_allowlist,
  b.connection_id,
  b.authorization_generation AS binding_authorization_generation,
  c.status AS connection_status,
  c.authorization_generation AS current_authorization_generation
FROM mcp_sessions AS s
JOIN mcp_session_connections AS b ON b.session_id = s.id
JOIN connections AS c ON c.id = b.connection_id
WHERE s.id = @session_id
  AND b.alias = @alias;
