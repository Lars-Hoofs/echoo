-- Every query here takes the caller's readable mailbox ids as a parameter, so out-of-scope
-- rows are never selected. Deleted conversations and messages are always excluded.

-- name: InboxMailboxCounts :many
SELECT mailboxes.id, mailboxes.name, mailboxes.email_address, count(conversations.id)::integer AS open_count
FROM mailboxes
LEFT JOIN conversations ON conversations.mailbox_id = mailboxes.id
    AND conversations.status = 'open' AND conversations.deleted_at IS NULL
    AND (conversations.snoozed_until IS NULL OR conversations.snoozed_until <= now())
WHERE mailboxes.id = ANY(@mailbox_ids::uuid[]) AND mailboxes.disabled_at IS NULL
GROUP BY mailboxes.id
ORDER BY lower(mailboxes.name), mailboxes.id;

-- name: InboxTeamCounts :many
SELECT teams.id, teams.name, count(conversations.id)::integer AS open_count
FROM teams
LEFT JOIN conversations ON conversations.assignee_team_id = teams.id
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[])
    AND conversations.status = 'open' AND conversations.deleted_at IS NULL
    AND (conversations.snoozed_until IS NULL OR conversations.snoozed_until <= now())
WHERE @all_teams::boolean
    OR teams.id IN (SELECT team_members.team_id FROM team_members WHERE team_members.user_id = @user_id)
GROUP BY teams.id
ORDER BY lower(teams.name), teams.id;

-- name: InboxOpenCounts :one
SELECT
    count(*) FILTER (WHERE assignee_user_id = @user_id)::integer AS mine,
    count(*) FILTER (WHERE assignee_user_id IS NULL)::integer AS unassigned,
    count(*)::integer AS total
FROM conversations
WHERE mailbox_id = ANY(@mailbox_ids::uuid[]) AND status = 'open' AND deleted_at IS NULL
    AND (snoozed_until IS NULL OR snoozed_until <= now());

-- The page queries only pick ids in keyset order; ListConversationsByID hydrates them.
-- The cursor defaults sort after every real row, so the first page needs no OR branch that
-- would keep the planner from using the list indexes.

-- name: ListConversationPageByMailbox :many
-- One index scan per mailbox, each stopping after page_size rows, merged and cut again.
SELECT page.id, page.last_message_at
FROM unnest(@mailbox_ids::uuid[]) AS mailbox(id)
CROSS JOIN LATERAL (
    SELECT conversations.id, conversations.last_message_at
    FROM conversations
    WHERE conversations.mailbox_id = mailbox.id
        AND conversations.status = ANY(@statuses::text[])
        AND COALESCE(conversations.snoozed_until > now(), false) = @snoozed::boolean
        AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (
            SELECT 1 FROM conversation_labels
            WHERE conversation_labels.conversation_id = conversations.id AND conversation_labels.label_id = sqlc.narg(label_id)::uuid))
        AND conversations.deleted_at IS NULL
        AND (conversations.last_message_at, conversations.id) < (
            COALESCE(sqlc.narg(cursor_at)::timestamptz, 'infinity'::timestamptz),
            COALESCE(sqlc.narg(cursor_id)::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid))
        AND (NOT @unassigned_only::boolean OR conversations.assignee_user_id IS NULL)
        AND (sqlc.narg(team_id)::uuid IS NULL OR conversations.assignee_team_id = sqlc.narg(team_id)::uuid)
    ORDER BY conversations.last_message_at DESC, conversations.id DESC
    LIMIT @page_size::integer
) AS page
ORDER BY page.last_message_at DESC, page.id DESC
LIMIT @page_size::integer;

-- name: ListConversationPageAssigned :many
SELECT conversations.id, conversations.last_message_at
FROM conversations
WHERE conversations.assignee_user_id = @user_id
    AND conversations.status = ANY(@statuses::text[])
    AND COALESCE(conversations.snoozed_until > now(), false) = @snoozed::boolean
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM conversation_labels
        WHERE conversation_labels.conversation_id = conversations.id AND conversation_labels.label_id = sqlc.narg(label_id)::uuid))
    AND conversations.deleted_at IS NULL
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[])
    AND (conversations.last_message_at, conversations.id) < (
        COALESCE(sqlc.narg(cursor_at)::timestamptz, 'infinity'::timestamptz),
        COALESCE(sqlc.narg(cursor_id)::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid))
    AND (sqlc.narg(team_id)::uuid IS NULL OR conversations.assignee_team_id = sqlc.narg(team_id)::uuid)
ORDER BY conversations.last_message_at DESC, conversations.id DESC
LIMIT @page_size::integer;

-- name: ListConversationsByID :many
SELECT
    conversations.id, conversations.number, conversations.subject, conversations.status, conversations.priority,
    conversations.preview, conversations.last_message_at, conversations.message_count, conversations.has_attachments,
    conversations.snoozed_until, conversations.version,
    conversations.created_at, conversations.first_responded_at, conversations.resolved_at,
    conversations.sla_policy_id, conversations.sla_state, conversations.first_response_due_at,
    conversations.first_response_met_at, conversations.resolution_due_at,
    (SELECT messages.direction FROM messages
        WHERE messages.conversation_id = conversations.id AND messages.kind = 'email' AND messages.deleted_at IS NULL
        ORDER BY messages.received_at DESC, messages.id DESC LIMIT 1) AS last_direction,
    mailboxes.id AS mailbox_id, mailboxes.name AS mailbox_name,
    contacts.id AS contact_id, contacts.name AS contact_name, COALESCE(contact_address.email, '')::text AS contact_email,
    users.id AS assignee_id, users.name AS assignee_name,
    teams.id AS team_id, teams.name AS team_name
FROM conversations
JOIN mailboxes ON mailboxes.id = conversations.mailbox_id
LEFT JOIN contacts ON contacts.id = conversations.contact_id
LEFT JOIN LATERAL (
    SELECT contact_addresses.email FROM contact_addresses
    WHERE contact_addresses.contact_id = contacts.id
    ORDER BY contact_addresses.is_primary DESC, contact_addresses.email
    LIMIT 1
) AS contact_address ON true
LEFT JOIN users ON users.id = conversations.assignee_user_id
LEFT JOIN teams ON teams.id = conversations.assignee_team_id
WHERE conversations.id = ANY(@ids::uuid[])
    AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[])
    -- Only the trash view asks for trashed conversations.
    AND (conversations.deleted_at IS NOT NULL) = @trashed::boolean
ORDER BY conversations.last_message_at DESC, conversations.id DESC;

-- name: ListConversationMessages :many
-- body_html is deliberately not selected: it is only ever served sanitized, in a sandboxed iframe.
SELECT
    messages.id, messages.kind, messages.direction, messages.from_name, messages.from_addr,
    messages.to_addrs, messages.cc_addrs, messages.subject, messages.body_text,
    messages.sent_at, messages.received_at,
    users.id AS author_id, users.name AS author_name,
    outbound.status AS outbound_status, outbound.error AS outbound_error
FROM messages
LEFT JOIN users ON users.id = messages.author_user_id
LEFT JOIN outbound ON outbound.message_id = messages.id
WHERE messages.conversation_id = @conversation_id AND messages.deleted_at IS NULL
ORDER BY messages.received_at, messages.id;

-- name: ListMessageAttachments :many
SELECT id, message_id, filename, size_bytes, sniffed_type, disposition, scan_status
FROM attachments
WHERE message_id = ANY(@message_ids::uuid[])
ORDER BY created_at, id;

-- name: GetConversationContact :one
-- The conversation count only covers mailboxes the caller can read.
SELECT
    contacts.id, contacts.name, COALESCE(contact_address.email, '')::text AS email,
    organizations.id AS organization_id, organizations.name AS organization_name,
    (SELECT count(*) FROM conversations
        WHERE conversations.contact_id = contacts.id AND conversations.deleted_at IS NULL
            AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[]))::integer AS conversation_count
FROM contacts
LEFT JOIN organizations ON organizations.id = contacts.organization_id
LEFT JOIN LATERAL (
    SELECT contact_addresses.email FROM contact_addresses
    WHERE contact_addresses.contact_id = contacts.id
    ORDER BY contact_addresses.is_primary DESC, contact_addresses.email
    LIMIT 1
) AS contact_address ON true
WHERE contacts.id = @id;
