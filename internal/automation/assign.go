package automation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
)

const (
	modeOff        = "off"
	modeBalanced   = "balanced"
	retryBatchSize = 50

	// Advisory lock keys; the numbers only have to differ from each other.
	lockAssign = 0x4543484f01
	lockTick   = 0x4543484f02
)

// assignBest picks an available agent under a global lock, so two evaluations at the same
// time cannot both fill the last free place of one agent, and assigns the conversation. It
// reports false when nobody is available.
func (e *Engine) assignBest(ctx context.Context, actor inbox.Actor, conv, mailbox, team pgtype.UUID, balanced bool) (bool, error) {
	var assigned bool
	_, err := e.withLock(ctx, lockAssign, true, func() error {
		agent, err := e.q.AutoPickAssignee(ctx, dbq.AutoPickAssigneeParams{MailboxID: mailbox, TeamID: team, Balanced: balanced})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("pick assignee: %w", err)
		}
		if _, err := e.inbox.Assign(ctx, actor, conv, nil, agent); err != nil {
			return fmt.Errorf("assign: %w", err)
		}
		if err := e.q.AutoMarkAssigned(ctx, agent); err != nil {
			return fmt.Errorf("mark assigned: %w", err)
		}
		assigned = true
		return nil
	})
	return assigned, err
}

// withLock runs fn while holding a Postgres advisory lock on a dedicated connection. With
// wait false it returns immediately (acquired false) when another instance holds the lock.
func (e *Engine) withLock(ctx context.Context, key int64, wait bool, fn func() error) (acquired bool, err error) {
	conn, err := e.d.Pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire connection: %w", err)
	}
	if wait {
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
			conn.Release()
			return false, fmt.Errorf("advisory lock: %w", err)
		}
	} else {
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
			conn.Release()
			return false, fmt.Errorf("advisory lock: %w", err)
		}
		if !acquired {
			conn.Release()
			return false, nil
		}
	}
	err = fn()
	// A session lock outlives the query, so a failed unlock must not return the connection to
	// the pool: closing it releases the lock.
	if _, uerr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, key); uerr != nil {
		_ = conn.Hijack().Close(context.WithoutCancel(ctx))
		return true, errors.Join(err, fmt.Errorf("advisory unlock: %w", uerr))
	}
	conn.Release()
	return true, err
}

// autoAssignNew assigns a new, still unassigned conversation according to its mailbox mode.
// A team set by a rule limits the choice to that team.
func (e *Engine) autoAssignNew(ctx context.Context, id pgtype.UUID) error {
	conv, err := e.q.AutoGetConversation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load conversation: %w", err)
	}
	if conv.AssigneeUserID.Valid || conv.Status != inbox.StatusOpen || conv.AutoAssignMode == modeOff {
		return nil
	}
	_, err = e.assignBest(ctx, systemActor(conv.MailboxID), id, conv.MailboxID, conv.AssigneeTeamID, conv.AutoAssignMode == modeBalanced)
	return err
}

// RetryAssignments gives conversations that could not be assigned earlier, because nobody
// was online or everybody was at capacity, another chance.
func (e *Engine) RetryAssignments(ctx context.Context) error {
	ids, err := e.q.AutoListUnassignedForRetry(ctx, retryBatchSize)
	if err != nil {
		return fmt.Errorf("list unassigned conversations: %w", err)
	}
	var errs []error
	for _, id := range ids {
		if err := e.autoAssignNew(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
