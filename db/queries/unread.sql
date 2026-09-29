-- Per-agent read state. A conversation is unread for a user when they marked it unread, or a
-- customer message or a note by someone else arrived after they last read it (or, without a
-- read row, after users.unread_since). created_at is the insert time, unlike received_at which
-- follows the mail headers and can be far in the past.

-- name: MarkConversationRead :exec
INSERT INTO conversation_reads (user_id, conversation_id, last_read_at, marked_unread)
VALUES (@user_id, @conversation_id, now(), false)
ON CONFLICT (user_id, conversation_id)
DO UPDATE SET last_read_at = now(), marked_unread = false;

-- name: MarkConversationUnread :exec
INSERT INTO conversation_reads (user_id, conversation_id, last_read_at, marked_unread)
VALUES (@user_id, @conversation_id, now(), true)
ON CONFLICT (user_id, conversation_id)
DO UPDATE SET marked_unread = true;

-- name: ListUnreadConversationIDs :many
SELECT conversations.id
FROM conversations
JOIN users ON users.id = @user_id
LEFT JOIN conversation_reads ON conversation_reads.conversation_id = conversations.id
    AND conversation_reads.user_id = @user_id
WHERE conversations.id = ANY(@ids::uuid[])
    AND (COALESCE(conversation_reads.marked_unread, false) OR EXISTS (
        SELECT 1 FROM messages
        WHERE messages.conversation_id = conversations.id AND messages.deleted_at IS NULL
            AND messages.created_at > COALESCE(conversation_reads.last_read_at, users.unread_since)
            AND ((messages.kind = 'email' AND messages.direction = 'in')
                OR (messages.kind = 'note' AND messages.author_user_id IS DISTINCT FROM @user_id))));

-- name: InboxUnreadMineCount :one
SELECT count(*)::integer
FROM conversations
JOIN users ON users.id = @user_id
LEFT JOIN conversation_reads ON conversation_reads.conversation_id = conversations.id
    AND conversation_reads.user_id = @user_id
WHERE conversations.assignee_user_id = @user_id
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[])
    AND conversations.status = 'open' AND conversations.deleted_at IS NULL
    AND (conversations.snoozed_until IS NULL OR conversations.snoozed_until <= now())
    AND (COALESCE(conversation_reads.marked_unread, false) OR EXISTS (
        SELECT 1 FROM messages
        WHERE messages.conversation_id = conversations.id AND messages.deleted_at IS NULL
            AND messages.created_at > COALESCE(conversation_reads.last_read_at, users.unread_since)
            AND ((messages.kind = 'email' AND messages.direction = 'in')
                OR (messages.kind = 'note' AND messages.author_user_id IS DISTINCT FROM @user_id))));

-- name: GetConversationAssignee :one
SELECT assignee_user_id FROM conversations WHERE id = $1;
