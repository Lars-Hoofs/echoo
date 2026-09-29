package reports

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/db/dbq"
)

// Dim is the dimension a table groups by.
type Dim string

const (
	DimNone    Dim = "none"
	DimAgent   Dim = "agent"
	DimTeam    Dim = "team"
	DimMailbox Dim = "mailbox"
	DimLabel   Dim = "label"
)

// Filter narrows a report. Mailboxes must already be limited to what the caller may read; an
// empty list yields empty results.
type Filter struct {
	Mailboxes          []pgtype.UUID
	Team, Agent, Label pgtype.UUID
}

// Metrics are the numbers of one period, or of one row of a table. Durations are in seconds.
type Metrics struct {
	New              int64
	CustomerMessages int64
	Replies          int64
	Resolved         int64
	Reopened         int64
	FirstResponse    Stat
	Resolution       Stat
	FirstResponseSLA Rate
	ResolutionSLA    Rate
}

// Point is the metrics of one chart bucket; Day is the bucket's first date. Unanswered counts
// the conversations created in the bucket that are open without a first reply (Lifecycle).
type Point struct {
	Day string
	Metrics
	Unanswered int64
}

// StatusCounts splits conversations by the status they have now (spam is counted apart).
type StatusCounts struct {
	Open, Waiting, Closed int64
}

// Total is the number of conversations in all three statuses.
func (c StatusCounts) Total() int64 { return c.Open + c.Waiting + c.Closed }

// Lifecycle follows the conversations created in a period to where they stand now. Answered
// ones have a first reply from us; Answered.Total + Unanswered.Total is the period's
// "nieuwe gesprekken", and Arrived adds the conversations marked as spam.
type Lifecycle struct {
	Spam       int64
	Answered   StatusCounts
	Unanswered StatusCounts
}

// Arrived is every conversation that came in, spam included.
func (l Lifecycle) Arrived() int64 { return l.Spam + l.Answered.Total() + l.Unanswered.Total() }

// Target is the first-response target of a period, in seconds. Count is the number of
// conversations that had one; it is zero when no SLA policy with a first-response target applied.
type Target struct {
	Count   int64
	Seconds float64
}

// Basis says how durations were measured.
type Basis string

const (
	BasisWallClock     Basis = "wall_clock"
	BasisBusinessHours Basis = "business_hours"
	BasisMixed         Basis = "mixed"
)

// Overview is the whole first tab.
type Overview struct {
	Current  Metrics
	Previous Metrics
	Series   []Point
	OpenNow  int64
	Basis    Basis
	CSAT     CSATSummary
	// Lifecycle covers the current period only.
	Lifecycle           Lifecycle
	FirstResponseTarget Target
}

// Row is one line of a per-agent, team, mailbox or label table. ID is empty for conversations
// without that attribute (unassigned, no label).
type Row struct {
	ID   string
	Name string
	Metrics
}

// Service reads report data.
type Service struct {
	pool *pgxpool.Pool
	q    *dbq.Queries
	sem  chan struct{}
}

// New returns a Service on pool.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: dbq.New(pool), sem: make(chan struct{}, queryParallelism)}
}

type cell struct {
	counts   map[string]int64
	frt, res []float64
	frSLA    Rate
	resSLA   Rate
}

func newCell() *cell { return &cell{counts: map[string]int64{}} }

func (c *cell) merge(o *cell) {
	for k, n := range o.counts {
		c.counts[k] += n
	}
	c.frt = append(c.frt, o.frt...)
	c.res = append(c.res, o.res...)
	c.frSLA.Met += o.frSLA.Met
	c.frSLA.Total += o.frSLA.Total
	c.resSLA.Met += o.resSLA.Met
	c.resSLA.Total += o.resSLA.Total
}

func (c *cell) metrics() Metrics {
	return Metrics{
		New: c.counts["new"], CustomerMessages: c.counts["customer_messages"], Replies: c.counts["replies"],
		Resolved: c.counts["resolved"], Reopened: c.counts["reopened"],
		FirstResponse: Summarize(c.frt), Resolution: Summarize(c.res),
		FirstResponseSLA: c.frSLA, ResolutionSLA: c.resSLA,
	}
}

// Overview computes the totals for r and for the period before it, the series per bucket and the
// number of conversations open now.
func (s *Service) Overview(ctx context.Context, r Range, f Filter) (Overview, error) {
	var cur, prev *collected
	var lifecycle lifecycleResult
	var out Overview
	err := parallel(
		func() (err error) { cur, err = s.collect(ctx, r, f, DimNone); return },
		func() (err error) { prev, err = s.collect(ctx, r.Previous(), f, DimNone); return },
		func() (err error) {
			if len(f.Mailboxes) == 0 {
				return nil
			}
			defer s.slot()()
			out.OpenNow, err = s.q.ReportOpenNow(ctx, dbq.ReportOpenNowParams{
				MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
			})
			if err != nil {
				return fmt.Errorf("report open now: %w", err)
			}
			return nil
		},
		func() (err error) { out.CSAT, err = s.csatSummary(ctx, r, f); return },
		func() (err error) { lifecycle, err = s.lifecycle(ctx, r, f); return },
		func() (err error) { out.FirstResponseTarget, err = s.firstResponseTarget(ctx, r, f); return },
	)
	if err != nil {
		return Overview{}, err
	}
	out.Current, out.Previous, out.Basis = cur.total("").metrics(), prev.total("").metrics(), cur.basis()
	out.Lifecycle = lifecycle.total
	for _, day := range r.Buckets() {
		p := Point{Day: day, Unanswered: lifecycle.unanswered[day]}
		if c := cur.cells[""][day]; c != nil {
			p.Metrics = c.metrics()
		}
		out.Series = append(out.Series, p)
	}
	return out, nil
}

type lifecycleResult struct {
	total Lifecycle
	// unanswered is Unanswered.Open per bucket.
	unanswered map[string]int64
}

func (s *Service) lifecycle(ctx context.Context, r Range, f Filter) (lifecycleResult, error) {
	out := lifecycleResult{unanswered: map[string]int64{}}
	if len(f.Mailboxes) == 0 {
		return out, nil
	}
	defer s.slot()()
	rows, err := s.q.ReportLifecycle(ctx, dbq.ReportLifecycleParams{
		Tz: r.Loc.String(), RangeFrom: stamp(r.From), RangeTo: stamp(r.To),
		MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
	})
	if err != nil {
		return lifecycleResult{}, fmt.Errorf("report lifecycle: %w", err)
	}
	for _, row := range rows {
		t := &out.total
		t.Spam += row.Spam
		t.Answered.Open += row.AnsweredOpen
		t.Answered.Waiting += row.AnsweredWaiting
		t.Answered.Closed += row.AnsweredClosed
		t.Unanswered.Open += row.UnansweredOpen
		t.Unanswered.Waiting += row.UnansweredWaiting
		t.Unanswered.Closed += row.UnansweredClosed
		out.unanswered[r.Bucket(row.Day.Time)] += row.UnansweredOpen
	}
	return out, nil
}

func (s *Service) firstResponseTarget(ctx context.Context, r Range, f Filter) (Target, error) {
	if len(f.Mailboxes) == 0 {
		return Target{}, nil
	}
	defer s.slot()()
	row, err := s.q.ReportFirstResponseTarget(ctx, dbq.ReportFirstResponseTargetParams{
		RangeFrom: stamp(r.From), RangeTo: stamp(r.To), MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
	})
	if err != nil {
		return Target{}, fmt.Errorf("report first response target: %w", err)
	}
	return Target{Count: row.N, Seconds: row.MedianSeconds}, nil
}

// Groups computes one table row per value of dim. keep, when set, drops every key it rejects
// before names are loaded; the caller uses it to show an agent only their own row.
func (s *Service) Groups(ctx context.Context, r Range, f Filter, dim Dim, keep func(key string) bool) ([]Row, Basis, error) {
	c, err := s.collect(ctx, r, f, dim)
	if err != nil {
		return nil, "", err
	}
	var rows []Row
	var ids []pgtype.UUID
	for key := range c.cells {
		if keep != nil && !keep(key) {
			continue
		}
		rows = append(rows, Row{ID: key, Metrics: c.total(key).metrics()})
		if key != "" {
			var id pgtype.UUID
			if err := id.Scan(key); err != nil {
				return nil, "", fmt.Errorf("group key %q: %w", key, err)
			}
			ids = append(ids, id)
		}
	}
	names, err := s.names(ctx, dim, ids)
	if err != nil {
		return nil, "", err
	}
	for i := range rows {
		rows[i].Name = names[rows[i].ID]
	}
	slices.SortFunc(rows, func(a, b Row) int {
		return cmp.Or(cmp.Compare(b.New+b.Resolved+b.Replies, a.New+a.Resolved+a.Replies), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return rows, c.basis(), nil
}

func (s *Service) names(ctx context.Context, dim Dim, ids []pgtype.UUID) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.q.ReportNames(ctx, dbq.ReportNamesParams{Dim: string(dim), Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("load %s names: %w", dim, err)
	}
	for _, row := range rows {
		out[keyString(row.ID)] = row.Name
	}
	return out, nil
}
