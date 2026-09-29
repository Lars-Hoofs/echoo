-- +goose Up
-- Unsubscribing is per contact and applies to every campaign; a hard bounce is per address.
ALTER TABLE contacts ADD COLUMN unsubscribed_at timestamptz;
ALTER TABLE contact_addresses ADD COLUMN bounced_at timestamptz;

-- Headers a message needs beyond the ones the sender derives from the stored message
-- (List-Unsubscribe and friends on campaign mail). The send package allow-lists the names.
ALTER TABLE outbound ADD COLUMN extra_headers jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(extra_headers) = 'array');

CREATE TABLE campaigns (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    name            text        NOT NULL CHECK (name <> '' AND char_length(name) <= 100),
    mailbox_id      uuid        NOT NULL REFERENCES mailboxes (id),
    segment_id      uuid REFERENCES contact_segments (id) ON DELETE SET NULL,
    -- Kept so a finished campaign still says whom it went to after the segment is deleted.
    segment_name    text        NOT NULL DEFAULT '',
    subject         text        NOT NULL DEFAULT '' CHECK (char_length(subject) <= 300),
    body_html       text        NOT NULL DEFAULT '',
    status          text        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'scheduled', 'sending', 'paused', 'done', 'cancelled')),
    scheduled_at    timestamptz,
    rate_per_minute integer     NOT NULL DEFAULT 60 CHECK (rate_per_minute BETWEEN 1 AND 1000),
    -- The recipients are resolved with this user's visibility, when the campaign starts.
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    materialized_at timestamptz,
    started_at      timestamptz,
    finished_at     timestamptz,
    -- A code for why the campaign stopped by itself: segment_missing, creator_unavailable, mailbox_unavailable.
    error           text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (status <> 'scheduled' OR scheduled_at IS NOT NULL)
);
CREATE INDEX campaigns_list ON campaigns (created_at DESC, id DESC);
CREATE INDEX campaigns_active ON campaigns (status) WHERE status IN ('scheduled', 'sending', 'paused');
CREATE INDEX campaigns_mailbox ON campaigns (mailbox_id);

CREATE TABLE campaign_recipients (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    campaign_id     uuid        NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    contact_id      uuid REFERENCES contacts (id) ON DELETE SET NULL,
    -- Snapshots: the report stays readable when the contact changes, and is scrubbed on erasure.
    email           text        NOT NULL DEFAULT '',
    name            text        NOT NULL DEFAULT '',
    -- queued: handed to the send queue, delivery still open. sent and failed are final and are
    -- copied from the outbound row by the dispatcher.
    state           text        NOT NULL CHECK (state IN ('pending', 'queued', 'sent', 'failed', 'skipped')),
    skip_reason     text        NOT NULL DEFAULT '' CHECK (skip_reason IN ('', 'unsubscribed', 'no_address', 'duplicate', 'bounced', 'cancelled')),
    message_id      uuid REFERENCES messages (id) ON DELETE SET NULL,
    conversation_id uuid REFERENCES conversations (id) ON DELETE SET NULL,
    error           text        NOT NULL DEFAULT '',
    -- Keys the HMAC of this recipient's unsubscribe link. Per recipient and stored, not derived
    -- from the encryption keys: rotating those must not break links in mail sent long ago.
    unsubscribe_secret uuid     NOT NULL DEFAULT gen_random_uuid(),
    queued_at       timestamptz,
    finished_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (campaign_id, contact_id),
    CHECK ((state = 'skipped') = (skip_reason <> ''))
);
CREATE INDEX campaign_recipients_pending ON campaign_recipients (campaign_id, id) WHERE state = 'pending';
CREATE INDEX campaign_recipients_queued ON campaign_recipients (campaign_id) WHERE state = 'queued';
CREATE INDEX campaign_recipients_report ON campaign_recipients (campaign_id, id);
CREATE INDEX campaign_recipients_message ON campaign_recipients (message_id) WHERE message_id IS NOT NULL;
CREATE INDEX campaign_recipients_queued_at ON campaign_recipients (queued_at) WHERE queued_at IS NOT NULL;
CREATE INDEX campaign_recipients_contact ON campaign_recipients (contact_id) WHERE contact_id IS NOT NULL;

-- +goose Down
DROP TABLE campaign_recipients;
DROP TABLE campaigns;
ALTER TABLE outbound DROP COLUMN extra_headers;
ALTER TABLE contact_addresses DROP COLUMN bounced_at;
ALTER TABLE contacts DROP COLUMN unsubscribed_at;
