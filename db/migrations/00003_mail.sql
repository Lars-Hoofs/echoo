-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS unaccent;

-- simple + unaccent: keeps English words, codes and order numbers searchable without stemming.
CREATE TEXT SEARCH CONFIGURATION echoo_simple (COPY = simple);
ALTER TEXT SEARCH CONFIGURATION echoo_simple
    ALTER MAPPING FOR hword, hword_part, word WITH unaccent, simple;

CREATE TABLE mailboxes (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    name                  text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    email_address         text        NOT NULL CHECK (email_address = lower(email_address)),
    display_name          text        NOT NULL DEFAULT '',
    transport             text        NOT NULL DEFAULT 'imap' CHECK (transport IN ('imap', 'webhook_postmark', 'webhook_mailgun', 'webhook_ses')),
    auth_type             text        NOT NULL DEFAULT 'password' CHECK (auth_type IN ('password', 'oauth_google', 'oauth_microsoft')),
    imap_host             text        NOT NULL DEFAULT '',
    imap_port             integer     NOT NULL DEFAULT 993 CHECK (imap_port BETWEEN 1 AND 65535),
    imap_tls              text        NOT NULL DEFAULT 'implicit' CHECK (imap_tls IN ('implicit', 'starttls')),
    imap_username         text        NOT NULL DEFAULT '',
    imap_secret_enc       bytea,
    smtp_host             text        NOT NULL DEFAULT '',
    smtp_port             integer     NOT NULL DEFAULT 465 CHECK (smtp_port BETWEEN 1 AND 65535),
    smtp_tls              text        NOT NULL DEFAULT 'implicit' CHECK (smtp_tls IN ('implicit', 'starttls')),
    smtp_username         text        NOT NULL DEFAULT '',
    smtp_secret_enc       bytea,
    oauth_token_enc       bytea,
    oauth_expires_at      timestamptz,
    inbound_secret_enc    bytea,
    sent_folder           text        NOT NULL DEFAULT '',
    mark_seen             boolean     NOT NULL DEFAULT false,
    send_delay_seconds    integer     NOT NULL DEFAULT 5 CHECK (send_delay_seconds BETWEEN 0 AND 30),
    sync_state            text        NOT NULL DEFAULT 'disabled' CHECK (sync_state IN ('connected', 'polling', 'backoff', 'auth_failed', 'disabled')),
    sync_error            text        NOT NULL DEFAULT '',
    sync_state_changed_at timestamptz NOT NULL DEFAULT now(),
    last_synced_at        timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    disabled_at           timestamptz
);
CREATE UNIQUE INDEX mailboxes_email_key ON mailboxes (email_address);

CREATE TABLE mailbox_access (
    mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    team_id    uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    level      text NOT NULL CHECK (level IN ('read', 'write')),
    PRIMARY KEY (mailbox_id, team_id)
);
CREATE INDEX mailbox_access_team ON mailbox_access (team_id);

CREATE TABLE mailbox_folders (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    mailbox_id     uuid        NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    name           text        NOT NULL,
    role           text        NOT NULL CHECK (role IN ('inbox', 'sent', 'other')),
    uidvalidity    bigint      NOT NULL DEFAULT 0,
    last_uid       bigint      NOT NULL DEFAULT 0,
    last_synced_at timestamptz,
    UNIQUE (mailbox_id, name)
);

-- Every fetched message is stored raw before anything else happens (ADR 0004).
CREATE TABLE raw_messages (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    mailbox_id  uuid        NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    folder_id   uuid REFERENCES mailbox_folders (id) ON DELETE SET NULL,
    source      text        NOT NULL CHECK (source IN ('imap', 'webhook', 'outbound')),
    uidvalidity bigint,
    uid         bigint,
    sha256      bytea       NOT NULL,
    size_bytes  integer     NOT NULL,
    blob_key    text        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    parse_status text       NOT NULL DEFAULT 'pending' CHECK (parse_status IN ('pending', 'parsed', 'failed', 'skipped')),
    parse_error text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX raw_messages_imap_key ON raw_messages (mailbox_id, folder_id, uidvalidity, uid) WHERE source = 'imap';
CREATE INDEX raw_messages_status ON raw_messages (parse_status) WHERE parse_status IN ('pending', 'failed');

CREATE TABLE organizations (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL,
    domains    text[]      NOT NULL DEFAULT '{}',
    notes      text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX organizations_domains ON organizations USING gin (domains);

CREATE TABLE contacts (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    name            text        NOT NULL DEFAULT '',
    organization_id uuid REFERENCES organizations (id) ON DELETE SET NULL,
    notes           text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    erased_at       timestamptz
);

CREATE TABLE contact_addresses (
    contact_id uuid    NOT NULL REFERENCES contacts (id) ON DELETE CASCADE,
    email      text    NOT NULL PRIMARY KEY CHECK (email = lower(email)),
    is_primary boolean NOT NULL DEFAULT false
);
CREATE INDEX contact_addresses_contact ON contact_addresses (contact_id);

CREATE SEQUENCE conversation_number_seq;

CREATE TABLE conversations (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    number                bigint      NOT NULL DEFAULT nextval('conversation_number_seq') UNIQUE,
    mailbox_id            uuid        NOT NULL REFERENCES mailboxes (id),
    subject               text        NOT NULL DEFAULT '',
    subject_normalized    text        NOT NULL DEFAULT '',
    status                text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'waiting', 'closed', 'spam')),
    priority              text        NOT NULL DEFAULT 'none' CHECK (priority IN ('none', 'low', 'normal', 'high', 'urgent')),
    assignee_user_id      uuid REFERENCES users (id),
    assignee_team_id      uuid REFERENCES teams (id) ON DELETE SET NULL,
    contact_id            uuid REFERENCES contacts (id) ON DELETE SET NULL,
    snoozed_until         timestamptz,
    last_message_at       timestamptz NOT NULL DEFAULT now(),
    last_inbound_at       timestamptz,
    last_outbound_at      timestamptz,
    first_responded_at    timestamptz,
    resolved_at           timestamptz,
    message_count         integer     NOT NULL DEFAULT 0,
    has_attachments       boolean     NOT NULL DEFAULT false,
    preview               text        NOT NULL DEFAULT '',
    version               integer     NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz
);
ALTER SEQUENCE conversation_number_seq OWNED BY conversations.number;
CREATE INDEX conversations_mailbox_list ON conversations (mailbox_id, status, last_message_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX conversations_assignee_list ON conversations (assignee_user_id, status, last_message_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX conversations_contact ON conversations (contact_id, last_message_at DESC);
CREATE INDEX conversations_subject_trgm ON conversations USING gin (subject gin_trgm_ops);
-- Subject fallback threading looks up recent conversations by normalized subject.
CREATE INDEX conversations_subject_norm ON conversations (mailbox_id, subject_normalized, last_message_at DESC);

CREATE TABLE messages (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    conversation_id   uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    mailbox_id        uuid        NOT NULL REFERENCES mailboxes (id),
    kind              text        NOT NULL CHECK (kind IN ('email', 'note', 'system')),
    direction         text CHECK (direction IN ('in', 'out')),
    raw_message_id    uuid REFERENCES raw_messages (id) ON DELETE SET NULL,
    raw_sha256        bytea,
    message_id_header text        NOT NULL DEFAULT '',
    message_id_hash   bytea,
    in_reply_to       text        NOT NULL DEFAULT '',
    references_hdr    text[]      NOT NULL DEFAULT '{}',
    from_addr         text        NOT NULL DEFAULT '',
    from_name         text        NOT NULL DEFAULT '',
    to_addrs          jsonb       NOT NULL DEFAULT '[]',
    cc_addrs          jsonb       NOT NULL DEFAULT '[]',
    bcc_addrs         jsonb       NOT NULL DEFAULT '[]',
    reply_to          jsonb       NOT NULL DEFAULT '[]',
    subject           text        NOT NULL DEFAULT '',
    sent_at           timestamptz,
    received_at       timestamptz NOT NULL DEFAULT now(),
    body_text         text        NOT NULL DEFAULT '',
    body_html         text        NOT NULL DEFAULT '',
    auto_submitted    boolean     NOT NULL DEFAULT false,
    is_bounce         boolean     NOT NULL DEFAULT false,
    author_user_id    uuid REFERENCES users (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz,
    fts tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('echoo_simple', coalesce(subject, '')), 'A') ||
        to_tsvector('dutch', coalesce(body_text, '')) ||
        to_tsvector('echoo_simple', coalesce(body_text, ''))
    ) STORED,
    CHECK ((kind = 'email') = (direction IS NOT NULL))
);
CREATE INDEX messages_conversation ON messages (conversation_id, received_at);
CREATE INDEX messages_message_id ON messages (mailbox_id, message_id_hash);
-- A re-fetched identical copy is dropped; two different messages sharing a Message-ID are kept.
CREATE UNIQUE INDEX messages_dedup ON messages (mailbox_id, message_id_hash, raw_sha256)
    WHERE message_id_hash IS NOT NULL AND raw_sha256 IS NOT NULL;
CREATE INDEX messages_fts ON messages USING gin (fts);

-- Every Message-ID seen or referenced, so threading is one indexed lookup and handles
-- replies that arrive before their parent.
CREATE TABLE thread_refs (
    mailbox_id      uuid  NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    message_id_hash bytea NOT NULL,
    conversation_id uuid  NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    PRIMARY KEY (mailbox_id, message_id_hash)
);
CREATE INDEX thread_refs_conversation ON thread_refs (conversation_id);

CREATE TABLE attachments (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    message_id    uuid        NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    filename      text        NOT NULL DEFAULT '',
    declared_type text        NOT NULL DEFAULT '',
    sniffed_type  text        NOT NULL DEFAULT '',
    size_bytes    bigint      NOT NULL,
    sha256        bytea       NOT NULL,
    blob_key      text        NOT NULL,
    content_id    text        NOT NULL DEFAULT '',
    disposition   text        NOT NULL CHECK (disposition IN ('attachment', 'inline')),
    scan_status   text        NOT NULL DEFAULT 'not_scanned' CHECK (scan_status IN ('not_scanned', 'clean', 'infected', 'error')),
    scan_detail   text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX attachments_message ON attachments (message_id);

CREATE TABLE outbound (
    message_id      uuid PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
    idempotency_key uuid        NOT NULL UNIQUE,
    status          text        NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'retry', 'sent', 'failed', 'uncertain', 'bounced', 'cancelled')),
    scheduled_at    timestamptz NOT NULL DEFAULT now(),
    attempt         integer     NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,
    next_attempt_at timestamptz,
    smtp_response   text        NOT NULL DEFAULT '',
    error           text        NOT NULL DEFAULT '',
    dsn_status      text        NOT NULL DEFAULT '',
    sent_at         timestamptz,
    created_by      uuid REFERENCES users (id),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbound_pending ON outbound (status, next_attempt_at) WHERE status IN ('queued', 'retry', 'sending');

CREATE TABLE conversation_events (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    mailbox_id      uuid        NOT NULL REFERENCES mailboxes (id),
    actor_user_id   uuid REFERENCES users (id),
    type            text        NOT NULL,
    data            jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversation_events_conversation ON conversation_events (conversation_id, created_at);
CREATE INDEX conversation_events_report ON conversation_events (mailbox_id, type, created_at);

-- +goose Down
DROP TABLE conversation_events;
DROP TABLE outbound;
DROP TABLE attachments;
DROP TABLE thread_refs;
DROP TABLE messages;
DROP TABLE conversations;
DROP TABLE contact_addresses;
DROP TABLE contacts;
DROP TABLE organizations;
DROP TABLE raw_messages;
DROP TABLE mailbox_folders;
DROP TABLE mailbox_access;
DROP TABLE mailboxes;
DROP TEXT SEARCH CONFIGURATION echoo_simple;
