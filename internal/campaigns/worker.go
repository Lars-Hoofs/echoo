package campaigns

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"echoo/internal/jobs"
)

// tickTimeout leaves room for resolving the recipients of a large segment.
const tickTimeout = 10 * time.Minute

// Worker runs campaigns.tick jobs.
type Worker struct {
	river.WorkerDefaults[jobs.CampaignTick]
	svc *Service
}

// NewWorker returns the worker for the dispatcher job.
func NewWorker(svc *Service) *Worker { return &Worker{svc: svc} }

func (w *Worker) Timeout(*river.Job[jobs.CampaignTick]) time.Duration { return tickTimeout }

func (w *Worker) Work(ctx context.Context, _ *river.Job[jobs.CampaignTick]) error {
	return w.svc.Tick(ctx)
}

// PeriodicJobs runs the dispatcher every TickInterval.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(TickInterval),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.CampaignTick{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}
