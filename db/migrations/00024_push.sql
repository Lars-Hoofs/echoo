-- +goose Up
-- Push notifications to phones and browsers. A device belongs to the session that registered
-- it: it keeps receiving pushes after that session merely expires (an agent on call may not
-- open the app for weeks), but logging out or revoking the session removes it.
CREATE TABLE push_devices (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    session_id   uuid REFERENCES sessions (id) ON DELETE SET NULL,
    kind         text        NOT NULL CHECK (kind IN ('webpush', 'apns', 'fcm')),
    -- The push service URL for webpush, the device token for apns and fcm.
    endpoint     text        NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 2048),
    -- The browser's ECDH public key and auth secret (RFC 8291); empty for native devices.
    p256dh       bytea       NOT NULL DEFAULT '',
    auth         bytea       NOT NULL DEFAULT '',
    label        text        NOT NULL DEFAULT '' CHECK (length(label) <= 200),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_push_at timestamptz,
    UNIQUE (kind, endpoint)
);
CREATE INDEX push_devices_user ON push_devices (user_id);
CREATE INDEX push_devices_session ON push_devices (session_id);

-- +goose StatementBegin
CREATE FUNCTION push_devices_drop_revoked() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM push_devices WHERE session_id = NEW.id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER sessions_revoked_drop_push AFTER UPDATE OF revoked_at ON sessions
    FOR EACH ROW WHEN (NEW.revoked_at IS NOT NULL AND OLD.revoked_at IS NULL)
    EXECUTE FUNCTION push_devices_drop_revoked();

-- The Web Push (VAPID) key pair of this installation, made on first use.
CREATE TABLE push_vapid (
    singleton       boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    public_key      bytea       NOT NULL,
    private_key_enc bytea       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Which notification kinds a user wants on their devices.
ALTER TABLE users
    ADD COLUMN push_notify_mentions    boolean NOT NULL DEFAULT true,
    ADD COLUMN push_notify_assignments boolean NOT NULL DEFAULT true,
    ADD COLUMN push_notify_replies     boolean NOT NULL DEFAULT true,
    ADD COLUMN push_notify_sla         boolean NOT NULL DEFAULT true;

-- Existing notifications are not pushed after the upgrade.
ALTER TABLE notifications ADD COLUMN push_handled_at timestamptz;
UPDATE notifications SET push_handled_at = now();
CREATE INDEX notifications_push_pending ON notifications (created_at) WHERE push_handled_at IS NULL;

-- +goose Down
DROP INDEX notifications_push_pending;
ALTER TABLE notifications DROP COLUMN push_handled_at;
ALTER TABLE users
    DROP COLUMN push_notify_sla, DROP COLUMN push_notify_replies,
    DROP COLUMN push_notify_assignments, DROP COLUMN push_notify_mentions;
DROP TABLE push_vapid;
DROP TRIGGER sessions_revoked_drop_push ON sessions;
DROP FUNCTION push_devices_drop_revoked();
DROP TABLE push_devices;
