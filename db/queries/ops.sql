-- Operations: retention purges and upload hygiene.

-- name: RetentionListMailboxIDs :many
SELECT id FROM mailboxes ORDER BY id;

-- name: RetentionListPolicies :many
SELECT mailbox_id, closed_conversation_months, attachment_months, spam_days FROM retention_policies;

-- name: RetentionDeletePolicies :exec
DELETE FROM retention_policies;

-- name: RetentionInsertPolicy :exec
INSERT INTO retention_policies (mailbox_id, closed_conversation_months, attachment_months, spam_days, updated_by)
VALUES (@mailbox_id, sqlc.narg(closed_conversation_months), sqlc.narg(attachment_months), sqlc.narg(spam_days), @updated_by);

-- name: RetentionPickConversations :many
-- Conversations of one mailbox in a status whose last activity (last message or, when later,
-- the moment they were resolved) is older than the cutoff. Anything with mail still waiting to
-- be sent stays. Locked rows are skipped so a purge never waits for an agent.
SELECT c.id FROM conversations c
WHERE c.mailbox_id = @mailbox_id AND c.status = @status
  AND GREATEST(c.last_message_at, COALESCE(c.resolved_at, c.last_message_at)) < @before::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM outbound o JOIN messages m ON m.id = o.message_id
      WHERE m.conversation_id = c.id AND o.status IN ('queued', 'sending', 'retry', 'uncertain'))
ORDER BY c.id
LIMIT @batch_size::integer
FOR UPDATE OF c SKIP LOCKED;

-- name: RetentionPreviewConversations :one
-- The counting twin of RetentionPickConversations; keep the predicates identical.
WITH picked AS (
    SELECT c.id FROM conversations c
    WHERE c.mailbox_id = @mailbox_id AND c.status = @status
      AND GREATEST(c.last_message_at, COALESCE(c.resolved_at, c.last_message_at)) < @before::timestamptz
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

-- name: RetentionTotalsOfConversations :one
SELECT (SELECT count(*) FROM messages WHERE conversation_id = ANY (@ids::uuid[]))::bigint AS messages,
       (SELECT count(*) FROM attachments a JOIN messages m ON m.id = a.message_id
         WHERE m.conversation_id = ANY (@ids::uuid[]))::bigint AS attachments;

-- name: RetentionRawIDsOfConversations :many
SELECT DISTINCT raw_message_id FROM messages
WHERE conversation_id = ANY (@ids::uuid[]) AND raw_message_id IS NOT NULL;

-- name: RetentionDeleteConversations :execrows
DELETE FROM conversations WHERE id = ANY (@ids::uuid[]);

-- name: RetentionPickAttachments :many
-- Attachments of messages received before the cutoff. A message that is still being sent keeps
-- its files, because the send job reads them.
SELECT a.id, a.message_id, a.blob_key, a.size_bytes
FROM attachments a JOIN messages m ON m.id = a.message_id
WHERE m.mailbox_id = @mailbox_id AND m.received_at < @before::timestamptz
  AND NOT EXISTS (SELECT 1 FROM outbound o WHERE o.message_id = m.id AND o.status IN ('queued', 'sending', 'retry'))
ORDER BY a.id
LIMIT @batch_size::integer
FOR UPDATE OF a SKIP LOCKED;

-- name: RetentionPreviewAttachments :one
SELECT count(*)::bigint AS attachments, COALESCE(sum(a.size_bytes), 0)::bigint AS attachment_bytes
FROM attachments a JOIN messages m ON m.id = a.message_id
WHERE m.mailbox_id = @mailbox_id AND m.received_at < @before::timestamptz
  AND NOT EXISTS (SELECT 1 FROM outbound o WHERE o.message_id = m.id AND o.status IN ('queued', 'sending', 'retry'));

-- name: RetentionDeleteAttachments :exec
DELETE FROM attachments WHERE id = ANY (@ids::uuid[]);

-- name: RetentionRawIDsOfMessages :many
SELECT DISTINCT raw_message_id FROM messages WHERE id = ANY (@ids::uuid[]) AND raw_message_id IS NOT NULL;

-- name: RetentionRefreshHasAttachments :exec
UPDATE conversations SET has_attachments = EXISTS (
    SELECT 1 FROM attachments a JOIN messages m ON m.id = a.message_id WHERE m.conversation_id = conversations.id)
WHERE id IN (SELECT conversation_id FROM messages WHERE id = ANY (@message_ids::uuid[]));

-- name: RetentionPreviewAudit :one
SELECT count(*)::bigint FROM audit_log WHERE at < @before::timestamptz;

-- name: RetentionPurgeAuditBatch :one
SELECT audit_log_purge(make_interval(months => @months::integer), @batch_size::integer)::integer;

-- name: RetentionExpireUploads :many
-- Composer uploads that were never attached and passed their 24 hours. The rows go here; the
-- files go through the reference-checked deletion queue, because content-addressed blobs are
-- shared.
DELETE FROM uploads WHERE id IN (
    SELECT id FROM uploads WHERE expires_at < now() ORDER BY expires_at LIMIT @batch_size::integer FOR UPDATE SKIP LOCKED)
RETURNING blob_key;

-- name: OpsReferencedBlobKeys :many
-- Which of the given keys anything still points at. Keys queued for deletion count as
-- referenced: they are on their way out and the drain job owns them.
SELECT k::text FROM unnest(@keys::text[]) AS k
WHERE EXISTS (SELECT 1 FROM attachments WHERE blob_key = k)
   OR EXISTS (SELECT 1 FROM raw_messages WHERE blob_key = k)
   OR EXISTS (SELECT 1 FROM uploads WHERE blob_key = k)
   OR EXISTS (SELECT 1 FROM image_proxy_cache WHERE blob_key = k)
   OR EXISTS (SELECT 1 FROM contact_imports WHERE source_key = k OR error_key = k)
   OR EXISTS (SELECT 1 FROM data_exports WHERE blob_key = k)
   OR EXISTS (SELECT 1 FROM kb_images WHERE blob_key = k)
   OR EXISTS (SELECT 1 FROM pending_blob_deletions WHERE blob_key = k);

-- name: CountPendingBlobDeletions :one
SELECT count(*)::bigint FROM pending_blob_deletions;
