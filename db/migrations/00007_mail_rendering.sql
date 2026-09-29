-- +goose Up
-- First Authentication-Results header as received, used only for the "failed authentication"
-- warning. Empty for messages ingested before this column existed.
ALTER TABLE messages ADD COLUMN auth_results text NOT NULL DEFAULT '';

-- Senders whose remote images may be shown. mailbox_id NULL applies to every mailbox.
-- pattern is a full address or "@domain".
CREATE TABLE sender_image_allowlist (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    mailbox_id uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    pattern    text        NOT NULL CHECK (pattern = lower(pattern) AND length(pattern) BETWEEN 3 AND 320 AND pattern ~ '^[^@\s]*@[^@\s]+$'),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX sender_image_allowlist_key
    ON sender_image_allowlist (COALESCE(mailbox_id, '00000000-0000-0000-0000-000000000000'::uuid), pattern);

-- Remote images fetched through /render/proxy, keyed by SHA-256 of the URL. The bytes live in
-- blob storage.
CREATE TABLE image_proxy_cache (
    url_hash     bytea PRIMARY KEY,
    blob_key     text        NOT NULL,
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL,
    fetched_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE image_proxy_cache;
DROP TABLE sender_image_allowlist;
ALTER TABLE messages DROP COLUMN auth_results;
