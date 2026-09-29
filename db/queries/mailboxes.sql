-- name: ListMailboxes :many
SELECT * FROM mailboxes ORDER BY lower(name), id;

-- name: GetMailbox :one
SELECT * FROM mailboxes WHERE id = $1;

-- name: GetMailboxForUpdate :one
SELECT * FROM mailboxes WHERE id = $1 FOR UPDATE;

-- name: InsertMailbox :one
INSERT INTO mailboxes (
    name, email_address, display_name,
    imap_host, imap_port, imap_tls, imap_username,
    smtp_host, smtp_port, smtp_tls, smtp_username,
    sent_folder, send_delay_seconds, allow_internal_host
) VALUES (
    @name, @email_address, @display_name,
    @imap_host, @imap_port, @imap_tls, @imap_username,
    @smtp_host, @smtp_port, @smtp_tls, @smtp_username,
    @sent_folder, @send_delay_seconds, @allow_internal_host
) RETURNING *;

-- name: UpdateMailbox :one
UPDATE mailboxes SET
    name = @name, email_address = @email_address, display_name = @display_name,
    imap_host = @imap_host, imap_port = @imap_port, imap_tls = @imap_tls, imap_username = @imap_username,
    smtp_host = @smtp_host, smtp_port = @smtp_port, smtp_tls = @smtp_tls, smtp_username = @smtp_username,
    sent_folder = @sent_folder, send_delay_seconds = @send_delay_seconds,
    allow_internal_host = @allow_internal_host, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: SetMailboxSecrets :one
UPDATE mailboxes SET
    imap_secret_enc = COALESCE(sqlc.narg(imap_secret_enc), imap_secret_enc),
    smtp_secret_enc = COALESCE(sqlc.narg(smtp_secret_enc), smtp_secret_enc),
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: SetMailboxDisabled :one
UPDATE mailboxes SET
    disabled_at = CASE WHEN @disabled::boolean THEN now() ELSE NULL END,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: ListMailboxAccess :many
SELECT * FROM mailbox_access WHERE mailbox_id = $1 ORDER BY team_id;

-- name: DeleteMailboxAccess :exec
DELETE FROM mailbox_access WHERE mailbox_id = $1;

-- name: AddMailboxAccess :exec
INSERT INTO mailbox_access (mailbox_id, team_id, level)
SELECT @mailbox_id, unnest(@team_ids::uuid[]), unnest(@levels::text[]);

-- name: ListAllMailboxIDs :many
SELECT id FROM mailboxes ORDER BY id;

-- name: ListUserMailboxAccess :many
SELECT mailbox_access.mailbox_id, bool_or(mailbox_access.level = 'write')::boolean AS can_write
FROM mailbox_access
JOIN team_members ON team_members.team_id = mailbox_access.team_id
WHERE team_members.user_id = $1
GROUP BY mailbox_access.mailbox_id
ORDER BY mailbox_access.mailbox_id;
