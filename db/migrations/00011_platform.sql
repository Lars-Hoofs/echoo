-- +goose Up
CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    prefix       text        NOT NULL,
    token_hash   bytea       NOT NULL,
    -- write implies read; a token never gets rights its user does not have.
    scopes       text[]      NOT NULL CHECK (scopes <@ ARRAY['read', 'write'] AND 'read' = ANY (scopes)),
    expires_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);
CREATE UNIQUE INDEX api_tokens_hash ON api_tokens (token_hash);
CREATE INDEX api_tokens_user ON api_tokens (user_id, created_at DESC);

CREATE TABLE webhooks (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    url                  text        NOT NULL CHECK (length(url) <= 2048),
    secret_enc           bytea       NOT NULL,
    events               text[]      NOT NULL CHECK (cardinality(events) > 0),
    include_content      boolean     NOT NULL DEFAULT false,
    allow_http           boolean     NOT NULL DEFAULT false,
    enabled              boolean     NOT NULL DEFAULT true,
    disabled_reason      text        NOT NULL DEFAULT '',
    consecutive_failures integer     NOT NULL DEFAULT 0,
    created_by           uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Outbox: written in the same transaction as the change it describes, so an event is never
-- lost and never announced for a change that rolled back. A periodic job fans it out.
CREATE TABLE webhook_events (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type            text        NOT NULL,
    mailbox_id      uuid,
    conversation_id uuid,
    message_id      uuid,
    contact_id      uuid,
    occurred_at     timestamptz NOT NULL DEFAULT now(),
    fanned_out_at   timestamptz
);
CREATE INDEX webhook_events_pending ON webhook_events (id) WHERE fanned_out_at IS NULL;
CREATE INDEX webhook_events_occurred ON webhook_events (occurred_at);

CREATE TABLE webhook_deliveries (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    webhook_id   uuid        NOT NULL REFERENCES webhooks (id) ON DELETE CASCADE,
    event_id     bigint      NOT NULL REFERENCES webhook_events (id) ON DELETE CASCADE,
    event        text        NOT NULL,
    payload_hash bytea,
    status       text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'retrying', 'succeeded', 'failed')),
    status_code  integer,
    error        text        NOT NULL DEFAULT '',
    duration_ms  integer,
    attempt      integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_deliveries_webhook ON webhook_deliveries (webhook_id, created_at DESC, id DESC);
CREATE INDEX webhook_deliveries_created ON webhook_deliveries (created_at);
CREATE INDEX webhook_deliveries_event ON webhook_deliveries (event_id);

CREATE INDEX audit_log_action ON audit_log (action text_pattern_ops, id DESC);
CREATE INDEX audit_log_at ON audit_log (at);

-- +goose Down
DROP INDEX audit_log_at;
DROP INDEX audit_log_action;
DROP TABLE webhook_deliveries;
DROP TABLE webhook_events;
DROP TABLE webhooks;
DROP TABLE api_tokens;
