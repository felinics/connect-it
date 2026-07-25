-- name: GetConnectorConfig :one
SELECT * FROM connector_configs WHERE connector_type = $1;

-- name: GetConnectorConfigForUpdate :one
SELECT * FROM connector_configs WHERE connector_type = $1 FOR UPDATE;

-- name: ListConnectorConfigs :many
SELECT * FROM connector_configs ORDER BY connector_type;

-- name: CreateConnectorConfig :one
INSERT INTO connector_configs (
  connector_type, config_schema_version, public_config, secret_config,
  secret_key_version, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, now(), now())
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

-- name: DeleteConnectorConfigIfMatch :execrows
DELETE FROM connector_configs
WHERE connector_type = $1 AND updated_at = $2;

-- name: EnsureConnectorPolicyIdentity :exec
INSERT INTO connector_policy_identities (
  connector_type, identity_version, identity_digest, definition_digest,
  initialized, created_at, updated_at
) VALUES ($1, $2, '\x'::bytea, '\x'::bytea, false, now(), now())
ON CONFLICT (connector_type) DO NOTHING;

-- name: GetConnectorPolicyIdentityForUpdate :one
SELECT * FROM connector_policy_identities
WHERE connector_type = $1
FOR UPDATE;

-- name: GetConnectorPolicyIdentity :one
SELECT * FROM connector_policy_identities
WHERE connector_type = $1;

-- name: SetConnectorPolicyIdentity :one
UPDATE connector_policy_identities
SET identity_version = $2,
    identity_digest = $3,
    definition_digest = $4,
    initialized = true,
    updated_at = now()
WHERE connector_type = $1
RETURNING *;

-- name: BumpConnectorAuthorizationGenerations :execrows
UPDATE connections
SET authorization_generation = authorization_generation + 1,
    updated_at = now()
WHERE connector_type = $1;

-- name: ClearConnectorConfigVerification :exec
UPDATE connector_configs
SET mcp_verified_endpoint = null,
    mcp_verified_at = null
WHERE connector_type = $1;
