-- name: ListImapSyncMailboxes :many
SELECT * FROM mailboxes
WHERE disabled_at IS NULL AND transport = 'imap'
ORDER BY id;

-- name: GetImapSyncMailbox :one
SELECT * FROM mailboxes
WHERE id = $1 AND disabled_at IS NULL AND transport = 'imap';

-- name: UpsertInboxFolder :one
INSERT INTO mailbox_folders (mailbox_id, name, role)
VALUES ($1, 'INBOX', 'inbox')
ON CONFLICT (mailbox_id, name) DO UPDATE SET role = EXCLUDED.role
RETURNING *;

-- name: ResetFolderUIDValidity :exec
UPDATE mailbox_folders SET uidvalidity = $2, last_uid = 0 WHERE id = $1;

-- name: AdvanceFolderCursor :exec
UPDATE mailbox_folders
SET last_uid = GREATEST(last_uid, @last_uid::bigint), last_synced_at = now()
WHERE id = @id;

-- name: InsertImapRawMessage :one
INSERT INTO raw_messages (mailbox_id, folder_id, source, uidvalidity, uid, sha256, size_bytes, blob_key)
VALUES ($1, $2, 'imap', $3, $4, $5, $6, $7)
ON CONFLICT (mailbox_id, folder_id, uidvalidity, uid) WHERE source = 'imap' DO NOTHING
RETURNING id;

-- name: SetMailboxSyncState :exec
UPDATE mailboxes
SET sync_state = @sync_state::text,
    sync_error = @sync_error::text,
    sync_state_changed_at = CASE WHEN sync_state = @sync_state::text THEN sync_state_changed_at ELSE now() END
WHERE id = @id;

-- name: TouchMailboxSynced :exec
UPDATE mailboxes SET last_synced_at = now() WHERE id = $1;

-- name: MarkMailboxSyncDisabled :exec
UPDATE mailboxes
SET sync_state = 'disabled', sync_error = '', sync_state_changed_at = now()
WHERE id = $1 AND disabled_at IS NOT NULL AND sync_state <> 'disabled';

-- name: InsertSkippedImapRawMessage :one
INSERT INTO raw_messages (mailbox_id, folder_id, source, uidvalidity, uid, sha256, size_bytes, blob_key, parse_status, parse_error)
VALUES ($1, $2, 'imap', $3, $4, $5, $6, $7, 'skipped', $8)
ON CONFLICT (mailbox_id, folder_id, uidvalidity, uid) WHERE source = 'imap' DO NOTHING
RETURNING id;
