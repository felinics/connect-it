-- name: GetOAuthAuthorizationByStateHash :one
SELECT * FROM oauth_authorizations WHERE state_hash = $1;
