-- Queries behind the mail rendering routes. Everything that takes a message or attachment id
-- for a user also takes the readable mailbox ids, so out-of-scope rows are never selected.

-- name: GetMessageForRender :one
SELECT
    messages.id, messages.conversation_id, conversations.mailbox_id, messages.kind, messages.direction,
    messages.from_addr, messages.from_name, messages.reply_to, messages.auth_results,
    messages.body_html, messages.body_text
FROM messages
JOIN conversations ON conversations.id = messages.conversation_id AND conversations.deleted_at IS NULL
WHERE messages.id = @id
    AND messages.deleted_at IS NULL
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[]);

-- name: ListConversationRenderInfo :many
-- The conversation must already have been authorized by the caller.
SELECT
    messages.id, messages.direction, messages.from_addr, messages.from_name,
    messages.reply_to, messages.auth_results, messages.body_html
FROM messages
WHERE messages.conversation_id = @conversation_id AND messages.deleted_at IS NULL AND messages.kind = 'email'
ORDER BY messages.received_at, messages.id;

-- name: ListRenderAttachments :many
SELECT id, content_id, sniffed_type
FROM attachments
WHERE message_id = @message_id AND content_id <> ''
ORDER BY created_at, id;

-- name: GetAttachmentForServe :one
-- Only reached with a valid signature, which is only handed out after a scoped message render.
SELECT id, filename, sniffed_type, size_bytes, blob_key, scan_status FROM attachments WHERE id = @id;

-- name: GetAttachmentForDownload :one
SELECT
    attachments.id, attachments.filename, attachments.sniffed_type, attachments.size_bytes,
    attachments.blob_key, attachments.scan_status
FROM attachments
JOIN messages ON messages.id = attachments.message_id AND messages.deleted_at IS NULL
JOIN conversations ON conversations.id = messages.conversation_id AND conversations.deleted_at IS NULL
WHERE attachments.id = @id AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[]);

-- name: ListSenderImagePatterns :many
SELECT pattern FROM sender_image_allowlist WHERE mailbox_id IS NULL OR mailbox_id = @mailbox_id;

-- name: AddSenderImagePattern :exec
INSERT INTO sender_image_allowlist (mailbox_id, pattern, created_by)
VALUES (@mailbox_id, @pattern, @created_by)
ON CONFLICT DO NOTHING;

-- name: GetImageProxyCache :one
SELECT blob_key, content_type, size_bytes FROM image_proxy_cache WHERE url_hash = @url_hash;

-- name: PutImageProxyCache :exec
INSERT INTO image_proxy_cache (url_hash, blob_key, content_type, size_bytes)
VALUES (@url_hash, @blob_key, @content_type, @size_bytes)
ON CONFLICT (url_hash) DO NOTHING;

-- name: GetMailboxIdentity :one
SELECT email_address, display_name, name FROM mailboxes WHERE id = @id;
