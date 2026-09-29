-- Report queries. Every one takes the readable mailbox ids (already narrowed by the caller's
-- mailbox filter) and the optional team, agent and label filters. Days are local days in the
-- workspace timezone (@tz), so a day is 23 or 25 hours long when the clocks change; the caller
-- groups days into weeks. docs/reports.md defines every metric.

-- name: ReportFacts :many
-- Counts one metric (@kind: new, customer_messages, replies, resolved or reopened) per local day
-- and attribution: the conversation's mailbox, team and assignee, plus the author of a reply or
-- the person who resolved. With @split each conversation also appears once per label (once with a
-- NULL label when it has none), which is how label tables and label filters are answered. The
-- guard on @kind in each branch lets the planner drop the other four, so callers run the kinds as
-- separate queries in parallel. Deleted and spam conversations are skipped, and 'new' leaves out
-- conversations that began as an outbound message or a campaign: nobody wrote in.
SELECT day, mailbox_id, team_id, assignee_id, actor_id, label_id, count(*)::bigint AS n
FROM (
    SELECT (c.created_at AT TIME ZONE @tz::text)::date AS day, c.mailbox_id,
           c.assignee_team_id AS team_id, c.assignee_user_id AS assignee_id, NULL::uuid AS actor_id, cl.label_id
    FROM conversations c
    LEFT JOIN conversation_labels cl ON @split::boolean AND cl.conversation_id = c.id
    WHERE @kind::text = 'new'
        AND c.created_at >= @range_from::timestamptz AND c.created_at < @range_to::timestamptz
        AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
        AND NOT EXISTS (SELECT 1 FROM conversation_events o WHERE o.conversation_id = c.id AND o.type = 'created' AND o.data ->> 'reason' IN ('outbound', 'campaign'))
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR cl.label_id = sqlc.narg(label_id))
    UNION ALL
    SELECT (m.received_at AT TIME ZONE @tz::text)::date, c.mailbox_id,
           c.assignee_team_id, c.assignee_user_id, NULL::uuid, cl.label_id
    FROM messages m
    JOIN conversations c ON c.id = m.conversation_id
    LEFT JOIN conversation_labels cl ON @split::boolean AND cl.conversation_id = c.id
    WHERE @kind::text = 'customer_messages'
        AND m.kind = 'email' AND m.direction = 'in' AND NOT m.is_bounce AND NOT m.auto_submitted AND m.deleted_at IS NULL
        AND m.received_at >= @range_from::timestamptz AND m.received_at < @range_to::timestamptz
        AND m.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR cl.label_id = sqlc.narg(label_id))
    UNION ALL
    SELECT (m.sent_at AT TIME ZONE @tz::text)::date, c.mailbox_id,
           c.assignee_team_id, c.assignee_user_id, m.author_user_id, cl.label_id
    FROM messages m
    JOIN conversations c ON c.id = m.conversation_id
    LEFT JOIN conversation_labels cl ON @split::boolean AND cl.conversation_id = c.id
    WHERE @kind::text = 'replies'
        AND m.kind = 'email' AND m.direction = 'out' AND NOT m.auto_submitted AND m.deleted_at IS NULL
        AND m.sent_at >= @range_from::timestamptz AND m.sent_at < @range_to::timestamptz
        AND m.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR cl.label_id = sqlc.narg(label_id))
    UNION ALL
    SELECT (e.created_at AT TIME ZONE @tz::text)::date, c.mailbox_id,
           c.assignee_team_id, c.assignee_user_id, e.actor_user_id, cl.label_id
    FROM conversation_events e
    JOIN conversations c ON c.id = e.conversation_id
    LEFT JOIN conversation_labels cl ON @split::boolean AND cl.conversation_id = c.id
    WHERE @kind::text = 'resolved' AND e.type = 'resolved'
        AND e.created_at >= @range_from::timestamptz AND e.created_at < @range_to::timestamptz
        AND e.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR cl.label_id = sqlc.narg(label_id))
    UNION ALL
    -- A reopening is a reopened event whose previous status event was a resolution. The ingest
    -- worker also records a customer reply to a waiting conversation as reopened; that is not one.
    SELECT (e.created_at AT TIME ZONE @tz::text)::date, c.mailbox_id,
           c.assignee_team_id, c.assignee_user_id, NULL::uuid, cl.label_id
    FROM conversation_events e
    JOIN conversations c ON c.id = e.conversation_id
    LEFT JOIN conversation_labels cl ON @split::boolean AND cl.conversation_id = c.id
    WHERE @kind::text = 'reopened' AND e.type = 'reopened'
        AND e.created_at >= @range_from::timestamptz AND e.created_at < @range_to::timestamptz
        AND e.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR cl.label_id = sqlc.narg(label_id))
        AND (SELECT p.type FROM conversation_events p
             WHERE p.conversation_id = e.conversation_id AND p.type IN ('resolved', 'status_changed', 'reopened')
                 AND (p.created_at, p.id) < (e.created_at, e.id)
             ORDER BY p.created_at DESC, p.id DESC LIMIT 1) = 'resolved'
) facts
GROUP BY day, mailbox_id, team_id, assignee_id, actor_id, label_id;

-- name: ReportFirstResponseRows :many
-- Conversations created in the range that have a first response, or whose first-response
-- target has passed, grouped by day (or all days as one when @per_day is false, which is all a
-- table needs), key and business-hours schedule. Conversations that
-- began as an outbound message are left out: nobody was waiting. Groups without a schedule carry
-- their wall-clock durations in wall_seconds; groups with one carry the timestamps, because
-- business time needs the schedule and is computed by the caller.
SELECT day, key, business_hours_id,
       count(*) FILTER (WHERE sla_known)::bigint AS sla_total,
       count(*) FILTER (WHERE sla_known AND sla_met)::bigint AS sla_met,
       coalesce(array_agg(wall) FILTER (WHERE wall IS NOT NULL AND business_hours_id IS NULL), '{}')::float8[] AS wall_seconds,
       coalesce(array_agg(started) FILTER (WHERE wall IS NOT NULL AND business_hours_id IS NOT NULL), '{}')::timestamptz[] AS starts,
       coalesce(array_agg(ended) FILTER (WHERE wall IS NOT NULL AND business_hours_id IS NOT NULL), '{}')::timestamptz[] AS ends
FROM (
    SELECT (CASE WHEN @per_day::boolean THEN (c.created_at AT TIME ZONE @tz::text)::date ELSE @first_day::date END) AS day,
           (CASE @dim::text WHEN 'agent' THEN c.assignee_user_id WHEN 'team' THEN c.assignee_team_id
                WHEN 'mailbox' THEN c.mailbox_id WHEN 'label' THEN cl.label_id END)::uuid AS key,
           p.business_hours_id,
           c.created_at AS started, c.first_responded_at AS ended,
           extract(epoch FROM c.first_responded_at - c.created_at)::float8 AS wall,
           (c.first_response_due_at IS NOT NULL AND (c.first_response_met_at IS NOT NULL OR c.first_response_due_at <= now()))::boolean AS sla_known,
           coalesce(c.first_response_met_at <= c.first_response_due_at, false)::boolean AS sla_met
    FROM conversations c
    LEFT JOIN conversation_labels cl ON @dim::text = 'label' AND cl.conversation_id = c.id
    LEFT JOIN sla_policies p ON p.id = c.sla_policy_id
    WHERE c.created_at >= @range_from::timestamptz AND c.created_at < @range_to::timestamptz
        AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
        AND (c.first_responded_at IS NOT NULL OR c.first_response_due_at <= now())
        AND NOT EXISTS (SELECT 1 FROM conversation_events o WHERE o.conversation_id = c.id AND o.type = 'created' AND o.data ->> 'reason' IN ('outbound', 'campaign'))
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)))
) rows
GROUP BY day, key, business_hours_id;

-- name: ReportResolutionRows :many
-- Conversations currently closed whose latest resolution falls in the range, grouped like
-- ReportFirstResponseRows.
SELECT day, key, business_hours_id,
       count(*) FILTER (WHERE sla_known)::bigint AS sla_total,
       count(*) FILTER (WHERE sla_known AND sla_met)::bigint AS sla_met,
       coalesce(array_agg(wall) FILTER (WHERE business_hours_id IS NULL), '{}')::float8[] AS wall_seconds,
       coalesce(array_agg(started) FILTER (WHERE business_hours_id IS NOT NULL), '{}')::timestamptz[] AS starts,
       coalesce(array_agg(ended) FILTER (WHERE business_hours_id IS NOT NULL), '{}')::timestamptz[] AS ends
FROM (
    SELECT (CASE WHEN @per_day::boolean THEN (c.resolved_at AT TIME ZONE @tz::text)::date ELSE @first_day::date END) AS day,
           (CASE @dim::text WHEN 'agent' THEN c.assignee_user_id WHEN 'team' THEN c.assignee_team_id
                WHEN 'mailbox' THEN c.mailbox_id WHEN 'label' THEN cl.label_id END)::uuid AS key,
           p.business_hours_id,
           c.created_at AS started, c.resolved_at AS ended,
           extract(epoch FROM c.resolved_at - c.created_at)::float8 AS wall,
           (c.resolution_due_at IS NOT NULL)::boolean AS sla_known,
           coalesce(c.resolved_at <= c.resolution_due_at, false)::boolean AS sla_met
    FROM conversations c
    LEFT JOIN conversation_labels cl ON @dim::text = 'label' AND cl.conversation_id = c.id
    LEFT JOIN sla_policies p ON p.id = c.sla_policy_id
    WHERE c.resolved_at >= @range_from::timestamptz AND c.resolved_at < @range_to::timestamptz
        AND c.status = 'closed'
        AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL
        AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
        AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
        AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)))
) rows
GROUP BY day, key, business_hours_id;

-- name: ReportLifecycle :many
-- The conversations created in the range (the same cohort as 'new', plus spam), per local day,
-- by whether we replied and by the status they have now. The Overzicht chart and flow diagram
-- read these. Deleted conversations and those that began as an outbound message or a campaign
-- are left out.
SELECT (c.created_at AT TIME ZONE @tz::text)::date AS day,
       count(*) FILTER (WHERE c.status = 'spam')::bigint AS spam,
       count(*) FILTER (WHERE c.status = 'open' AND c.first_responded_at IS NOT NULL)::bigint AS answered_open,
       count(*) FILTER (WHERE c.status = 'waiting' AND c.first_responded_at IS NOT NULL)::bigint AS answered_waiting,
       count(*) FILTER (WHERE c.status = 'closed' AND c.first_responded_at IS NOT NULL)::bigint AS answered_closed,
       count(*) FILTER (WHERE c.status = 'open' AND c.first_responded_at IS NULL)::bigint AS unanswered_open,
       count(*) FILTER (WHERE c.status = 'waiting' AND c.first_responded_at IS NULL)::bigint AS unanswered_waiting,
       count(*) FILTER (WHERE c.status = 'closed' AND c.first_responded_at IS NULL)::bigint AS unanswered_closed
FROM conversations c
WHERE c.created_at >= @range_from::timestamptz AND c.created_at < @range_to::timestamptz
    AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM conversation_events o WHERE o.conversation_id = c.id AND o.type = 'created' AND o.data ->> 'reason' IN ('outbound', 'campaign'))
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)))
GROUP BY day;

-- name: ReportFirstResponseTarget :one
-- The first-response target the conversations of the range were held to: the median of their
-- SLA policies' first_response_minutes, in seconds, over the same cohort as the first-response
-- times. n is 0 (and the median 0) when none of them has a target.
SELECT count(*)::bigint AS n,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY p.first_response_minutes * 60), 0)::float8 AS median_seconds
FROM conversations c
JOIN sla_policies p ON p.id = c.sla_policy_id
WHERE p.first_response_minutes IS NOT NULL
    AND c.created_at >= @range_from::timestamptz AND c.created_at < @range_to::timestamptz
    AND c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND c.status <> 'spam'
    AND NOT EXISTS (SELECT 1 FROM conversation_events o WHERE o.conversation_id = c.id AND o.type = 'created' AND o.data ->> 'reason' IN ('outbound', 'campaign'))
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)));

-- name: ReportOpenNow :one
SELECT count(*)::bigint AS open
FROM conversations c
WHERE c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.status = 'open' AND c.deleted_at IS NULL
    AND (c.snoozed_until IS NULL OR c.snoozed_until <= now())
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)));

-- name: ReportLive :one
-- The state right now. Open excludes snoozed conversations, like the inbox does; at risk and
-- breached count open and waiting conversations whose SLA sweep has not finalized them.
SELECT
    count(*) FILTER (WHERE c.status = 'open' AND (c.snoozed_until IS NULL OR c.snoozed_until <= now()))::bigint AS open,
    count(*) FILTER (WHERE c.status = 'open' AND (c.snoozed_until IS NULL OR c.snoozed_until <= now()) AND c.assignee_user_id IS NULL)::bigint AS unassigned,
    count(*) FILTER (WHERE c.status = 'waiting')::bigint AS waiting,
    count(*) FILTER (WHERE c.sla_state = 'at_risk')::bigint AS sla_at_risk,
    count(*) FILTER (WHERE c.sla_state = 'breached')::bigint AS sla_breached
FROM conversations c
WHERE c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.status IN ('open', 'waiting') AND c.deleted_at IS NULL
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)));

-- name: ReportLiveByAgent :many
SELECT c.assignee_user_id AS user_id,
       count(*) FILTER (WHERE c.status = 'open' AND (c.snoozed_until IS NULL OR c.snoozed_until <= now()))::bigint AS open,
       count(*) FILTER (WHERE c.status = 'waiting')::bigint AS waiting,
       count(*) FILTER (WHERE c.sla_state IN ('at_risk', 'breached'))::bigint AS sla_risk
FROM conversations c
WHERE c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.status IN ('open', 'waiting') AND c.deleted_at IS NULL
    AND c.assignee_user_id IS NOT NULL
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)))
GROUP BY c.assignee_user_id;

-- name: ReportAttention :one
-- Open, not snoozed conversations that we have not replied to yet. Breached ones are past their
-- first-response due time; due soon ones reach it within @window_minutes. next_due_seconds is
-- the time until the first of the due soon ones (0 when there are none) and oldest_seconds the
-- age of the longest-waiting unanswered conversation.
SELECT
    count(*)::bigint AS unanswered,
    count(*) FILTER (WHERE c.first_response_due_at <= now())::bigint AS breached,
    count(*) FILTER (WHERE c.first_response_due_at > now() AND c.first_response_due_at <= now() + make_interval(mins => @window_minutes::int))::bigint AS due_soon,
    coalesce(extract(epoch FROM min(c.first_response_due_at) FILTER (WHERE c.first_response_due_at > now() AND c.first_response_due_at <= now() + make_interval(mins => @window_minutes::int)) - now()), 0)::float8 AS next_due_seconds,
    coalesce(extract(epoch FROM now() - min(c.created_at)), 0)::float8 AS oldest_seconds
FROM conversations c
WHERE c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.status = 'open' AND c.deleted_at IS NULL
    AND (c.snoozed_until IS NULL OR c.snoozed_until <= now())
    AND c.first_responded_at IS NULL
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)));

-- name: ReportAttentionAssignees :many
-- Who holds the breached and due soon conversations of ReportAttention; user_id is NULL for
-- the unassigned ones.
SELECT c.assignee_user_id AS user_id, count(*)::bigint AS n
FROM conversations c
WHERE c.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.status = 'open' AND c.deleted_at IS NULL
    AND (c.snoozed_until IS NULL OR c.snoozed_until <= now())
    AND c.first_responded_at IS NULL
    AND c.first_response_due_at <= now() + make_interval(mins => @window_minutes::int)
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)))
GROUP BY c.assignee_user_id;

-- name: ReportNames :many
-- Display names for the group keys of a table, for whichever dimension it groups by.
SELECT id, name FROM (
    SELECT id, name FROM users WHERE @dim::text = 'agent'
    UNION ALL SELECT id, name FROM teams WHERE @dim::text = 'team'
    UNION ALL SELECT id, name FROM mailboxes WHERE @dim::text = 'mailbox'
    UNION ALL SELECT id, name FROM labels WHERE @dim::text = 'label'
) names
WHERE id = ANY(@ids::uuid[]);

-- name: ReportCsatRows :many
-- One row per survey sent in the range, with the rating when the customer answered.
SELECT (r.sent_at AT TIME ZONE @tz::text)::date AS day,
       (CASE @dim::text WHEN 'agent' THEN c.assignee_user_id WHEN 'team' THEN c.assignee_team_id
            WHEN 'mailbox' THEN c.mailbox_id WHEN 'label' THEN cl.label_id END)::uuid AS key,
       p.rating
FROM csat_requests r
JOIN conversations c ON c.id = r.conversation_id
LEFT JOIN csat_responses p ON p.conversation_id = r.conversation_id
LEFT JOIN conversation_labels cl ON @dim::text = 'label' AND cl.conversation_id = r.conversation_id
WHERE r.sent_at >= @range_from::timestamptz AND r.sent_at < @range_to::timestamptz
    AND r.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)));

-- name: ReportCsatComments :many
SELECT c.id AS conversation_id, c.number, c.subject, p.rating, p.comment, p.updated_at, u.name AS assignee_name
FROM csat_requests r
JOIN csat_responses p ON p.conversation_id = r.conversation_id
JOIN conversations c ON c.id = r.conversation_id
LEFT JOIN users u ON u.id = c.assignee_user_id
WHERE r.sent_at >= @range_from::timestamptz AND r.sent_at < @range_to::timestamptz
    AND r.mailbox_id = ANY(@mailbox_ids::uuid[]) AND c.deleted_at IS NULL AND p.comment <> ''
    AND (sqlc.narg(team_id)::uuid IS NULL OR c.assignee_team_id = sqlc.narg(team_id))
    AND (sqlc.narg(agent_id)::uuid IS NULL OR c.assignee_user_id = sqlc.narg(agent_id))
    AND (sqlc.narg(label_id)::uuid IS NULL OR EXISTS (SELECT 1 FROM conversation_labels f WHERE f.conversation_id = c.id AND f.label_id = sqlc.narg(label_id)))
ORDER BY p.updated_at DESC
LIMIT 50;

-- name: ReportBusinessHours :many
SELECT * FROM business_hours WHERE id = ANY(@ids::uuid[]);

-- name: ReportAgents :many
SELECT id, name, availability FROM users
WHERE deactivated_at IS NULL AND id = ANY(@ids::uuid[]);
