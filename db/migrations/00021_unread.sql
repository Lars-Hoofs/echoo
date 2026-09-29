-- +goose Up
-- Per-agent read state. A row means the agent has opened the conversation (or answered it) at
-- last_read_at; marked_unread is the agent's explicit "mark as unread". Without a row, only
-- messages that arrived after users.unread_since count, so existing conversations do not all
-- turn unread when this migration runs.
CREATE TABLE conversation_reads (
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    last_read_at    timestamptz NOT NULL DEFAULT now(),
    marked_unread   boolean     NOT NULL DEFAULT false,
    PRIMARY KEY (user_id, conversation_id)
);
CREATE INDEX conversation_reads_conversation ON conversation_reads (conversation_id);

ALTER TABLE users
    ADD COLUMN unread_since timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN email_notify_replies boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE users DROP COLUMN email_notify_replies, DROP COLUMN unread_since;
DROP TABLE conversation_reads;
