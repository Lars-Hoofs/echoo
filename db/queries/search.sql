-- Search and advanced list filters. Every query takes the caller's readable mailbox ids, so
-- out-of-scope rows are never selected. The filter conditions are repeated in the list and the
-- search query on purpose: sqlc has no fragments and the list query needs its own plan shape.

-- name: ResolveMailboxNames :many
SELECT id FROM mailboxes
WHERE id = ANY(@scope_ids::uuid[]) AND (id = ANY(@ids::uuid[]) OR name ILIKE ANY(@patterns::text[]));

-- name: ResolveLabelNames :many
SELECT id FROM labels WHERE id = ANY(@ids::uuid[]) OR name ILIKE ANY(@patterns::text[]);

-- name: ResolveTeamNames :many
SELECT id FROM teams WHERE id = ANY(@ids::uuid[]) OR name ILIKE ANY(@patterns::text[]);

-- name: ResolveUserNames :many
SELECT id FROM users WHERE deactivated_at IS NULL AND (id = ANY(@ids::uuid[]) OR name ILIKE ANY(@patterns::text[]));

-- name: ListConversationPageFiltered :many
-- Same keyset order as the inbox list. One index scan per mailbox, each stopping after page_size rows.
SELECT page.id, page.last_message_at
FROM unnest(@mailbox_ids::uuid[]) AS mailbox(id)
CROSS JOIN LATERAL (
    SELECT conversations.id, conversations.last_message_at
    FROM conversations
    WHERE conversations.mailbox_id = mailbox.id
        AND conversations.deleted_at IS NULL
        AND conversations.status = ANY(@statuses::text[])
        AND (@snoozed_mode::text = 'include' OR (@snoozed_mode::text = 'only') = COALESCE(conversations.snoozed_until > now(), false))
        AND (conversations.last_message_at, conversations.id) < (
            COALESCE(sqlc.narg(cursor_at)::timestamptz, 'infinity'::timestamptz),
            COALESCE(sqlc.narg(cursor_id)::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid))
        AND (cardinality(@label_ids::uuid[]) = 0 OR EXISTS (
            SELECT 1 FROM conversation_labels
            WHERE conversation_labels.conversation_id = conversations.id AND conversation_labels.label_id = ANY(@label_ids::uuid[])))
        AND ((cardinality(@assignee_ids::uuid[]) = 0 AND NOT @assignee_none::boolean)
            OR conversations.assignee_user_id = ANY(@assignee_ids::uuid[])
            OR (@assignee_none::boolean AND conversations.assignee_user_id IS NULL))
        AND (cardinality(@team_ids::uuid[]) = 0 OR conversations.assignee_team_id = ANY(@team_ids::uuid[]))
        AND (cardinality(@priorities::text[]) = 0 OR conversations.priority = ANY(@priorities::text[]))
        AND (NOT @has_attachment::boolean OR conversations.has_attachments)
        AND (sqlc.narg(after)::timestamptz IS NULL OR conversations.last_message_at >= sqlc.narg(after)::timestamptz)
        AND (sqlc.narg(before)::timestamptz IS NULL OR conversations.last_message_at < sqlc.narg(before)::timestamptz)
        AND (sqlc.narg(contact_id)::uuid IS NULL OR conversations.contact_id = sqlc.narg(contact_id)::uuid)
        AND (sqlc.narg(organization_id)::uuid IS NULL OR EXISTS (
            SELECT 1 FROM contacts
            WHERE contacts.id = conversations.contact_id AND contacts.organization_id = sqlc.narg(organization_id)::uuid))
        AND (sqlc.narg(number)::bigint IS NULL OR conversations.number = sqlc.narg(number)::bigint)
        AND (cardinality(@from_patterns::text[]) = 0 OR EXISTS (
            SELECT 1 FROM messages
            WHERE messages.conversation_id = conversations.id AND messages.deleted_at IS NULL AND messages.kind = 'email'
                AND lower(messages.from_addr) ILIKE ANY(@from_patterns::text[])))
        AND (cardinality(@to_patterns::text[]) = 0 OR EXISTS (
            SELECT 1 FROM messages, jsonb_array_elements(messages.to_addrs || messages.cc_addrs) AS recipient
            WHERE messages.conversation_id = conversations.id AND messages.deleted_at IS NULL AND messages.kind = 'email'
                AND lower(recipient ->> 'address') ILIKE ANY(@to_patterns::text[])))
    ORDER BY conversations.last_message_at DESC, conversations.id DESC
    LIMIT @page_size::integer
) AS page
ORDER BY page.last_message_at DESC, page.id DESC
LIMIT @page_size::integer;

-- name: SearchConversationPage :many
-- Candidates come from four separately indexed sources: message full text (best message per
-- conversation), subject word similarity, contact name and contact address. Score is the message
-- rank (0..1) plus 0.5 for a subject hit and 0.25 for a contact hit, rounded so that recency
-- decides between near-equal scores. plain_text is the free text without quotes. With exact
-- (the text has a quoted phrase) only the unstemmed config matches: the Dutch one drops stop
-- words such as "niet", which would make a phrase match too much.
-- Only the 2000 newest matching messages are ranked, so a term found in most of the mail costs
-- as much as a rare one (messages_received_at serves that scan). Conversations are then fetched
-- by primary key, one lookup per candidate (OFFSET 0 keeps the planner from flattening that
-- lookup into a scan of every conversation).
WITH hits AS MATERIALIZED (
    SELECT messages.id, messages.conversation_id, messages.received_at,
        GREATEST(
            CASE WHEN @exact::boolean THEN 0 ELSE ts_rank_cd(messages.fts, websearch_to_tsquery('dutch', @text::text), 32) END,
            ts_rank_cd(messages.fts, websearch_to_tsquery('echoo_simple', @text::text), 32)
        ) AS rank
    FROM messages
    WHERE messages.mailbox_id = ANY(@mailbox_ids::uuid[]) AND messages.deleted_at IS NULL
        AND ((NOT @exact::boolean AND messages.fts @@ websearch_to_tsquery('dutch', @text::text))
            OR messages.fts @@ websearch_to_tsquery('echoo_simple', @text::text))
    ORDER BY messages.received_at DESC, messages.id DESC
    LIMIT 2000
),
msg AS (
    SELECT DISTINCT ON (hits.conversation_id) hits.conversation_id, hits.id AS message_id, hits.rank
    FROM hits
    ORDER BY hits.conversation_id, hits.rank DESC, hits.received_at DESC
),
subj AS (
    SELECT conversations.id FROM conversations
    WHERE conversations.mailbox_id = ANY(@mailbox_ids::uuid[]) AND conversations.deleted_at IS NULL
        AND @plain_text::text <% conversations.subject
    ORDER BY conversations.last_message_at DESC
    LIMIT 2000
),
contact_name AS (
    SELECT conversations.id FROM contacts
    JOIN conversations ON conversations.contact_id = contacts.id
    WHERE @plain_text::text <% contacts.name
        AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[]) AND conversations.deleted_at IS NULL
    ORDER BY conversations.last_message_at DESC
    LIMIT 2000
),
contact_email AS (
    SELECT conversations.id FROM contact_addresses
    JOIN conversations ON conversations.contact_id = contact_addresses.contact_id
    WHERE sqlc.narg(email_pattern)::text IS NOT NULL AND contact_addresses.email ILIKE sqlc.narg(email_pattern)::text
        AND conversations.mailbox_id = ANY(@mailbox_ids::uuid[]) AND conversations.deleted_at IS NULL
    ORDER BY conversations.last_message_at DESC
    LIMIT 2000
),
candidate AS (
    SELECT conversation_id AS id, rank, message_id, false AS in_subject, false AS in_contact FROM msg
    UNION ALL SELECT id, 0, NULL, true, false FROM subj
    UNION ALL SELECT id, 0, NULL, false, true FROM contact_name
    UNION ALL SELECT id, 0, NULL, false, true FROM contact_email
),
merged AS (
    SELECT candidate.id, max(candidate.rank) AS rank,
        (array_agg(candidate.message_id) FILTER (WHERE candidate.message_id IS NOT NULL))[1]::uuid AS message_id,
        bool_or(candidate.in_subject) AS in_subject, bool_or(candidate.in_contact) AS in_contact
    FROM candidate
    GROUP BY candidate.id
)
SELECT found.id, found.last_message_at, scored.score, merged.message_id AS best_message_id
FROM merged
CROSS JOIN LATERAL (
    SELECT conversations.id, conversations.last_message_at
    FROM conversations
    WHERE conversations.id = merged.id
        AND conversations.deleted_at IS NULL
        AND conversations.status = ANY(@statuses::text[])
        AND (@snoozed_mode::text = 'include' OR (@snoozed_mode::text = 'only') = COALESCE(conversations.snoozed_until > now(), false))
        AND (cardinality(@label_ids::uuid[]) = 0 OR EXISTS (
            SELECT 1 FROM conversation_labels
            WHERE conversation_labels.conversation_id = conversations.id AND conversation_labels.label_id = ANY(@label_ids::uuid[])))
        AND ((cardinality(@assignee_ids::uuid[]) = 0 AND NOT @assignee_none::boolean)
            OR conversations.assignee_user_id = ANY(@assignee_ids::uuid[])
            OR (@assignee_none::boolean AND conversations.assignee_user_id IS NULL))
        AND (cardinality(@team_ids::uuid[]) = 0 OR conversations.assignee_team_id = ANY(@team_ids::uuid[]))
        AND (cardinality(@priorities::text[]) = 0 OR conversations.priority = ANY(@priorities::text[]))
        AND (NOT @has_attachment::boolean OR conversations.has_attachments)
        AND (sqlc.narg(after)::timestamptz IS NULL OR conversations.last_message_at >= sqlc.narg(after)::timestamptz)
        AND (sqlc.narg(before)::timestamptz IS NULL OR conversations.last_message_at < sqlc.narg(before)::timestamptz)
        AND (sqlc.narg(number)::bigint IS NULL OR conversations.number = sqlc.narg(number)::bigint)
        AND (cardinality(@from_patterns::text[]) = 0 OR EXISTS (
            SELECT 1 FROM messages
            WHERE messages.conversation_id = conversations.id AND messages.deleted_at IS NULL AND messages.kind = 'email'
                AND lower(messages.from_addr) ILIKE ANY(@from_patterns::text[])))
        AND (cardinality(@to_patterns::text[]) = 0 OR EXISTS (
            SELECT 1 FROM messages, jsonb_array_elements(messages.to_addrs || messages.cc_addrs) AS recipient
            WHERE messages.conversation_id = conversations.id AND messages.deleted_at IS NULL AND messages.kind = 'email'
                AND lower(recipient ->> 'address') ILIKE ANY(@to_patterns::text[])))
    OFFSET 0
) AS found
CROSS JOIN LATERAL (
    SELECT round((COALESCE(merged.rank, 0)
        + CASE WHEN merged.in_subject THEN 0.5 ELSE 0 END
        + CASE WHEN merged.in_contact THEN 0.25 ELSE 0 END)::numeric, 2)::float8 AS score
) AS scored
WHERE (scored.score, found.last_message_at, found.id) < (
    COALESCE(sqlc.narg(cursor_score)::float8, 'Infinity'::float8),
    COALESCE(sqlc.narg(cursor_at)::timestamptz, 'infinity'::timestamptz),
    COALESCE(sqlc.narg(cursor_id)::uuid, 'ffffffff-ffff-ffff-ffff-ffffffffffff'::uuid))
ORDER BY scored.score DESC, found.last_message_at DESC, found.id DESC
LIMIT @page_size::integer;

-- name: SearchSnippets :many
-- Plain-text markers (U+27E6 and U+27E7) delimit the matches; they are stripped from the source
-- first so the text itself can never fake a highlight. The client turns them into text segments.
SELECT messages.id,
    ts_headline(
        (CASE WHEN @exact::boolean THEN 'echoo_simple' ELSE 'dutch' END)::regconfig,
        left(translate(messages.body_text, '⟦⟧', ''), 50000),
        CASE WHEN @exact::boolean THEN websearch_to_tsquery('echoo_simple', @text::text)
            ELSE websearch_to_tsquery('dutch', @text::text) || websearch_to_tsquery('echoo_simple', @text::text) END,
        'StartSel=⟦, StopSel=⟧, MaxFragments=1, MaxWords=30, MinWords=12, ShortWord=2')::text AS snippet
FROM messages
WHERE messages.id = ANY(@message_ids::uuid[]) AND messages.mailbox_id = ANY(@mailbox_ids::uuid[]);

-- name: ListSavedViewsFor :many
-- Personal views of the caller plus shared views. Admins see every shared view; others only
-- those shared with everyone or with one of their teams.
SELECT *
FROM saved_views
WHERE owner_user_id = @user_id
    OR (owner_user_id IS NULL AND (
        team_id IS NULL OR @all_teams::boolean
        OR team_id IN (SELECT team_members.team_id FROM team_members WHERE team_members.user_id = @user_id)))
ORDER BY (owner_user_id IS NULL), position, lower(name), id;

-- name: GetSavedView :one
SELECT *
FROM saved_views WHERE id = @id;

-- name: InsertSavedView :one
INSERT INTO saved_views (name, owner_user_id, team_id, created_by, filters, position)
VALUES (@name, sqlc.narg(owner_user_id), sqlc.narg(team_id), @created_by, @filters,
    (SELECT COALESCE(max(position), 0) + 1 FROM saved_views
        WHERE owner_user_id IS NOT DISTINCT FROM sqlc.narg(owner_user_id)))
RETURNING *;

-- name: UpdateSavedView :one
UPDATE saved_views SET
    name = COALESCE(sqlc.narg(name), name),
    filters = COALESCE(sqlc.narg(filters), filters),
    position = COALESCE(sqlc.narg(position), position),
    owner_user_id = CASE WHEN @set_scope::boolean THEN sqlc.narg(owner_user_id) ELSE owner_user_id END,
    team_id = CASE WHEN @set_scope::boolean THEN sqlc.narg(team_id) ELSE team_id END,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: RemoveSavedView :execrows
DELETE FROM saved_views WHERE id = @id;

-- name: IsTeamMember :one
SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id = @team_id AND user_id = @user_id);
