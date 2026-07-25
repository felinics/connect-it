-- 计划 4：Tool 执行引擎所需查询。
-- connection 读取复用计划 3 的 GetConnection；GetConnectorHealth 在 connector_health.sql。

-- name: UpsertConnectorHealthSuccess :exec
insert into connector_health (connector_type, last_ok_at, consecutive_failures)
values ($1, now(), 0)
on conflict (connector_type) do update
set last_ok_at = now(),
    consecutive_failures = 0;

-- name: UpsertConnectorHealthFailure :exec
insert into connector_health (connector_type, last_error_at, consecutive_failures, last_error)
values (sqlc.arg(connector_type), now(), 1, sqlc.arg(last_error))
on conflict (connector_type) do update
set last_error_at = now(),
    consecutive_failures = connector_health.consecutive_failures + 1,
    last_error = excluded.last_error;

-- name: InsertToolRun :exec
insert into tool_runs (
  id, connector_type, connection_id, tool_id, session_id,
  status, error, error_code, upstream_status, input, output_summary,
  duration_ms, created_at
) values (
  sqlc.arg(id), sqlc.arg(connector_type), sqlc.narg(connection_id), sqlc.arg(tool_id),
  sqlc.narg(session_id), sqlc.arg(status), sqlc.narg(error),
  sqlc.narg(error_code), sqlc.narg(upstream_status), sqlc.narg(input),
  sqlc.narg(output_summary), sqlc.arg(duration_ms), now()
);

-- name: SetConnectorConfigVerified :exec
update connector_configs
set mcp_verified_at = now(),
    mcp_verified_endpoint = sqlc.narg(endpoint),
    updated_at = now()
where connector_type = sqlc.arg(connector_type);

-- name: GetConnectorConfigVerification :one
select mcp_verified_at, mcp_verified_endpoint
from connector_configs
where connector_type = $1;
