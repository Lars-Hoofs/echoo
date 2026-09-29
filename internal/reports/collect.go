package reports

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/automation"
	"echoo/internal/db/dbq"
	"echoo/internal/sla"
)

func keyString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}

func stamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func dateOf(t time.Time) pgtype.Date {
	y, m, d := t.Date()
	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// measure returns the business time between from and to, in seconds, on the schedule hours.
type measure func(hours pgtype.UUID, from, to time.Time) float64

func (s *Service) measurer(ctx context.Context, ids []pgtype.UUID) (measure, error) {
	schedules := map[pgtype.UUID]*sla.Schedule{}
	if len(ids) > 0 {
		rows, err := s.q.ReportBusinessHours(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load business hours: %w", err)
		}
		for _, row := range rows {
			def, err := automation.DefinitionFromRow(row)
			if err != nil {
				return nil, err
			}
			sched, err := sla.New(def)
			if err != nil {
				return nil, fmt.Errorf("business hours %s: %w", row.ID.String(), err)
			}
			schedules[row.ID] = sched
		}
	}
	return func(hours pgtype.UUID, from, to time.Time) float64 {
		return schedules[hours].Elapsed(from, to).Seconds()
	}, nil
}

func distinctHours(a []dbq.ReportFirstResponseRowsRow, b []dbq.ReportResolutionRowsRow) []pgtype.UUID {
	seen := map[pgtype.UUID]bool{}
	var out []pgtype.UUID
	add := func(id pgtype.UUID) {
		if id.Valid && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, r := range a {
		add(r.BusinessHoursID)
	}
	for _, r := range b {
		add(r.BusinessHoursID)
	}
	return out
}

// queryParallelism caps the report queries in flight across all requests, so reports cannot
// take the whole connection pool from the inbox.
const queryParallelism = 5

// slot waits for a free query slot and returns the function that releases it. Only the closures
// that run one query take a slot; the ones that wait for other closures must not, or nested
// waits could exhaust the slots and stall.
func (s *Service) slot() func() {
	s.sem <- struct{}{}
	return func() { <-s.sem }
}

// parallel runs the functions concurrently and returns every error.
func parallel(fns ...func() error) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

var volumeKinds = []string{"new", "customer_messages", "replies", "resolved", "reopened"}

// fact is one row of counts with the attribution the tables group by.
type fact struct {
	day                                   pgtype.Date
	mailbox, team, assignee, actor, label pgtype.UUID
	n                                     int64
}

// key is the group a fact belongs to in a table of dimension dim. Replies count for their
// author and resolutions for whoever resolved; everything else counts for the assignee.
func (f fact) key(dim Dim, kind string) string {
	switch dim {
	case DimAgent:
		if kind == "replies" || kind == "resolved" {
			return keyString(f.actor)
		}
		return keyString(f.assignee)
	case DimTeam:
		return keyString(f.team)
	case DimMailbox:
		return keyString(f.mailbox)
	case DimLabel:
		return keyString(f.label)
	}
	return ""
}

func (s *Service) facts(ctx context.Context, kind string, r Range, split bool, f Filter) ([]fact, error) {
	defer s.slot()()
	rows, err := s.q.ReportFacts(ctx, dbq.ReportFactsParams{
		Tz: r.Loc.String(), Split: split, Kind: kind, RangeFrom: stamp(r.From), RangeTo: stamp(r.To),
		MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
	})
	if err != nil {
		return nil, fmt.Errorf("report %s: %w", kind, err)
	}
	out := make([]fact, len(rows))
	for i, r := range rows {
		out[i] = fact{r.Day, r.MailboxID, r.TeamID, r.AssigneeID, r.ActorID, r.LabelID, r.N}
	}
	return out, nil
}

// collected holds every measurement of one query round, by group key and bucket.
type collected struct {
	rng                    Range
	cells                  map[string]map[string]*cell
	withHours, withoutHour int
}

func (c *collected) cell(key, bucket string) *cell {
	byDay := c.cells[key]
	if byDay == nil {
		byDay = map[string]*cell{}
		c.cells[key] = byDay
	}
	x := byDay[bucket]
	if x == nil {
		x = newCell()
		byDay[bucket] = x
	}
	return x
}

func (c *collected) total(key string) *cell {
	sum := newCell()
	for _, x := range c.cells[key] {
		sum.merge(x)
	}
	return sum
}

func (c *collected) basis() Basis {
	switch {
	case c.withHours > 0 && c.withoutHour > 0:
		return BasisMixed
	case c.withHours > 0:
		return BasisBusinessHours
	}
	return BasisWallClock
}

func (s *Service) collect(ctx context.Context, r Range, f Filter, dim Dim) (*collected, error) {
	out := &collected{rng: r, cells: map[string]map[string]*cell{}}
	if len(f.Mailboxes) == 0 {
		return out, nil
	}
	tz := r.Loc.String()
	// Label tables and label filters need the per-label rows.
	split := dim == DimLabel || f.Label.Valid
	// Only the overview has a chart to draw per day; tables need one total per row.
	perDay := dim == DimNone

	var (
		mu  sync.Mutex
		frt []dbq.ReportFirstResponseRowsRow
		res []dbq.ReportResolutionRowsRow
	)
	add := func(kind string, facts []fact) {
		mu.Lock()
		defer mu.Unlock()
		for _, fc := range facts {
			out.cell(fc.key(dim, kind), r.Bucket(fc.day.Time)).counts[kind] += fc.n
		}
	}
	var fns []func() error
	for _, kind := range volumeKinds {
		fns = append(fns, func() error {
			facts, err := s.facts(ctx, kind, r, split, f)
			if err != nil {
				return err
			}
			add(kind, facts)
			return nil
		})
	}
	fns = append(fns, func() (err error) {
		defer s.slot()()
		frt, err = s.q.ReportFirstResponseRows(ctx, dbq.ReportFirstResponseRowsParams{
			Tz: tz, Dim: string(dim), PerDay: perDay, FirstDay: dateOf(r.From), RangeFrom: stamp(r.From), RangeTo: stamp(r.To),
			MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
		})
		if err != nil {
			return fmt.Errorf("report first response: %w", err)
		}
		return nil
	}, func() (err error) {
		defer s.slot()()
		res, err = s.q.ReportResolutionRows(ctx, dbq.ReportResolutionRowsParams{
			Tz: tz, Dim: string(dim), PerDay: perDay, FirstDay: dateOf(r.From), RangeFrom: stamp(r.From), RangeTo: stamp(r.To),
			MailboxIds: f.Mailboxes, TeamID: f.Team, AgentID: f.Agent, LabelID: f.Label,
		})
		if err != nil {
			return fmt.Errorf("report resolution: %w", err)
		}
		return nil
	})
	if err := parallel(fns...); err != nil {
		return nil, err
	}

	dur, err := s.measurer(ctx, distinctHours(frt, res))
	if err != nil {
		return nil, err
	}
	for _, row := range frt {
		c := out.cell(keyString(row.Key), r.Bucket(row.Day.Time))
		out.durations(&c.frt, dur, row.BusinessHoursID, row.WallSeconds, row.Starts, row.Ends)
		c.frSLA.Total += int(row.SlaTotal)
		c.frSLA.Met += int(row.SlaMet)
	}
	for _, row := range res {
		c := out.cell(keyString(row.Key), r.Bucket(row.Day.Time))
		out.durations(&c.res, dur, row.BusinessHoursID, row.WallSeconds, row.Starts, row.Ends)
		c.resSLA.Total += int(row.SlaTotal)
		c.resSLA.Met += int(row.SlaMet)
	}
	return out, nil
}

// durations appends the durations of one query group to dst: as given when the group has no
// schedule, measured in business time when it has one.
func (c *collected) durations(dst *[]float64, dur measure, hours pgtype.UUID, wall []float64, starts, ends []pgtype.Timestamptz) {
	if !hours.Valid {
		*dst = append(*dst, wall...)
		c.withoutHour += len(wall)
		return
	}
	for i := range starts {
		*dst = append(*dst, dur(hours, starts[i].Time, ends[i].Time))
	}
	c.withHours += len(starts)
}
