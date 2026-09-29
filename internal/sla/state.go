package sla

import "time"

// State is the SLA standing of a conversation; the values match conversations.sla_state.
type State string

const (
	StateNone     State = "none"
	StateOK       State = "ok"
	StateAtRisk   State = "at_risk"
	StateBreached State = "breached"
)

// Target is one deadline of a policy. Minutes is the policy's target length, used to decide
// when the deadline is at risk; Due may have been moved by a pause.
type Target struct {
	Due     time.Time
	Minutes int
	// Met is when the target was fulfilled; nil while it is still pending.
	Met *time.Time
	// Paused targets are not counted: the clock is stopped.
	Paused bool
}

// Evaluate returns the worst standing over the given targets (nil entries are skipped). A
// pending target is breached once now is past Due, and at risk once the business time left
// is no more than (100 - atRiskPercent) percent of the target length. A met target is
// breached only if it was met late.
func Evaluate(now time.Time, sched *Schedule, atRiskPercent int, targets ...*Target) State {
	state := StateOK
	for _, t := range targets {
		if t == nil {
			continue
		}
		switch targetState(now, sched, atRiskPercent, *t) {
		case StateBreached:
			return StateBreached
		case StateAtRisk:
			state = StateAtRisk
		}
	}
	return state
}

func targetState(now time.Time, sched *Schedule, atRiskPercent int, t Target) State {
	if t.Met != nil {
		if t.Met.After(t.Due) {
			return StateBreached
		}
		return StateOK
	}
	if t.Paused {
		return StateOK
	}
	if now.After(t.Due) {
		return StateBreached
	}
	threshold := time.Duration(t.Minutes) * time.Minute * time.Duration(100-atRiskPercent) / 100
	if sched.Elapsed(now, t.Due) <= threshold {
		return StateAtRisk
	}
	return StateOK
}
