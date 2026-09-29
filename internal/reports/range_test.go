package reports

import (
	"reflect"
	"testing"
	"time"
)

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := LoadLocation(DefaultTimezone)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestSummarizeUsesLinearInterpolation(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		want   Stat
	}{
		{"empty", nil, Stat{}},
		{"one", []float64{42}, Stat{Count: 1, Median: 42, P90: 42}},
		{"odd, unsorted", []float64{50, 10, 30, 20, 40}, Stat{Count: 5, Median: 30, P90: 46}},
		{"even", []float64{1, 2, 3, 4}, Stat{Count: 4, Median: 2.5, P90: 3.7}},
		{"ten", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, Stat{Count: 10, Median: 5.5, P90: 9.1}},
	}
	for _, c := range cases {
		got := Summarize(c.values)
		if got.Count != c.want.Count || !near(got.Median, c.want.Median) || !near(got.P90, c.want.P90) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestRatePercent(t *testing.T) {
	if _, ok := (Rate{}).Percent(); ok {
		t.Error("an empty rate has no percentage")
	}
	if p, ok := (Rate{Met: 47, Total: 50}).Percent(); !ok || !near(p, 94) {
		t.Errorf("got %v %v", p, ok)
	}
}

func TestPresetsEndToday(t *testing.T) {
	loc := amsterdam(t)
	now := time.Date(2026, 9, 28, 15, 30, 0, 0, loc)
	cases := []struct {
		preset   Preset
		wantFrom string
		days     int
		group    Group
	}{
		{PresetToday, "2026-09-28", 1, GroupDay},
		{Preset7Days, "2026-09-22", 7, GroupDay},
		{Preset30Days, "2026-08-30", 30, GroupDay},
		{Preset90Days, "2026-07-01", 90, GroupDay},
	}
	for _, c := range cases {
		r, err := ResolveRange(now, loc, c.preset, "", "", "")
		if err != nil {
			t.Fatalf("%s: %v", c.preset, err)
		}
		if got := r.From.Format(time.DateOnly); got != c.wantFrom || r.Days != c.days || r.Group != c.group {
			t.Errorf("%s: from %s, %d days, %s", c.preset, got, r.Days, r.Group)
		}
		if !r.To.Equal(time.Date(2026, 9, 29, 0, 0, 0, 0, loc)) {
			t.Errorf("%s: to %s", c.preset, r.To)
		}
	}
}

func TestRangeAcrossDaylightSavingTime(t *testing.T) {
	loc := amsterdam(t)
	// Clocks went forward on 29 March 2026: that day has 23 hours.
	r, err := ResolveRange(time.Now(), loc, PresetCustom, "2026-03-28", "2026-03-30", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Days != 3 {
		t.Errorf("days: %d", r.Days)
	}
	if want := time.Date(2026, 3, 27, 23, 0, 0, 0, time.UTC); !r.From.Equal(want) {
		t.Errorf("from %s, want %s", r.From.UTC(), want)
	}
	if want := time.Date(2026, 3, 30, 22, 0, 0, 0, time.UTC); !r.To.Equal(want) {
		t.Errorf("to %s, want %s", r.To.UTC(), want)
	}
	if got := r.To.Sub(r.From); got != 71*time.Hour {
		t.Errorf("duration %s, want 71h", got)
	}
	want := []string{"2026-03-28", "2026-03-29", "2026-03-30"}
	if got := r.Buckets(); !reflect.DeepEqual(got, want) {
		t.Errorf("buckets %v", got)
	}
	prev := r.Previous()
	if got := prev.From.Format(time.DateOnly); got != "2026-03-25" || !prev.To.Equal(r.From) {
		t.Errorf("previous %s to %s", got, prev.To)
	}
}

func TestWeekBucketsStartOnMonday(t *testing.T) {
	loc := amsterdam(t)
	// 1 October 2026 is a Thursday.
	r, err := ResolveRange(time.Now(), loc, PresetCustom, "2026-10-01", "2026-10-20", GroupWeek)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-09-28", "2026-10-05", "2026-10-12", "2026-10-19"}
	if got := r.Buckets(); !reflect.DeepEqual(got, want) {
		t.Errorf("buckets %v, want %v", got, want)
	}
}

func TestLongRangesDefaultToWeeks(t *testing.T) {
	loc := amsterdam(t)
	r, err := ResolveRange(time.Now(), loc, PresetCustom, "2026-01-01", "2026-06-30", "")
	if err != nil || r.Group != GroupWeek {
		t.Fatalf("%v %v", r.Group, err)
	}
}

func TestInvalidRanges(t *testing.T) {
	loc := amsterdam(t)
	now := time.Now()
	for name, args := range map[string][4]string{
		"unknown preset": {"yesterday", "", "", ""},
		"missing dates":  {"custom", "", "", ""},
		"bad date":       {"custom", "2026-13-01", "2026-13-02", ""},
		"reversed":       {"custom", "2026-05-02", "2026-05-01", ""},
		"too long":       {"custom", "2025-01-01", "2026-12-31", ""},
		"bad grouping":   {"7d", "", "", "month"},
	} {
		if _, err := ResolveRange(now, loc, Preset(args[0]), args[1], args[2], Group(args[3])); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadLocationRejectsLocalAndUnknown(t *testing.T) {
	for _, name := range []string{"", "Local", "Mars/Olympus"} {
		if _, err := LoadLocation(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}
