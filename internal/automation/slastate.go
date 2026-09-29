package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/jobs"
	"echoo/internal/realtime"
	"echoo/internal/sla"
)

const slaPageSize = 200

func isFinished(status string) bool {
	return status == inbox.StatusClosed || status == inbox.StatusSpam
}

// window is the SLA clock of one conversation right after a policy was applied or restarted.
type window struct {
	started, firstDue, resolutionDue, firstMet pgtype.Timestamptz
	state                                      sla.State
}

func (e *Engine) newWindow(ctx context.Context, conv dbq.AutoGetConversationRow, policy dbq.SlaPolicy, start time.Time, keepMet bool) (window, error) {
	sched, err := e.schedule(ctx, policy.BusinessHoursID)
	if err != nil {
		return window{}, err
	}
	w := window{started: ts(start)}
	var first, res *sla.Target
	if policy.FirstResponseMinutes.Valid {
		minutes := int(policy.FirstResponseMinutes.Int32)
		due, err := sched.Add(start, minutes)
		if err != nil {
			return window{}, fmt.Errorf("first response due: %w", err)
		}
		w.firstDue = ts(due)
		first = &sla.Target{Due: due, Minutes: minutes}
		if keepMet && conv.FirstRespondedAt.Valid {
			w.firstMet = conv.FirstRespondedAt
			first.Met = &conv.FirstRespondedAt.Time
		}
		if isFinished(conv.Status) && first.Met == nil {
			first = nil
		}
	}
	if policy.ResolutionMinutes.Valid && !isFinished(conv.Status) {
		minutes := int(policy.ResolutionMinutes.Int32)
		due, err := sched.Add(start, minutes)
		if err != nil {
			return window{}, fmt.Errorf("resolution due: %w", err)
		}
		w.resolutionDue = ts(due)
		res = &sla.Target{Due: due, Minutes: minutes, Paused: conv.Status == inbox.StatusWaiting}
	}
	w.state = sla.Evaluate(e.d.Now(), sched, int(policy.AtRiskPercent), first, res)
	return w, nil
}

// applyPolicy starts the SLA of a conversation under policy. A first reply that was already
// sent counts as the first response.
func (e *Engine) applyPolicy(ctx context.Context, conv dbq.AutoGetConversationRow, policy dbq.SlaPolicy, start time.Time) error {
	w, err := e.newWindow(ctx, conv, policy, start, true)
	if err != nil {
		return err
	}
	return e.inTx(ctx, func(q *dbq.Queries) error {
		version, err := q.AutoApplySLA(ctx, dbq.AutoApplySLAParams{
			ID: conv.ID, SlaPolicyID: policy.ID, SlaStartedAt: w.started, FirstResponseDueAt: w.firstDue,
			FirstResponseMetAt: w.firstMet, ResolutionDueAt: w.resolutionDue, SlaState: string(w.state),
		})
		if err != nil {
			return fmt.Errorf("apply sla: %w", err)
		}
		return notifyUpdated(ctx, q, conv, version)
	})
}

// restartSLA starts a new window for a conversation that was closed and has been reopened.
func (e *Engine) restartSLA(ctx context.Context, conv dbq.AutoGetConversationRow, policy dbq.SlaPolicy, start time.Time) error {
	w, err := e.newWindow(ctx, conv, policy, start, false)
	if err != nil {
		return err
	}
	return e.inTx(ctx, func(q *dbq.Queries) error {
		version, err := q.AutoRestartSLA(ctx, dbq.AutoRestartSLAParams{
			ID: conv.ID, StartedAt: w.started, FirstResponseDueAt: w.firstDue, ResolutionDueAt: w.resolutionDue, SlaState: string(w.state),
		})
		if err != nil {
			return fmt.Errorf("restart sla: %w", err)
		}
		return notifyUpdated(ctx, q, conv, version)
	})
}

// prepareSLA gives a new conversation the mailbox default policy and restarts the clocks of a
// reopened one, before any rule runs. It returns the conversation as it is afterwards.
func (e *Engine) prepareSLA(ctx context.Context, conv dbq.AutoGetConversationRow, t Trigger) (dbq.AutoGetConversationRow, error) {
	switch {
	case !conv.SlaPolicyID.Valid && t.Name == jobs.TriggerConversationCreated && conv.DefaultSlaPolicyID.Valid:
		policy, err := e.q.AutoGetSLAPolicy(ctx, conv.DefaultSlaPolicyID)
		if err != nil {
			return conv, fmt.Errorf("load default sla policy: %w", err)
		}
		if err := e.applyPolicy(ctx, conv, policy, conv.CreatedAt.Time); err != nil {
			return conv, err
		}
	case conv.SlaPolicyID.Valid && conv.SlaFinalizedAt.Valid && !isFinished(conv.Status):
		policy, err := e.q.AutoGetSLAPolicy(ctx, conv.SlaPolicyID)
		if err != nil {
			return conv, fmt.Errorf("load sla policy: %w", err)
		}
		if err := e.restartSLA(ctx, conv, policy, e.d.Now()); err != nil {
			return conv, err
		}
	default:
		return conv, nil
	}
	fresh, err := e.q.AutoGetConversation(ctx, conv.ID)
	if err != nil {
		return conv, fmt.Errorf("reload conversation: %w", err)
	}
	return fresh, nil
}

func notifyUpdated(ctx context.Context, q *dbq.Queries, conv dbq.AutoGetConversationRow, version int32) error {
	return realtime.Notify(ctx, q, realtime.Event{
		Type: realtime.TypeConversationUpdated, ConversationID: conv.ID.String(), MailboxID: conv.MailboxID.String(), Version: int64(version),
	})
}

type policyClock struct {
	policy dbq.SlaPolicy
	sched  *sla.Schedule
}

// SweepSLA moves every tracked conversation to its current SLA state. It first turns a
// finished stay in status waiting into a later resolution deadline, then evaluates, and
// records an event, a notification and a rule trigger when the state worsens.
func (e *Engine) SweepSLA(ctx context.Context) error {
	policies, err := e.q.AutoListSLAPolicies(ctx)
	if err != nil {
		return fmt.Errorf("list sla policies: %w", err)
	}
	clocks := make(map[pgtype.UUID]policyClock, len(policies))
	for _, p := range policies {
		sched, err := e.schedule(ctx, p.BusinessHoursID)
		if err != nil {
			return err
		}
		clocks[p.ID] = policyClock{policy: p, sched: sched}
	}
	client, err := e.client(ctx)
	if err != nil {
		return err
	}
	after := pgtype.UUID{Valid: true}
	var errs []error
	for {
		page, err := e.q.AutoListSLACandidates(ctx, dbq.AutoListSLACandidatesParams{After: after, PageSize: slaPageSize})
		if err != nil {
			return errors.Join(append(errs, fmt.Errorf("list sla candidates: %w", err))...)
		}
		for _, c := range page {
			clock, ok := clocks[c.SlaPolicyID]
			if !ok {
				continue
			}
			if err := e.sweepOne(ctx, client, clock, c.ID); err != nil {
				errs = append(errs, err)
			}
		}
		if len(page) < slaPageSize {
			return errors.Join(errs...)
		}
		after = page[len(page)-1].ID
	}
}

func (e *Engine) sweepOne(ctx context.Context, client *river.Client[pgx.Tx], clock policyClock, id pgtype.UUID) error {
	return pgx.BeginFunc(ctx, e.d.Pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		c, err := q.AutoLockSLAConversation(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.SlaFinalizedAt.Valid) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock conversation: %w", err)
		}
		now := e.d.Now()
		finished := isFinished(c.Status)
		paused, resumed, due := c.SlaPausedAt, c.SlaResumedAt, c.ResolutionDueAt
		if paused.Valid && resumed.Valid {
			if due.Valid {
				shifted, err := clock.sched.Add(due.Time, int(clock.sched.Elapsed(paused.Time, resumed.Time)/time.Minute))
				if err != nil {
					return fmt.Errorf("shift resolution due: %w", err)
				}
				due = ts(shifted)
			}
			paused, resumed = pgtype.Timestamptz{}, pgtype.Timestamptz{}
		}

		var first, res *sla.Target
		if c.FirstResponseDueAt.Valid && clock.policy.FirstResponseMinutes.Valid {
			first = &sla.Target{Due: c.FirstResponseDueAt.Time, Minutes: int(clock.policy.FirstResponseMinutes.Int32)}
			if c.FirstResponseMetAt.Valid {
				first.Met = &c.FirstResponseMetAt.Time
			} else if finished {
				first = nil
			}
		}
		if due.Valid && clock.policy.ResolutionMinutes.Valid {
			res = &sla.Target{Due: due.Time, Minutes: int(clock.policy.ResolutionMinutes.Int32), Paused: paused.Valid}
			if finished && c.ResolvedAt.Valid {
				res.Met = &c.ResolvedAt.Time
			}
		}
		pct := int(clock.policy.AtRiskPercent)
		state := sla.Evaluate(now, clock.sched, pct, first, res)
		update := dbq.AutoUpdateSLAParams{
			ID: id, SlaState: string(state), ResolutionDueAt: due, SlaPausedAt: paused, SlaResumedAt: resumed,
		}
		if finished {
			update.SlaFinalizedAt = ts(now)
		}
		version, err := q.AutoUpdateSLA(ctx, update)
		if err != nil {
			return fmt.Errorf("update sla: %w", err)
		}
		if string(state) != c.SlaState || !sameTime(due, c.ResolutionDueAt) {
			err := realtime.Notify(ctx, q, realtime.Event{
				Type: realtime.TypeConversationUpdated, ConversationID: c.ID.String(), MailboxID: c.MailboxID.String(), Version: int64(version),
			})
			if err != nil {
				return err
			}
		}
		if string(state) == c.SlaState || state == sla.StateOK {
			return nil
		}
		target := "resolution"
		if first != nil && sla.Evaluate(now, clock.sched, pct, first) == state {
			target = "first_response"
		}
		return e.announce(ctx, tx, client, c, state, target, clock.policy.Name)
	})
}

// announce records the timeline event, notifies the assignee and queues the sla_at_risk or
// sla_breached rules. A state that improves is not announced.
func (e *Engine) announce(ctx context.Context, tx pgx.Tx, client *river.Client[pgx.Tx], c dbq.AutoLockSLAConversationRow, state sla.State, target, policy string) error {
	q := dbq.New(tx)
	eventType, trigger := "sla_at_risk", jobs.TriggerSLAAtRisk
	if state == sla.StateBreached {
		eventType, trigger = "sla_breached", jobs.TriggerSLABreached
	} else if c.SlaState == string(sla.StateBreached) {
		return nil
	}
	data, err := json.Marshal(map[string]string{"source": string(inbox.SourceSystem), "target": target, "policy": policy})
	if err != nil {
		return fmt.Errorf("encode %s event: %w", eventType, err)
	}
	err = q.InsertConversationEvent(ctx, dbq.InsertConversationEventParams{
		ConversationID: c.ID, MailboxID: c.MailboxID, Type: eventType, Data: data,
	})
	if err != nil {
		return fmt.Errorf("insert %s event: %w", eventType, err)
	}
	if c.AssigneeUserID.Valid && !isFinished(c.Status) {
		err := compose.Notify(ctx, q, compose.Notification{UserID: c.AssigneeUserID, Kind: compose.KindSLA, ConversationID: c.ID})
		if err != nil {
			return err
		}
	}
	if _, err := client.InsertTx(ctx, tx, jobs.EvaluateRules{ConversationID: c.ID.String(), Trigger: trigger}, nil); err != nil {
		return fmt.Errorf("queue %s rules: %w", trigger, err)
	}
	return nil
}

func sameTime(a, b pgtype.Timestamptz) bool {
	return a.Valid == b.Valid && a.Time.Equal(b.Time)
}
