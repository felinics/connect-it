-- name: GetConnectorConfig :one
SELECT * FROM connector_configs WHERE connector_type = $1;

-- name: ListConnectorConfigs :many
SELECT * FROM connector_configs ORDER BY connector_type;

-- name: UpsertConnectorConfig :one
INSERT INTO connector_configs (
  connector_type, config_schema_version, public_config, secret_config,
  secret_key_version, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, now(), now())
ON CONFLICT (connector_type) DO UPDATE SET
  config_schema_version = EXCLUDED.config_schema_version,
  public_config = EXCLUDED.public_config,
  secret_config = EXCLUDED.secret_config,
  secret_key_version = EXCLUDED.secret_key_version,
  updated_at = now()
RETURNING *;

-- name: UpdateConnectorConfigIfMatch :one
UPDATE connector_configs SET
  config_schema_version = $2,
  public_config = $3,
  secret_config = $4,
  secret_key_version = $5,
  updated_at = now()
WHERE connector_type = $1 AND updated_at = $6
RETURNING *;

-- name: DeleteConnectorConfig :execrows
DELETE FROM connector_configs WHERE connector_type = $1;
