-- name: NewUUID :one
SELECT uuidv7()::uuid;

-- API tokens

-- name: CreateAPIToken :one
INSERT INTO api_tokens (user_id, name, prefix, token_hash, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetActiveAPIToken :one
SELECT sqlc.embed(api_tokens), sqlc.embed(users)
FROM api_tokens
JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.token_hash = $1
  AND api_tokens.revoked_at IS NULL
  AND (api_tokens.expires_at IS NULL OR api_tokens.expires_at > now())
  AND users.deactivated_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;

-- name: ListUserAPITokens :many
SELECT * FROM api_tokens
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: ListAllAPITokens :many
SELECT sqlc.embed(api_tokens), users.name AS user_name, users.email AS user_email
FROM api_tokens
JOIN users ON users.id = api_tokens.user_id
WHERE api_tokens.revoked_at IS NULL
ORDER BY api_tokens.created_at DESC, api_tokens.id DESC;

-- Pass user_id to restrict revocation to the token's owner; leave it NULL for admins.
-- name: RevokeAPIToken :one
UPDATE api_tokens SET revoked_at = now()
WHERE id = sqlc.arg(id)
  AND revoked_at IS NULL
  AND (sqlc.narg(user_id)::uuid IS NULL OR user_id = sqlc.narg(user_id)::uuid)
RETURNING *;

-- Webhooks

-- name: InsertWebhook :one
INSERT INTO webhooks (id, url, secret_enc, events, include_content, allow_http, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListWebhooks :many
SELECT * FROM webhooks ORDER BY created_at, id;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = $1;

-- Re-enabling clears the failure streak, so a fixed receiver gets a fresh budget.
-- name: UpdateWebhook :one
UPDATE webhooks SET
    url = $2, events = $3, include_content = $4, allow_http = $5, enabled = $6,
    disabled_reason = CASE WHEN $6 THEN '' ELSE disabled_reason END,
    consecutive_failures = CASE WHEN $6 AND NOT enabled THEN 0 ELSE consecutive_failures END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteWebhook :execrows
DELETE FROM webhooks WHERE id = $1;

-- Only records an event somebody would receive; without subscribers the outbox stays empty.
-- name: RecordWebhookEvent :exec
INSERT INTO webhook_events (type, mailbox_id, conversation_id, message_id, contact_id)
SELECT sqlc.arg(type)::text, sqlc.narg(mailbox_id)::uuid, sqlc.narg(conversation_id)::uuid,
       sqlc.narg(message_id)::uuid, sqlc.narg(contact_id)::uuid
WHERE EXISTS (SELECT 1 FROM webhooks WHERE enabled AND sqlc.arg(type)::text = ANY (events));

-- name: InsertTestWebhookEvent :one
INSERT INTO webhook_events (type, fanned_out_at) VALUES ('webhook.test', now())
RETURNING id;

-- name: ClaimWebhookEvents :many
SELECT * FROM webhook_events
WHERE fanned_out_at IS NULL
ORDER BY id
LIMIT sqlc.arg(batch)::integer
FOR UPDATE SKIP LOCKED;

-- name: MarkWebhookEventFannedOut :exec
UPDATE webhook_events SET fanned_out_at = now() WHERE id = $1;

-- name: ListSubscribedWebhookIDs :many
SELECT id FROM webhooks WHERE enabled AND sqlc.arg(event)::text = ANY (events);

-- name: GetWebhookEvent :one
SELECT * FROM webhook_events WHERE id = $1;

-- name: InsertWebhookDelivery :one
INSERT INTO webhook_deliveries (webhook_id, event_id, event)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries WHERE id = $1;

-- name: SetWebhookDeliveryResult :exec
UPDATE webhook_deliveries SET
    status = $2, status_code = $3, error = $4, duration_ms = $5, attempt = $6,
    payload_hash = COALESCE($7, payload_hash), updated_at = now()
WHERE id = $1;

-- Keyset pagination on the time-ordered UUID: pass a NULL before_id for the first page.
-- name: ListWebhookDeliveries :many
SELECT * FROM webhook_deliveries
WHERE webhook_id = sqlc.arg(webhook_id)
  AND (sqlc.narg(before_id)::uuid IS NULL OR id < sqlc.narg(before_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(page_size)::integer;

-- name: RecordWebhookSuccess :exec
UPDATE webhooks SET consecutive_failures = 0 WHERE id = $1 AND consecutive_failures <> 0;

-- name: RecordWebhookFailure :one
UPDATE webhooks SET
    consecutive_failures = consecutive_failures + 1,
    enabled = enabled AND consecutive_failures + 1 < sqlc.arg(threshold)::integer,
    disabled_reason = CASE
        WHEN enabled AND consecutive_failures + 1 >= sqlc.arg(threshold)::integer THEN 'too_many_failures'
        ELSE disabled_reason END,
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: PurgeWebhookDeliveries :execrows
DELETE FROM webhook_deliveries WHERE created_at < $1;

-- name: PurgeWebhookEvents :execrows
DELETE FROM webhook_events WHERE occurred_at < $1 AND fanned_out_at IS NOT NULL;

-- Payload sources, read at delivery time.

-- name: WebhookConversation :one
SELECT id, number, mailbox_id, status, assignee_user_id, subject FROM conversations WHERE id = $1;

-- name: WebhookMessage :one
SELECT id, conversation_id, mailbox_id, kind, direction, subject, left(body_text, 2000)::text AS body_text
FROM messages WHERE id = $1;

-- name: WebhookContact :one
SELECT contacts.id, contacts.name,
       COALESCE((SELECT email FROM contact_addresses WHERE contact_id = contacts.id
                 ORDER BY is_primary DESC, email LIMIT 1), '')::text AS email
FROM contacts WHERE contacts.id = $1;

-- Admin job viewer

-- name: ListProblemRawMessages :many
SELECT raw_messages.id, raw_messages.mailbox_id, mailboxes.name AS mailbox_name,
       raw_messages.parse_status, raw_messages.parse_error, raw_messages.size_bytes, raw_messages.received_at
FROM raw_messages
JOIN mailboxes ON mailboxes.id = raw_messages.mailbox_id
WHERE raw_messages.parse_status IN ('failed', 'skipped')
ORDER BY raw_messages.received_at DESC, raw_messages.id DESC
LIMIT 100;

-- name: ResetRawMessage :execrows
UPDATE raw_messages SET parse_status = 'pending', parse_error = ''
WHERE id = $1 AND parse_status IN ('failed', 'skipped');
