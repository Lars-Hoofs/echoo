-- name: CsatGetSettings :one
SELECT mailbox_id, enabled, delay_hours, enabled_at FROM csat_settings WHERE mailbox_id = $1;

-- name: CsatUpsertSettings :one
-- enabled_at restarts whenever the survey is switched on, so switching it off and on again
-- never surveys conversations that were resolved in between.
INSERT INTO csat_settings (mailbox_id, enabled, delay_hours, enabled_at)
VALUES (@mailbox_id, @enabled, @delay_hours, CASE WHEN @enabled::boolean THEN now() END)
ON CONFLICT (mailbox_id) DO UPDATE SET
    enabled = EXCLUDED.enabled, delay_hours = EXCLUDED.delay_hours, updated_at = now(),
    enabled_at = CASE WHEN EXCLUDED.enabled AND NOT csat_settings.enabled THEN now()
                      WHEN EXCLUDED.enabled THEN csat_settings.enabled_at END
RETURNING mailbox_id, enabled, delay_hours, enabled_at;

-- name: CsatDueConversations :many
-- Closed conversations whose delay has passed and that have no survey row yet. Only recent
-- resolutions qualify, so a long outage of the sweep does not send stale surveys.
SELECT c.id
FROM csat_settings s
JOIN conversations c ON c.mailbox_id = s.mailbox_id
WHERE s.enabled AND c.status = 'closed' AND c.deleted_at IS NULL
    AND c.resolved_at >= s.enabled_at
    AND c.resolved_at <= now() - make_interval(hours => s.delay_hours)
    AND c.resolved_at > now() - make_interval(hours => s.delay_hours) - interval '3 days'
    AND NOT EXISTS (SELECT 1 FROM csat_requests r WHERE r.conversation_id = c.id)
ORDER BY c.resolved_at
LIMIT @batch_size::integer;

-- name: CsatLockConversation :one
SELECT c.id, c.mailbox_id, c.number, c.subject, c.status, c.resolved_at, c.assignee_user_id, c.contact_id,
       m.name AS mailbox_name, m.display_name AS mailbox_display_name
FROM conversations c
JOIN mailboxes m ON m.id = c.mailbox_id
WHERE c.id = $1 AND c.deleted_at IS NULL
FOR UPDATE OF c;

-- name: CsatLatestInbound :one
SELECT id, from_addr, from_name, auto_submitted, is_bounce, is_bulk, message_id_header, references_hdr
FROM messages
WHERE conversation_id = @conversation_id AND kind = 'email' AND direction = 'in' AND deleted_at IS NULL
    AND lower(from_addr) = ANY (@known_customers::text[])
ORDER BY received_at DESC, id DESC
LIMIT 1;

-- name: CsatClaimRequest :one
-- Inserts the survey row; nothing comes back when the conversation already has one.
INSERT INTO csat_requests (conversation_id, mailbox_id, token_hash, sent_at, expires_at, skipped_reason)
VALUES (@conversation_id, @mailbox_id, @token_hash, @sent_at, @expires_at, @skipped_reason)
ON CONFLICT (conversation_id) DO NOTHING
RETURNING conversation_id;

-- name: CsatSetRequestMessage :exec
UPDATE csat_requests SET message_id = $2 WHERE conversation_id = $1;

-- name: CsatGetRequest :one
SELECT r.conversation_id, r.mailbox_id, r.token_hash, r.sent_at, r.expires_at,
       m.name AS mailbox_name, m.display_name AS mailbox_display_name
FROM csat_requests r
JOIN mailboxes m ON m.id = r.mailbox_id
WHERE r.conversation_id = $1 AND r.sent_at IS NOT NULL;

-- name: CsatGetResponse :one
SELECT rating, comment, created_at, updated_at FROM csat_responses WHERE conversation_id = $1;

-- name: CsatUpsertResponse :one
-- A rating can be changed for seven days after the first answer; later attempts match no row.
INSERT INTO csat_responses (conversation_id, mailbox_id, rating, comment, token_hash)
VALUES (@conversation_id, @mailbox_id, @rating, @comment, @token_hash)
ON CONFLICT (conversation_id) DO UPDATE
SET rating = EXCLUDED.rating, comment = EXCLUDED.comment, updated_at = now()
WHERE csat_responses.created_at > now() - interval '7 days'
RETURNING (xmax = 0)::boolean AS inserted, rating, created_at;

-- name: CsatConversationRating :one
-- The rating shown in the conversation, scoped to the caller's mailboxes.
SELECT p.rating, p.comment, p.updated_at
FROM csat_responses p
JOIN conversations c ON c.id = p.conversation_id
WHERE p.conversation_id = @id AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL;

-- name: CsatAssignee :one
SELECT assignee_user_id FROM conversations WHERE id = $1;
