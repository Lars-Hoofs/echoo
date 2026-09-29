-- Invitations, password reset links and notification emails.

-- name: CreateInvitedUser :one
INSERT INTO users (email, name, role, password_hash, invited_at, deactivated_at)
VALUES ($1, $2, $3, $4, now(), now())
RETURNING *;

-- name: InsertAccountToken :one
INSERT INTO account_tokens (user_id, purpose, token_hash, created_by, expires_at)
VALUES (@user_id, @purpose, @token_hash, @created_by, now() + make_interval(secs => @ttl_seconds::integer))
RETURNING *;

-- The password-reset request runs this for unknown addresses too, with the zero UUID, so both
-- paths issue the same statements.
-- name: InsertPasswordResetToken :one
INSERT INTO account_tokens (user_id, purpose, token_hash, expires_at)
SELECT users.id, 'password_reset', @token_hash, now() + make_interval(secs => @ttl_seconds::integer)
FROM users WHERE users.id = @user_id
RETURNING id;

-- name: InvalidateAccountTokens :exec
UPDATE account_tokens SET used_at = now()
WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL;

-- name: PeekAccountToken :one
SELECT account_tokens.id, account_tokens.user_id, users.email, users.name
FROM account_tokens
JOIN users ON users.id = account_tokens.user_id
WHERE account_tokens.token_hash = $1 AND account_tokens.purpose = $2
  AND account_tokens.used_at IS NULL AND account_tokens.expires_at > now();

-- Zero rows: unknown, already used or expired.
-- name: ConsumeAccountToken :one
UPDATE account_tokens SET used_at = now()
WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: AcceptInvitation :one
UPDATE users
SET name = $2, password_hash = $3, password_must_change = false, password_changed_at = now(),
    temp_password_expires_at = NULL, invited_at = NULL, deactivated_at = NULL, updated_at = now()
WHERE id = $1 AND invited_at IS NOT NULL
RETURNING *;

-- name: DeleteInvitedUser :execrows
DELETE FROM users WHERE id = $1 AND invited_at IS NOT NULL;

-- name: SetUserNotifyPrefs :one
UPDATE users SET email_notify_mentions = $2, email_notify_assignments = $3, email_notify_replies = $4, updated_at = now()
WHERE id = $1
RETURNING *;

-- A notification that waited this long is no longer worth an email (system mail was off, or
-- the job queue was down).
-- name: ExpireStaleNotificationMail :execrows
UPDATE notifications SET email_handled_at = now()
WHERE email_handled_at IS NULL AND created_at < now() - interval '1 hour';

-- name: ClaimNotificationsForMail :many
SELECT n.id, n.kind, n.conversation_id, n.read_at,
       c.number AS conversation_number,
       u.email AS recipient_email, u.deactivated_at AS recipient_deactivated_at,
       u.email_notify_mentions, u.email_notify_assignments, u.email_notify_replies,
       actor.name AS actor_name
FROM notifications n
JOIN users u ON u.id = n.user_id
JOIN conversations c ON c.id = n.conversation_id
LEFT JOIN users actor ON actor.id = n.actor_id
WHERE n.email_handled_at IS NULL
ORDER BY n.created_at, n.id
LIMIT 100
FOR UPDATE OF n SKIP LOCKED;

-- name: MarkNotificationsMailHandled :exec
UPDATE notifications SET email_handled_at = now() WHERE id = ANY(@ids::uuid[]);

-- name: GetMailboxByAddress :one
SELECT * FROM mailboxes WHERE email_address = $1;
