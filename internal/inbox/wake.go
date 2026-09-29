package inbox

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

const wakeBatch = 200

// WakeSnoozed clears the snooze of every conversation whose time has passed and returns how
// many woke up. The status stays as it was; the conversation reappears in its normal lists.
func (s *Service) WakeSnoozed(ctx context.Context) (int, error) {
	total := 0
	for {
		var n int
		err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
			rows, err := q.WakeSnoozedConversations(ctx, wakeBatch)
			if err != nil {
				return fmt.Errorf("wake snoozed conversations: %w", err)
			}
			for _, r := range rows {
				if err := notify(ctx, q, r.ID, r.MailboxID, r.Version); err != nil {
					return err
				}
			}
			n = len(rows)
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < wakeBatch {
			return total, nil
		}
	}
}

// WakeWorker runs the periodic inbox.wake_snoozed job.
type WakeWorker struct {
	river.WorkerDefaults[jobs.WakeSnoozed]
	svc *Service
}

func NewWakeWorker(svc *Service) *WakeWorker { return &WakeWorker{svc: svc} }

func (w *WakeWorker) Work(ctx context.Context, _ *river.Job[jobs.WakeSnoozed]) error {
	_, err := w.svc.WakeSnoozed(ctx)
	return err
}
