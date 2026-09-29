-- Automation: business hours, SLA, rules, macros, auto-assignment and their periodic jobs.

-- name: AutoListBusinessHours :many
SELECT * FROM business_hours ORDER BY is_default DESC, lower(name), id;

-- name: AutoGetBusinessHours :one
SELECT * FROM business_hours WHERE id = $1;

-- name: AutoDefaultBusinessHours :one
SELECT * FROM business_hours WHERE is_default;

-- name: AutoInsertBusinessHours :one
INSERT INTO business_hours (name, timezone, weekly, holidays, is_default)
VALUES (@name, @timezone, @weekly, @holidays, @is_default)
RETURNING *;

-- name: AutoUpdateBusinessHours :one
UPDATE business_hours SET name = @name, timezone = @timezone, weekly = @weekly, holidays = @holidays,
    is_default = @is_default, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: AutoClearDefaultBusinessHours :exec
UPDATE business_hours SET is_default = false, updated_at = now() WHERE is_default AND id IS DISTINCT FROM sqlc.narg(keep)::uuid;

-- name: AutoDeleteBusinessHours :one
DELETE FROM business_hours WHERE id = $1 AND NOT is_default RETURNING name;

-- name: AutoListSLAPolicies :many
SELECT * FROM sla_policies ORDER BY lower(name), id;

-- name: AutoGetSLAPolicy :one
SELECT * FROM sla_policies WHERE id = $1;

-- name: AutoInsertSLAPolicy :one
INSERT INTO sla_policies (name, first_response_minutes, resolution_minutes, at_risk_percent, business_hours_id)
VALUES (@name, @first_response_minutes, @resolution_minutes, @at_risk_percent, @business_hours_id)
RETURNING *;

-- name: AutoUpdateSLAPolicy :one
UPDATE sla_policies SET name = @name, first_response_minutes = @first_response_minutes,
    resolution_minutes = @resolution_minutes, at_risk_percent = @at_risk_percent,
    business_hours_id = @business_hours_id, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: AutoDeleteSLAPolicy :one
DELETE FROM sla_policies WHERE id = $1 RETURNING name;

-- name: AutoListMailboxAutomation :many
SELECT id, name, email_address, auto_assign_mode, default_sla_policy_id, business_hours_id
FROM mailboxes WHERE disabled_at IS NULL ORDER BY lower(name), id;

-- name: AutoUpdateMailboxAutomation :one
UPDATE mailboxes SET auto_assign_mode = @auto_assign_mode, default_sla_policy_id = @default_sla_policy_id,
    business_hours_id = @business_hours_id, updated_at = now()
WHERE id = @id
RETURNING id, name, email_address, auto_assign_mode, default_sla_policy_id, business_hours_id;

-- name: AutoListAgents :many
SELECT users.id, users.name, users.email, users.role, users.max_open, users.availability,
    (SELECT count(*) FROM conversations
        WHERE conversations.assignee_user_id = users.id AND conversations.status = 'open'
            AND conversations.deleted_at IS NULL)::integer AS open_count
FROM users
WHERE users.deactivated_at IS NULL AND users.role <> 'readonly' AND (users.role <> 'custom' OR 'conversations.write' = ANY(users.permissions))
ORDER BY lower(users.name), users.id;

-- name: AutoSetUserCapacity :one
UPDATE users SET max_open = @max_open, updated_at = now()
WHERE id = @id AND deactivated_at IS NULL AND role <> 'readonly' AND (role <> 'custom' OR 'conversations.write' = ANY(permissions))
RETURNING id;

-- name: AutoSetUserAvailability :exec
UPDATE users SET availability = @availability, updated_at = now() WHERE id = @id;

-- name: AutoListRules :many
SELECT * FROM rules ORDER BY position, id;

-- name: AutoGetRule :one
SELECT * FROM rules WHERE id = $1;

-- name: AutoInsertRule :one
INSERT INTO rules (name, mailbox_id, trigger, idle_hours, conditions, actions, stop_processing, enabled, created_by, position)
VALUES (@name, @mailbox_id, @trigger, @idle_hours, @conditions, @actions, @stop_processing, @enabled, @created_by,
    COALESCE((SELECT max(position) FROM rules), 0) + 1)
RETURNING *;

-- name: AutoUpdateRule :one
UPDATE rules SET name = @name, mailbox_id = @mailbox_id, trigger = @trigger, idle_hours = @idle_hours,
    conditions = @conditions, actions = @actions, stop_processing = @stop_processing, enabled = @enabled,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: AutoDeleteRule :one
DELETE FROM rules WHERE id = $1 RETURNING name;

-- name: AutoReorderRules :exec
UPDATE rules SET position = ordered.position, updated_at = now()
FROM unnest(@ids::uuid[]) WITH ORDINALITY AS ordered(id, position)
WHERE rules.id = ordered.id;

-- name: AutoListRulesForTrigger :many
SELECT * FROM rules
WHERE enabled AND trigger = @trigger AND (mailbox_id IS NULL OR mailbox_id = @mailbox_id)
ORDER BY position, id;

-- name: AutoListIdleRules :many
SELECT * FROM rules WHERE enabled AND trigger = 'customer_idle' ORDER BY position, id;

-- name: AutoInsertRuleRun :exec
INSERT INTO rule_runs (rule_id, conversation_id, trigger, matched, actions_applied, error)
VALUES (@rule_id, @conversation_id, @trigger, @matched, @actions_applied, @error);

-- name: AutoListRuleRuns :many
SELECT rule_runs.id, rule_runs.conversation_id, rule_runs.trigger, rule_runs.matched, rule_runs.actions_applied,
    rule_runs.error, rule_runs.created_at, conversations.number AS conversation_number
FROM rule_runs
JOIN conversations ON conversations.id = rule_runs.conversation_id
WHERE rule_runs.rule_id = @rule_id
ORDER BY rule_runs.created_at DESC, rule_runs.id DESC
LIMIT @page_size::integer;

-- name: AutoPurgeRuleRuns :execrows
DELETE FROM rule_runs WHERE created_at < now() - interval '30 days';

-- name: AutoPurgeReplies :execrows
DELETE FROM automation_replies WHERE sent_at < now() - interval '24 hours';

-- name: AutoClaimReply :execrows
-- Succeeds only when the rule has not replied to this address in the last 24 hours.
INSERT INTO automation_replies (rule_id, address) VALUES (@rule_id, @address)
ON CONFLICT (rule_id, address) DO UPDATE SET sent_at = now()
WHERE automation_replies.sent_at < now() - interval '24 hours';

-- name: AutoGetConversation :one
SELECT
    c.id, c.number, c.mailbox_id, c.subject, c.status, c.priority, c.assignee_user_id, c.assignee_team_id,
    c.contact_id, c.snoozed_until, c.version, c.created_at, c.last_inbound_at, c.first_responded_at, c.resolved_at,
    c.sla_policy_id, c.sla_started_at, c.first_response_due_at, c.first_response_met_at, c.resolution_due_at,
    c.sla_state, c.sla_paused_at, c.sla_resumed_at, c.sla_finalized_at,
    COALESCE(organizations.name, '')::text AS organization_name,
    mailboxes.email_address AS mailbox_address, mailboxes.display_name AS mailbox_display_name,
    mailboxes.name AS mailbox_name, mailboxes.auto_assign_mode, mailboxes.default_sla_policy_id,
    mailboxes.business_hours_id AS mailbox_business_hours_id
FROM conversations c
JOIN mailboxes ON mailboxes.id = c.mailbox_id
LEFT JOIN contacts ON contacts.id = c.contact_id
LEFT JOIN organizations ON organizations.id = contacts.organization_id
WHERE c.id = @id AND c.deleted_at IS NULL;

-- name: AutoLatestInbound :one
SELECT m.id, m.from_addr, m.from_name, m.subject, m.body_text, m.auto_submitted, m.is_bulk, m.received_at,
    m.message_id_header, m.references_hdr,
    EXISTS (SELECT 1 FROM attachments a WHERE a.message_id = m.id AND a.disposition = 'attachment')::boolean AS has_attachment
FROM messages m
WHERE m.conversation_id = @conversation_id AND m.kind = 'email' AND m.direction = 'in' AND m.deleted_at IS NULL
    AND (sqlc.narg(message_id)::uuid IS NULL OR m.id = sqlc.narg(message_id)::uuid)
ORDER BY m.received_at DESC, m.id DESC
LIMIT 1;

-- name: AutoContactAddress :one
SELECT email FROM contact_addresses WHERE contact_id = @contact_id ORDER BY is_primary DESC, email LIMIT 1;

-- name: AutoContactName :one
SELECT name FROM contacts WHERE id = @id;

-- name: AutoUserName :one
SELECT name FROM users WHERE id = @id;

-- name: AutoInsertNote :one
INSERT INTO messages (conversation_id, mailbox_id, kind, from_addr, from_name, body_text, body_html)
VALUES (@conversation_id, @mailbox_id, 'note', '', @from_name, @body_text, @body_html)
RETURNING id;

-- name: AutoTouchConversation :one
UPDATE conversations SET version = version + 1, updated_at = now() WHERE id = @id RETURNING version;

-- name: AutoApplySLA :one
UPDATE conversations SET
    sla_policy_id = @sla_policy_id, sla_started_at = @sla_started_at,
    first_response_due_at = @first_response_due_at, first_response_met_at = @first_response_met_at,
    resolution_due_at = @resolution_due_at, sla_state = @sla_state,
    sla_paused_at = CASE WHEN status = 'waiting' THEN now() ELSE NULL END, sla_resumed_at = NULL,
    sla_finalized_at = CASE WHEN status IN ('closed', 'spam') THEN now() ELSE NULL END,
    version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;

-- name: AutoListSLACandidates :many
SELECT id, mailbox_id, status, assignee_user_id, sla_policy_id, sla_state, first_response_due_at,
    first_response_met_at, resolution_due_at, sla_paused_at, sla_resumed_at, resolved_at
FROM conversations
WHERE sla_policy_id IS NOT NULL AND sla_finalized_at IS NULL AND deleted_at IS NULL AND id > @after::uuid
ORDER BY id
LIMIT @page_size::integer;

-- name: AutoLockSLAConversation :one
SELECT id, mailbox_id, status, assignee_user_id, sla_policy_id, sla_state, first_response_due_at,
    first_response_met_at, resolution_due_at, sla_paused_at, sla_resumed_at, resolved_at, sla_finalized_at
FROM conversations
WHERE id = @id AND sla_policy_id IS NOT NULL AND deleted_at IS NULL
FOR UPDATE SKIP LOCKED;

-- name: AutoUpdateSLA :one
UPDATE conversations SET
    sla_state = @sla_state, resolution_due_at = @resolution_due_at,
    sla_paused_at = @sla_paused_at, sla_resumed_at = @sla_resumed_at, sla_finalized_at = @sla_finalized_at,
    version = version + CASE WHEN sla_state <> @sla_state OR resolution_due_at IS DISTINCT FROM @resolution_due_at THEN 1 ELSE 0 END,
    updated_at = now()
WHERE id = @id
RETURNING version;

-- name: AutoRestartSLA :one
UPDATE conversations SET
    sla_started_at = @started_at, first_response_due_at = @first_response_due_at, first_response_met_at = NULL,
    resolution_due_at = @resolution_due_at, sla_state = @sla_state, sla_finalized_at = NULL,
    sla_paused_at = CASE WHEN status = 'waiting' THEN now() ELSE NULL END, sla_resumed_at = NULL,
    version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;

-- name: AutoListIdleConversations :many
SELECT c.id FROM conversations c
WHERE c.status = 'waiting' AND c.deleted_at IS NULL
    AND (sqlc.narg(mailbox_id)::uuid IS NULL OR c.mailbox_id = sqlc.narg(mailbox_id)::uuid)
    AND c.last_message_at <= @idle_before::timestamptz
    AND c.last_message_at > @not_older_than::timestamptz
    AND NOT EXISTS (
        SELECT 1 FROM rule_runs r
        WHERE r.rule_id = @rule_id AND r.conversation_id = c.id AND r.created_at >= c.last_message_at)
ORDER BY c.last_message_at
LIMIT @page_size::integer;

-- name: AutoPickAssignee :one
-- Candidates are active non-readonly team members with write access to the mailbox who are
-- online and below their capacity. Round robin takes the agent who was auto-assigned longest
-- ago; balanced takes the agent with the fewest open conversations first.
SELECT users.id
FROM users
LEFT JOIN (
    SELECT assignee_user_id, count(*) AS open_count FROM conversations
    WHERE status = 'open' AND deleted_at IS NULL AND assignee_user_id IS NOT NULL
    GROUP BY assignee_user_id
) AS load ON load.assignee_user_id = users.id
WHERE users.deactivated_at IS NULL AND users.role <> 'readonly' AND (users.role <> 'custom' OR 'conversations.write' = ANY(users.permissions)) AND users.availability = 'online'
    AND EXISTS (
        SELECT 1 FROM team_members
        JOIN mailbox_access ON mailbox_access.team_id = team_members.team_id
        WHERE team_members.user_id = users.id AND mailbox_access.mailbox_id = @mailbox_id
            AND mailbox_access.level = 'write'
            AND (sqlc.narg(team_id)::uuid IS NULL OR team_members.team_id = sqlc.narg(team_id)::uuid))
    AND (users.max_open IS NULL OR COALESCE(load.open_count, 0) < users.max_open)
ORDER BY CASE WHEN @balanced::boolean THEN COALESCE(load.open_count, 0) ELSE 0 END,
    users.last_auto_assigned_at NULLS FIRST, users.id
LIMIT 1;

-- name: AutoMarkAssigned :exec
UPDATE users SET last_auto_assigned_at = now() WHERE id = @id;

-- name: AutoListUnassignedForRetry :many
SELECT c.id FROM conversations c
JOIN mailboxes ON mailboxes.id = c.mailbox_id
WHERE mailboxes.auto_assign_mode <> 'off' AND mailboxes.disabled_at IS NULL
    AND c.assignee_user_id IS NULL AND c.status = 'open' AND c.deleted_at IS NULL
    AND (c.snoozed_until IS NULL OR c.snoozed_until <= now())
    AND c.created_at > now() - interval '7 days'
    AND NOT EXISTS (
        SELECT 1 FROM conversation_events e
        WHERE e.conversation_id = c.id AND e.type IN ('assigned', 'unassigned'))
ORDER BY c.created_at
LIMIT @page_size::integer;

-- name: AutoListAutoResolvable :many
SELECT id FROM conversations
WHERE status = 'waiting' AND deleted_at IS NULL AND last_message_at <= @cutoff::timestamptz
    AND (last_inbound_at IS NULL OR last_inbound_at <= @cutoff::timestamptz)
ORDER BY last_message_at
LIMIT @page_size::integer;

-- name: AutoListMacros :many
SELECT * FROM macros
WHERE scope = 'global' OR owner_user_id = @user_id
ORDER BY scope, lower(name), id;

-- name: AutoGetMacro :one
SELECT * FROM macros WHERE id = $1;

-- name: AutoInsertMacro :one
INSERT INTO macros (name, scope, owner_user_id, actions) VALUES (@name, @scope, @owner_user_id, @actions)
RETURNING *;

-- name: AutoUpdateMacro :one
UPDATE macros SET name = @name, actions = @actions, updated_at = now() WHERE id = @id RETURNING *;

-- name: AutoDeleteMacro :exec
DELETE FROM macros WHERE id = $1;
