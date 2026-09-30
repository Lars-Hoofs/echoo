-- name: ListBlockedSenders :many
SELECT b.id, b.pattern, b.created_at, mailboxes.id AS mailbox_id, mailboxes.name AS mailbox_name,
    users.id AS created_by_id, users.name AS created_by_name
FROM blocked_senders b
JOIN mailboxes ON mailboxes.id = b.mailbox_id
LEFT JOIN users ON users.id = b.created_by
WHERE b.mailbox_id = ANY(@mailbox_ids::uuid[])
ORDER BY lower(mailboxes.name), mailboxes.id, b.pattern;

-- name: InsertBlockedSender :one
-- created is false when the pattern was already blocked; the existing row is returned then.
WITH inserted AS (
    INSERT INTO blocked_senders (mailbox_id, pattern, created_by)
    VALUES (@mailbox_id, @pattern, @created_by)
    ON CONFLICT (mailbox_id, pattern) DO NOTHING
    RETURNING id
)
SELECT COALESCE((SELECT id FROM inserted), (SELECT existing.id FROM blocked_senders existing WHERE existing.mailbox_id = @mailbox_id AND existing.pattern = @pattern))::uuid AS id,
    EXISTS (SELECT 1 FROM inserted)::boolean AS created;

-- name: GetBlockedSender :one
SELECT b.id, b.pattern, b.created_at, mailboxes.id AS mailbox_id, mailboxes.name AS mailbox_name,
    users.id AS created_by_id, users.name AS created_by_name
FROM blocked_senders b
JOIN mailboxes ON mailboxes.id = b.mailbox_id
LEFT JOIN users ON users.id = b.created_by
WHERE b.id = @id;

-- name: DeleteBlockedSender :one
DELETE FROM blocked_senders WHERE id = @id AND mailbox_id = ANY(@mailbox_ids::uuid[])
RETURNING mailbox_id, pattern;

-- name: IngestSenderBlocked :one
-- A pattern matches the whole address or its domain.
SELECT EXISTS (
    SELECT 1 FROM blocked_senders
    WHERE mailbox_id = @mailbox_id AND pattern IN (lower(@address::text), split_part(lower(@address::text), '@', 2))
)::boolean AS blocked;

-- name: ListMailboxRefs :many
SELECT id, name FROM mailboxes WHERE id = ANY(@ids::uuid[]) ORDER BY lower(name), id;
