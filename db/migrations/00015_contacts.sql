-- +goose Up
ALTER TABLE contacts
    ADD COLUMN phone             text        NOT NULL DEFAULT '' CHECK (char_length(phone) <= 50),
    ADD COLUMN custom_attributes jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(custom_attributes) = 'object'),
    ADD COLUMN created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN last_activity_at  timestamptz NOT NULL DEFAULT now();

UPDATE contacts SET last_activity_at = GREATEST(created_at, COALESCE(
    (SELECT max(conversations.last_message_at) FROM conversations WHERE conversations.contact_id = contacts.id), created_at));

ALTER TABLE organizations
    ADD COLUMN custom_attributes jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(custom_attributes) = 'object'),
    ADD COLUMN created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN updated_at        timestamptz NOT NULL DEFAULT now();

ALTER TABLE conversations
    ADD COLUMN custom_attributes jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(custom_attributes) = 'object');

CREATE INDEX contacts_organization ON contacts (organization_id) WHERE organization_id IS NOT NULL;
CREATE INDEX contacts_name_trgm ON contacts USING gin (name gin_trgm_ops);
CREATE INDEX contacts_created_by ON contacts (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX contacts_activity ON contacts (last_activity_at DESC, id DESC);
CREATE INDEX contact_addresses_email_trgm ON contact_addresses USING gin (email gin_trgm_ops);
CREATE INDEX organizations_name_trgm ON organizations USING gin (name gin_trgm_ops);

-- Keeps contacts.last_activity_at (list sorting) current without touching the ingest code.
-- +goose StatementBegin
CREATE FUNCTION bump_contact_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.contact_id IS NOT NULL THEN
        UPDATE contacts SET last_activity_at = NEW.last_message_at
        WHERE id = NEW.contact_id AND last_activity_at < NEW.last_message_at;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER conversations_bump_contact_activity
    AFTER INSERT OR UPDATE OF last_message_at, contact_id ON conversations
    FOR EACH ROW EXECUTE FUNCTION bump_contact_activity();

-- The visibility rule (SECURITY.md section C): a contact is visible to a user who can read at
-- least one of its conversations; a contact without conversations is visible to the user who
-- created it. Admins see everything. p_mailboxes is the user's readable mailbox scope.
CREATE FUNCTION contact_visible(p_contact uuid, p_user uuid, p_admin boolean, p_mailboxes uuid[])
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT p_admin
        OR EXISTS (SELECT 1 FROM conversations cv
                   WHERE cv.contact_id = p_contact AND cv.deleted_at IS NULL AND cv.mailbox_id = ANY (p_mailboxes))
        OR (NOT EXISTS (SELECT 1 FROM conversations cv WHERE cv.contact_id = p_contact AND cv.deleted_at IS NULL)
            AND EXISTS (SELECT 1 FROM contacts c WHERE c.id = p_contact AND c.created_by = p_user))
$$;

-- An organization is visible when one of its contacts is; an organization without contacts
-- is visible to its creator.
CREATE FUNCTION organization_visible(p_org uuid, p_user uuid, p_admin boolean, p_mailboxes uuid[])
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT p_admin
        OR EXISTS (SELECT 1 FROM contacts c
                   WHERE c.organization_id = p_org AND contact_visible(c.id, p_user, p_admin, p_mailboxes))
        OR (NOT EXISTS (SELECT 1 FROM contacts c WHERE c.organization_id = p_org)
            AND EXISTS (SELECT 1 FROM organizations o WHERE o.id = p_org AND o.created_by = p_user))
$$;

CREATE TABLE crm_notes (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    contact_id      uuid REFERENCES contacts (id) ON DELETE CASCADE,
    organization_id uuid REFERENCES organizations (id) ON DELETE CASCADE,
    author_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    body            text        NOT NULL CHECK (body <> '' AND char_length(body) <= 10000),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((contact_id IS NULL) <> (organization_id IS NULL))
);
CREATE INDEX crm_notes_contact ON crm_notes (contact_id, created_at DESC, id DESC) WHERE contact_id IS NOT NULL;
CREATE INDEX crm_notes_organization ON crm_notes (organization_id, created_at DESC, id DESC) WHERE organization_id IS NOT NULL;

CREATE TABLE custom_attribute_defs (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    entity     text        NOT NULL CHECK (entity IN ('contact', 'organization', 'conversation')),
    key        text        NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,39}$'),
    label      text        NOT NULL CHECK (label <> '' AND char_length(label) <= 60),
    type       text        NOT NULL CHECK (type IN ('text', 'number', 'date', 'boolean', 'list', 'link')),
    options    text[]      NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entity, key)
);

CREATE TABLE contact_segments (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    name          text        NOT NULL CHECK (name <> '' AND char_length(name) <= 80),
    owner_user_id uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    shared        boolean     NOT NULL DEFAULT false,
    filter        jsonb       NOT NULL CHECK (jsonb_typeof(filter) = 'object'),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX contact_segments_owner_name ON contact_segments (owner_user_id, lower(name));

CREATE TABLE contact_imports (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    created_by     uuid        NOT NULL REFERENCES users (id),
    filename       text        NOT NULL DEFAULT '',
    status         text        NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    dedupe         text        NOT NULL CHECK (dedupe IN ('update', 'skip')),
    delimiter      text        NOT NULL CHECK (delimiter IN (',', ';')),
    mapping        jsonb       NOT NULL,
    source_key     text        NOT NULL DEFAULT '',
    error_key      text        NOT NULL DEFAULT '',
    total_rows     integer     NOT NULL,
    processed_rows integer     NOT NULL DEFAULT 0,
    created_count  integer     NOT NULL DEFAULT 0,
    updated_count  integer     NOT NULL DEFAULT 0,
    skipped_count  integer     NOT NULL DEFAULT 0,
    failed_count   integer     NOT NULL DEFAULT 0,
    error          text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);
CREATE INDEX contact_imports_finished ON contact_imports (finished_at) WHERE finished_at IS NOT NULL;

-- GDPR exports: the ZIP is a blob that exists only until it is downloaded once or expires.
CREATE TABLE data_exports (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    contact_id   uuid REFERENCES contacts (id) ON DELETE SET NULL,
    requested_by uuid        NOT NULL REFERENCES users (id),
    status       text        NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'ready', 'failed', 'downloaded', 'expired')),
    blob_key     text        NOT NULL DEFAULT '',
    size_bytes   bigint      NOT NULL DEFAULT 0,
    error        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL DEFAULT now() + interval '24 hours'
);
CREATE INDEX data_exports_contact ON data_exports (contact_id) WHERE contact_id IS NOT NULL;
CREATE INDEX data_exports_expires ON data_exports (expires_at) WHERE blob_key <> '';

-- Blobs are content-addressed and shared, so a file is only removed once nothing refers to it.
-- Keys are queued in the transaction that drops the references and removed afterwards.
CREATE TABLE pending_blob_deletions (
    blob_key  text PRIMARY KEY,
    queued_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE pending_blob_deletions;
DROP TABLE data_exports;
DROP TABLE contact_imports;
DROP TABLE contact_segments;
DROP TABLE custom_attribute_defs;
DROP TABLE crm_notes;
DROP FUNCTION organization_visible(uuid, uuid, boolean, uuid[]);
DROP FUNCTION contact_visible(uuid, uuid, boolean, uuid[]);
DROP TRIGGER conversations_bump_contact_activity ON conversations;
DROP FUNCTION bump_contact_activity();
DROP INDEX organizations_name_trgm, contact_addresses_email_trgm, contacts_activity, contacts_created_by, contacts_name_trgm, contacts_organization;
ALTER TABLE conversations DROP COLUMN custom_attributes;
ALTER TABLE organizations DROP COLUMN updated_at, DROP COLUMN created_by, DROP COLUMN custom_attributes;
ALTER TABLE contacts DROP COLUMN last_activity_at, DROP COLUMN created_by, DROP COLUMN custom_attributes, DROP COLUMN phone;
