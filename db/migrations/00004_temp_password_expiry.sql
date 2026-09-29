-- +goose Up
ALTER TABLE users ADD COLUMN temp_password_expires_at timestamptz;
-- Accounts that already hold a temporary password get a fresh 72 hour window.
UPDATE users SET temp_password_expires_at = now() + interval '72 hours' WHERE password_must_change;

-- +goose Down
ALTER TABLE users DROP COLUMN temp_password_expires_at;
