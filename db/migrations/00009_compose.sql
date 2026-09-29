-- +goose Up
-- Files uploaded by an agent before they are attached to a reply. They expire after 24 hours
-- and are only usable by their uploader.
CREATE TABLE uploads (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    filename     text        NOT NULL,
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL CHECK (size_bytes >= 0),
    sha256       bytea       NOT NULL,
    blob_key     text        NOT NULL,
    content_id   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL DEFAULT now() + interval '24 hours'
);
CREATE INDEX uploads_user ON uploads (user_id);
CREATE INDEX uploads_expires ON uploads (expires_at);

CREATE TABLE drafts (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    to_addrs        jsonb       NOT NULL DEFAULT '[]',
    cc_addrs        jsonb       NOT NULL DEFAULT '[]',
    bcc_addrs       jsonb       NOT NULL DEFAULT '[]',
    subject         text        NOT NULL DEFAULT '',
    body_html       text        NOT NULL DEFAULT '',
    attachment_ids  uuid[]      NOT NULL DEFAULT '{}',
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversation_id, user_id)
);

CREATE TABLE mentions (
    message_id uuid NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, user_id)
);
CREATE INDEX mentions_user ON mentions (user_id);

CREATE TABLE notifications (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind            text        NOT NULL CHECK (kind IN ('mention', 'assigned', 'reply', 'sla')),
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    message_id      uuid REFERENCES messages (id) ON DELETE CASCADE,
    actor_id        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    read_at         timestamptz
);
CREATE INDEX notifications_user ON notifications (user_id, read_at NULLS FIRST, created_at DESC);

CREATE TABLE templates (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    name          text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    shortcode     text        NOT NULL DEFAULT '' CHECK (shortcode ~ '^[a-z0-9_-]{0,32}$'),
    scope         text        NOT NULL CHECK (scope IN ('personal', 'team', 'mailbox', 'global')),
    owner_user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    team_id       uuid REFERENCES teams (id) ON DELETE CASCADE,
    mailbox_id    uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    subject       text        NOT NULL DEFAULT '',
    body_html     text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (scope = 'personal' AND owner_user_id IS NOT NULL AND team_id IS NULL AND mailbox_id IS NULL) OR
        (scope = 'team'     AND owner_user_id IS NULL AND team_id IS NOT NULL AND mailbox_id IS NULL) OR
        (scope = 'mailbox'  AND owner_user_id IS NULL AND team_id IS NULL AND mailbox_id IS NOT NULL) OR
        (scope = 'global'   AND owner_user_id IS NULL AND team_id IS NULL AND mailbox_id IS NULL)
    )
);
CREATE INDEX templates_owner ON templates (owner_user_id) WHERE owner_user_id IS NOT NULL;
CREATE INDEX templates_team ON templates (team_id) WHERE team_id IS NOT NULL;
CREATE INDEX templates_mailbox ON templates (mailbox_id) WHERE mailbox_id IS NOT NULL;

-- user_id and mailbox_id together: a user's signature for one mailbox; user only: their default;
-- mailbox only: the mailbox default set by an admin.
CREATE TABLE signatures (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id    uuid REFERENCES users (id) ON DELETE CASCADE,
    mailbox_id uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    body_html  text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (user_id IS NOT NULL OR mailbox_id IS NOT NULL)
);
CREATE UNIQUE INDEX signatures_user_mailbox ON signatures (user_id, mailbox_id) WHERE user_id IS NOT NULL AND mailbox_id IS NOT NULL;
CREATE UNIQUE INDEX signatures_user_default ON signatures (user_id) WHERE user_id IS NOT NULL AND mailbox_id IS NULL;
CREATE UNIQUE INDEX signatures_mailbox_default ON signatures (mailbox_id) WHERE user_id IS NULL AND mailbox_id IS NOT NULL;

-- The status a queued reply moved its conversation to, so undo send can move it back.
CREATE TABLE reply_status_changes (
    message_id  uuid PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    from_status text NOT NULL,
    to_status   text NOT NULL
);

-- +goose Down
DROP TABLE reply_status_changes;
DROP TABLE signatures;
DROP TABLE templates;
DROP TABLE notifications;
DROP TABLE mentions;
DROP TABLE drafts;
DROP TABLE uploads;
