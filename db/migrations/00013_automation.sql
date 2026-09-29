-- +goose Up
-- Weekly hours are {"mon": [{"start": "09:00", "end": "17:00"}], ...}; the closed schema is
-- validated in internal/sla. Holidays close the whole calendar day in the schedule's timezone.
CREATE TABLE business_hours (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    timezone   text        NOT NULL CHECK (length(timezone) BETWEEN 1 AND 64),
    weekly     jsonb       NOT NULL,
    holidays   date[]      NOT NULL DEFAULT '{}',
    is_default boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX business_hours_name ON business_hours (lower(name));
CREATE UNIQUE INDEX business_hours_single_default ON business_hours ((true)) WHERE is_default;

-- A NULL business_hours_id counts calendar time (24/7).
CREATE TABLE sla_policies (
    id                     uuid PRIMARY KEY DEFAULT uuidv7(),
    name                   text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    first_response_minutes integer CHECK (first_response_minutes BETWEEN 1 AND 525600),
    resolution_minutes     integer CHECK (resolution_minutes BETWEEN 1 AND 525600),
    at_risk_percent        integer     NOT NULL DEFAULT 80 CHECK (at_risk_percent BETWEEN 1 AND 99),
    business_hours_id      uuid REFERENCES business_hours (id),
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CHECK (first_response_minutes IS NOT NULL OR resolution_minutes IS NOT NULL)
);
CREATE UNIQUE INDEX sla_policies_name ON sla_policies (lower(name));

ALTER TABLE mailboxes
    ADD COLUMN auto_assign_mode text NOT NULL DEFAULT 'off' CHECK (auto_assign_mode IN ('off', 'round_robin', 'balanced')),
    ADD COLUMN default_sla_policy_id uuid REFERENCES sla_policies (id) ON DELETE SET NULL,
    ADD COLUMN business_hours_id uuid REFERENCES business_hours (id) ON DELETE SET NULL;

ALTER TABLE users
    ADD COLUMN max_open integer CHECK (max_open IS NULL OR max_open BETWEEN 1 AND 10000),
    ADD COLUMN availability text NOT NULL DEFAULT 'online' CHECK (availability IN ('online', 'busy', 'offline')),
    ADD COLUMN last_auto_assigned_at timestamptz;

-- Bulk and list mail must never get an automatic reply.
ALTER TABLE messages ADD COLUMN is_bulk boolean NOT NULL DEFAULT false;

-- sla_paused_at and sla_resumed_at bracket the latest stay in status waiting; the SLA sweep
-- turns them into a shifted resolution_due_at. sla_finalized_at is set once a closed
-- conversation has its final sla_state.
ALTER TABLE conversations
    ADD COLUMN sla_policy_id uuid REFERENCES sla_policies (id) ON DELETE SET NULL,
    ADD COLUMN sla_started_at timestamptz,
    ADD COLUMN first_response_due_at timestamptz,
    ADD COLUMN first_response_met_at timestamptz,
    ADD COLUMN resolution_due_at timestamptz,
    ADD COLUMN sla_state text NOT NULL DEFAULT 'none' CHECK (sla_state IN ('none', 'ok', 'at_risk', 'breached')),
    ADD COLUMN sla_paused_at timestamptz,
    ADD COLUMN sla_resumed_at timestamptz,
    ADD COLUMN sla_finalized_at timestamptz;
CREATE INDEX conversations_sla_sweep ON conversations (id) WHERE sla_policy_id IS NOT NULL AND sla_finalized_at IS NULL;
CREATE INDEX conversations_unassigned_recent ON conversations (mailbox_id, created_at DESC) WHERE assignee_user_id IS NULL AND status = 'open' AND deleted_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION conversations_track_waiting() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'waiting' THEN
        NEW.sla_paused_at := now();
        NEW.sla_resumed_at := NULL;
    ELSIF OLD.status = 'waiting' AND OLD.sla_paused_at IS NOT NULL AND OLD.sla_resumed_at IS NULL THEN
        NEW.sla_resumed_at := now();
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER conversations_track_waiting
    BEFORE UPDATE OF status ON conversations
    FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
    EXECUTE FUNCTION conversations_track_waiting();

CREATE TABLE rules (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    name            text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    mailbox_id      uuid REFERENCES mailboxes (id) ON DELETE CASCADE,
    trigger         text        NOT NULL CHECK (trigger IN ('conversation_created', 'message_received', 'conversation_updated', 'sla_at_risk', 'sla_breached', 'customer_idle')),
    idle_hours      integer CHECK (idle_hours BETWEEN 1 AND 720),
    conditions      jsonb       NOT NULL,
    actions         jsonb       NOT NULL,
    stop_processing boolean     NOT NULL DEFAULT false,
    position        integer     NOT NULL,
    enabled         boolean     NOT NULL DEFAULT true,
    created_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((trigger = 'customer_idle') = (idle_hours IS NOT NULL))
);
CREATE INDEX rules_trigger ON rules (trigger, position, id) WHERE enabled;

CREATE TABLE rule_runs (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    rule_id         uuid        NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
    conversation_id uuid        NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    trigger         text        NOT NULL,
    matched         boolean     NOT NULL,
    actions_applied jsonb       NOT NULL DEFAULT '[]',
    error           text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rule_runs_rule ON rule_runs (rule_id, created_at DESC, id DESC);
CREATE INDEX rule_runs_conversation ON rule_runs (rule_id, conversation_id, created_at DESC);
CREATE INDEX rule_runs_created ON rule_runs (created_at);

-- One row per rule and recipient; the row is claimed atomically so two concurrent evaluations
-- cannot both send within the 24 hour window.
CREATE TABLE automation_replies (
    rule_id uuid        NOT NULL REFERENCES rules (id) ON DELETE CASCADE,
    address text        NOT NULL,
    sent_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, address)
);

CREATE TABLE macros (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    name          text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    scope         text        NOT NULL CHECK (scope IN ('personal', 'global')),
    owner_user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    actions       jsonb       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((scope = 'personal') = (owner_user_id IS NOT NULL))
);
CREATE INDEX macros_owner ON macros (owner_user_id) WHERE owner_user_id IS NOT NULL;

-- +goose Down
DROP TABLE macros;
DROP TABLE automation_replies;
DROP TABLE rule_runs;
DROP TABLE rules;
ALTER TABLE messages DROP COLUMN is_bulk;
DROP TRIGGER conversations_track_waiting ON conversations;
DROP FUNCTION conversations_track_waiting();
DROP INDEX conversations_unassigned_recent;
DROP INDEX conversations_sla_sweep;
ALTER TABLE conversations
    DROP COLUMN sla_finalized_at, DROP COLUMN sla_resumed_at, DROP COLUMN sla_paused_at, DROP COLUMN sla_state,
    DROP COLUMN resolution_due_at, DROP COLUMN first_response_met_at, DROP COLUMN first_response_due_at,
    DROP COLUMN sla_started_at, DROP COLUMN sla_policy_id;
ALTER TABLE users DROP COLUMN last_auto_assigned_at, DROP COLUMN availability, DROP COLUMN max_open;
ALTER TABLE mailboxes DROP COLUMN business_hours_id, DROP COLUMN default_sla_policy_id, DROP COLUMN auto_assign_mode;
DROP TABLE sla_policies;
DROP TABLE business_hours;
