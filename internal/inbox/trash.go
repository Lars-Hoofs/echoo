package inbox

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/webhooks"
)

// Trash moves conversations to the trash, each in its own transaction like Bulk. A trashed
// conversation keeps its rows but drops out of every list, count, rule and SLA, all of which
// filter on deleted_at; retention or a purge removes it for good.
func (s *Service) Trash(ctx context.Context, actor Actor, ids []pgtype.UUID) ([]BulkResult, error) {
	return s.each(ctx, ids, func(tx pgx.Tx, id pgtype.UUID) (int32, error) {
		q := dbq.New(tx)
		cur, err := q.LockConversationForUpdate(ctx, dbq.LockConversationForUpdateParams{ID: id, MailboxIds: actor.Read})
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("lock conversation: %w", err)
		}
		if !slices.Contains(actor.Write, cur.MailboxID) {
			return 0, ErrForbidden
		}
		version, err := q.TrashMarkDeleted(ctx, dbq.TrashMarkDeletedParams{ID: id, DeletedBy: actor.UserID})
		if err != nil {
			return 0, fmt.Errorf("move to trash: %w", err)
		}
		return version, s.recordTrashChange(ctx, q, actor, id, cur.MailboxID, version, "deleted")
	})
}

// Restore takes conversations out of the trash. Only mailboxes the actor may write count, so a
// conversation in the trash of any other mailbox is not found.
func (s *Service) Restore(ctx context.Context, actor Actor, ids []pgtype.UUID) ([]BulkResult, error) {
	return s.each(ctx, ids, func(tx pgx.Tx, id pgtype.UUID) (int32, error) {
		q := dbq.New(tx)
		cur, err := q.TrashLockDeleted(ctx, dbq.TrashLockDeletedParams{ID: id, MailboxIds: actor.Write})
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("lock conversation: %w", err)
		}
		version, err := q.TrashRestore(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("restore from trash: %w", err)
		}
		return version, s.recordTrashChange(ctx, q, actor, id, cur.MailboxID, version, "restored")
	})
}

func (s *Service) recordTrashChange(ctx context.Context, q *dbq.Queries, actor Actor, id, mailbox pgtype.UUID, version int32, typ string) error {
	if err := insertEvent(ctx, q, id, mailbox, actor, event{typ, map[string]any{}}); err != nil {
		return err
	}
	if err := webhooks.Record(ctx, q, webhooks.Event{Type: webhooks.ConversationUpdated, MailboxID: mailbox, ConversationID: id}); err != nil {
		return err
	}
	return notify(ctx, q, id, mailbox, version)
}

// each runs fn for every distinct id in a transaction of its own and collects the outcomes.
func (s *Service) each(ctx context.Context, ids []pgtype.UUID, fn func(pgx.Tx, pgtype.UUID) (int32, error)) ([]BulkResult, error) {
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
		var version int32
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			version, err = fn(tx, id)
			return err
		})
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		results = append(results, BulkResult{ID: id, Version: version, Err: err})
	}
	return results, nil
}
