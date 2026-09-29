package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
)

const (
	settingsKey  = "automation"
	idleBatch    = 100
	resolveBatch = 100
	// idleGrace bounds how long after its threshold a silent conversation is still picked up.
	// Rule runs are purged after 30 days; without a bound a very old waiting conversation
	// would fire its rule again once its run was purged.
	idleGrace = 7 * 24 * time.Hour
)

// Settings are the workspace-wide automation settings.
type Settings struct {
	// AutoResolveDays closes conversations that wait this many days for the customer; 0 is off.
	AutoResolveDays int `json:"auto_resolve_days"`
}

// LoadSettings reads the settings; nothing stored means everything off.
func LoadSettings(ctx context.Context, q *dbq.Queries) (Settings, error) {
	var s Settings
	raw, err := q.GetSetting(ctx, settingsKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("load automation settings: %w", err)
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("decode automation settings: %w", err)
	}
	return s, nil
}

// Tick runs the periodic checks one after the other and reports every failure. Only one tick
// runs at a time across instances.
func (e *Engine) Tick(ctx context.Context) error {
	var errs []error
	acquired, err := e.withLock(ctx, lockTick, false, func() error {
		for _, step := range []func(context.Context) error{e.SweepSLA, e.RunIdleRules, e.RetryAssignments, e.AutoResolve} {
			if err := step(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	if !acquired && err == nil {
		e.d.Logger.DebugContext(ctx, "automation tick skipped: another one is running")
	}
	return errors.Join(errs...)
}

// RunIdleRules fires the customer_idle rules for conversations that have waited long enough
// for the customer. A rule looks at a conversation once per silence: its run is recorded
// whether or not the conditions matched, and only a newer message starts a new silence.
func (e *Engine) RunIdleRules(ctx context.Context) error {
	rules, err := e.q.AutoListIdleRules(ctx)
	if err != nil {
		return fmt.Errorf("list idle rules: %w", err)
	}
	stopped := map[pgtype.UUID]bool{}
	var errs []error
	for _, r := range rules {
		idleFor := time.Duration(r.IdleHours.Int32) * time.Hour
		now := e.d.Now()
		ids, err := e.q.AutoListIdleConversations(ctx, dbq.AutoListIdleConversationsParams{
			MailboxID: r.MailboxID, RuleID: r.ID, IdleBefore: ts(now.Add(-idleFor)),
			NotOlderThan: ts(now.Add(-idleFor - idleGrace)), PageSize: idleBatch,
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("list idle conversations: %w", err))
			continue
		}
		for _, id := range ids {
			if stopped[id] {
				continue
			}
			matched, stop, err := e.runRule(ctx, r, Trigger{ConversationID: id, Name: TriggerCustomerIdle})
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if matched && stop {
				stopped[id] = true
			}
		}
	}
	return errors.Join(errs...)
}

// AutoResolve closes conversations that have waited for the customer longer than the
// workspace setting allows. The close is a system change, so no rules run for it.
func (e *Engine) AutoResolve(ctx context.Context) error {
	settings, err := LoadSettings(ctx, e.q)
	if err != nil {
		return err
	}
	if settings.AutoResolveDays <= 0 {
		return nil
	}
	cutoff := e.d.Now().Add(-time.Duration(settings.AutoResolveDays) * 24 * time.Hour)
	ids, err := e.q.AutoListAutoResolvable(ctx, dbq.AutoListAutoResolvableParams{Cutoff: ts(cutoff), PageSize: resolveBatch})
	if err != nil {
		return fmt.Errorf("list auto-resolvable conversations: %w", err)
	}
	var errs []error
	for _, id := range ids {
		conv, err := e.q.AutoGetConversation(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("load conversation: %w", err))
			continue
		}
		if _, err := e.inbox.SetStatus(ctx, systemActor(conv.MailboxID), id, nil, inbox.StatusClosed); err != nil {
			errs = append(errs, fmt.Errorf("auto-resolve: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Purge deletes rule runs older than 30 days and expired auto-reply claims.
func (e *Engine) Purge(ctx context.Context) error {
	if _, err := e.q.AutoPurgeRuleRuns(ctx); err != nil {
		return fmt.Errorf("purge rule runs: %w", err)
	}
	if _, err := e.q.AutoPurgeReplies(ctx); err != nil {
		return fmt.Errorf("purge auto replies: %w", err)
	}
	return nil
}
