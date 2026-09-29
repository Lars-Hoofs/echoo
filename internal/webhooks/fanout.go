package webhooks

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

const (
	fanoutBatch    = 100
	FanoutInterval = 10 * time.Second
	PurgeInterval  = time.Hour
)

// PeriodicJobs returns the schedule of the fan-out and purge jobs for the River config.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(FanoutInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.WebhookFanout{}, &river.InsertOpts{MaxAttempts: 1}
			}, &river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(PurgeInterval),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.WebhookPurge{}, &river.InsertOpts{MaxAttempts: 1}
			}, nil),
	}
}

type FanoutWorker struct {
	river.WorkerDefaults[jobs.WebhookFanout]
	pool *pgxpool.Pool
}

func NewFanoutWorker(pool *pgxpool.Pool) *FanoutWorker { return &FanoutWorker{pool: pool} }

func (w *FanoutWorker) Work(ctx context.Context, _ *river.Job[jobs.WebhookFanout]) error {
	return w.Run(ctx, river.ClientFromContext[pgx.Tx](ctx))
}

// Run drains the outbox in batches. A batch is one transaction, so an event either produced
// all its deliveries and jobs or none; SKIP LOCKED lets concurrent runs share the work.
func (w *FanoutWorker) Run(ctx context.Context, enq Enqueuer) error {
	for {
		n, err := w.batch(ctx, enq)
		if err != nil {
			return err
		}
		if n < fanoutBatch {
			return nil
		}
	}
}

func (w *FanoutWorker) batch(ctx context.Context, enq Enqueuer) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		events, err := q.ClaimWebhookEvents(ctx, fanoutBatch)
		if err != nil {
			return fmt.Errorf("claim webhook events: %w", err)
		}
		n = len(events)
		for _, ev := range events {
			ids, err := q.ListSubscribedWebhookIDs(ctx, ev.Type)
			if err != nil {
				return fmt.Errorf("list subscribed webhooks: %w", err)
			}
			for _, hookID := range ids {
				del, err := q.InsertWebhookDelivery(ctx, dbq.InsertWebhookDeliveryParams{WebhookID: hookID, EventID: ev.ID, Event: ev.Type})
				if err != nil {
					return fmt.Errorf("insert webhook delivery: %w", err)
				}
				if err := EnqueueDelivery(ctx, enq, tx, del.ID); err != nil {
					return err
				}
			}
			if err := q.MarkWebhookEventFannedOut(ctx, ev.ID); err != nil {
				return fmt.Errorf("mark webhook event: %w", err)
			}
		}
		return nil
	})
	return n, err
}

type PurgeWorker struct {
	river.WorkerDefaults[jobs.WebhookPurge]
	q     *dbq.Queries
	clock func() time.Time
}

func NewPurgeWorker(pool *pgxpool.Pool) *PurgeWorker {
	return &PurgeWorker{q: dbq.New(pool), clock: time.Now}
}

func (w *PurgeWorker) Work(ctx context.Context, _ *river.Job[jobs.WebhookPurge]) error {
	return w.Run(ctx)
}

func (w *PurgeWorker) Run(ctx context.Context) error {
	cutoff := pgtypeTime(w.clock().Add(-Retention))
	deliveries, err := w.q.PurgeWebhookDeliveries(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("purge webhook deliveries: %w", err)
	}
	events, err := w.q.PurgeWebhookEvents(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("purge webhook events: %w", err)
	}
	if deliveries+events > 0 {
		slog.InfoContext(ctx, "webhook logs purged", "deliveries", deliveries, "events", events)
	}
	return nil
}
