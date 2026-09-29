package sla

import (
	"errors"
	"testing"
	"time"
)

var ams = mustLoc("Europe/Amsterdam")

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at parses "2006-01-02 15:04" as Amsterdam wall-clock time.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, ams)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func hours(start, end string, days ...string) map[string][]Range {
	m := map[string][]Range{}
	for _, d := range days {
		m[d] = []Range{{start, end}}
	}
	return m
}

func date(t *testing.T, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func schedule(t *testing.T, weekly map[string][]Range, holidays ...string) *Schedule {
	t.Helper()
	def := Definition{Timezone: "Europe/Amsterdam", Weekly: weekly}
	for _, h := range holidays {
		def.Holidays = append(def.Holidays, date(t, h))
	}
	s, err := New(def)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var workdays = []string{"mon", "tue", "wed", "thu", "fri"}

func TestAdd(t *testing.T) {
	office := schedule(t, hours("09:00", "17:00", workdays...))
	officeHoliday := schedule(t, hours("09:00", "17:00", workdays...), "2026-03-03")
	mwf := schedule(t, hours("09:00", "17:00", "mon", "wed", "fri"))
	split := schedule(t, map[string][]Range{"mon": {{"09:00", "12:00"}, {"13:00", "17:00"}}})
	overlap := schedule(t, map[string][]Range{"mon": {{"09:00", "13:00"}, {"12:00", "17:00"}}})
	nights := schedule(t, hours("22:00", "06:00", "mon", "tue", "wed", "thu", "fri", "sat", "sun"))
	nightsHoliday := schedule(t, hours("22:00", "06:00", "mon", "tue", "wed", "thu", "fri", "sat", "sun"), "2026-03-03")
	satNight := schedule(t, hours("22:00", "06:00", "sat"))
	allDay := schedule(t, hours("00:00", "24:00", "mon", "tue", "wed", "thu", "fri", "sat", "sun"))
	sunEarly := schedule(t, hours("01:00", "05:00", "sun"))

	tests := []struct {
		name    string
		s       *Schedule
		from    string
		minutes int
		want    string
	}{
		{"within a day", office, "2026-03-02 10:00", 60, "2026-03-02 11:00"},
		{"crosses closing time", office, "2026-03-02 16:30", 60, "2026-03-03 09:30"},
		{"before opening", office, "2026-03-02 07:00", 30, "2026-03-02 09:30"},
		{"after closing on friday", office, "2026-03-06 18:00", 30, "2026-03-09 09:30"},
		{"saturday", office, "2026-03-07 12:00", 1, "2026-03-09 09:01"},
		{"ends exactly at closing time", office, "2026-03-02 16:00", 60, "2026-03-02 17:00"},
		{"zero minutes keeps the instant", office, "2026-03-07 12:00", 0, "2026-03-07 12:00"},
		{"three full days", office, "2026-03-02 09:00", 3 * 480, "2026-03-04 17:00"},
		{"across a week", office, "2026-03-06 16:00", 2 * 60, "2026-03-09 10:00"},
		{"holiday is skipped", officeHoliday, "2026-03-02 16:00", 120, "2026-03-04 10:00"},
		{"start on a holiday", officeHoliday, "2026-03-03 10:00", 30, "2026-03-04 09:30"},
		{"days without hours are skipped", mwf, "2026-03-02 16:00", 120, "2026-03-04 10:00"},
		{"lunch break", split, "2026-03-02 11:30", 60, "2026-03-02 13:30"},
		{"overlapping ranges merge", overlap, "2026-03-02 12:30", 60, "2026-03-02 13:30"},
		{"overnight range", nights, "2026-03-02 23:00", 120, "2026-03-03 01:00"},
		{"overnight from the morning spill", nights, "2026-03-02 05:30", 60, "2026-03-02 22:30"},
		{"overnight starting on a holiday is dropped", nightsHoliday, "2026-03-03 21:00", 120, "2026-03-05 00:00"},
		{"overnight spilling into a holiday is kept", nightsHoliday, "2026-03-02 23:00", 420, "2026-03-03 06:00"},
		{"overnight after a spill into a holiday resumes two days later", nightsHoliday, "2026-03-02 23:00", 480, "2026-03-04 23:00"},
		{"spring forward night has seven hours", satNight, "2026-03-28 22:00", 420, "2026-03-29 06:00"},
		{"spring forward night overflows to next saturday", satNight, "2026-03-28 22:00", 421, "2026-04-04 22:01"},
		{"fall back night has nine hours", satNight, "2026-10-24 22:00", 540, "2026-10-25 06:00"},
		{"fall back night overflows to next saturday", satNight, "2026-10-24 22:00", 541, "2026-10-31 22:01"},
		{"office hours across spring forward", office, "2026-03-27 16:00", 120, "2026-03-30 10:00"},
		{"office hours across fall back", office, "2026-10-23 16:00", 120, "2026-10-26 10:00"},
		{"all day across spring forward loses an hour", allDay, "2026-03-29 00:00", 24 * 60, "2026-03-30 01:00"},
		{"all day across fall back gains an hour", allDay, "2026-10-25 00:00", 24 * 60, "2026-10-25 23:00"},
		{"range around the skipped hour", sunEarly, "2026-03-29 01:00", 180, "2026-03-29 05:00"},
		{"range around the repeated hour", sunEarly, "2026-10-25 01:00", 240, "2026-10-25 04:00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.s.Add(at(t, tc.from), tc.minutes)
			if err != nil {
				t.Fatal(err)
			}
			if want := at(t, tc.want); !got.Equal(want) {
				t.Errorf("Add(%s, %d) = %s, want %s", tc.from, tc.minutes, got.In(ams), want)
			}
		})
	}
}

func TestAddAcceptsAnyLocation(t *testing.T) {
	office := schedule(t, hours("09:00", "17:00", workdays...))
	from := time.Date(2026, 3, 2, 8, 30, 0, 0, time.UTC) // 09:30 in Amsterdam
	got, err := office.Add(from, 30)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestAddErrors(t *testing.T) {
	office := schedule(t, hours("09:00", "17:00", workdays...))
	if _, err := office.Add(time.Now(), -1); err == nil {
		t.Error("negative minutes must fail")
	}
}

func TestNilScheduleIsCalendarTime(t *testing.T) {
	var s *Schedule
	from := at(t, "2026-03-07 12:00")
	got, err := s.Add(from, 90)
	if err != nil || !got.Equal(from.Add(90*time.Minute)) {
		t.Errorf("Add = %s, %v", got, err)
	}
	if e := s.Elapsed(from, from.Add(3*time.Hour)); e != 3*time.Hour {
		t.Errorf("Elapsed = %s", e)
	}
	if !s.IsOpen(from) {
		t.Error("a nil schedule is always open")
	}
}

func TestElapsed(t *testing.T) {
	office := schedule(t, hours("09:00", "17:00", workdays...))
	officeHoliday := schedule(t, hours("09:00", "17:00", workdays...), "2026-03-03")
	nights := schedule(t, hours("22:00", "06:00", "mon", "tue", "wed", "thu", "fri", "sat", "sun"))
	satNight := schedule(t, hours("22:00", "06:00", "sat"))
	allDay := schedule(t, hours("00:00", "24:00", "mon", "tue", "wed", "thu", "fri", "sat", "sun"))
	sunEarly := schedule(t, hours("01:00", "05:00", "sun"))

	tests := []struct {
		name     string
		s        *Schedule
		from, to string
		want     time.Duration
	}{
		{"within business hours", office, "2026-03-02 10:00", "2026-03-02 12:30", 150 * time.Minute},
		{"a full day", office, "2026-03-02 00:00", "2026-03-03 00:00", 8 * time.Hour},
		{"across closing time", office, "2026-03-02 16:00", "2026-03-03 10:00", 2 * time.Hour},
		{"over a weekend", office, "2026-03-06 16:00", "2026-03-09 10:00", 2 * time.Hour},
		{"entirely outside hours", office, "2026-03-02 18:00", "2026-03-03 08:00", 0},
		{"over a holiday", officeHoliday, "2026-03-02 16:00", "2026-03-04 10:00", 2 * time.Hour},
		{"to before from", office, "2026-03-02 12:00", "2026-03-02 10:00", 0},
		{"equal instants", office, "2026-03-02 12:00", "2026-03-02 12:00", 0},
		{"a working week", office, "2026-03-02 00:00", "2026-03-09 00:00", 40 * time.Hour},
		{"overnight range", nights, "2026-03-02 22:00", "2026-03-03 06:00", 8 * time.Hour},
		{"morning spill only", nights, "2026-03-03 00:00", "2026-03-03 12:00", 6 * time.Hour},
		{"spring forward night", satNight, "2026-03-28 22:00", "2026-03-29 06:00", 7 * time.Hour},
		{"fall back night", satNight, "2026-10-24 22:00", "2026-10-25 06:00", 9 * time.Hour},
		{"spring forward day", allDay, "2026-03-29 00:00", "2026-03-30 00:00", 23 * time.Hour},
		{"fall back day", allDay, "2026-10-25 00:00", "2026-10-26 00:00", 25 * time.Hour},
		{"range around the skipped hour", sunEarly, "2026-03-29 00:00", "2026-03-29 12:00", 3 * time.Hour},
		{"range around the repeated hour", sunEarly, "2026-10-25 00:00", "2026-10-25 12:00", 5 * time.Hour},
		{"office week across spring forward", office, "2026-03-27 09:00", "2026-03-30 17:00", 16 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Elapsed(at(t, tc.from), at(t, tc.to)); got != tc.want {
				t.Errorf("Elapsed(%s, %s) = %s, want %s", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestAddAndElapsedAgree(t *testing.T) {
	s := schedule(t, map[string][]Range{
		"mon": {{"09:00", "12:00"}, {"13:00", "17:30"}},
		"tue": {{"22:00", "04:00"}},
		"fri": {{"08:00", "16:00"}},
	}, "2026-03-27")
	start := at(t, "2026-03-25 07:13")
	for _, minutes := range []int{1, 59, 60, 61, 600, 4000, 20000} {
		end, err := s.Add(start, minutes)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Elapsed(start, end); got != time.Duration(minutes)*time.Minute {
			t.Errorf("Elapsed(Add(%d)) = %s", minutes, got)
		}
	}
}

func TestIsOpen(t *testing.T) {
	office := schedule(t, hours("09:00", "17:00", workdays...), "2026-03-03")
	nights := schedule(t, hours("22:00", "06:00", "mon"))
	tests := []struct {
		name string
		s    *Schedule
		at   string
		want bool
	}{
		{"opening minute", office, "2026-03-02 09:00", true},
		{"just before opening", office, "2026-03-02 08:59", false},
		{"closing minute", office, "2026-03-02 17:00", false},
		{"weekend", office, "2026-03-07 10:00", false},
		{"holiday", office, "2026-03-03 10:00", false},
		{"overnight evening", nights, "2026-03-02 23:00", true},
		{"overnight morning", nights, "2026-03-03 05:59", true},
		{"overnight end", nights, "2026-03-03 06:00", false},
		{"overnight other day", nights, "2026-03-04 23:00", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.IsOpen(at(t, tc.at)); got != tc.want {
				t.Errorf("IsOpen(%s) = %v", tc.at, got)
			}
		})
	}
}

func TestNewRejectsInvalidDefinitions(t *testing.T) {
	ok := hours("09:00", "17:00", "mon")
	tests := []struct {
		name string
		def  Definition
		want error
	}{
		{"unknown timezone", Definition{Timezone: "Mars/Base", Weekly: ok}, nil},
		{"empty timezone", Definition{Weekly: ok}, nil},
		{"local timezone", Definition{Timezone: "Local", Weekly: ok}, nil},
		{"unknown weekday", Definition{Timezone: "UTC", Weekly: hours("09:00", "17:00", "monday")}, nil},
		{"bad clock", Definition{Timezone: "UTC", Weekly: hours("9:00", "17:00", "mon")}, nil},
		{"hour out of range", Definition{Timezone: "UTC", Weekly: hours("09:00", "25:00", "mon")}, nil},
		{"minute out of range", Definition{Timezone: "UTC", Weekly: hours("09:60", "17:00", "mon")}, nil},
		{"24:00 as start", Definition{Timezone: "UTC", Weekly: hours("24:00", "06:00", "mon")}, nil},
		{"empty range", Definition{Timezone: "UTC", Weekly: hours("09:00", "09:00", "mon")}, nil},
		{"no ranges", Definition{Timezone: "UTC", Weekly: map[string][]Range{"mon": {}}}, ErrNoBusinessTime},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.def)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	if d, err := ParseDate("2026-12-25"); err != nil || d.String() != "2026-12-25" {
		t.Errorf("got %v, %v", d, err)
	}
	for _, bad := range []string{"", "2026-13-01", "25-12-2026", "2026-02-30"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}
