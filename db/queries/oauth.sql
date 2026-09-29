-- OAuth2 mailboxes (Google Workspace, Microsoft 365).

-- name: InsertOAuthMailbox :one
INSERT INTO mailboxes (
    name, email_address, auth_type,
    imap_host, imap_port, imap_tls, imap_username,
    smtp_host, smtp_port, smtp_tls, smtp_username,
    oauth_token_enc, oauth_expires_at, oauth_connected_at
) VALUES (
    @name, @email_address, @auth_type,
    @imap_host, @imap_port, @imap_tls, @email_address,
    @smtp_host, @smtp_port, @smtp_tls, @email_address,
    @oauth_token_enc, @oauth_expires_at, now()
) RETURNING *;

-- The token is encrypted with the row id in its AAD, so a new mailbox gets its token in a
-- second statement.
-- name: ConnectMailboxOAuth :one
UPDATE mailboxes SET
    auth_type = @auth_type,
    imap_host = @imap_host, imap_port = @imap_port, imap_tls = @imap_tls, imap_username = @email_address,
    smtp_host = @smtp_host, smtp_port = @smtp_port, smtp_tls = @smtp_tls, smtp_username = @email_address,
    imap_secret_enc = NULL, smtp_secret_enc = NULL,
    oauth_token_enc = @oauth_token_enc, oauth_expires_at = @oauth_expires_at, oauth_connected_at = now(),
    sync_state = 'disabled', sync_error = '', sync_state_changed_at = now(),
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: GetMailboxOAuth :one
SELECT id, auth_type, oauth_token_enc, oauth_expires_at FROM mailboxes WHERE id = $1;

-- name: LockMailboxOAuth :exec
SELECT pg_advisory_xact_lock(hashtextextended('mailauth:' || @mailbox_id::text, 0));

-- name: StoreRefreshedOAuthToken :exec
UPDATE mailboxes SET oauth_token_enc = @oauth_token_enc, oauth_expires_at = @oauth_expires_at
WHERE id = @id AND auth_type <> 'password';

-- name: MarkMailboxReauthRequired :exec
UPDATE mailboxes
SET sync_state = 'auth_failed', sync_error = @reason::text,
    sync_state_changed_at = CASE WHEN sync_state = 'auth_failed' THEN sync_state_changed_at ELSE now() END
WHERE id = @id;
