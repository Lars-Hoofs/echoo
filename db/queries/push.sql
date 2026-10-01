-- Push notifications: devices, the VAPID key and the dispatch queue.

-- name: PushUpsertDevice :one
-- Registering the same endpoint again (the browser renewed its keys, or another user signed in
-- on the device) moves it to the current user and session. A device that changes hands gets a
-- new id, so a push or a "gone" answer meant for the previous user cannot touch it.
INSERT INTO push_devices (user_id, session_id, kind, endpoint, p256dh, auth, label)
VALUES (@user_id, @session_id, @kind, @endpoint, @p256dh, @auth, @label)
ON CONFLICT (kind, endpoint) DO UPDATE SET
    id = CASE WHEN push_devices.user_id = EXCLUDED.user_id THEN push_devices.id ELSE uuidv7() END,
    user_id = EXCLUDED.user_id, session_id = EXCLUDED.session_id, p256dh = EXCLUDED.p256dh,
    auth = EXCLUDED.auth, label = EXCLUDED.label, created_at = now()
RETURNING id, kind, label, created_at, last_push_at;

-- name: PushListDevices :many
SELECT id, kind, label, created_at, last_push_at, session_id
FROM push_devices WHERE user_id = @user_id
ORDER BY created_at DESC, id;

-- name: PushDeleteDevice :execrows
DELETE FROM push_devices WHERE id = @id AND user_id = @user_id;

-- name: PushDeleteDeviceByEndpoint :execrows
DELETE FROM push_devices WHERE user_id = @user_id AND kind = @kind AND endpoint = @endpoint;

-- name: PushDropDevice :exec
-- A push service said the device is gone for good.
DELETE FROM push_devices WHERE id = @id;

-- name: PushTouchDevices :exec
UPDATE push_devices SET last_push_at = now() WHERE id = ANY(@ids::uuid[]);

-- name: PushGetVapid :one
SELECT public_key, private_key_enc FROM push_vapid;

-- name: PushInsertVapid :exec
-- Two instances starting at once both try; the first one wins and the other reads it back.
INSERT INTO push_vapid (public_key, private_key_enc) VALUES (@public_key, @private_key_enc)
ON CONFLICT (singleton) DO NOTHING;

-- name: PushExpireStale :execrows
-- A notification that waited this long is no longer worth a push (the dispatcher was down).
UPDATE notifications SET push_handled_at = now()
WHERE push_handled_at IS NULL AND created_at < now() - interval '30 minutes';

-- name: PushClaimNotifications :many
-- Notifications to push, with what the message needs. Hidden conversations and deactivated
-- users are claimed too, so they leave the queue, and skipped by the caller. The lock waits
-- instead of skipping: the notification mail claims the same rows for a moment, and skipping
-- would delay the push until the next sweep.
SELECT n.id, n.kind, n.user_id, n.conversation_id, c.mailbox_id AS conversation_mailbox_id,
       c.number AS conversation_number, c.subject AS conversation_subject,
       (c.deleted_at IS NOT NULL)::boolean AS conversation_deleted,
       u.deactivated_at AS recipient_deactivated_at,
       u.push_notify_mentions, u.push_notify_assignments, u.push_notify_replies, u.push_notify_sla,
       actor.name AS actor_name
FROM notifications n
JOIN users u ON u.id = n.user_id
JOIN conversations c ON c.id = n.conversation_id
LEFT JOIN users actor ON actor.id = n.actor_id
WHERE n.push_handled_at IS NULL
ORDER BY n.created_at, n.id
LIMIT 100
FOR UPDATE OF n;

-- name: PushMarkHandled :exec
UPDATE notifications SET push_handled_at = now() WHERE id = ANY(@ids::uuid[]);

-- name: PushDevicesOfUsers :many
SELECT id, user_id, kind, endpoint, p256dh, auth
FROM push_devices WHERE user_id = ANY(@user_ids::uuid[]);

-- name: PushSetPrefs :exec
UPDATE users SET push_notify_mentions = @mentions, push_notify_assignments = @assignments,
    push_notify_replies = @replies, push_notify_sla = @sla
WHERE id = @id;

-- name: PushUsers :many
SELECT * FROM users WHERE id = ANY(@ids::uuid[]);
