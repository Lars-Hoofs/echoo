// Package inbox holds the conversation state changes (status, assignment, priority, labels,
// snooze). Each change runs in one transaction that bumps the version, records timeline events
// and emits a content-free NOTIFY, so the API, rules and automation share one set of rules.
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/webhooks"
)

const (
	// EventChannel is the Postgres NOTIFY channel that announces conversation changes.
	EventChannel = "echoo_events"
	// EventConversationUpdated is the payload type for every change made by this package.
	EventConversationUpdated = "conversation.updated"
	// MaxBulk is the largest number of conversations one bulk call accepts.
	MaxBulk = 500
	// maxSnooze bounds how far ahead a conversation can be snoozed.
	maxSnooze = 5 * 365 * 24 * time.Hour
)

const (
	StatusOpen    = "open"
	StatusWaiting = "waiting"
	StatusClosed  = "closed"
	StatusSpam    = "spam"
)

var (
	ErrNotFound         = errors.New("conversation not found")
	ErrForbidden        = errors.New("no write access to the mailbox of this conversation")
	ErrAssigneeNoAccess = errors.New("assignee cannot write to the mailbox")
	ErrTeamNoAccess     = errors.New("team has no access to the mailbox")
	ErrUnknownLabel     = errors.New("unknown label")
	ErrInvalidStatus    = errors.New("invalid status")
	ErrInvalidPriority  = errors.New("invalid priority")
	ErrInvalidSnooze    = errors.New("snooze time must be in the future")
	ErrNotSnoozable     = errors.New("closed and spam conversations cannot be snoozed")
	ErrTooManyIDs       = errors.New("too many conversations")
)

// VersionConflictError reports that the conversation changed since the caller loaded it.
type VersionConflictError struct{ Current int32 }

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict, current version is %d", e.Current)
}

var (
	statuses   = []string{StatusOpen, StatusWaiting, StatusClosed, StatusSpam}
	priorities = []string{"none", "low", "normal", "high", "urgent"}
)

func ValidStatus(s string) bool   { return slices.Contains(statuses, s) }
func ValidPriority(p string) bool { return slices.Contains(priorities, p) }

// Source says who made a change. Changes by a person queue the automation rules for
// conversation updates; changes by a rule or by the system never do, which is the loop guard.
type Source string

const (
	SourceUser   Source = ""
	SourceRule   Source = "rule"
	SourceMacro  Source = "macro"
	SourceSystem Source = "system"
)

// Actor is who makes a change and which mailboxes they may read and write. Read decides
// visibility (out of scope is ErrNotFound), Write decides whether a visible conversation may
// change (ErrForbidden). Automation passes every mailbox in both. A rule or system actor has
// no UserID; its events carry the source and the rule id instead.
type Actor struct {
	UserID pgtype.UUID
	Source Source
	RuleID pgtype.UUID
	Read   []pgtype.UUID
	Write  []pgtype.UUID
}

// Change lists the fields to modify; nil fields stay as they are. For the pointer-to-UUID and
// pointer-to-time fields a non-nil pointer to an invalid value clears the field.
type Change struct {
	Status       *string
	Priority     *string
	Assignee     *pgtype.UUID
	Team         *pgtype.UUID
	SnoozedUntil *pgtype.Timestamptz
	AddLabels    []pgtype.UUID
	RemoveLabels []pgtype.UUID
	// SetLabels replaces the whole label set when non-nil.
	SetLabels *[]pgtype.UUID
}

// Enqueuer inserts a background job in a running transaction; *river.Client[pgx.Tx] is one.
type Enqueuer interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
	jobs Enqueuer
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now}
}

// WithJobs returns a Service that queues the automation rules for conversation updates made
// by people, in the transaction of the change.
func (s *Service) WithJobs(e Enqueuer) *Service {
	c := *s
	c.jobs = e
	return &c
}

// Apply runs ch on one conversation and returns its new version. When expected is non-nil and
// differs from the stored version, nothing changes and a *VersionConflictError is returned. A
// change that alters nothing returns the current version without an event.
func (s *Service) Apply(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, ch Change) (int32, error) {
	var version int32
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		version, err = s.ApplyTx(ctx, tx, actor, id, expected, ch)
		return err
	})
	return version, err
}

// ApplyTx is Apply inside the caller's transaction, for actions that must commit together with
// another change, such as a reply that also moves the conversation to waiting. It queues the
// automation rules like Apply does, so callers must not queue them again.
func (s *Service) ApplyTx(ctx context.Context, tx pgx.Tx, actor Actor, id pgtype.UUID, expected *int32, ch Change) (int32, error) {
	res, err := s.apply(ctx, dbq.New(tx), actor, id, expected, ch)
	if err != nil {
		return 0, err
	}
	if s.jobs != nil && res.relevantToRules && actor.Source == SourceUser {
		args := jobs.EvaluateRules{ConversationID: id.String(), Trigger: jobs.TriggerConversationUpdated}
		if _, err := s.jobs.InsertTx(ctx, tx, args, nil); err != nil {
			return 0, fmt.Errorf("queue rules: %w", err)
		}
	}
	return res.version, nil
}

func (s *Service) SetStatus(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, status string) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{Status: &status})
}

// Assign sets the assignee; an invalid user unassigns.
func (s *Service) Assign(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, user pgtype.UUID) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{Assignee: &user})
}

// AssignTeam sets the team; an invalid team clears it.
func (s *Service) AssignTeam(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, team pgtype.UUID) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{Team: &team})
}

func (s *Service) SetPriority(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, priority string) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{Priority: &priority})
}

func (s *Service) AddLabels(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, labels []pgtype.UUID) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{AddLabels: labels})
}

func (s *Service) RemoveLabels(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, labels []pgtype.UUID) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{RemoveLabels: labels})
}

func (s *Service) Snooze(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32, until time.Time) (int32, error) {
	v := pgtype.Timestamptz{Time: until, Valid: true}
	return s.Apply(ctx, actor, id, expected, Change{SnoozedUntil: &v})
}

func (s *Service) Unsnooze(ctx context.Context, actor Actor, id pgtype.UUID, expected *int32) (int32, error) {
	return s.Apply(ctx, actor, id, expected, Change{SnoozedUntil: &pgtype.Timestamptz{}})
}

// BulkResult is the outcome for one conversation of a bulk call; Err is nil on success.
type BulkResult struct {
	ID      pgtype.UUID
	Version int32
	Err     error
}

// Bulk applies ch to each conversation in its own transaction, so one failure (no access,
// assignee without access) does not undo the others. Duplicate ids are applied once.
func (s *Service) Bulk(ctx context.Context, actor Actor, ids []pgtype.UUID, ch Change) ([]BulkResult, error) {
	if len(ids) > MaxBulk {
		return nil, ErrTooManyIDs
	}
	seen := make(map[pgtype.UUID]bool, len(ids))
	results := make([]BulkResult, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		v, err := s.Apply(ctx, actor, id, nil, ch)
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		results = append(results, BulkResult{ID: id, Version: v, Err: err})
	}
	return results, nil
}

type event struct {
	typ  string
	data map[string]any
}

// outcome is what apply reports besides errors. relevantToRules is set when the change is one
// the conversation_updated trigger covers: status, assignment or labels.
type outcome struct {
	version         int32
	relevantToRules bool
}

func (s *Service) apply(ctx context.Context, q *dbq.Queries, actor Actor, id pgtype.UUID, expected *int32, ch Change) (outcome, error) {
	cur, err := q.LockConversationForUpdate(ctx, dbq.LockConversationForUpdateParams{ID: id, MailboxIds: actor.Read})
	if errors.Is(err, pgx.ErrNoRows) {
		return outcome{}, ErrNotFound
	}
	if err != nil {
		return outcome{}, fmt.Errorf("lock conversation: %w", err)
	}
	if !slices.Contains(actor.Write, cur.MailboxID) {
		return outcome{}, ErrForbidden
	}
	if expected != nil && *expected != cur.Version {
		return outcome{}, &VersionConflictError{Current: cur.Version}
	}

	next := dbq.UpdateConversationStateParams{
		ID: cur.ID, Status: cur.Status, Priority: cur.Priority,
		AssigneeUserID: cur.AssigneeUserID, AssigneeTeamID: cur.AssigneeTeamID,
		SnoozedUntil: cur.SnoozedUntil, ResolvedAt: cur.ResolvedAt,
	}
	var events []event
	var statusChanged, assignChanged bool

	if ch.Status != nil && *ch.Status != cur.Status {
		if !ValidStatus(*ch.Status) {
			return outcome{}, ErrInvalidStatus
		}
		statusChanged = true
		events = append(events, s.statusEvent(&next, cur.Status, *ch.Status))
	}
	if ch.Priority != nil && *ch.Priority != cur.Priority {
		if !ValidPriority(*ch.Priority) {
			return outcome{}, ErrInvalidPriority
		}
		next.Priority = *ch.Priority
		events = append(events, event{"priority_changed", map[string]any{"from": cur.Priority, "to": next.Priority}})
	}
	if ch.Assignee != nil && *ch.Assignee != cur.AssigneeUserID {
		ev, err := assigneeEvent(ctx, q, cur, *ch.Assignee)
		if err != nil {
			return outcome{}, err
		}
		next.AssigneeUserID = *ch.Assignee
		assignChanged = true
		events = append(events, ev)
	}
	if ch.Team != nil && *ch.Team != cur.AssigneeTeamID {
		ev, err := teamEvent(ctx, q, cur, *ch.Team)
		if err != nil {
			return outcome{}, err
		}
		next.AssigneeTeamID = *ch.Team
		assignChanged = true
		events = append(events, ev)
	}
	if ch.SnoozedUntil != nil {
		ev, err := s.snoozeEvent(&next, *ch.SnoozedUntil)
		if err != nil {
			return outcome{}, err
		}
		if ev != nil {
			events = append(events, *ev)
		}
	}
	labelEvents, err := labelChanges(ctx, q, cur.ID, ch)
	if err != nil {
		return outcome{}, err
	}
	events = append(events, labelEvents.events...)

	if len(events) == 0 {
		return outcome{version: cur.Version}, nil
	}
	// Moving to closed or spam ends a snooze silently; the status event already says why.
	if (next.Status == StatusClosed || next.Status == StatusSpam) && next.SnoozedUntil.Valid {
		next.SnoozedUntil = pgtype.Timestamptz{}
	}
	version, err := q.UpdateConversationState(ctx, next)
	if err != nil {
		return outcome{}, fmt.Errorf("update conversation: %w", err)
	}
	if len(labelEvents.add) > 0 {
		if err := q.AddConversationLabels(ctx, dbq.AddConversationLabelsParams{ConversationID: cur.ID, LabelIds: labelEvents.add}); err != nil {
			return outcome{}, fmt.Errorf("add labels: %w", err)
		}
	}
	if len(labelEvents.remove) > 0 {
		if err := q.RemoveConversationLabels(ctx, dbq.RemoveConversationLabelsParams{ConversationID: cur.ID, LabelIds: labelEvents.remove}); err != nil {
			return outcome{}, fmt.Errorf("remove labels: %w", err)
		}
	}
	for _, ev := range events {
		if err := insertEvent(ctx, q, cur.ID, cur.MailboxID, actor, ev); err != nil {
			return outcome{}, err
		}
	}
	if ch.Assignee != nil && ch.Assignee.Valid && *ch.Assignee != cur.AssigneeUserID {
		if err := compose.NotifyAssigned(ctx, q, cur.ID, *ch.Assignee, actor.UserID); err != nil {
			return outcome{}, err
		}
	}
	if err := recordWebhooks(ctx, q, cur.MailboxID, cur.ID, statusChanged, assignChanged); err != nil {
		return outcome{}, err
	}
	if err := notify(ctx, q, cur.ID, cur.MailboxID, version); err != nil {
		return outcome{}, err
	}
	return outcome{version: version, relevantToRules: statusChanged || assignChanged || len(labelEvents.events) > 0}, nil
}

// recordWebhooks writes the outbox events for a change; each is a no-op without a subscriber.
func recordWebhooks(ctx context.Context, q *dbq.Queries, mailbox, conversation pgtype.UUID, statusChanged, assignChanged bool) error {
	types := []string{webhooks.ConversationUpdated}
	if statusChanged {
		types = append(types, webhooks.ConversationStatusChanged)
	}
	if assignChanged {
		types = append(types, webhooks.ConversationAssigned)
	}
	for _, typ := range types {
		if err := webhooks.Record(ctx, q, webhooks.Event{Type: typ, MailboxID: mailbox, ConversationID: conversation}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) statusEvent(next *dbq.UpdateConversationStateParams, from, to string) event {
	next.Status = to
	next.ResolvedAt = pgtype.Timestamptz{}
	switch {
	case to == StatusClosed:
		next.ResolvedAt = pgtype.Timestamptz{Time: s.now(), Valid: true}
		return event{"resolved", map[string]any{"from": from}}
	case from == StatusClosed && to != StatusSpam:
		return event{"reopened", map[string]any{"to": to}}
	}
	return event{"status_changed", map[string]any{"from": from, "to": to}}
}

func assigneeEvent(ctx context.Context, q *dbq.Queries, cur dbq.LockConversationForUpdateRow, to pgtype.UUID) (event, error) {
	if !to.Valid {
		return event{"unassigned", map[string]any{"user_id": cur.AssigneeUserID.String()}}, nil
	}
	ok, err := q.CanAssignMailbox(ctx, dbq.CanAssignMailboxParams{UserID: to, MailboxID: cur.MailboxID})
	if err != nil {
		return event{}, fmt.Errorf("check assignee access: %w", err)
	}
	if !ok {
		return event{}, ErrAssigneeNoAccess
	}
	return event{"assigned", map[string]any{"user_id": to.String()}}, nil
}

func teamEvent(ctx context.Context, q *dbq.Queries, cur dbq.LockConversationForUpdateRow, to pgtype.UUID) (event, error) {
	if !to.Valid {
		return event{"team_assigned", map[string]any{"team_id": nil, "team": ""}}, nil
	}
	ok, err := q.TeamHasMailboxAccess(ctx, dbq.TeamHasMailboxAccessParams{TeamID: to, MailboxID: cur.MailboxID})
	if err != nil {
		return event{}, fmt.Errorf("check team access: %w", err)
	}
	if !ok {
		return event{}, ErrTeamNoAccess
	}
	name, err := q.GetTeamName(ctx, to)
	if err != nil {
		return event{}, fmt.Errorf("load team: %w", err)
	}
	return event{"team_assigned", map[string]any{"team_id": to.String(), "team": name}}, nil
}

// snoozeEvent updates next and returns the event, or nil when the snooze state is unchanged.
func (s *Service) snoozeEvent(next *dbq.UpdateConversationStateParams, until pgtype.Timestamptz) (*event, error) {
	if !until.Valid {
		if !next.SnoozedUntil.Valid {
			return nil, nil
		}
		next.SnoozedUntil = pgtype.Timestamptz{}
		return &event{"woke", map[string]any{"manual": true}}, nil
	}
	now := s.now()
	if !until.Time.After(now) || until.Time.After(now.Add(maxSnooze)) {
		return nil, ErrInvalidSnooze
	}
	if next.Status == StatusClosed || next.Status == StatusSpam {
		return nil, ErrNotSnoozable
	}
	next.SnoozedUntil = pgtype.Timestamptz{Time: until.Time.UTC().Truncate(time.Microsecond), Valid: true}
	return &event{"snoozed", map[string]any{"until": next.SnoozedUntil.Time}}, nil
}

type labelDiff struct {
	add, remove []pgtype.UUID
	events      []event
}

// labelChanges works out which labels really change, so re-adding an existing label is a no-op.
func labelChanges(ctx context.Context, q *dbq.Queries, conversationID pgtype.UUID, ch Change) (labelDiff, error) {
	var d labelDiff
	if ch.SetLabels == nil && len(ch.AddLabels) == 0 && len(ch.RemoveLabels) == 0 {
		return d, nil
	}
	current, err := q.ListConversationLabelIDs(ctx, conversationID)
	if err != nil {
		return d, fmt.Errorf("load labels: %w", err)
	}
	have := make(map[pgtype.UUID]bool, len(current))
	for _, id := range current {
		have[id] = true
	}
	want := make(map[pgtype.UUID]bool, len(current))
	for id := range have {
		want[id] = true
	}
	if ch.SetLabels != nil {
		want = make(map[pgtype.UUID]bool, len(*ch.SetLabels))
		for _, id := range *ch.SetLabels {
			want[id] = true
		}
	}
	for _, id := range ch.AddLabels {
		want[id] = true
	}
	for _, id := range ch.RemoveLabels {
		delete(want, id)
	}
	for id := range want {
		if !have[id] {
			d.add = append(d.add, id)
		}
	}
	for id := range have {
		if !want[id] {
			d.remove = append(d.remove, id)
		}
	}
	if len(d.add) == 0 && len(d.remove) == 0 {
		return d, nil
	}
	rows, err := q.ListLabelsByID(ctx, append(slices.Clone(d.add), d.remove...))
	if err != nil {
		return d, fmt.Errorf("load label names: %w", err)
	}
	names := make(map[pgtype.UUID]string, len(rows))
	for _, r := range rows {
		names[r.ID] = r.Name
	}
	for _, id := range d.add {
		name, ok := names[id]
		if !ok {
			return d, ErrUnknownLabel
		}
		d.events = append(d.events, event{"labeled", map[string]any{"label_id": id.String(), "label": name}})
	}
	for _, id := range d.remove {
		d.events = append(d.events, event{"unlabeled", map[string]any{"label_id": id.String(), "label": names[id]}})
	}
	return d, nil
}

func insertEvent(ctx context.Context, q *dbq.Queries, conversationID, mailboxID pgtype.UUID, actor Actor, ev event) error {
	if actor.Source != SourceUser {
		ev.data["source"] = string(actor.Source)
	}
	if actor.RuleID.Valid {
		ev.data["rule_id"] = actor.RuleID.String()
	}
	data, err := json.Marshal(ev.data)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", ev.typ, err)
	}
	err = q.InsertConversationEvent(ctx, dbq.InsertConversationEventParams{
		ConversationID: conversationID, MailboxID: mailboxID, ActorUserID: actor.UserID, Type: ev.typ, Data: data,
	})
	if err != nil {
		return fmt.Errorf("insert %s event: %w", ev.typ, err)
	}
	return nil
}

// notify emits the change inside the transaction, so listeners only hear about committed
// changes. The payload carries ids and the version, never content.
func notify(ctx context.Context, q *dbq.Queries, conversationID, mailboxID pgtype.UUID, version int32) error {
	payload, err := json.Marshal(map[string]any{
		"type": EventConversationUpdated, "conversation_id": conversationID.String(),
		"mailbox_id": mailboxID.String(), "version": version,
	})
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	if err := q.NotifyEvent(ctx, dbq.NotifyEventParams{Channel: EventChannel, Payload: string(payload)}); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}
