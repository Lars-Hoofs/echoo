-- +goose Up
-- Team filter on the inbox list and the per-team open counts in the sidebar.
CREATE INDEX conversations_team_list ON conversations (assignee_team_id, status, last_message_at DESC, id DESC)
    WHERE deleted_at IS NULL AND assignee_team_id IS NOT NULL;

-- +goose Down
DROP INDEX conversations_team_list;
