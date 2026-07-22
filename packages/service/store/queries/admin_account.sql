-- name: GetAdminAccount :one
SELECT * FROM admin_account WHERE id = 1;

-- name: InsertAdminAccountIfAbsent :execrows
INSERT INTO admin_account (id, username, password_hash, updated_at)
VALUES (1, $1, $2, now())
ON CONFLICT (id) DO NOTHING;

-- name: UpdateAdminPassword :execrows
UPDATE admin_account SET password_hash = $1, updated_at = now() WHERE id = 1;
