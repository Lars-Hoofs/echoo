-- +goose Up
-- Report ranges scan by time inside a mailbox; the existing indexes lead with status or
-- conversation id, which cannot serve a period query.
CREATE INDEX conversations_report_created ON conversations (mailbox_id, created_at) WHERE deleted_at IS NULL;
CREATE INDEX conversations_report_resolved ON conversations (mailbox_id, resolved_at) WHERE resolved_at IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX messages_report_in ON messages (mailbox_id, received_at) WHERE kind = 'email' AND direction = 'in' AND deleted_at IS NULL;
CREATE INDEX messages_report_out ON messages (mailbox_id, sent_at) WHERE kind = 'email' AND direction = 'out' AND sent_at IS NOT NULL AND deleted_at IS NULL;

-- Response times skip conversations that began as an outbound message; without this index that
-- check reads every event of the period.
CREATE INDEX conversation_events_outbound_created ON conversation_events (conversation_id) WHERE type = 'created' AND data ->> 'reason' = 'outbound';

-- A mailbox without a row has CSAT off. enabled_at bounds the sweep: conversations resolved
-- before CSAT was switched on never get a survey.
CREATE TABLE csat_settings (
    mailbox_id  uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    enabled     boolean     NOT NULL DEFAULT false,
    delay_hours integer     NOT NULL DEFAULT 0 CHECK (delay_hours BETWEEN 0 AND 24),
    enabled_at  timestamptz,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One row per conversation: it is claimed before the mail is queued, so a conversation gets at
-- most one survey. sent_at stays NULL when the survey was skipped; skipped_reason says why.
CREATE TABLE csat_requests (
    conversation_id uuid PRIMARY KEY REFERENCES conversations (id) ON DELETE CASCADE,
    mailbox_id      uuid        NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    token_hash      bytea       UNIQUE,
    message_id      uuid REFERENCES messages (id) ON DELETE SET NULL,
    sent_at         timestamptz,
    expires_at      timestamptz,
    skipped_reason  text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((sent_at IS NULL) = (token_hash IS NULL))
);
CREATE INDEX csat_requests_report ON csat_requests (mailbox_id, sent_at) WHERE sent_at IS NOT NULL;

CREATE TABLE csat_responses (
    conversation_id uuid PRIMARY KEY REFERENCES csat_requests (conversation_id) ON DELETE CASCADE,
    mailbox_id      uuid        NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    rating          smallint    NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment         text        NOT NULL DEFAULT '' CHECK (length(comment) <= 2000),
    token_hash      bytea       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK (kind IN ('mention', 'assigned', 'reply', 'sla', 'csat'));

-- +goose Down
DELETE FROM notifications WHERE kind = 'csat';
ALTER TABLE notifications DROP CONSTRAINT notifications_kind_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_kind_check CHECK (kind IN ('mention', 'assigned', 'reply', 'sla'));
DROP TABLE csat_responses;
DROP TABLE csat_requests;
DROP TABLE csat_settings;
DROP INDEX conversation_events_outbound_created;
DROP INDEX messages_report_out;
DROP INDEX messages_report_in;
DROP INDEX conversations_report_resolved;
DROP INDEX conversations_report_created;
