-- name: InsertAudit :exec
INSERT INTO audit_log (actor_user_id, actor_ip, action, target_type, target_id, metadata)
VALUES ($1, $2, $3, $4, $5, $6);

-- Keyset pagination: pass before_id = 0 for the first page. action_prefix must already be
-- LIKE-escaped; the other filters are optional.
-- name: ListAudit :many
SELECT sqlc.embed(a), u.name AS actor_name, u.email AS actor_email
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_user_id
WHERE (sqlc.arg(before_id)::bigint = 0 OR a.id < sqlc.arg(before_id)::bigint)
  AND (sqlc.arg(action_prefix)::text = '' OR a.action LIKE sqlc.arg(action_prefix)::text || '%')
  AND (sqlc.narg(actor_id)::uuid IS NULL OR a.actor_user_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(from_at)::timestamptz IS NULL OR a.at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR a.at < sqlc.narg(to_at)::timestamptz)
ORDER BY a.id DESC
LIMIT sqlc.arg(page_size)::integer;
