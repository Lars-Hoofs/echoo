-- +goose Up
-- The trash: a deleted conversation keeps its rows (deleted_at set) until it is restored,
-- purged by hand or emptied by retention after trash_days.
ALTER TABLE conversations ADD COLUMN deleted_by uuid REFERENCES users (id) ON DELETE SET NULL;
CREATE INDEX conversations_trash ON conversations (mailbox_id, deleted_at DESC, id DESC) WHERE deleted_at IS NOT NULL;

-- Existing mailbox overrides get the workspace default, so their trash does not stay forever.
ALTER TABLE retention_policies ADD COLUMN trash_days integer CHECK (trash_days BETWEEN 1 AND 3650);
UPDATE retention_policies SET trash_days = 30;

-- A pattern is a full address or a domain (no @); new conversations from a match start as spam.
CREATE TABLE blocked_senders (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    mailbox_id uuid        NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    pattern    text        NOT NULL CHECK (pattern = lower(pattern) AND pattern <> '' AND left(pattern, 1) <> '@'),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, pattern)
);

-- +goose Down
DROP TABLE blocked_senders;
ALTER TABLE retention_policies DROP COLUMN trash_days;
DROP INDEX conversations_trash;
ALTER TABLE conversations DROP COLUMN deleted_by;
