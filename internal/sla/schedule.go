// Package sla computes business time and SLA state. It is pure: no database, no clock.
//
// A schedule is a set of weekly wall-clock ranges in one IANA timezone plus holidays. A range
// belongs to the day it starts; an overnight range (end before start, such as 22:00-06:00)
// continues into the next morning. A holiday removes every range that starts on that date.
// Business minutes are real elapsed minutes inside those windows, so a daylight saving day
// simply has 23 or 25 hours of wall-clock time.
package sla

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrNoBusinessTime is returned when a schedule offers no business time within the search horizon.
var ErrNoBusinessTime = errors.New("schedule has no business time")

// horizonDays bounds how far ahead Add and NextOpen look for an opening.
const horizonDays = 3 * 366

const minutesPerDay = 24 * 60

var weekdayKeys = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Weekdays lists the keys of Definition.Weekly in display order.
var Weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// Range is a wall-clock range as "HH:MM"; End may be "24:00". End before Start is overnight.
type Range struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Date is a calendar date without a timezone.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// ParseDate parses "YYYY-MM-DD".
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("invalid date %q", s)
	}
	return Date{t.Year(), t.Month(), t.Day()}, nil
}

func (d Date) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// Definition is the stored form of a schedule.
type Definition struct {
	Timezone string
	// Weekly maps "mon".."sun" to ranges; a missing or empty day is closed.
	Weekly   map[string][]Range
	Holidays []Date
}

type span struct{ start, end int } // minutes since midnight, end <= 1440

// Schedule answers business-time questions. A nil *Schedule is calendar time (24/7).
type Schedule struct {
	loc *time.Location
	// same[w] are the parts of ranges that start on weekday w and end the same day; spill[w]
	// is the part of overnight ranges that started on the day before w.
	same, spill [7][]span
	holidays    map[Date]bool
}

// New validates def and builds a Schedule.
func New(def Definition) (*Schedule, error) {
	loc, err := time.LoadLocation(def.Timezone)
	if err != nil || def.Timezone == "" || def.Timezone == "Local" {
		return nil, fmt.Errorf("unknown timezone %q", def.Timezone)
	}
	s := &Schedule{loc: loc, holidays: make(map[Date]bool, len(def.Holidays))}
	for key := range def.Weekly {
		if !slices.Contains(Weekdays, key) {
			return nil, fmt.Errorf("unknown weekday %q", key)
		}
	}
	hasRange := false
	for w, key := range weekdayKeys {
		for _, r := range def.Weekly[key] {
			start, err := parseClock(r.Start, false)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			end, err := parseClock(r.End, true)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			switch {
			case end == start:
				return nil, fmt.Errorf("%s: range %s-%s is empty", key, r.Start, r.End)
			case end > start:
				s.same[w] = append(s.same[w], span{start, end})
			default:
				s.same[w] = append(s.same[w], span{start, minutesPerDay})
				s.spill[(w+1)%7] = append(s.spill[(w+1)%7], span{0, end})
			}
			hasRange = true
		}
	}
	if !hasRange {
		return nil, ErrNoBusinessTime
	}
	for _, h := range def.Holidays {
		s.holidays[h] = true
	}
	return s, nil
}

func parseClock(s string, allow24 bool) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(h) != 2 || len(m) != 2 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	total := hh*60 + mm
	if total > minutesPerDay || (total == minutesPerDay && !allow24) {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return total, nil
}

// daySpans returns the business windows of one calendar date in absolute time, sorted and
// merged.
func (s *Schedule) daySpans(y int, m time.Month, d int) [][2]time.Time {
	date := time.Date(y, m, d, 12, 0, 0, 0, s.loc)
	w := int(date.Weekday())
	prev := date.AddDate(0, 0, -1)
	var spans []span
	if !s.holidays[Date{y, m, d}] {
		spans = append(spans, s.same[w]...)
	}
	if !s.holidays[Date{prev.Year(), prev.Month(), prev.Day()}] {
		spans = append(spans, s.spill[w]...)
	}
	if len(spans) == 0 {
		return nil
	}
	slices.SortFunc(spans, func(a, b span) int { return a.start - b.start })
	merged := spans[:1]
	for _, sp := range spans[1:] {
		last := &merged[len(merged)-1]
		if sp.start <= last.end {
			last.end = max(last.end, sp.end)
			continue
		}
		merged = append(merged, sp)
	}
	out := make([][2]time.Time, len(merged))
	for i, sp := range merged {
		out[i] = [2]time.Time{
			time.Date(y, m, d, 0, sp.start, 0, 0, s.loc),
			time.Date(y, m, d, 0, sp.end, 0, 0, s.loc),
		}
	}
	return out
}

// Add returns the instant at which the given number of business minutes have passed after t.
// Counting starts at the next opening when t is outside business hours; zero minutes returns t.
func (s *Schedule) Add(t time.Time, minutes int) (time.Time, error) {
	if minutes < 0 {
		return t, errors.New("negative duration")
	}
	if s == nil || minutes == 0 {
		return t.Add(time.Duration(minutes) * time.Minute), nil
	}
	remaining := time.Duration(minutes) * time.Minute
	local := t.In(s.loc)
	y, m, d := local.Date()
	cursor := t
	for i := 0; i < horizonDays; i++ {
		for _, sp := range s.daySpans(y, m, d) {
			if !sp[1].After(cursor) {
				continue
			}
			from := sp[0]
			if cursor.After(from) {
				from = cursor
			}
			avail := sp[1].Sub(from)
			if remaining <= avail {
				return from.Add(remaining), nil
			}
			remaining -= avail
		}
		y, m, d = nextDay(y, m, d)
	}
	return t, ErrNoBusinessTime
}

// Elapsed returns the business time between from and to; zero when to is not after from.
func (s *Schedule) Elapsed(from, to time.Time) time.Duration {
	if !to.After(from) {
		return 0
	}
	if s == nil {
		return to.Sub(from)
	}
	var total time.Duration
	local := from.In(s.loc)
	y, m, d := local.Date()
	last := to.In(s.loc)
	ly, lm, ld := last.Date()
	for {
		for _, sp := range s.daySpans(y, m, d) {
			lo, hi := sp[0], sp[1]
			if from.After(lo) {
				lo = from
			}
			if to.Before(hi) {
				hi = to
			}
			if hi.After(lo) {
				total += hi.Sub(lo)
			}
		}
		if y == ly && m == lm && d == ld {
			return total
		}
		y, m, d = nextDay(y, m, d)
	}
}

// IsOpen reports whether t is inside business hours.
func (s *Schedule) IsOpen(t time.Time) bool {
	if s == nil {
		return true
	}
	local := t.In(s.loc)
	y, m, d := local.Date()
	for _, sp := range s.daySpans(y, m, d) {
		if !t.Before(sp[0]) && t.Before(sp[1]) {
			return true
		}
	}
	return false
}

func nextDay(y int, m time.Month, d int) (int, time.Month, int) {
	n := time.Date(y, m, d+1, 12, 0, 0, 0, time.UTC)
	return n.Year(), n.Month(), n.Day()
}
