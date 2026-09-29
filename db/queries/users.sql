-- name: CreateUser :one
INSERT INTO users (email, name, role, password_hash, password_must_change, temp_password_expires_at)
VALUES ($1, $2, $3, $4, $5, CASE WHEN $5::boolean THEN now() + interval '72 hours' END)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserForUpdate :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY lower(name), id;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: UpdateUserName :one
UPDATE users SET name = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: UpdateUserTheme :one
UPDATE users SET theme = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: UpdateUserRole :one
UPDATE users SET role = $2, custom_role_id = NULL, permissions = '{}', updated_at = now() WHERE id = $1 RETURNING *;

-- name: SetUserDeactivated :one
UPDATE users
SET deactivated_at = CASE WHEN sqlc.arg(deactivated)::boolean THEN now() ELSE NULL END,
    updated_at     = now()
WHERE id = $1
RETURNING *;

-- name: SetUserPassword :exec
UPDATE users
SET password_hash        = $2,
    password_must_change = $3,
    password_changed_at  = now(),
    temp_password_expires_at = CASE WHEN $3::boolean THEN now() + interval '72 hours' END,
    failed_login_count   = 0,
    locked_until         = NULL,
    updated_at           = now()
WHERE id = $1;

-- A failure after a previous lock has expired starts a new count at 1. A lock that is still
-- active is left alone, so failures during a race cannot extend it.
-- name: RecordLoginFailure :one
WITH cur AS (
    SELECT users.id,
           (locked_until IS NOT NULL AND locked_until > now()) AS locked,
           CASE
               WHEN locked_until IS NOT NULL AND locked_until <= now() THEN 1
               ELSE failed_login_count + 1
           END AS next_count
    FROM users
    WHERE users.id = $1
    FOR UPDATE
)
UPDATE users
SET failed_login_count = cur.next_count,
    locked_until = CASE
        WHEN cur.locked THEN users.locked_until
        WHEN cur.next_count >= sqlc.arg(lock_threshold)::integer
            THEN now() + make_interval(secs => sqlc.arg(lock_seconds)::integer)
    END
FROM cur
WHERE users.id = cur.id
RETURNING users.failed_login_count, users.locked_until, (NOT cur.locked AND users.locked_until IS NOT NULL) AS newly_locked;

-- name: RecordLoginSuccess :exec
UPDATE users
SET failed_login_count = 0, locked_until = NULL, last_login_at = now()
WHERE id = $1;

-- name: SetPendingTOTPSecret :exec
UPDATE users SET totp_secret_enc = $2, updated_at = now()
WHERE id = $1 AND totp_enabled_at IS NULL;

-- The secret must be the one the caller verified the code against.
-- name: EnableTOTP :execrows
UPDATE users SET totp_enabled_at = now(), totp_last_step = $2, updated_at = now()
WHERE id = $1 AND totp_enabled_at IS NULL AND totp_secret_enc = $3;

-- name: DisableTOTP :exec
UPDATE users
SET totp_secret_enc = NULL, totp_enabled_at = NULL, totp_last_step = 0, updated_at = now()
WHERE id = $1;

-- Advances the last accepted TOTP step; zero rows means the code was already used or the
-- account is locked.
-- name: ConsumeTOTPStep :execrows
UPDATE users SET totp_last_step = $2
WHERE id = $1 AND totp_last_step < $2 AND (locked_until IS NULL OR locked_until <= now());

-- name: DeleteRecoveryCodes :exec
DELETE FROM recovery_codes WHERE user_id = $1;

-- name: InsertRecoveryCodes :copyfrom
INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2);

-- Zero rows means the code is unknown, already used, or the account is locked.
-- name: UseRecoveryCode :execrows
UPDATE recovery_codes SET used_at = now()
WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
  AND EXISTS (SELECT 1 FROM users WHERE users.id = $1 AND (users.locked_until IS NULL OR users.locked_until <= now()));

-- name: CountUnusedRecoveryCodes :one
SELECT count(*) FROM recovery_codes WHERE user_id = $1 AND used_at IS NULL;

-- Only upgrades the hash that was verified; a password changed in the meantime stays.
-- name: RehashPassword :execrows
UPDATE users SET password_hash = $3 WHERE id = $1 AND password_hash = $2;
