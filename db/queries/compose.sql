-- Composer: replies, notes, uploads, drafts, templates, signatures and notifications.
-- Conversation lookups take the caller's readable mailbox ids, so out-of-scope rows are never
-- selected; write access is decided by the caller from the same scope.

-- name: ComposeGetConversation :one
SELECT id, number, mailbox_id, subject, status, contact_id
FROM conversations
WHERE id = @id AND mailbox_id = ANY(@mailbox_ids::uuid[]) AND deleted_at IS NULL;

-- name: ComposeLockConversation :one
SELECT id, mailbox_id, status FROM conversations WHERE id = @id AND deleted_at IS NULL FOR UPDATE;

-- name: ComposeMailboxAddresses :many
SELECT email_address FROM mailboxes;

-- name: ComposeLastEmail :one
-- Our own forwards are skipped: their recipients are not part of the conversation with the customer.
-- With known_customers set, inbound mail only counts when its From and every Reply-To address are one of them.
SELECT id, direction, from_addr, from_name, to_addrs, cc_addrs, reply_to, message_id_header, references_hdr
FROM messages
WHERE conversation_id = @conversation_id AND kind = 'email' AND deleted_at IS NULL
    AND NOT (direction = 'out' AND subject ~* '^\s*(fwd?|wg|doorst|doorgestuurd)\s*:')
    AND (sqlc.narg(direction)::text IS NULL OR direction = sqlc.narg(direction)::text)
    AND (sqlc.narg(known_customers)::text[] IS NULL
         OR (lower(from_addr) = ANY (sqlc.narg(known_customers)::text[])
             AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(reply_to) e
                             WHERE lower(e ->> 'address') <> ALL (sqlc.narg(known_customers)::text[]))))
ORDER BY received_at DESC, id DESC
LIMIT 1;

-- name: ComposeKnownAddresses :many
-- Who counts as a participant of the conversation: the contact, everyone we wrote to or from, and
-- the parties of the first inbound message. Later inbound senders are not included, so a stranger
-- who injected a message into the thread does not become known by having written to it.
WITH first_in AS (
    SELECT id FROM messages
    WHERE messages.conversation_id = @conversation_id AND kind = 'email' AND direction = 'in' AND deleted_at IS NULL
    ORDER BY received_at, id LIMIT 1
), sel AS (
    SELECT from_addr, to_addrs || cc_addrs || reply_to AS others FROM messages
    WHERE messages.conversation_id = @conversation_id AND kind = 'email' AND deleted_at IS NULL
        AND (direction = 'out' OR id IN (SELECT id FROM first_in))
)
SELECT lower(from_addr)::text AS address FROM sel
UNION SELECT lower(e ->> 'address') FROM sel, jsonb_array_elements(sel.others) AS e
UNION SELECT lower(ca.email) FROM contact_addresses ca JOIN conversations cv ON cv.contact_id = ca.contact_id
WHERE cv.id = @conversation_id;

-- name: ComposeGetEmail :one
SELECT id, conversation_id, mailbox_id, direction, raw_message_id, from_addr, from_name, to_addrs, cc_addrs,
    subject, body_text, sent_at, received_at
FROM messages
WHERE id = @id AND conversation_id = @conversation_id AND kind = 'email' AND deleted_at IS NULL;

-- name: ComposeGetRawBlob :one
SELECT sha256, size_bytes, blob_key FROM raw_messages WHERE id = $1;

-- name: ComposeGetContact :one
SELECT contacts.name,
    COALESCE((SELECT contact_addresses.email FROM contact_addresses
        WHERE contact_addresses.contact_id = contacts.id
        ORDER BY contact_addresses.is_primary DESC, contact_addresses.email LIMIT 1), '')::text AS email
FROM contacts
WHERE contacts.id = $1;

-- name: ComposeCreateConversation :one
INSERT INTO conversations (mailbox_id, subject, subject_normalized, contact_id)
VALUES ($1, $2, $3, $4)
RETURNING id, number;

-- name: ComposeInsertEvent :exec
INSERT INTO conversation_events (conversation_id, mailbox_id, actor_user_id, type, data)
VALUES ($1, $2, $3, $4, $5);

-- name: ComposeTouchConversation :one
-- Bumps the version so clients refetch; message_count and preview follow when the send worker
-- delivers the message.
UPDATE conversations
SET has_attachments = has_attachments OR @has_attachments::boolean,
    version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;

-- name: ComposeRecordStatusChange :exec
INSERT INTO reply_status_changes (message_id, from_status, to_status) VALUES ($1, $2, $3);

-- name: ComposeTakeStatusChange :one
DELETE FROM reply_status_changes WHERE message_id = $1 RETURNING from_status, to_status;

-- name: ComposeGetOutgoing :one
SELECT messages.id, messages.conversation_id, messages.mailbox_id, o.created_by, o.status, o.scheduled_at
FROM messages
JOIN outbound o ON o.message_id = messages.id
WHERE messages.id = @id AND messages.conversation_id = @conversation_id
    AND messages.kind = 'email' AND messages.direction = 'out' AND messages.deleted_at IS NULL;

-- name: ComposeGetOutgoingByID :one
SELECT messages.conversation_id, o.created_by
FROM messages JOIN outbound o ON o.message_id = messages.id
WHERE messages.id = $1;

-- name: ComposeSoftDeleteMessage :exec
UPDATE messages SET deleted_at = now() WHERE id = $1;

-- name: ComposeGetMessageView :one
SELECT
    messages.id, messages.kind, messages.direction, messages.from_name, messages.from_addr,
    messages.to_addrs, messages.cc_addrs, messages.subject, messages.body_text,
    messages.sent_at, messages.received_at,
    users.id AS author_id, users.name AS author_name,
    outbound.status AS outbound_status, outbound.error AS outbound_error, outbound.scheduled_at AS undo_until
FROM messages
LEFT JOIN users ON users.id = messages.author_user_id
LEFT JOIN outbound ON outbound.message_id = messages.id
WHERE messages.id = @id AND messages.deleted_at IS NULL;

-- name: ComposeInsertNote :one
INSERT INTO messages (conversation_id, mailbox_id, kind, from_addr, from_name, body_text, body_html, author_user_id)
VALUES ($1, $2, 'note', $3, $4, $5, $6, $7)
RETURNING id;

-- name: ComposeInsertMentions :exec
INSERT INTO mentions (message_id, user_id)
SELECT @message_id, unnest(@user_ids::uuid[])
ON CONFLICT DO NOTHING;

-- name: ComposeListMentionable :many
-- Everyone who can read the mailbox: owners and admins, plus members of a team it was shared with.
SELECT users.id, users.name, users.email
FROM users
WHERE users.deactivated_at IS NULL
    AND (users.role IN ('owner', 'admin') OR EXISTS (
        SELECT 1 FROM team_members
        JOIN mailbox_access ON mailbox_access.team_id = team_members.team_id
        WHERE team_members.user_id = users.id AND mailbox_access.mailbox_id = @mailbox_id))
ORDER BY lower(users.name), users.id;

-- name: ComposeInsertUpload :one
INSERT INTO uploads (user_id, filename, content_type, size_bytes, sha256, blob_key, content_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ComposeRestoreUploads :many
-- Undo send gives the attachments of a cancelled message back to its author as fresh uploads.
INSERT INTO uploads (user_id, filename, content_type, size_bytes, sha256, blob_key, content_id)
SELECT @user_id, filename, declared_type, size_bytes, sha256, blob_key,
    COALESCE(NULLIF(content_id, ''), md5(random()::text || id::text) || '@echoo.upload')
FROM attachments
WHERE message_id = @message_id
ORDER BY created_at, id
RETURNING *;

-- name: ComposeListUploads :many
-- Only the uploader's unexpired files; anything else is treated as not existing.
SELECT * FROM uploads
WHERE id = ANY(@ids::uuid[]) AND user_id = @user_id AND expires_at > now()
ORDER BY created_at, id;

-- name: ComposeDeleteUpload :many
-- Returns the file's key so the caller can queue it for the reference-checked deletion.
DELETE FROM uploads WHERE id = @id AND user_id = @user_id RETURNING blob_key;

-- name: ComposeDeleteUploads :exec
DELETE FROM uploads WHERE id = ANY(@ids::uuid[]) AND user_id = @user_id;

-- name: ComposeUpsertDraft :one
INSERT INTO drafts (conversation_id, user_id, to_addrs, cc_addrs, bcc_addrs, subject, body_html, attachment_ids)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (conversation_id, user_id) DO UPDATE SET
    to_addrs = EXCLUDED.to_addrs, cc_addrs = EXCLUDED.cc_addrs, bcc_addrs = EXCLUDED.bcc_addrs,
    subject = EXCLUDED.subject, body_html = EXCLUDED.body_html, attachment_ids = EXCLUDED.attachment_ids,
    updated_at = now()
RETURNING *;

-- name: ComposeGetDraft :one
SELECT * FROM drafts WHERE conversation_id = $1 AND user_id = $2;

-- name: ComposeDeleteDraft :exec
DELETE FROM drafts WHERE conversation_id = $1 AND user_id = $2;

-- name: ComposeInsertNotification :one
INSERT INTO notifications (user_id, kind, conversation_id, message_id, actor_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: ComposeListNotifications :many
-- Hidden conversations are left out, so a notification never reveals one.
SELECT notifications.id, notifications.kind, notifications.conversation_id, notifications.message_id,
    notifications.created_at, notifications.read_at,
    conversations.number AS conversation_number, conversations.subject AS conversation_subject,
    actors.id AS actor_id, actors.name AS actor_name
FROM notifications
JOIN conversations ON conversations.id = notifications.conversation_id AND conversations.deleted_at IS NULL
LEFT JOIN users AS actors ON actors.id = notifications.actor_id
WHERE notifications.user_id = @user_id
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[])
    AND (NOT @unread_only::boolean OR notifications.read_at IS NULL)
ORDER BY notifications.created_at DESC, notifications.id DESC
LIMIT @page_size::integer;

-- name: ComposeCountUnreadNotifications :one
SELECT count(*)::integer
FROM notifications
JOIN conversations ON conversations.id = notifications.conversation_id AND conversations.deleted_at IS NULL
WHERE notifications.user_id = @user_id AND notifications.read_at IS NULL
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[]);

-- name: ComposeMarkNotificationsRead :exec
UPDATE notifications SET read_at = now()
WHERE user_id = @user_id AND read_at IS NULL
    AND (@mark_all::boolean OR id = ANY(@ids::uuid[]));

-- name: ComposeListTemplates :many
-- Visible to the caller: their own personal templates, those of their teams, of mailboxes they can
-- read, and global ones. Admins managing the workspace see every non-personal template.
SELECT * FROM templates
WHERE (scope = 'personal' AND owner_user_id = @user_id)
    OR (NOT @manage::boolean AND (
        scope = 'global'
        OR (scope = 'team' AND team_id IN (SELECT team_members.team_id FROM team_members WHERE team_members.user_id = @user_id))
        OR (scope = 'mailbox' AND mailbox_id = ANY(@mailbox_ids::uuid[]))))
    OR (@manage::boolean AND scope <> 'personal')
ORDER BY lower(name), id;

-- name: ComposeGetTemplate :one
SELECT * FROM templates WHERE id = $1;

-- name: ComposeTemplateVisible :one
SELECT EXISTS (
    SELECT 1 FROM templates
    WHERE id = @id AND (
        (scope = 'personal' AND owner_user_id = @user_id)
        OR scope = 'global'
        OR (scope = 'team' AND team_id IN (SELECT team_members.team_id FROM team_members WHERE team_members.user_id = @user_id))
        OR (scope = 'mailbox' AND mailbox_id = ANY(@mailbox_ids::uuid[])))
);

-- name: ComposeInsertTemplate :one
INSERT INTO templates (name, shortcode, scope, owner_user_id, team_id, mailbox_id, subject, body_html)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ComposeUpdateTemplate :one
UPDATE templates
SET name = @name, shortcode = @shortcode, subject = @subject, body_html = @body_html, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: ComposeDeleteTemplate :exec
DELETE FROM templates WHERE id = $1;

-- name: ComposeListUserSignatures :many
SELECT id, mailbox_id, body_html FROM signatures WHERE user_id = $1 ORDER BY mailbox_id NULLS FIRST;

-- name: ComposeResolveSignature :one
-- user+mailbox beats user default beats mailbox default.
SELECT body_html,
    CASE WHEN user_id IS NOT NULL AND mailbox_id IS NOT NULL THEN 'user_mailbox'
         WHEN user_id IS NOT NULL THEN 'user'
         ELSE 'mailbox' END::text AS source
FROM signatures
WHERE (user_id = @user_id AND (mailbox_id = @mailbox_id OR mailbox_id IS NULL))
    OR (user_id IS NULL AND mailbox_id = @mailbox_id)
ORDER BY (user_id IS NOT NULL AND mailbox_id IS NOT NULL) DESC, (user_id IS NOT NULL) DESC
LIMIT 1;

-- name: ComposeUpsertUserSignature :exec
INSERT INTO signatures (user_id, mailbox_id, body_html) VALUES (@user_id, sqlc.narg(mailbox_id), @body_html)
ON CONFLICT (user_id, mailbox_id) WHERE user_id IS NOT NULL AND mailbox_id IS NOT NULL
    DO UPDATE SET body_html = EXCLUDED.body_html, updated_at = now();

-- name: ComposeUpsertUserDefaultSignature :exec
INSERT INTO signatures (user_id, body_html) VALUES (@user_id, @body_html)
ON CONFLICT (user_id) WHERE user_id IS NOT NULL AND mailbox_id IS NULL
    DO UPDATE SET body_html = EXCLUDED.body_html, updated_at = now();

-- name: ComposeDeleteUserSignature :exec
DELETE FROM signatures
WHERE user_id = @user_id AND mailbox_id IS NOT DISTINCT FROM sqlc.narg(mailbox_id)::uuid;

-- name: ComposeGetMailboxSignature :one
SELECT body_html FROM signatures WHERE mailbox_id = $1 AND user_id IS NULL;

-- name: ComposeUpsertMailboxSignature :exec
INSERT INTO signatures (mailbox_id, body_html) VALUES ($1, $2)
ON CONFLICT (mailbox_id) WHERE user_id IS NULL AND mailbox_id IS NOT NULL
    DO UPDATE SET body_html = EXCLUDED.body_html, updated_at = now();

-- name: ComposeDeleteMailboxSignature :exec
DELETE FROM signatures WHERE mailbox_id = $1 AND user_id IS NULL;

-- name: ComposeMailboxExists :one
SELECT EXISTS (SELECT 1 FROM mailboxes WHERE id = $1);
