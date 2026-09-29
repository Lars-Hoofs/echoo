-- name: GetSSOSettings :one
SELECT * FROM sso_settings WHERE singleton;

-- Read in the login transaction: waits for a settings change in flight, so a session is never
-- marked as MFA-backed on trust that was withdrawn a moment earlier.
-- name: GetSSOSettingsForShare :one
SELECT * FROM sso_settings WHERE singleton FOR SHARE;

-- A NULL secret keeps the stored one: the secret is write-only.
-- name: UpsertSSOSettings :one
INSERT INTO sso_settings (
    singleton, enabled, issuer_url, client_id, client_secret_enc, allowed_domains, button_label, required,
    auto_provision, default_role, default_custom_role_id, trust_idp_mfa, trust_missing_email_verified, allow_internal_issuer,
    updated_by, updated_at
) VALUES (
    true, @enabled, @issuer_url, @client_id, sqlc.narg(client_secret_enc), @allowed_domains, @button_label, @required,
    @auto_provision, @default_role, sqlc.narg(default_custom_role_id), @trust_idp_mfa, @trust_missing_email_verified,
    @allow_internal_issuer, @updated_by, now()
)
ON CONFLICT (singleton) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    issuer_url = EXCLUDED.issuer_url,
    client_id = EXCLUDED.client_id,
    client_secret_enc = COALESCE(EXCLUDED.client_secret_enc, sso_settings.client_secret_enc),
    allowed_domains = EXCLUDED.allowed_domains,
    button_label = EXCLUDED.button_label,
    required = EXCLUDED.required,
    auto_provision = EXCLUDED.auto_provision,
    default_role = EXCLUDED.default_role,
    default_custom_role_id = EXCLUDED.default_custom_role_id,
    trust_idp_mfa = EXCLUDED.trust_idp_mfa,
    trust_missing_email_verified = EXCLUDED.trust_missing_email_verified,
    allow_internal_issuer = EXCLUDED.allow_internal_issuer,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING *;

-- name: CreateSSOUser :one
INSERT INTO users (email, name, role, custom_role_id, permissions, password_hash)
VALUES (
    @email, @name, @role, sqlc.narg(custom_role_id),
    COALESCE((SELECT custom_roles.permissions FROM custom_roles WHERE custom_roles.id = sqlc.narg(custom_role_id)), '{}'),
    @password_hash
)
RETURNING *;

-- name: MarkSessionIdPMFA :exec
UPDATE sessions SET idp_mfa = true WHERE id = $1;
