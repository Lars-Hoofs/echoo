package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/jobs"
	"echoo/internal/sla"
)

// Deps are the engine's collaborators. Jobs is optional: workers fall back to the client that
// River puts in the job context.
type Deps struct {
	Pool   *pgxpool.Pool
	Jobs   *river.Client[pgx.Tx]
	Now    func() time.Time
	Logger *slog.Logger
}

// Engine evaluates rules, runs actions and keeps SLA state. Everything it changes on a
// conversation goes through inbox.Service with a rule or system actor.
type Engine struct {
	d     Deps
	q     *dbq.Queries
	inbox *inbox.Service
}

func NewEngine(d Deps) *Engine {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Engine{d: d, q: dbq.New(d.Pool), inbox: inbox.NewService(d.Pool)}
}

func (e *Engine) client(ctx context.Context) (*river.Client[pgx.Tx], error) {
	if e.d.Jobs != nil {
		return e.d.Jobs, nil
	}
	c, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return nil, fmt.Errorf("job client: %w", err)
	}
	return c, nil
}

// Trigger identifies one evaluation.
type Trigger struct {
	ConversationID pgtype.UUID
	Name           string
	MessageID      pgtype.UUID
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func ruleActor(mailbox, rule pgtype.UUID) inbox.Actor {
	ids := []pgtype.UUID{mailbox}
	return inbox.Actor{Source: inbox.SourceRule, RuleID: rule, Read: ids, Write: ids}
}

func systemActor(mailbox pgtype.UUID) inbox.Actor {
	ids := []pgtype.UUID{mailbox}
	return inbox.Actor{Source: inbox.SourceSystem, Read: ids, Write: ids}
}

// Evaluate prepares the SLA of the conversation, runs the enabled rules of the trigger in
// order and finally auto-assigns a new conversation. Rule actions carry a rule actor, so they
// never queue another evaluation: a rule cannot trigger rules.
func (e *Engine) Evaluate(ctx context.Context, t Trigger) error {
	conv, err := e.q.AutoGetConversation(ctx, t.ConversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	if conv, err = e.prepareSLA(ctx, conv, t); err != nil {
		return err
	}
	rules, err := e.q.AutoListRulesForTrigger(ctx, dbq.AutoListRulesForTriggerParams{Trigger: t.Name, MailboxID: conv.MailboxID})
	if err != nil {
		return fmt.Errorf("list rules: %w", err)
	}
	for _, r := range rules {
		matched, stop, err := e.runRule(ctx, r, t)
		if err != nil {
			return err
		}
		if matched && stop {
			break
		}
	}
	if t.Name == jobs.TriggerConversationCreated {
		return e.autoAssignNew(ctx, t.ConversationID)
	}
	return nil
}

// runRule evaluates one rule against fresh facts and records the run. Failures of individual
// actions are recorded on the run, not returned: retrying the job would repeat the actions
// that did work.
func (e *Engine) runRule(ctx context.Context, r dbq.Rule, t Trigger) (matched, stop bool, err error) {
	conv, err := e.q.AutoGetConversation(ctx, t.ConversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("load conversation: %w", err)
	}
	facts, err := e.loadFacts(ctx, conv, t.MessageID)
	if err != nil {
		return false, false, err
	}
	run := dbq.AutoInsertRuleRunParams{RuleID: r.ID, ConversationID: conv.ID, Trigger: t.Name, ActionsApplied: []byte("[]")}
	conds, err := ParseConditions(r.Conditions)
	if err != nil {
		run.Error = "stored conditions are invalid: " + err.Error()
		return false, false, e.record(ctx, run)
	}
	run.Matched = conds.Match(facts)
	if !run.Matched {
		return false, false, e.record(ctx, run)
	}
	actions, err := ParseActions(r.Actions, true)
	if err != nil {
		run.Error = "stored actions are invalid: " + err.Error()
		return true, false, e.record(ctx, run)
	}
	results := e.runActions(ctx, &runContext{
		conv: conv, actor: ruleActor(conv.MailboxID, r.ID), ruleID: r.ID, label: "Regel: " + r.Name, messageID: t.MessageID,
	}, actions)
	run.ActionsApplied, run.Error = results.json(), results.errorText()
	return true, r.StopProcessing, e.record(ctx, run)
}

func (e *Engine) record(ctx context.Context, run dbq.AutoInsertRuleRunParams) error {
	if err := e.q.AutoInsertRuleRun(ctx, run); err != nil {
		return fmt.Errorf("record rule run: %w", err)
	}
	return nil
}

func (e *Engine) loadFacts(ctx context.Context, conv dbq.AutoGetConversationRow, messageID pgtype.UUID) (*Facts, error) {
	labelIDs, err := e.q.ListConversationLabelIDs(ctx, conv.ID)
	if err != nil {
		return nil, fmt.Errorf("load labels: %w", err)
	}
	labels := make(map[string]bool, len(labelIDs))
	for _, id := range labelIDs {
		labels[id.String()] = true
	}
	sched, err := e.mailboxSchedule(ctx, conv.MailboxBusinessHoursID)
	if err != nil {
		return nil, err
	}
	f := &Facts{
		ConversationSubject: conv.Subject, MailboxID: conv.MailboxID, Status: conv.Status, Priority: conv.Priority,
		HasAssignee: conv.AssigneeUserID.Valid, HasTeam: conv.AssigneeTeamID.Valid, Organization: conv.OrganizationName,
		Labels: labels, WithinHours: sched.IsOpen, Now: e.d.Now(),
	}
	msg, err := e.q.AutoLatestInbound(ctx, dbq.AutoLatestInboundParams{ConversationID: conv.ID, MessageID: messageID})
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load message: %w", err)
	}
	f.HasMessage, f.FromAddress, f.Body = true, msg.FromAddr, msg.BodyText
	f.HasAttachment, f.AutoSubmitted, f.ReceivedAt = msg.HasAttachment, msg.AutoSubmitted, msg.ReceivedAt.Time
	return f, nil
}

// mailboxSchedule is the schedule of a mailbox, else the workspace default, else calendar time.
func (e *Engine) mailboxSchedule(ctx context.Context, id pgtype.UUID) (*sla.Schedule, error) {
	if id.Valid {
		return e.schedule(ctx, id)
	}
	row, err := e.q.AutoDefaultBusinessHours(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load default business hours: %w", err)
	}
	return scheduleFromRow(row)
}

// ActionResult is what one action did; the list is stored on the rule run.
type ActionResult struct {
	Type   string `json:"type"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

const (
	resultApplied = "applied"
	resultSkipped = "skipped"
	resultFailed  = "failed"
)

type actionResults []ActionResult

func (r actionResults) json() []byte {
	if r == nil {
		return []byte("[]")
	}
	// A slice of plain structs always marshals.
	b, _ := json.Marshal(r)
	return b
}

func (r actionResults) errorText() string {
	msg := ""
	for _, a := range r {
		if a.Result == resultFailed {
			if msg != "" {
				msg += "; "
			}
			msg += a.Type + ": " + a.Detail
		}
	}
	return msg
}

func (e *Engine) inTx(ctx context.Context, fn func(q *dbq.Queries) error) error {
	return pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error { return fn(dbq.New(tx)) })
}
