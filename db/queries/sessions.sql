-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, csrf_token, mfa_pending, idle_expires_at, expires_at, ip, user_agent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetActiveSession :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1
  AND sessions.revoked_at IS NULL
  AND sessions.expires_at > now()
  AND sessions.idle_expires_at > now()
  AND users.deactivated_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now(), idle_expires_at = $2
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeUserSession :execrows
UPDATE sessions SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeUserSessions :execrows
UPDATE sessions SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL AND id <> sqlc.arg(except_id)::uuid;

-- name: RevokeIdPMFASessions :execrows
UPDATE sessions SET revoked_at = now() WHERE idp_mfa AND revoked_at IS NULL;

-- name: ListUserSessions :many
SELECT * FROM sessions
WHERE user_id = $1
  AND revoked_at IS NULL
  AND mfa_pending = false
  AND expires_at > now()
  AND idle_expires_at > now()
ORDER BY last_seen_at DESC;
