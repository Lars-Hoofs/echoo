-- +goose Up
-- Search also matches contacts by name and address; the trigram indexes it needs
-- (contacts_name_trgm, contact_addresses_email_trgm) come from 00015_contacts.sql.

-- Search ranks only the newest matching messages; for a term found in most of the mail this
-- index lets the scan stop early instead of visiting every match.
CREATE INDEX messages_received_at ON messages (received_at DESC, id DESC) WHERE deleted_at IS NULL;

-- van:@domain and van:address filters on their own would otherwise scan every message.
CREATE INDEX messages_from_addr_trgm ON messages USING gin (lower(from_addr) gin_trgm_ops)
    WHERE deleted_at IS NULL AND kind = 'email';

-- owner_user_id set: personal. owner NULL and team_id NULL: shared with everyone. owner NULL and
-- team_id set: shared with that team. filters follow the closed schema in internal/search.
-- Deleting a team deletes its views rather than widening them to everyone.
CREATE TABLE saved_views (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    name          text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
    owner_user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    team_id       uuid REFERENCES teams (id) ON DELETE CASCADE,
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    filters       jsonb       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(filters) = 'object'),
    sort          text        NOT NULL DEFAULT 'last_message_desc' CHECK (sort IN ('last_message_desc')),
    position      integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (owner_user_id IS NULL OR team_id IS NULL)
);
CREATE UNIQUE INDEX saved_views_personal_name ON saved_views (owner_user_id, lower(name)) WHERE owner_user_id IS NOT NULL;
CREATE UNIQUE INDEX saved_views_shared_name ON saved_views (lower(name)) WHERE owner_user_id IS NULL;
CREATE INDEX saved_views_team ON saved_views (team_id) WHERE team_id IS NOT NULL;

-- +goose Down
DROP TABLE saved_views;
DROP INDEX messages_from_addr_trgm;
DROP INDEX messages_received_at;
