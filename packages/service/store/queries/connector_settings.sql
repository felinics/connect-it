-- name: GetConnectorEnabled :one
SELECT enabled FROM connector_settings WHERE connector_type = $1;

-- name: ListConnectorSettings :many
SELECT * FROM connector_settings ORDER BY connector_type;

-- name: UpsertConnectorEnabled :one
INSERT INTO connector_settings (connector_type, enabled, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (connector_type) DO UPDATE SET
  enabled = EXCLUDED.enabled,
  updated_at = now()
RETURNING *;
