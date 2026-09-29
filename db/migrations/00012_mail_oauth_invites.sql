-- +goose Up
-- Set when an admin (re)connects the account; token refreshes do not touch it, so a running
-- sync can tell a reconnect from a routine refresh.
ALTER TABLE mailboxes ADD COLUMN oauth_connected_at timestamptz;

-- An invited user has invited_at set and deactivated_at set until the invitation is accepted:
-- every existing query that hides deactivated users then hides them too, and they cannot sign in.
ALTER TABLE users
    ADD COLUMN invited_at timestamptz,
    ADD COLUMN email_notify_mentions boolean NOT NULL DEFAULT false,
    ADD COLUMN email_notify_assignments boolean NOT NULL DEFAULT false;

-- Single-use links for invitations and password resets; only the SHA-256 of the token is kept.
CREATE TABLE account_tokens (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    text        NOT NULL CHECK (purpose IN ('invitation', 'password_reset')),
    token_hash bytea       NOT NULL UNIQUE,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX account_tokens_open ON account_tokens (user_id, purpose) WHERE used_at IS NULL;

-- Set when the mail job has decided about a notification (sent, or skipped because the user
-- did not opt in or already read it), so the periodic job never handles a row twice.
ALTER TABLE notifications ADD COLUMN email_handled_at timestamptz;
UPDATE notifications SET email_handled_at = now();
CREATE INDEX notifications_email_pending ON notifications (created_at) WHERE email_handled_at IS NULL;

-- +goose Down
DROP INDEX notifications_email_pending;
ALTER TABLE notifications DROP COLUMN email_handled_at;
DROP TABLE account_tokens;
ALTER TABLE users DROP COLUMN email_notify_assignments, DROP COLUMN email_notify_mentions, DROP COLUMN invited_at;
ALTER TABLE mailboxes DROP COLUMN oauth_connected_at;
