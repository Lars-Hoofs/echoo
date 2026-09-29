-- +goose Up
CREATE TABLE users (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    email                text        NOT NULL CHECK (email = lower(email) AND length(email) <= 254),
    name                 text        NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    role                 text        NOT NULL CHECK (role IN ('owner', 'admin', 'agent', 'readonly')),
    password_hash        text        NOT NULL,
    password_changed_at  timestamptz NOT NULL DEFAULT now(),
    password_must_change boolean     NOT NULL DEFAULT false,
    totp_secret_enc      bytea,
    totp_enabled_at      timestamptz,
    totp_last_step       bigint      NOT NULL DEFAULT 0,
    failed_login_count   integer     NOT NULL DEFAULT 0,
    locked_until         timestamptz,
    last_login_at        timestamptz,
    theme                text        NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark')),
    deactivated_at       timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CHECK (role <> 'owner' OR deactivated_at IS NULL)
);
CREATE UNIQUE INDEX users_email_key ON users (email);
-- Exactly one owner; ownership transfer is an explicit action.
CREATE UNIQUE INDEX users_single_owner ON users ((true)) WHERE role = 'owner';

CREATE TABLE recovery_codes (
    user_id   uuid  NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    used_at   timestamptz,
    PRIMARY KEY (user_id, code_hash)
);

CREATE TABLE sessions (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash      bytea       NOT NULL UNIQUE,
    csrf_token      text        NOT NULL,
    mfa_pending     boolean     NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    idle_expires_at timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL,
    ip              inet,
    user_agent      text        NOT NULL DEFAULT '',
    revoked_at      timestamptz
);
CREATE INDEX sessions_user_active ON sessions (user_id) WHERE revoked_at IS NULL;

CREATE TABLE teams (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX teams_name_key ON teams (lower(name));

CREATE TABLE team_members (
    team_id uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user ON team_members (user_id);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb       NOT NULL,
    updated_by uuid REFERENCES users (id),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE settings;
DROP TABLE team_members;
DROP TABLE teams;
DROP TABLE sessions;
DROP TABLE recovery_codes;
DROP TABLE users;
