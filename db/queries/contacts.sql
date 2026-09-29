-- Contacts, organizations, notes, custom attributes, segments, imports and GDPR.
-- Visibility is decided by contact_visible / organization_visible (migration 00015); every
-- query that takes a user-supplied contact or organization id goes through them.

-- name: GetVisibleContact :one
SELECT contacts.id, contacts.name, contacts.phone, contacts.custom_attributes, contacts.created_at,
       contacts.updated_at, contacts.last_activity_at, contacts.created_by, contacts.unsubscribed_at,
       organizations.id AS organization_id, organizations.name AS organization_name,
       (SELECT count(*) FROM conversations
        WHERE conversations.contact_id = contacts.id AND conversations.deleted_at IS NULL
          AND (@admin::boolean OR conversations.mailbox_id = ANY (@mailbox_ids::uuid[])))::integer AS conversation_count
FROM contacts
LEFT JOIN organizations ON organizations.id = contacts.organization_id
WHERE contacts.id = @id AND contact_visible(contacts.id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[]);

-- name: LockContact :one
SELECT id, name, phone, organization_id, custom_attributes FROM contacts WHERE id = $1 FOR UPDATE;

-- name: ContactIsVisible :one
SELECT contact_visible(@id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[])::boolean;

-- name: OrganizationIsVisible :one
SELECT organization_visible(@id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[])::boolean;

-- name: ListAddressesOfContacts :many
SELECT contact_id, email, is_primary, bounced_at FROM contact_addresses
WHERE contact_id = ANY (@ids::uuid[])
ORDER BY contact_id, is_primary DESC, email;

-- name: InsertContact :one
INSERT INTO contacts (name, phone, organization_id, custom_attributes, created_by)
VALUES (@name, @phone, sqlc.narg(organization_id), @custom_attributes, sqlc.narg(created_by))
RETURNING id;

-- name: UpdateContact :exec
UPDATE contacts
SET name = @name, phone = @phone, organization_id = sqlc.narg(organization_id),
    custom_attributes = @custom_attributes, updated_at = now()
WHERE id = @id;

-- name: InsertContactAddress :exec
INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES (@contact_id, @email, @is_primary);

-- name: DeleteContactAddress :exec
DELETE FROM contact_addresses WHERE contact_id = @contact_id AND email = @email;

-- name: SetPrimaryContactAddress :exec
UPDATE contact_addresses SET is_primary = (email = @email::text) WHERE contact_id = @contact_id;

-- name: FindContactByEmail :one
SELECT contact_id FROM contact_addresses WHERE email = $1;

-- name: ListContactConversationPage :many
-- Open and waiting conversations first, then by recency. Only conversations the caller can read.
SELECT conversations.id, conversations.last_message_at,
       CASE WHEN conversations.status IN ('open', 'waiting') THEN 0 ELSE 1 END::integer AS rank
FROM conversations
WHERE conversations.contact_id = @contact_id AND conversations.deleted_at IS NULL
  AND (@admin::boolean OR conversations.mailbox_id = ANY (@mailbox_ids::uuid[]))
  AND (sqlc.narg(cursor_id)::uuid IS NULL
       OR CASE WHEN conversations.status IN ('open', 'waiting') THEN 0 ELSE 1 END > sqlc.arg(cursor_rank)::integer
       OR (CASE WHEN conversations.status IN ('open', 'waiting') THEN 0 ELSE 1 END = sqlc.arg(cursor_rank)::integer
           AND (conversations.last_message_at, conversations.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)))
ORDER BY rank, conversations.last_message_at DESC, conversations.id DESC
LIMIT @page_size::integer;

-- name: ListOrganizationConversationPage :many
SELECT conversations.id, conversations.last_message_at,
       CASE WHEN conversations.status IN ('open', 'waiting') THEN 0 ELSE 1 END::integer AS rank
FROM conversations
JOIN contacts ON contacts.id = conversations.contact_id
WHERE contacts.organization_id = @organization_id AND conversations.deleted_at IS NULL
  AND (@admin::boolean OR conversations.mailbox_id = ANY (@mailbox_ids::uuid[]))
  AND (sqlc.narg(cursor_id)::uuid IS NULL
       OR CASE WHEN conversations.status IN ('open', 'waiting') THEN 0 ELSE 1 END > sqlc.arg(cursor_rank)::integer
       OR (CASE WHEN conversations.status IN ('open', 'waiting') THEN 0 ELSE 1 END = sqlc.arg(cursor_rank)::integer
           AND (conversations.last_message_at, conversations.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)))
ORDER BY rank, conversations.last_message_at DESC, conversations.id DESC
LIMIT @page_size::integer;

-- name: ListContactTimeline :many
-- Conversation events of readable conversations plus the contact's notes, newest first.
SELECT * FROM (
    SELECT conversation_events.id, 'event'::text AS kind, conversation_events.type, conversation_events.created_at AS at,
           conversation_events.conversation_id, conversations.number AS conversation_number,
           conversations.subject AS conversation_subject, actor.name AS actor_name, ''::text AS body,
           conversation_events.data, target.id AS target_id, target.name AS target_name
    FROM conversation_events
    JOIN conversations ON conversations.id = conversation_events.conversation_id
    LEFT JOIN users AS actor ON actor.id = conversation_events.actor_user_id
    LEFT JOIN users AS target ON target.id = NULLIF(conversation_events.data ->> 'user_id', '')::uuid
    WHERE conversations.contact_id = @contact_id AND conversations.deleted_at IS NULL
      AND (@admin::boolean OR conversations.mailbox_id = ANY (@mailbox_ids::uuid[]))
    UNION ALL
    SELECT crm_notes.id, 'note'::text, 'note', crm_notes.created_at,
           NULL::uuid, 0::bigint, ''::text, author.name, crm_notes.body, '{}'::jsonb, NULL::uuid, NULL::text
    FROM crm_notes
    LEFT JOIN users AS author ON author.id = crm_notes.author_user_id
    WHERE crm_notes.contact_id = @contact_id
) AS timeline
WHERE (sqlc.narg(cursor_id)::uuid IS NULL
       OR (timeline.at, timeline.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY timeline.at DESC, timeline.id DESC
LIMIT @page_size::integer;

-- Notes

-- name: ListContactNotes :many
SELECT crm_notes.id, crm_notes.body, crm_notes.created_at, crm_notes.updated_at,
       users.id AS author_id, users.name AS author_name
FROM crm_notes LEFT JOIN users ON users.id = crm_notes.author_user_id
WHERE crm_notes.contact_id = @contact_id
ORDER BY crm_notes.created_at DESC, crm_notes.id DESC
LIMIT @row_limit::integer;

-- name: ListOrganizationNotes :many
SELECT crm_notes.id, crm_notes.body, crm_notes.created_at, crm_notes.updated_at,
       users.id AS author_id, users.name AS author_name
FROM crm_notes LEFT JOIN users ON users.id = crm_notes.author_user_id
WHERE crm_notes.organization_id = @organization_id
ORDER BY crm_notes.created_at DESC, crm_notes.id DESC
LIMIT @row_limit::integer;

-- name: InsertCRMNote :one
INSERT INTO crm_notes (contact_id, organization_id, author_user_id, body)
VALUES (sqlc.narg(contact_id), sqlc.narg(organization_id), @author_user_id, @body)
RETURNING id;

-- name: GetCRMNote :one
SELECT id, contact_id, organization_id, author_user_id FROM crm_notes WHERE id = $1;

-- name: UpdateCRMNote :exec
UPDATE crm_notes SET body = @body, updated_at = now() WHERE id = @id;

-- name: DeleteCRMNote :exec
DELETE FROM crm_notes WHERE id = $1;

-- name: GetCRMNoteView :one
SELECT crm_notes.id, crm_notes.body, crm_notes.created_at, crm_notes.updated_at,
       users.id AS author_id, users.name AS author_name
FROM crm_notes LEFT JOIN users ON users.id = crm_notes.author_user_id
WHERE crm_notes.id = $1;

-- Organizations

-- name: ListVisibleOrganizations :many
SELECT organizations.id, organizations.name, organizations.domains, organizations.created_at,
       lower(organizations.name)::text AS sort_name,
       (SELECT count(*) FROM contacts
        WHERE contacts.organization_id = organizations.id
          AND contact_visible(contacts.id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[]))::integer AS contact_count
FROM organizations
WHERE organization_visible(organizations.id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[])
  AND (sqlc.narg(search)::text IS NULL OR organizations.name ILIKE '%' || sqlc.narg(search)::text || '%'
       OR EXISTS (SELECT 1 FROM unnest(organizations.domains) AS domain WHERE domain ILIKE '%' || sqlc.narg(search)::text || '%'))
  AND (lower(organizations.name), organizations.id) > (
        COALESCE(sqlc.narg(cursor_name)::text, ''),
        COALESCE(sqlc.narg(cursor_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid))
ORDER BY lower(organizations.name), organizations.id
LIMIT @page_size::integer;

-- name: GetVisibleOrganization :one
SELECT organizations.id, organizations.name, organizations.domains, organizations.custom_attributes,
       organizations.created_at, organizations.updated_at,
       (SELECT count(*) FROM contacts
        WHERE contacts.organization_id = organizations.id
          AND contact_visible(contacts.id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[]))::integer AS contact_count
FROM organizations
WHERE organizations.id = @id AND organization_visible(organizations.id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[]);

-- name: LockOrganization :one
SELECT id, name, domains, custom_attributes FROM organizations WHERE id = $1 FOR UPDATE;

-- name: ListOrganizationContacts :many
SELECT contacts.id, contacts.name, contacts.last_activity_at,
       COALESCE((SELECT email FROM contact_addresses WHERE contact_id = contacts.id
                 ORDER BY is_primary DESC, email LIMIT 1), '')::text AS email
FROM contacts
WHERE contacts.organization_id = @organization_id
  AND contact_visible(contacts.id, @user_id::uuid, @admin::boolean, @mailbox_ids::uuid[])
ORDER BY contacts.last_activity_at DESC, contacts.id DESC
LIMIT @row_limit::integer;

-- name: InsertOrganization :one
INSERT INTO organizations (name, domains, custom_attributes, created_by)
VALUES (@name, @domains::text[], @custom_attributes, sqlc.narg(created_by))
RETURNING id;

-- name: UpdateOrganization :exec
UPDATE organizations
SET name = @name, domains = @domains::text[], custom_attributes = @custom_attributes, updated_at = now()
WHERE id = @id;

-- name: OrganizationsOwningDomains :many
SELECT id, domains FROM organizations
WHERE domains && @domains::text[] AND id <> COALESCE(sqlc.narg(except_id)::uuid, '00000000-0000-0000-0000-000000000000'::uuid);

-- name: FindOrganizationByName :one
SELECT id FROM organizations WHERE lower(name) = lower(@name::text) ORDER BY created_at, id LIMIT 1;

-- Custom attribute definitions

-- name: ListAttributeDefs :many
SELECT id, entity, key, label, type, options, created_at FROM custom_attribute_defs
WHERE (sqlc.narg(entity)::text IS NULL OR entity = sqlc.narg(entity)::text)
ORDER BY entity, created_at, id;

-- name: GetAttributeDef :one
SELECT id, entity, key, label, type, options, created_at FROM custom_attribute_defs WHERE id = $1;

-- name: InsertAttributeDef :one
INSERT INTO custom_attribute_defs (entity, key, label, type, options)
VALUES (@entity, @key, @label, @type, @options::text[])
RETURNING id, entity, key, label, type, options, created_at;

-- name: UpdateAttributeDef :one
UPDATE custom_attribute_defs SET label = @label, options = @options::text[] WHERE id = @id
RETURNING id, entity, key, label, type, options, created_at;

-- name: DeleteAttributeDef :one
DELETE FROM custom_attribute_defs WHERE id = $1 RETURNING entity, key;

-- name: StripContactAttribute :execrows
UPDATE contacts SET custom_attributes = custom_attributes - @key::text WHERE custom_attributes ? @key::text;

-- name: StripOrganizationAttribute :execrows
UPDATE organizations SET custom_attributes = custom_attributes - @key::text WHERE custom_attributes ? @key::text;

-- name: StripConversationAttribute :execrows
UPDATE conversations SET custom_attributes = custom_attributes - @key::text WHERE custom_attributes ? @key::text;

-- name: LockConversationAttributes :one
SELECT id, mailbox_id, custom_attributes FROM conversations
WHERE id = @id AND mailbox_id = ANY (@mailbox_ids::uuid[]) AND deleted_at IS NULL
FOR UPDATE;

-- name: GetConversationAttributes :one
SELECT id, custom_attributes FROM conversations
WHERE id = @id AND mailbox_id = ANY (@mailbox_ids::uuid[]) AND deleted_at IS NULL;

-- name: UpdateConversationAttributes :exec
UPDATE conversations SET custom_attributes = @custom_attributes, updated_at = now() WHERE id = @id;

-- Segments

-- name: ListSegments :many
SELECT contact_segments.id, contact_segments.name, contact_segments.owner_user_id, contact_segments.shared,
       contact_segments.filter, contact_segments.created_at, contact_segments.updated_at, users.name AS owner_name
FROM contact_segments JOIN users ON users.id = contact_segments.owner_user_id
WHERE contact_segments.owner_user_id = @user_id OR contact_segments.shared
ORDER BY lower(contact_segments.name), contact_segments.id;

-- name: GetSegment :one
SELECT id, name, owner_user_id, shared, filter, created_at, updated_at FROM contact_segments WHERE id = $1;

-- name: InsertSegment :one
INSERT INTO contact_segments (name, owner_user_id, shared, filter)
VALUES (@name, @owner_user_id, @shared, @filter)
RETURNING id;

-- name: UpdateSegment :exec
UPDATE contact_segments SET name = @name, shared = @shared, filter = @filter, updated_at = now() WHERE id = @id;

-- name: DeleteSegment :exec
DELETE FROM contact_segments WHERE id = $1;

-- Imports

-- name: InsertContactImport :one
INSERT INTO contact_imports (created_by, filename, dedupe, delimiter, mapping, source_key, total_rows)
VALUES (@created_by, @filename, @dedupe, @delimiter, @mapping, @source_key, @total_rows)
RETURNING id;

-- name: GetContactImport :one
SELECT id, created_by, filename, status, dedupe, delimiter, mapping, source_key, error_key, total_rows,
       processed_rows, created_count, updated_count, skipped_count, failed_count, error, created_at, finished_at
FROM contact_imports WHERE id = $1;

-- name: StartContactImport :exec
UPDATE contact_imports SET status = 'running' WHERE id = $1;

-- name: UpdateContactImportProgress :exec
UPDATE contact_imports
SET processed_rows = @processed_rows, created_count = @created_count, updated_count = @updated_count,
    skipped_count = @skipped_count, failed_count = @failed_count
WHERE id = @id;

-- name: FinishContactImport :exec
UPDATE contact_imports
SET status = @status, error = @error, error_key = @error_key, source_key = '', finished_at = now(),
    processed_rows = @processed_rows, created_count = @created_count, updated_count = @updated_count,
    skipped_count = @skipped_count, failed_count = @failed_count
WHERE id = @id;

-- name: ExpiredContactImports :many
SELECT id, source_key, error_key FROM contact_imports
WHERE (finished_at IS NOT NULL AND finished_at < @before::timestamptz AND error_key <> '')
   OR (finished_at IS NULL AND created_at < @before::timestamptz AND source_key <> '');

-- name: ClearContactImportBlobs :exec
UPDATE contact_imports SET source_key = '', error_key = '' WHERE id = $1;

-- Data exports (GDPR)

-- name: InsertDataExport :one
INSERT INTO data_exports (contact_id, requested_by) VALUES (@contact_id, @requested_by) RETURNING id;

-- name: GetDataExport :one
SELECT id, contact_id, requested_by, status, blob_key, size_bytes, error, created_at, expires_at
FROM data_exports WHERE id = $1;

-- name: MarkDataExportReady :exec
UPDATE data_exports SET status = 'ready', blob_key = @blob_key, size_bytes = @size_bytes WHERE id = @id;

-- name: MarkDataExportFailed :exec
UPDATE data_exports SET status = 'failed', error = @error WHERE id = @id;

-- name: ClaimDataExportDownload :one
UPDATE data_exports SET status = 'downloaded'
WHERE id = @id AND requested_by = @requested_by AND status = 'ready' AND expires_at > now()
RETURNING blob_key, size_bytes;

-- name: ClearDataExportBlob :exec
UPDATE data_exports SET blob_key = '' WHERE id = $1;

-- name: ExpiredDataExports :many
SELECT id, blob_key FROM data_exports
WHERE blob_key <> '' AND (status = 'downloaded' OR expires_at < now());

-- name: MarkDataExportExpired :exec
UPDATE data_exports SET blob_key = '', status = CASE WHEN status = 'downloaded' THEN status ELSE 'expired' END WHERE id = $1;

-- name: DeleteDataExportsOfContact :many
DELETE FROM data_exports WHERE contact_id = $1 AND blob_key <> '' RETURNING blob_key;

-- Blob reference counting for erasure and purges

-- name: BlobIsReferenced :one
SELECT (EXISTS (SELECT 1 FROM attachments WHERE attachments.blob_key = @key::text)
     OR EXISTS (SELECT 1 FROM raw_messages WHERE raw_messages.blob_key = @key::text)
     OR EXISTS (SELECT 1 FROM uploads WHERE uploads.blob_key = @key::text)
     OR EXISTS (SELECT 1 FROM image_proxy_cache WHERE image_proxy_cache.blob_key = @key::text)
     OR EXISTS (SELECT 1 FROM contact_imports WHERE contact_imports.source_key = @key::text OR contact_imports.error_key = @key::text)
     OR EXISTS (SELECT 1 FROM data_exports WHERE data_exports.blob_key = @key::text)
     OR EXISTS (SELECT 1 FROM kb_images WHERE kb_images.blob_key = @key::text))::boolean;

-- Merge

-- name: MoveContactAddresses :execrows
UPDATE contact_addresses SET contact_id = @primary_id, is_primary = false WHERE contact_id = @secondary_id;

-- name: MoveContactConversations :many
UPDATE conversations SET contact_id = @primary_id WHERE contact_id = @secondary_id RETURNING id, mailbox_id;

-- name: MoveContactNotes :execrows
UPDATE crm_notes SET contact_id = @primary_id WHERE contact_id = @secondary_id;

-- name: MergeContactFields :exec
UPDATE contacts AS primary_contact
SET name = CASE WHEN primary_contact.name = '' THEN secondary.name ELSE primary_contact.name END,
    phone = CASE WHEN primary_contact.phone = '' THEN secondary.phone ELSE primary_contact.phone END,
    organization_id = COALESCE(primary_contact.organization_id, secondary.organization_id),
    custom_attributes = secondary.custom_attributes || primary_contact.custom_attributes,
    last_activity_at = GREATEST(primary_contact.last_activity_at, secondary.last_activity_at),
    unsubscribed_at = COALESCE(primary_contact.unsubscribed_at, secondary.unsubscribed_at),
    updated_at = now()
FROM contacts AS secondary
WHERE primary_contact.id = @primary_id AND secondary.id = @secondary_id;

-- name: DeleteContact :exec
DELETE FROM contacts WHERE id = $1;

-- name: ContactAddressEmails :many
SELECT email FROM contact_addresses WHERE contact_id = $1 ORDER BY is_primary DESC, email;

-- name: InsertContactMergedEvent :exec
INSERT INTO conversation_events (conversation_id, mailbox_id, actor_user_id, type, data)
VALUES (@conversation_id, @mailbox_id, @actor_user_id, 'contact_merged',
        jsonb_build_object('contact_id', @primary_id::text, 'merged_contact_id', @secondary_id::text));

-- Erasure (docs/data-model.md, GDPR)

-- name: ConversationsOfContact :many
SELECT id FROM conversations WHERE contact_id = $1;

-- name: ConversationsWithOtherParties :many
-- Conversations among @ids that involve an external address other than the contact's own and
-- the mailboxes' addresses: senders of inbound mail, recipients of every email.
SELECT DISTINCT messages.conversation_id
FROM messages
CROSS JOIN LATERAL (
    SELECT lower(messages.from_addr) AS addr WHERE messages.direction = 'in'
    UNION ALL
    SELECT lower(a ->> 'address')
    FROM jsonb_array_elements(messages.to_addrs || messages.cc_addrs || messages.bcc_addrs || messages.reply_to) AS a
) AS party
WHERE messages.conversation_id = ANY (@ids::uuid[]) AND messages.kind = 'email'
  AND party.addr <> '' AND NOT (party.addr = ANY (@own::text[]))
  AND NOT EXISTS (SELECT 1 FROM mailboxes WHERE lower(mailboxes.email_address) = party.addr);

-- name: TombstoneMessagesOfAddresses :many
-- Replaces the contact's address and name on messages they sent or received, in the
-- conversations passed and anywhere they appear as sender. Returns the raw messages and ids
-- so the raw copies (which still hold the address) can be dropped.
UPDATE messages SET
    from_name = CASE WHEN lower(from_addr) = ANY (@addrs::text[]) THEN @tombstone_name::text ELSE from_name END,
    from_addr = CASE WHEN lower(from_addr) = ANY (@addrs::text[]) THEN @tombstone_addr::text ELSE from_addr END,
    to_addrs = (SELECT COALESCE(jsonb_agg(CASE WHEN lower(a ->> 'address') = ANY (@addrs::text[])
                    THEN jsonb_build_object('name', @tombstone_name::text, 'address', @tombstone_addr::text) ELSE a END), '[]'::jsonb)
                FROM jsonb_array_elements(messages.to_addrs) AS a),
    cc_addrs = (SELECT COALESCE(jsonb_agg(CASE WHEN lower(a ->> 'address') = ANY (@addrs::text[])
                    THEN jsonb_build_object('name', @tombstone_name::text, 'address', @tombstone_addr::text) ELSE a END), '[]'::jsonb)
                FROM jsonb_array_elements(messages.cc_addrs) AS a),
    bcc_addrs = (SELECT COALESCE(jsonb_agg(CASE WHEN lower(a ->> 'address') = ANY (@addrs::text[])
                    THEN jsonb_build_object('name', @tombstone_name::text, 'address', @tombstone_addr::text) ELSE a END), '[]'::jsonb)
                FROM jsonb_array_elements(messages.bcc_addrs) AS a),
    reply_to = (SELECT COALESCE(jsonb_agg(CASE WHEN lower(a ->> 'address') = ANY (@addrs::text[])
                    THEN jsonb_build_object('name', @tombstone_name::text, 'address', @tombstone_addr::text) ELSE a END), '[]'::jsonb)
                FROM jsonb_array_elements(messages.reply_to) AS a)
WHERE (messages.conversation_id = ANY (@conversation_ids::uuid[]) OR lower(messages.from_addr) = ANY (@addrs::text[]))
  AND messages.kind = 'email'
  AND (lower(messages.from_addr) = ANY (@addrs::text[])
       OR EXISTS (SELECT 1 FROM jsonb_array_elements(messages.to_addrs || messages.cc_addrs || messages.bcc_addrs || messages.reply_to) AS a
                  WHERE lower(a ->> 'address') = ANY (@addrs::text[])))
RETURNING messages.id, messages.raw_message_id;

-- name: BlankMessagesOfConversations :many
-- Deletes what was said: bodies, subjects and the header values that name the other side.
UPDATE messages SET
    body_text = '', body_html = '', subject = '', auth_results = '',
    message_id_header = '', in_reply_to = '', references_hdr = '{}',
    to_addrs = '[]', cc_addrs = '[]', bcc_addrs = '[]', reply_to = '[]',
    from_name = CASE WHEN direction = 'in' OR kind <> 'email' THEN @tombstone_name::text ELSE from_name END,
    from_addr = CASE WHEN direction = 'in' THEN @tombstone_addr::text ELSE from_addr END
WHERE conversation_id = ANY (@conversation_ids::uuid[])
RETURNING id, raw_message_id;

-- name: BlankConversations :execrows
UPDATE conversations
SET subject = '', subject_normalized = '', preview = '', custom_attributes = '{}',
    has_attachments = false, version = version + 1, updated_at = now()
WHERE id = ANY (@ids::uuid[]);

-- name: DeleteThreadRefsOfConversations :exec
DELETE FROM thread_refs WHERE conversation_id = ANY (@ids::uuid[]);

-- name: DeleteDraftsOfConversations :exec
DELETE FROM drafts WHERE conversation_id = ANY (@ids::uuid[]);

-- name: ClearOutboundResponses :exec
UPDATE outbound SET smtp_response = '', error = ''
WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ANY (@ids::uuid[]));

-- name: DeleteAttachmentsOfConversations :many
DELETE FROM attachments
WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = ANY (@ids::uuid[]))
RETURNING blob_key;

-- name: RawBlobKeys :many
SELECT blob_key FROM raw_messages WHERE id = ANY (@ids::uuid[]) AND blob_key <> '';

-- name: DropRawMessages :exec
-- The .eml copy still holds the address and body. The row stays so IMAP sync does not fetch
-- the message again.
UPDATE raw_messages
SET blob_key = '', size_bytes = 0, parse_status = 'skipped', parse_error = 'erased'
WHERE id = ANY (@ids::uuid[]);

-- name: DeleteContactWebhookEvents :exec
DELETE FROM webhook_events WHERE contact_id = $1 AND fanned_out_at IS NULL;

-- GDPR export data

-- name: ExportContactConversations :many
SELECT conversations.id, conversations.number, conversations.subject, conversations.status,
       conversations.custom_attributes, conversations.created_at, mailboxes.name AS mailbox_name
FROM conversations JOIN mailboxes ON mailboxes.id = conversations.mailbox_id
WHERE conversations.contact_id = @contact_id AND conversations.mailbox_id = ANY (@mailbox_ids::uuid[])
  AND conversations.deleted_at IS NULL
ORDER BY conversations.created_at, conversations.id;

-- name: ExportConversationMessages :many
SELECT messages.id, messages.conversation_id, messages.kind, messages.direction, messages.from_name,
       messages.from_addr, messages.to_addrs, messages.cc_addrs, messages.subject, messages.body_text,
       messages.sent_at, messages.received_at, users.name AS author_name
FROM messages LEFT JOIN users ON users.id = messages.author_user_id
WHERE messages.conversation_id = ANY (@ids::uuid[]) AND messages.deleted_at IS NULL
ORDER BY messages.conversation_id, messages.received_at, messages.id;

-- name: ExportMessageAttachments :many
SELECT id, message_id, filename, blob_key, size_bytes FROM attachments
WHERE message_id = ANY (@ids::uuid[]) ORDER BY message_id, created_at, id;

-- name: ExportContactRow :one
SELECT contacts.id, contacts.name, contacts.phone, contacts.custom_attributes, contacts.created_at,
       contacts.updated_at, organizations.name AS organization_name, organizations.domains AS organization_domains
FROM contacts LEFT JOIN organizations ON organizations.id = contacts.organization_id
WHERE contacts.id = $1;

-- name: ExportContactNotes :many
SELECT crm_notes.body, crm_notes.created_at, users.name AS author_name
FROM crm_notes LEFT JOIN users ON users.id = crm_notes.author_user_id
WHERE crm_notes.contact_id = $1 ORDER BY crm_notes.created_at, crm_notes.id;

-- name: CountContactNotes :one
SELECT count(*)::integer FROM crm_notes WHERE contact_id = $1;

-- name: QueueBlobDeletions :exec
INSERT INTO pending_blob_deletions (blob_key)
SELECT DISTINCT key FROM unnest(@keys::text[]) AS key WHERE key <> ''
ON CONFLICT DO NOTHING;

-- name: ListPendingBlobDeletions :many
SELECT blob_key FROM pending_blob_deletions ORDER BY queued_at, blob_key LIMIT @row_limit::integer;

-- name: DeletePendingBlobDeletion :exec
DELETE FROM pending_blob_deletions WHERE blob_key = $1;

-- name: DeleteAutomationRepliesOfAddresses :execrows
DELETE FROM automation_replies WHERE address = ANY (@addrs::text[]);

-- name: DeleteImageAllowlistOfAddresses :execrows
DELETE FROM sender_image_allowlist WHERE pattern = ANY (@addrs::text[]);

-- name: BlankCSATComments :execrows
UPDATE csat_responses SET comment = '' WHERE conversation_id = ANY (@ids::uuid[]) AND comment <> '';

-- name: ExportCSATResponses :many
SELECT conversation_id, rating, comment, created_at FROM csat_responses WHERE conversation_id = ANY (@ids::uuid[]);
