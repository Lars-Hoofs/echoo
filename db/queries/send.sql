-- name: SendGetMailbox :one
SELECT * FROM mailboxes WHERE id = $1;

-- name: SendGetMessage :one
SELECT * FROM messages WHERE id = $1 AND kind = 'email' AND direction = 'out';

-- name: SendListAttachments :many
SELECT * FROM attachments WHERE message_id = $1 ORDER BY created_at, id;

-- name: SendInsertMessage :one
INSERT INTO messages (
    conversation_id, mailbox_id, kind, direction, message_id_header, message_id_hash,
    in_reply_to, references_hdr, from_addr, from_name, to_addrs, cc_addrs, bcc_addrs,
    subject, body_text, body_html, auto_submitted, author_user_id
) VALUES ($1, $2, 'email', 'out', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING id;

-- name: SendInsertAttachment :exec
INSERT INTO attachments (message_id, filename, declared_type, size_bytes, sha256, blob_key, content_id, disposition)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: SendInsertThreadRef :exec
INSERT INTO thread_refs (mailbox_id, message_id_hash, conversation_id)
VALUES ($1, $2, $3)
ON CONFLICT (mailbox_id, message_id_hash) DO NOTHING;

-- name: SendInsertOutbound :exec
INSERT INTO outbound (message_id, idempotency_key, scheduled_at, created_by, extra_headers)
VALUES ($1, $2, $3, $4, $5);

-- name: SendGetEnqueuedByKey :one
SELECT o.message_id, m.message_id_header
FROM outbound o JOIN messages m ON m.id = o.message_id
WHERE o.idempotency_key = $1;

-- name: SendGetOutbound :one
SELECT * FROM outbound WHERE message_id = $1;

-- name: SendClaimOutbound :one
UPDATE outbound
SET status = 'sending', attempt = attempt + 1, last_attempt_at = @now, updated_at = @now
WHERE message_id = @message_id
  AND status IN ('queued', 'retry')
  AND scheduled_at <= @now
  AND (next_attempt_at IS NULL OR next_attempt_at <= @now)
RETURNING *;

-- name: SendSetOutboundResult :execrows
UPDATE outbound
SET status = @status, smtp_response = @smtp_response, error = @error,
    next_attempt_at = @next_attempt_at, sent_at = @sent_at, updated_at = @now
WHERE message_id = @message_id AND status IN ('queued', 'retry', 'sending');

-- name: SendSetMessageSentAt :exec
UPDATE messages SET sent_at = $2 WHERE id = $1;

-- Counts a delivered reply on its conversation; queued or cancelled replies do not count
-- towards response times, and neither does an automatic reply: only a person answering meets
-- the first-response SLA.
-- name: SendRecordConversationReply :exec
UPDATE conversations c
SET message_count      = c.message_count + 1,
    last_message_at    = greatest(c.last_message_at, @sent_at::timestamptz),
    last_outbound_at   = @sent_at::timestamptz,
    first_responded_at = CASE WHEN m.auto_submitted THEN c.first_responded_at
                              ELSE coalesce(c.first_responded_at, @sent_at::timestamptz) END,
    first_response_met_at = CASE WHEN m.auto_submitted THEN c.first_response_met_at
                                 ELSE coalesce(c.first_response_met_at, @sent_at::timestamptz) END,
    preview            = left(regexp_replace(m.body_text, '\s+', ' ', 'g'), 200),
    version            = c.version + 1,
    updated_at         = now()
FROM messages m
WHERE m.id = @message_id AND c.id = m.conversation_id;

-- name: SendCancelOutbound :execrows
UPDATE outbound SET status = 'cancelled', updated_at = now()
WHERE message_id = $1 AND status = 'queued';
