package automation

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/riverqueue/river"

	"echoo/internal/jobs"
)

// EvaluateWorker runs rules.evaluate jobs.
type EvaluateWorker struct {
	river.WorkerDefaults[jobs.EvaluateRules]
	e *Engine
}

func (w *EvaluateWorker) Work(ctx context.Context, job *river.Job[jobs.EvaluateRules]) error {
	conv, ok := parseID(job.Args.ConversationID)
	if !ok || !slices.Contains(Triggers, job.Args.Trigger) || job.Args.Trigger == TriggerCustomerIdle {
		return river.JobCancel(fmt.Errorf("invalid rules job %+v", job.Args))
	}
	t := Trigger{ConversationID: conv, Name: job.Args.Trigger}
	if job.Args.MessageID != "" {
		if t.MessageID, ok = parseID(job.Args.MessageID); !ok {
			return river.JobCancel(fmt.Errorf("invalid message id %q", job.Args.MessageID))
		}
	}
	return w.e.Evaluate(ctx, t)
}

type tickWorker struct {
	river.WorkerDefaults[jobs.AutomationTick]
	e *Engine
}

func (w *tickWorker) Work(ctx context.Context, _ *river.Job[jobs.AutomationTick]) error {
	return w.e.Tick(ctx)
}

type purgeWorker struct {
	river.WorkerDefaults[jobs.AutomationPurge]
	e *Engine
}

func (w *purgeWorker) Work(ctx context.Context, _ *river.Job[jobs.AutomationPurge]) error {
	return w.e.Purge(ctx)
}

// Register adds the automation workers to a River worker set.
func Register(workers *river.Workers, e *Engine) {
	river.AddWorker(workers, &EvaluateWorker{e: e})
	river.AddWorker(workers, &tickWorker{e: e})
	river.AddWorker(workers, &purgeWorker{e: e})
}

// PeriodicJobs schedules the SLA sweep and the other minute-by-minute checks, and the purge.
func PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.AutomationTick{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.AutomationPurge{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}
