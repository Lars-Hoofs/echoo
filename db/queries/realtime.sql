-- name: RealtimeNotify :exec
SELECT pg_notify('echoo_events', @payload::text);

-- name: RealtimeMessageRef :one
SELECT conversation_id, mailbox_id FROM messages WHERE id = @id;

-- The mailbox filter is what keeps presence from confirming that a hidden conversation exists.
-- name: RealtimeConversationMailbox :one
SELECT mailbox_id FROM conversations
WHERE id = @id AND deleted_at IS NULL AND mailbox_id = ANY(@mailbox_ids::uuid[]);
