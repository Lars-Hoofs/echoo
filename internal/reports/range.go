// Package reports computes the numbers behind the Rapportage pages. docs/reports.md defines
// every metric; the definitions here and there must stay in step.
package reports

import (
	"errors"
	"fmt"
	"time"
)

// DefaultTimezone buckets days for workspaces that never chose one.
const DefaultTimezone = "Europe/Amsterdam"

// maxRangeDays keeps a custom range within what the queries and charts handle well.
const maxRangeDays = 366

// Preset names a period selector value.
type Preset string

const (
	PresetToday  Preset = "today"
	Preset7Days  Preset = "7d"
	Preset30Days Preset = "30d"
	Preset90Days Preset = "90d"
	PresetCustom Preset = "custom"
)

// Group is the width of one chart bucket.
type Group string

const (
	GroupDay  Group = "day"
	GroupWeek Group = "week"
)

// Range is a half-open time range aligned to whole local days.
type Range struct {
	From, To time.Time
	Loc      *time.Location
	Group    Group
	// Days is the number of local calendar days in the range.
	Days int
}

// LoadLocation resolves a stored timezone name.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("unknown timezone %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q", name)
	}
	return loc, nil
}

// ErrInvalidRange is returned for a period the caller cannot use.
var ErrInvalidRange = errors.New("invalid report period")

func startOfDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func addDays(t time.Time, days int) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+days, 0, 0, 0, 0, t.Location())
}

// ResolveRange turns a preset, or a custom first and last date (both inclusive, YYYY-MM-DD),
// into a range of whole days in loc. The presets end with today. group may be empty, which picks
// days up to about a quarter and weeks beyond that.
func ResolveRange(now time.Time, loc *time.Location, preset Preset, from, to string, group Group) (Range, error) {
	today := startOfDay(now, loc)
	var first time.Time
	var days int
	switch preset {
	case PresetToday:
		first, days = today, 1
	case Preset7Days:
		first, days = addDays(today, -6), 7
	case Preset30Days:
		first, days = addDays(today, -29), 30
	case Preset90Days:
		first, days = addDays(today, -89), 90
	case PresetCustom:
		f, err := time.ParseInLocation(time.DateOnly, from, loc)
		if err != nil {
			return Range{}, fmt.Errorf("%w: from must be YYYY-MM-DD", ErrInvalidRange)
		}
		t, err := time.ParseInLocation(time.DateOnly, to, loc)
		if err != nil {
			return Range{}, fmt.Errorf("%w: to must be YYYY-MM-DD", ErrInvalidRange)
		}
		if t.Before(f) {
			return Range{}, fmt.Errorf("%w: to is before from", ErrInvalidRange)
		}
		first = f
		days = calendarDays(f, t) + 1
		if days > maxRangeDays {
			return Range{}, fmt.Errorf("%w: at most %d days", ErrInvalidRange, maxRangeDays)
		}
	default:
		return Range{}, fmt.Errorf("%w: unknown period %q", ErrInvalidRange, preset)
	}
	switch group {
	case "":
		group = GroupDay
		if days > 92 {
			group = GroupWeek
		}
	case GroupDay, GroupWeek:
	default:
		return Range{}, fmt.Errorf("%w: unknown grouping %q", ErrInvalidRange, group)
	}
	return Range{From: first, To: addDays(first, days), Loc: loc, Group: group, Days: days}, nil
}

// calendarDays counts the days between two local midnights; a day with a clock change is one day.
func calendarDays(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return int(time.Date(by, bm, bd, 12, 0, 0, 0, time.UTC).Sub(time.Date(ay, am, ad, 12, 0, 0, 0, time.UTC)).Hours() / 24)
}

// Previous is the range of the same number of days directly before r.
func (r Range) Previous() Range {
	return Range{From: addDays(r.From, -r.Days), To: r.From, Loc: r.Loc, Group: r.Group, Days: r.Days}
}

// Buckets lists the start date of every chart bucket in r, oldest first. Weeks start on Monday,
// like date_trunc('week') in the queries, so the first bucket can begin before r.From.
func (r Range) Buckets() []string {
	var out []string
	day := r.From
	if r.Group == GroupWeek {
		day = addDays(day, -((int(day.Weekday()) + 6) % 7))
	}
	step := 1
	if r.Group == GroupWeek {
		step = 7
	}
	for day.Before(r.To) {
		out = append(out, day.Format(time.DateOnly))
		day = addDays(day, step)
	}
	return out
}

// Last is the final day of r, for display.
func (r Range) Last() time.Time { return addDays(r.To, -1) }

// Bucket returns the first day, as YYYY-MM-DD, of the chart bucket that holds day. day is a
// calendar date (the time of day and location are ignored).
func (r Range) Bucket(day time.Time) string {
	y, m, d := day.Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if r.Group == GroupWeek {
		t = t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7))
	}
	return t.Format(time.DateOnly)
}
