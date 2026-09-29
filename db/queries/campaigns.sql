-- One-off email campaigns to contact segments.

-- name: CampaignInsert :one
INSERT INTO campaigns (name, mailbox_id, segment_id, segment_name, subject, body_html, rate_per_minute, created_by)
VALUES (@name, @mailbox_id, @segment_id, @segment_name, @subject, @body_html, @rate_per_minute, @created_by)
RETURNING id;

-- name: CampaignUpdateDraft :execrows
UPDATE campaigns
SET name = @name, mailbox_id = @mailbox_id, segment_id = @segment_id, segment_name = @segment_name,
    subject = @subject, body_html = @body_html, rate_per_minute = @rate_per_minute, updated_at = now()
WHERE id = @id AND status = 'draft';

-- name: CampaignDelete :execrows
DELETE FROM campaigns WHERE id = $1 AND status IN ('draft', 'done', 'cancelled');

-- name: CampaignGet :one
SELECT c.*, m.name AS mailbox_name, m.email_address AS mailbox_address, u.name AS creator_name
FROM campaigns c
JOIN mailboxes m ON m.id = c.mailbox_id
LEFT JOIN users u ON u.id = c.created_by
WHERE c.id = $1;

-- name: CampaignList :many
SELECT c.*, m.name AS mailbox_name, m.email_address AS mailbox_address, u.name AS creator_name
FROM campaigns c
JOIN mailboxes m ON m.id = c.mailbox_id
LEFT JOIN users u ON u.id = c.created_by
ORDER BY c.created_at DESC, c.id DESC
LIMIT 200;

-- name: CampaignCounts :many
SELECT campaign_id, state, skip_reason, count(*)::bigint AS n
FROM campaign_recipients
WHERE campaign_id = ANY (@ids::uuid[])
GROUP BY campaign_id, state, skip_reason;

-- name: CampaignStart :execrows
UPDATE campaigns
SET status = @status, scheduled_at = @scheduled_at, started_at = @started_at, started_by = @started_by, error = '', updated_at = now()
WHERE id = @id AND status = 'draft';

-- name: CampaignPause :execrows
UPDATE campaigns SET status = 'paused', updated_at = now() WHERE id = $1 AND status = 'sending';

-- name: CampaignResume :execrows
UPDATE campaigns SET status = 'sending', error = '', updated_at = now() WHERE id = $1 AND status = 'paused';

-- name: CampaignCancel :execrows
UPDATE campaigns SET status = 'cancelled', finished_at = @now, updated_at = @now
WHERE id = @id AND status IN ('scheduled', 'sending', 'paused');

-- Campaigns the dispatcher has to look at: due, running, or with deliveries still open.
-- name: CampaignListActive :many
SELECT id FROM campaigns c
WHERE c.status = 'sending'
   OR (c.status = 'scheduled' AND c.scheduled_at <= @now)
   OR (c.status IN ('paused', 'cancelled') AND EXISTS (
        SELECT 1 FROM campaign_recipients r WHERE r.campaign_id = c.id AND r.state = 'queued'))
ORDER BY c.created_at, c.id;

-- SKIP LOCKED: a second dispatcher run, or a pause waiting behind this one, never works on
-- the same campaign at the same time.
-- name: CampaignLock :one
SELECT * FROM campaigns WHERE id = $1 FOR UPDATE SKIP LOCKED;

-- name: CampaignBegin :exec
UPDATE campaigns SET status = 'sending', started_at = coalesce(started_at, @now), updated_at = @now WHERE id = @id;

-- name: CampaignSetMaterialized :exec
UPDATE campaigns SET materialized_at = @now, updated_at = @now WHERE id = @id;

-- name: CampaignStop :exec
UPDATE campaigns
SET status = @status, error = @error, finished_at = CASE WHEN @status::text = 'paused' THEN NULL ELSE @now::timestamptz END, updated_at = @now
WHERE id = @id;

-- name: CampaignFinish :exec
UPDATE campaigns SET status = 'done', finished_at = @now, updated_at = @now WHERE id = @id;

-- name: CampaignSegmentName :one
SELECT name FROM contact_segments WHERE id = $1;

-- The address a contact is written to: the primary one, unless it bounced and another does not.
-- name: CampaignCandidates :many
SELECT c.id, c.name, c.unsubscribed_at, c.erased_at,
       coalesce(a.email, '')::text AS email,
       (a.email IS NOT NULL)::boolean AS has_address,
       (a.bounced_at IS NOT NULL)::boolean AS bounced
FROM contacts c
LEFT JOIN LATERAL (
    SELECT email, bounced_at FROM contact_addresses
    WHERE contact_id = c.id
    ORDER BY (bounced_at IS NOT NULL), is_primary DESC, email
    LIMIT 1
) a ON true
WHERE c.id = ANY (@ids::uuid[])
ORDER BY c.id;

-- name: CampaignInsertRecipients :exec
INSERT INTO campaign_recipients (campaign_id, contact_id, email, name, state, skip_reason)
SELECT @campaign_id::uuid, unnest(@contact_ids::uuid[]), unnest(@emails::text[]), unnest(@names::text[]),
       unnest(@states::text[]), unnest(@reasons::text[]);

-- name: CampaignClaimPending :many
SELECT r.id, r.contact_id, r.email, r.name, r.unsubscribe_secret,
       (c.unsubscribed_at IS NOT NULL)::boolean AS unsubscribed,
       EXISTS (SELECT 1 FROM contact_addresses a WHERE a.email = r.email AND a.bounced_at IS NOT NULL) AS bounced
FROM campaign_recipients r
LEFT JOIN contacts c ON c.id = r.contact_id
WHERE r.campaign_id = @campaign_id AND r.state = 'pending'
ORDER BY r.id
LIMIT @batch
FOR UPDATE OF r SKIP LOCKED;

-- name: CampaignSkipRecipient :exec
UPDATE campaign_recipients SET state = 'skipped', skip_reason = @reason, finished_at = @now WHERE id = @id;

-- name: CampaignQueueRecipient :exec
UPDATE campaign_recipients
SET state = 'queued', message_id = @message_id, conversation_id = @conversation_id, queued_at = @now
WHERE id = @id;

-- name: CampaignCreateConversation :one
INSERT INTO conversations (mailbox_id, subject, subject_normalized, contact_id, status)
VALUES (@mailbox_id, @subject, @subject_normalized, @contact_id, 'closed')
RETURNING id;

-- Messages sent in the last window from the mailbox, by any campaign. The pacing budget is
-- what is left of the rate after these.
-- name: CampaignCountQueuedSince :one
SELECT count(*)::bigint
FROM campaign_recipients r
JOIN campaigns c ON c.id = r.campaign_id
WHERE c.mailbox_id = @mailbox_id AND r.queued_at > @since;

-- Copies the final outcome of the send queue onto the recipients. A message the send worker
-- reports as uncertain is not resent: it counts as failed and says so.
-- name: CampaignSyncResults :execrows
UPDATE campaign_recipients r
SET state = CASE WHEN o.status = 'sent' THEN 'sent' ELSE 'failed' END,
    error = CASE o.status
                WHEN 'sent' THEN ''
                WHEN 'bounced' THEN 'bounced: ' || o.error
                WHEN 'uncertain' THEN 'uncertain: ' || o.error
                WHEN 'cancelled' THEN 'cancelled'
                ELSE o.error
            END,
    finished_at = @now
FROM outbound o
WHERE r.campaign_id = @campaign_id AND r.state = 'queued' AND o.message_id = r.message_id
  AND o.status IN ('sent', 'failed', 'uncertain', 'bounced', 'cancelled');

-- Recipients whose message row is gone (retention) can no longer be followed.
-- name: CampaignFailOrphans :execrows
UPDATE campaign_recipients
SET state = 'failed', error = 'message removed', finished_at = @now
WHERE campaign_id = @campaign_id AND state = 'queued' AND message_id IS NULL;

-- name: CampaignCountOpen :one
SELECT count(*) FILTER (WHERE state = 'pending')::bigint AS pending,
       count(*) FILTER (WHERE state = 'queued')::bigint AS queued
FROM campaign_recipients WHERE campaign_id = $1;

-- Stops the messages that have not left yet. Messages already being delivered cannot be
-- called back; the dispatcher records how they end.
-- name: CampaignCancelOutbound :execrows
WITH stopped AS (
    UPDATE outbound o SET status = 'cancelled', updated_at = @now
    FROM campaign_recipients r
    WHERE r.campaign_id = @campaign_id AND r.state = 'queued' AND o.message_id = r.message_id
      AND o.status IN ('queued', 'retry')
    RETURNING o.message_id
)
UPDATE campaign_recipients r
SET state = 'skipped', skip_reason = 'cancelled', finished_at = @now
WHERE r.message_id IN (SELECT message_id FROM stopped);

-- name: CampaignSkipPending :execrows
UPDATE campaign_recipients SET state = 'skipped', skip_reason = 'cancelled', finished_at = @now
WHERE campaign_id = @campaign_id AND state = 'pending';

-- The report. Keyset paging on the recipient id; @state filters when set. The delivery column
-- follows the send queue while a message is open. Only recipients whose contact the viewer may
-- see are listed; a recipient without a contact (erased, merged away) is for admins only.
-- name: CampaignListRecipients :many
SELECT r.id, r.contact_id, r.email, r.name, r.state, r.skip_reason, r.error, r.queued_at, r.finished_at,
       r.conversation_id, coalesce(o.status, '')::text AS delivery
FROM campaign_recipients r
LEFT JOIN outbound o ON o.message_id = r.message_id
WHERE r.campaign_id = @campaign_id
  AND (@state::text = '' OR r.state = @state)
  AND r.id > @after
  AND (@admin::boolean OR (r.contact_id IS NOT NULL AND contact_visible(r.contact_id, @user_id::uuid, false, @mailbox_ids::uuid[])))
ORDER BY r.id
LIMIT @batch;

-- The contact is found through the address when the recipient lost its contact_id, which a
-- merge does: the unsubscribe must still land on whoever owns the address now.
-- name: CampaignRecipientForToken :one
SELECT r.id, coalesce(r.contact_id, (SELECT a.contact_id FROM contact_addresses a WHERE a.email = r.email AND r.email <> ''))::uuid AS contact_id,
       r.email, r.unsubscribe_secret, c.name AS campaign_name,
       coalesce(nullif(m.display_name, ''), m.name)::text AS brand
FROM campaign_recipients r
JOIN campaigns c ON c.id = r.campaign_id
JOIN mailboxes m ON m.id = c.mailbox_id
WHERE r.id = $1;

-- Reports whether this call did the unsubscribing, so a repeated click is not audited again.
-- name: ContactUnsubscribe :execrows
UPDATE contacts SET unsubscribed_at = @now WHERE id = @id AND unsubscribed_at IS NULL;

-- Called when a permanent bounce comes in for one of our messages. The address that failed and
-- the address a campaign wrote to are both marked, so a later campaign skips them.
-- name: CampaignMarkBounced :exec
WITH campaign_row AS (
    UPDATE campaign_recipients
    SET state = 'failed', error = 'bounced: ' || @diagnostic::text, finished_at = coalesce(finished_at, @now)
    WHERE message_id = @message_id AND state IN ('queued', 'sent')
    RETURNING email
)
UPDATE contact_addresses SET bounced_at = @now
WHERE bounced_at IS NULL
  AND (email = lower(@address::text) OR email IN (SELECT email FROM campaign_row));

-- Undo an unsubscription or a bounce by hand. Like ContactUnsubscribe they report whether they
-- changed anything, so a repeated click is not audited again.
-- name: ContactResubscribe :execrows
UPDATE contacts SET unsubscribed_at = NULL WHERE id = @id AND unsubscribed_at IS NOT NULL;

-- name: ContactClearBounce :execrows
UPDATE contact_addresses SET bounced_at = NULL
WHERE contact_id = @contact_id AND email = @email AND bounced_at IS NOT NULL;

-- name: CampaignScrubRecipients :exec
UPDATE campaign_recipients SET email = '', name = '' WHERE contact_id = $1;

-- name: CampaignFailRecipient :exec
UPDATE campaign_recipients SET state = 'failed', error = @error, finished_at = @now WHERE id = @id;

-- name: ContactIsUnsubscribed :one
SELECT (unsubscribed_at IS NOT NULL)::boolean FROM contacts WHERE id = $1;
