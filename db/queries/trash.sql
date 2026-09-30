-- The trash: conversations with deleted_at set. Moving to the trash locks through
-- LockConversationForUpdate; everything here works on rows that are already in it.

-- name: TrashMarkDeleted :one
UPDATE conversations SET deleted_at = now(), deleted_by = @deleted_by, version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;

-- name: TrashLockDeleted :one
SELECT id, mailbox_id FROM conversations
WHERE id = @id AND mailbox_id = ANY(@mailbox_ids::uuid[]) AND deleted_at IS NOT NULL
FOR UPDATE;

-- name: TrashRestore :one
UPDATE conversations SET deleted_at = NULL, deleted_by = NULL, version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;

-- name: TrashListPage :many
SELECT
    c.id, c.number, c.subject, c.status, c.preview, c.last_message_at, c.message_count, c.deleted_at,
    mailboxes.id AS mailbox_id, mailboxes.name AS mailbox_name,
    contacts.id AS contact_id, contacts.name AS contact_name, COALESCE(contact_address.email, '')::text AS contact_email,
    users.id AS deleted_by_id, users.name AS deleted_by_name
FROM conversations c
JOIN mailboxes ON mailboxes.id = c.mailbox_id
LEFT JOIN contacts ON contacts.id = c.contact_id
LEFT JOIN LATERAL (
    SELECT contact_addresses.email FROM contact_addresses
    WHERE contact_addresses.contact_id = contacts.id
    ORDER BY contact_addresses.is_primary DESC, contact_addresses.email
    LIMIT 1
) AS contact_address ON true
LEFT JOIN users ON users.id = c.deleted_by
WHERE c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NOT NULL
    AND (sqlc.narg(cursor_at)::timestamptz IS NULL OR (c.deleted_at, c.id) < (sqlc.narg(cursor_at)::timestamptz, @cursor_id::uuid))
ORDER BY c.deleted_at DESC, c.id DESC
LIMIT @page_size::integer;

-- name: TrashLockForPurge :many
-- The given conversations that are in the trash of the mailboxes. sending marks the ones with
-- mail still on its way out; they stay, because the send job reads them.
SELECT c.id, EXISTS (
    SELECT 1 FROM outbound o JOIN messages m ON m.id = o.message_id
    WHERE m.conversation_id = c.id AND o.status IN ('queued', 'sending', 'retry', 'uncertain'))::boolean AS sending
FROM conversations c
WHERE c.id = ANY(@ids::uuid[]) AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NOT NULL
ORDER BY c.id
FOR UPDATE OF c;

-- name: TrashPickExpired :many
-- Conversations of one mailbox that went into the trash before the cutoff; same exclusions as
-- RetentionPickConversations.
SELECT c.id FROM conversations c
WHERE c.mailbox_id = @mailbox_id AND c.deleted_at < @before::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM outbound o JOIN messages m ON m.id = o.message_id
      WHERE m.conversation_id = c.id AND o.status IN ('queued', 'sending', 'retry', 'uncertain'))
ORDER BY c.id
LIMIT @batch_size::integer
FOR UPDATE OF c SKIP LOCKED;

-- name: TrashPreviewExpired :one
-- The counting twin of TrashPickExpired; keep the predicates identical.
WITH picked AS (
    SELECT c.id FROM conversations c
    WHERE c.mailbox_id = @mailbox_id AND c.deleted_at < @before::timestamptz
      AND NOT EXISTS (
          SELECT 1 FROM outbound o JOIN messages m ON m.id = o.message_id
          WHERE m.conversation_id = c.id AND o.status IN ('queued', 'sending', 'retry', 'uncertain'))
)
SELECT (SELECT count(*) FROM picked)::bigint AS conversations,
       (SELECT count(*) FROM messages WHERE conversation_id IN (SELECT id FROM picked))::bigint AS messages,
       (SELECT count(*) FROM attachments a JOIN messages m ON m.id = a.message_id
         WHERE m.conversation_id IN (SELECT id FROM picked))::bigint AS attachments,
       (SELECT COALESCE(sum(a.size_bytes), 0) FROM attachments a JOIN messages m ON m.id = a.message_id
         WHERE m.conversation_id IN (SELECT id FROM picked))::bigint AS attachment_bytes;

-- name: TrashGetInfo :one
SELECT c.deleted_at, users.id AS deleted_by_id, users.name AS deleted_by_name
FROM conversations c
LEFT JOIN users ON users.id = c.deleted_by
WHERE c.id = @id AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NOT NULL;
