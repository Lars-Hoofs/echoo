-- +goose Up
-- Per-mailbox retention overrides. The workspace-wide periods live in the `retention` row of
-- `settings`; a mailbox without a row here follows them. A NULL period in a row means "keep
-- forever" for that mailbox, so a mailbox can opt out of a workspace-wide period.
CREATE TABLE retention_policies (
    mailbox_id                 uuid PRIMARY KEY REFERENCES mailboxes (id) ON DELETE CASCADE,
    closed_conversation_months integer CHECK (closed_conversation_months BETWEEN 1 AND 120),
    attachment_months          integer CHECK (attachment_months BETWEEN 1 AND 120),
    spam_days                  integer CHECK (spam_days BETWEEN 1 AND 3650),
    updated_by                 uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

-- The append-only trigger now has exactly one exception: audit_log_purge below, which runs as
-- the table owner. The application role owns nothing, so it cannot delete rows itself, and it
-- cannot get around this by setting the flag because current_user is checked as well.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND current_setting('echoo.audit_purge', true) = 'on'
       AND current_user = (SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid = TG_RELID) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd

-- Deletes up to batch_limit entries older than the given age and writes an audit entry with the
-- count in the same transaction, before the rows go. It refuses ages under 60 days so that a
-- compromised server cannot use it to wipe recent history. Returns the number of rows deleted.
-- +goose StatementBegin
CREATE FUNCTION audit_log_purge(older_than interval, batch_limit integer) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    cutoff timestamptz := now() - older_than;
    doomed bigint[];
    n      integer;
BEGIN
    IF older_than < interval '60 days' THEN
        RAISE EXCEPTION 'audit_log_purge: entries younger than 60 days are never purged';
    END IF;
    IF batch_limit < 1 OR batch_limit > 10000 THEN
        RAISE EXCEPTION 'audit_log_purge: batch_limit must be between 1 and 10000';
    END IF;

    SELECT coalesce(array_agg(id), '{}') INTO doomed FROM (
        SELECT id FROM audit_log WHERE at < cutoff ORDER BY at, id LIMIT batch_limit FOR UPDATE SKIP LOCKED
    ) old;
    n := cardinality(doomed);
    IF n = 0 THEN
        RETURN 0;
    END IF;

    INSERT INTO audit_log (action, target_type, target_id, metadata)
    VALUES ('audit.purged', 'audit_log', '', jsonb_build_object('count', n, 'before', cutoff));

    PERFORM set_config('echoo.audit_purge', 'on', true);
    DELETE FROM audit_log WHERE id = ANY (doomed);
    PERFORM set_config('echoo.audit_purge', 'off', true);
    RETURN n;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION audit_log_purge(interval, integer) FROM PUBLIC;
-- The role name is the one docker/postgres-init creates. A hand-written setup with another
-- role has to grant EXECUTE itself (docs/operations.md).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'echoo_app') THEN
        GRANT EXECUTE ON FUNCTION audit_log_purge(interval, integer) TO echoo_app;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION audit_log_purge(interval, integer);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd
DROP TABLE retention_policies;
