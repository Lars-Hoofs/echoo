-- name: IngestGetRaw :one
SELECT * FROM raw_messages WHERE id = $1;

-- name: IngestLockRaw :one
SELECT id, parse_status FROM raw_messages WHERE id = $1 FOR UPDATE;

-- name: IngestSetRawStatus :exec
UPDATE raw_messages
SET parse_status = @parse_status::text, parse_error = @parse_error::text
WHERE id = @id;

-- name: IngestGetMailbox :one
SELECT id, email_address FROM mailboxes WHERE id = $1;

-- name: IngestFindOutbound :one
SELECT m.id AS message_id, m.conversation_id, o.status
FROM messages m
JOIN outbound o ON o.message_id = m.id
WHERE m.mailbox_id = $1 AND m.message_id_hash = $2 AND m.kind = 'email' AND m.direction = 'out'
ORDER BY m.created_at
LIMIT 1
FOR UPDATE OF o;

-- name: IngestMarkOutboundBounced :execrows
UPDATE outbound
SET status = 'bounced', dsn_status = @dsn_status::text, error = @diagnostic::text, updated_at = now()
WHERE message_id = @message_id AND status IN ('sent', 'uncertain');

-- name: IngestMessageExists :one
SELECT EXISTS (
    SELECT 1 FROM messages
    WHERE mailbox_id = $1 AND message_id_hash = $2 AND raw_sha256 = $3
);

-- name: IngestAdvisoryLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(@key::text, 0));

-- name: IngestGetContactByEmail :one
SELECT contact_id FROM contact_addresses WHERE email = $1;

-- name: IngestCreateContact :one
INSERT INTO contacts (name, organization_id) VALUES ($1, $2) RETURNING id;

-- name: IngestCreateContactAddress :exec
INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, $2, true);

-- name: IngestFillContact :exec
UPDATE contacts
SET name = CASE WHEN name = '' THEN @name::text ELSE name END,
    organization_id = COALESCE(organization_id, @organization_id),
    updated_at = now()
WHERE id = @id
  AND ((name = '' AND @name::text <> '') OR (organization_id IS NULL AND @organization_id::uuid IS NOT NULL));

-- name: IngestFindOrganizationByDomain :one
SELECT id FROM organizations WHERE domains @> ARRAY[@domain::text] ORDER BY created_at, id LIMIT 1;

-- name: IngestCreateOrganization :one
INSERT INTO organizations (name, domains) VALUES (@domain::text, ARRAY[@domain::text]) RETURNING id;

-- name: IngestCreateConversation :one
INSERT INTO conversations (mailbox_id, subject, subject_normalized, contact_id, last_message_at, status)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: IngestLockConversation :one
SELECT id, status, snoozed_until FROM conversations
WHERE id = $1 AND mailbox_id = $2 AND deleted_at IS NULL
FOR UPDATE;

-- name: IngestUpdateConversation :exec
UPDATE conversations
SET message_count = message_count + 1,
    last_message_at = GREATEST(last_message_at, @received_at::timestamptz),
    last_inbound_at = GREATEST(COALESCE(last_inbound_at, @received_at::timestamptz), @received_at::timestamptz),
    preview = CASE WHEN @received_at::timestamptz >= last_message_at THEN @preview::text ELSE preview END,
    has_attachments = has_attachments OR @has_attachments::boolean,
    status = CASE WHEN @reopen::boolean THEN 'open' ELSE status END,
    resolved_at = CASE WHEN @reopen::boolean THEN NULL ELSE resolved_at END,
    snoozed_until = CASE WHEN @wake::boolean THEN NULL ELSE snoozed_until END,
    contact_id = COALESCE(contact_id, @contact_id),
    version = version + 1,
    updated_at = now()
WHERE id = @id;

-- name: IngestInsertMessage :one
INSERT INTO messages (
    conversation_id, mailbox_id, kind, direction, raw_message_id, raw_sha256,
    message_id_header, message_id_hash, in_reply_to, references_hdr, from_addr, from_name,
    to_addrs, cc_addrs, bcc_addrs, reply_to, subject, sent_at, received_at,
    body_text, body_html, auto_submitted, is_bounce, auth_results, is_bulk
) VALUES (
    $1, $2, 'email', 'in', $3, $4,
    $5, $6, $7, $8, $9, $10,
    $11, $12, $13, $14, $15, $16, $17,
    $18, $19, $20, false, $21, $22
)
ON CONFLICT (mailbox_id, message_id_hash, raw_sha256)
    WHERE message_id_hash IS NOT NULL AND raw_sha256 IS NOT NULL DO NOTHING
RETURNING id;

-- name: IngestInsertAttachment :exec
INSERT INTO attachments (message_id, filename, declared_type, sniffed_type, size_bytes, sha256, blob_key, content_id, disposition, scan_status, scan_detail)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: IngestInsertThreadRefs :exec
INSERT INTO thread_refs (mailbox_id, message_id_hash, conversation_id)
SELECT @mailbox_id, h, @conversation_id FROM unnest(@hashes::bytea[]) AS h
ON CONFLICT (mailbox_id, message_id_hash) DO NOTHING;

-- name: IngestInsertEvent :exec
INSERT INTO conversation_events (conversation_id, mailbox_id, type, data)
VALUES ($1, $2, $3, $4);
