-- name: ListThreadRefConversations :many
SELECT message_id_hash, conversation_id
FROM thread_refs
WHERE mailbox_id = $1 AND message_id_hash = ANY(sqlc.arg(hashes)::bytea[]);

-- name: ConversationInMailbox :one
SELECT EXISTS (SELECT 1 FROM conversations WHERE id = $1 AND mailbox_id = $2);

-- name: ListSubjectCandidates :many
SELECT
    c.id,
    c.status,
    c.last_message_at,
    c.resolved_at,
    ARRAY(
        SELECT DISTINCT p.addr
        FROM (
            SELECT lower(m.from_addr) AS addr
            FROM messages m
            WHERE m.conversation_id = c.id AND m.kind = 'email' AND m.deleted_at IS NULL
            UNION ALL
            SELECT lower(e.value ->> 'address')
            FROM messages m, jsonb_array_elements(m.to_addrs || m.cc_addrs) AS e
            WHERE m.conversation_id = c.id AND m.kind = 'email' AND m.deleted_at IS NULL
        ) p
        WHERE p.addr <> ''
    )::text[] AS participants
FROM conversations c
WHERE c.mailbox_id = $1
  AND c.subject_normalized = $2
  AND c.last_message_at >= $3
  AND c.deleted_at IS NULL
  AND c.status <> 'spam'
ORDER BY c.last_message_at DESC
LIMIT 20;
