-- name: GetConnectorHealth :one
SELECT * FROM connector_health WHERE connector_type = $1;
