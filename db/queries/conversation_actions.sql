-- Mutations lock the row inside the caller's readable mailboxes, so an out-of-scope id is
-- indistinguishable from a missing one. Whether the mailbox is writable is decided by the caller.

-- name: LockConversationForUpdate :one
SELECT id, mailbox_id, status, priority, assignee_user_id, assignee_team_id, snoozed_until, resolved_at, version
FROM conversations
WHERE id = @id AND mailbox_id = ANY(@mailbox_ids::uuid[]) AND deleted_at IS NULL
FOR UPDATE;

-- name: UpdateConversationState :one
UPDATE conversations SET
    status = @status, priority = @priority,
    assignee_user_id = @assignee_user_id, assignee_team_id = @assignee_team_id,
    snoozed_until = @snoozed_until, resolved_at = @resolved_at,
    version = version + 1, updated_at = now()
WHERE id = @id
RETURNING version;

-- name: InsertConversationEvent :exec
INSERT INTO conversation_events (conversation_id, mailbox_id, actor_user_id, type, data)
VALUES (@conversation_id, @mailbox_id, @actor_user_id, @type, @data);

-- name: NotifyEvent :exec
SELECT pg_notify(@channel::text, @payload::text);

-- name: CanAssignMailbox :one
-- Same rule as policy.MailboxScope for write access, for one user and one mailbox.
SELECT EXISTS (
    SELECT 1 FROM users
    WHERE users.id = @user_id AND users.deactivated_at IS NULL AND users.role <> 'readonly' AND (users.role <> 'custom' OR 'conversations.write' = ANY(users.permissions))
        AND (users.role IN ('owner', 'admin') OR EXISTS (
            SELECT 1 FROM team_members
            JOIN mailbox_access ON mailbox_access.team_id = team_members.team_id
            WHERE team_members.user_id = users.id AND mailbox_access.mailbox_id = @mailbox_id
                AND mailbox_access.level = 'write'))
)::boolean AS allowed;

-- name: TeamHasMailboxAccess :one
SELECT EXISTS (
    SELECT 1 FROM mailbox_access WHERE team_id = @team_id AND mailbox_id = @mailbox_id
)::boolean AS allowed;

-- name: GetTeamName :one
SELECT name FROM teams WHERE id = @id;

-- name: ListMailboxAssignees :many
SELECT users.id, users.name
FROM users
WHERE users.deactivated_at IS NULL AND users.role <> 'readonly' AND (users.role <> 'custom' OR 'conversations.write' = ANY(users.permissions))
    AND (users.role IN ('owner', 'admin') OR EXISTS (
        SELECT 1 FROM team_members
        JOIN mailbox_access ON mailbox_access.team_id = team_members.team_id
        WHERE team_members.user_id = users.id AND mailbox_access.mailbox_id = @mailbox_id
            AND mailbox_access.level = 'write'))
ORDER BY lower(users.name), users.id;

-- name: ListLabels :many
SELECT id, name, color_token, description, created_at FROM labels ORDER BY lower(name), id;

-- name: InsertLabel :one
INSERT INTO labels (name, color_token, description) VALUES (@name, @color_token, @description)
RETURNING id, name, color_token, description, created_at;

-- name: UpdateLabel :one
UPDATE labels SET
    name = COALESCE(sqlc.narg(name), name),
    color_token = COALESCE(sqlc.narg(color_token), color_token),
    description = COALESCE(sqlc.narg(description), description)
WHERE id = @id
RETURNING id, name, color_token, description, created_at;

-- name: DeleteLabel :one
DELETE FROM labels WHERE id = @id RETURNING name;

-- name: ListLabelsByID :many
SELECT id, name FROM labels WHERE id = ANY(@ids::uuid[]);

-- name: ListConversationLabelIDs :many
SELECT label_id FROM conversation_labels WHERE conversation_id = @conversation_id;

-- name: AddConversationLabels :exec
INSERT INTO conversation_labels (conversation_id, label_id)
SELECT @conversation_id, unnest(@label_ids::uuid[])
ON CONFLICT DO NOTHING;

-- name: RemoveConversationLabels :exec
DELETE FROM conversation_labels
WHERE conversation_id = @conversation_id AND label_id = ANY(@label_ids::uuid[]);

-- name: ListLabelsForConversations :many
SELECT conversation_labels.conversation_id, labels.id, labels.name, labels.color_token
FROM conversation_labels
JOIN labels ON labels.id = conversation_labels.label_id
WHERE conversation_labels.conversation_id = ANY(@conversation_ids::uuid[])
ORDER BY lower(labels.name), labels.id;

-- name: ListConversationEvents :many
SELECT
    conversation_events.id, conversation_events.type, conversation_events.data, conversation_events.created_at,
    actor.id AS actor_id, actor.name AS actor_name,
    target.id AS user_id, target.name AS user_name
FROM conversation_events
LEFT JOIN users AS actor ON actor.id = conversation_events.actor_user_id
LEFT JOIN users AS target ON target.id = NULLIF(conversation_events.data ->> 'user_id', '')::uuid
WHERE conversation_events.conversation_id = @conversation_id
ORDER BY conversation_events.created_at, conversation_events.id;

-- name: WakeSnoozedConversations :many
-- SKIP LOCKED lets overlapping runs and concurrent agent edits go past each other.
WITH due AS (
    SELECT id FROM conversations
    WHERE snoozed_until <= now() AND deleted_at IS NULL
    ORDER BY snoozed_until
    LIMIT @batch_size::integer
    FOR UPDATE SKIP LOCKED
), woken AS (
    UPDATE conversations SET snoozed_until = NULL, version = version + 1, updated_at = now()
    FROM due WHERE conversations.id = due.id
    RETURNING conversations.id, conversations.mailbox_id, conversations.version
), logged AS (
    INSERT INTO conversation_events (conversation_id, mailbox_id, type)
    SELECT id, mailbox_id, 'woke' FROM woken
)
SELECT id, mailbox_id, version FROM woken;

-- name: ConversationInScope :one
SELECT EXISTS (
    SELECT 1 FROM conversations
    WHERE id = @id AND mailbox_id = ANY(@mailbox_ids::uuid[]) AND deleted_at IS NULL
)::boolean AS readable;

-- name: ListMailboxTeams :many
SELECT teams.id, teams.name
FROM mailbox_access
JOIN teams ON teams.id = mailbox_access.team_id
WHERE mailbox_access.mailbox_id = @mailbox_id
ORDER BY lower(teams.name), teams.id;
