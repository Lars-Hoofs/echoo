-- name: ListCustomRoles :many
SELECT custom_roles.*,
    (SELECT count(*) FROM users WHERE users.custom_role_id = custom_roles.id)::integer AS member_count
FROM custom_roles
ORDER BY lower(custom_roles.name), custom_roles.id;

-- name: GetCustomRole :one
SELECT * FROM custom_roles WHERE id = $1;

-- Held for the length of a transaction that hands the role to someone, so a concurrent edit of
-- its permissions cannot leave the copy on the user behind.
-- name: GetCustomRoleForShare :one
SELECT * FROM custom_roles WHERE id = $1 FOR SHARE;

-- name: GetCustomRoleForUpdate :one
SELECT * FROM custom_roles WHERE id = $1 FOR UPDATE;

-- name: CreateCustomRole :one
INSERT INTO custom_roles (name, description, permissions) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateCustomRole :one
UPDATE custom_roles SET name = $2, description = $3, permissions = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SyncCustomRoleMembers :exec
UPDATE users SET permissions = $2, updated_at = now() WHERE custom_role_id = $1;

-- Fails on the foreign keys while users or the SSO default still use the role.
-- name: DeleteCustomRole :execrows
DELETE FROM custom_roles WHERE id = $1;

-- name: SetUserCustomRole :one
UPDATE users
SET role = 'custom', custom_role_id = @role_id,
    permissions = (SELECT custom_roles.permissions FROM custom_roles WHERE custom_roles.id = @role_id),
    updated_at = now()
WHERE users.id = @id
RETURNING *;
