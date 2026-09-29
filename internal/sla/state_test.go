package sla

import (
	"testing"
	"time"
)

func TestEvaluate(t *testing.T) {
	office := schedule(t, hours("09:00", "17:00", workdays...))
	now := at(t, "2026-03-02 10:00")
	ptr := func(s string) *time.Time { v := at(t, s); return &v }

	tests := []struct {
		name    string
		now     string
		sched   *Schedule
		percent int
		targets []*Target
		want    State
	}{
		{"no targets", "", nil, 80, nil, StateOK},
		{"far from due", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 14:00"), Minutes: 480}}, StateOK},
		{"inside the risk window", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 10:30"), Minutes: 240}}, StateAtRisk},
		{"exactly at the risk threshold", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 10:48"), Minutes: 240}}, StateAtRisk},
		{"just outside the risk window", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 10:49"), Minutes: 240}}, StateOK},
		{"past due", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 09:59"), Minutes: 240}}, StateBreached},
		{"due right now is not yet breached", "", nil, 80, []*Target{{Due: now, Minutes: 240}}, StateAtRisk},
		{"met in time", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 09:00"), Minutes: 60, Met: ptr("2026-03-02 08:50")}}, StateOK},
		{"met late", "", nil, 80, []*Target{{Due: at(t, "2026-03-02 09:00"), Minutes: 60, Met: ptr("2026-03-02 09:10")}}, StateBreached},
		{"paused target is ignored", "", nil, 80, []*Target{{Due: at(t, "2026-03-01 09:00"), Minutes: 60, Paused: true}}, StateOK},
		{"worst target wins", "", nil, 80, []*Target{
			{Due: at(t, "2026-03-02 10:30"), Minutes: 240},
			{Due: at(t, "2026-03-02 09:00"), Minutes: 60},
		}, StateBreached},
		{"nil targets are skipped", "", nil, 80, []*Target{nil, {Due: at(t, "2026-03-02 10:30"), Minutes: 240}}, StateAtRisk},
		{"business time counts, not wall time", "", office, 80, []*Target{{Due: at(t, "2026-03-03 09:30"), Minutes: 480}}, StateOK},
		{"risk measured in business time", "2026-03-02 16:00", office, 50, []*Target{{Due: at(t, "2026-03-03 09:30"), Minutes: 240}}, StateAtRisk},
		{"over the weekend still pending", "2026-03-06 17:30", office, 90, []*Target{{Due: at(t, "2026-03-09 10:00"), Minutes: 120}}, StateOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := now
			if tc.now != "" {
				current = at(t, tc.now)
			}
			if got := Evaluate(current, tc.sched, tc.percent, tc.targets...); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}
