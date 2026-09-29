-- +goose Up
-- Custom roles: a name and a closed list of permission keys (validated in the application).
-- The built-in roles stay presets in code and have no row here.
CREATE TABLE custom_roles (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    description text        NOT NULL DEFAULT '' CHECK (length(description) <= 300),
    permissions text[]      NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX custom_roles_name_key ON custom_roles (lower(name));

ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('owner', 'admin', 'agent', 'readonly', 'custom'));
-- A user with the role 'custom' points at their custom role. permissions is a copy of that
-- role's list, kept in step by the queries that change either side, so a session loads the
-- effective rights together with the user. It is empty for the built-in roles.
ALTER TABLE users
    ADD COLUMN custom_role_id uuid REFERENCES custom_roles (id) ON DELETE RESTRICT,
    ADD COLUMN permissions text[] NOT NULL DEFAULT '{}',
    ADD CONSTRAINT users_custom_role_consistent CHECK ((role = 'custom') = (custom_role_id IS NOT NULL));
CREATE INDEX users_custom_role ON users (custom_role_id) WHERE custom_role_id IS NOT NULL;

-- OpenID Connect single sign-on. One row at most.
CREATE TABLE sso_settings (
    singleton              boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled                boolean     NOT NULL DEFAULT false,
    issuer_url             text        NOT NULL DEFAULT '' CHECK (length(issuer_url) <= 500),
    client_id              text        NOT NULL DEFAULT '' CHECK (length(client_id) <= 500),
    client_secret_enc      bytea,
    allowed_domains        text[]      NOT NULL DEFAULT '{}',
    button_label           text        NOT NULL DEFAULT 'SSO' CHECK (length(button_label) BETWEEN 1 AND 40),
    required               boolean     NOT NULL DEFAULT false,
    auto_provision         boolean     NOT NULL DEFAULT false,
    default_role           text        NOT NULL DEFAULT 'agent' CHECK (default_role IN ('agent', 'readonly', 'custom')),
    default_custom_role_id uuid        REFERENCES custom_roles (id) ON DELETE RESTRICT,
    trust_idp_mfa          boolean     NOT NULL DEFAULT false,
    -- Some providers (Microsoft Entra ID) never send email_verified; false is always refused.
    trust_missing_email_verified boolean NOT NULL DEFAULT false,
    allow_internal_issuer  boolean     NOT NULL DEFAULT false,
    updated_by             uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CHECK ((default_role = 'custom') = (default_custom_role_id IS NOT NULL))
);

-- True when the session was signed in through an IdP that reported multi-factor
-- authentication and the workspace trusts that, so Echoo asks for no second factor.
ALTER TABLE sessions ADD COLUMN idp_mfa boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE sessions DROP COLUMN idp_mfa;
DROP TABLE sso_settings;
ALTER TABLE users DROP CONSTRAINT users_custom_role_consistent;
UPDATE users SET role = 'readonly' WHERE role = 'custom';
ALTER TABLE users DROP COLUMN permissions, DROP COLUMN custom_role_id;
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('owner', 'admin', 'agent', 'readonly'));
DROP TABLE custom_roles;
