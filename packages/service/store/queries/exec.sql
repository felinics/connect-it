-- Tool 调用 metadata-only 审计。

-- name: InsertToolRun :exec
insert into tool_runs (
  id, connector_type, connection_id, tool_id, session_id, api_token_id,
  status, error_kind, upstream_status, duration_ms, created_at
) values (
  sqlc.arg(id), sqlc.arg(connector_type), sqlc.narg(connection_id), sqlc.arg(tool_id),
  sqlc.narg(session_id), sqlc.narg(api_token_id), sqlc.arg(status),
  sqlc.narg(error_kind), sqlc.narg(upstream_status), sqlc.arg(duration_ms), now()
);
