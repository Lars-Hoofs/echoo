package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/jobs"
)

var ams = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		panic(err)
	}
	return loc
}()

func at(day string, hour, minute int) time.Time {
	d, err := time.ParseInLocation("2006-01-02", day, ams)
	if err != nil {
		panic(err)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, ams)
}

type slaSetup struct {
	hours, policy pgtype.UUID
}

// officeHours creates a Monday to Friday 09:00-17:00 schedule in Amsterdam and a policy with a
// one hour first response and a one day (480 minute) resolution target.
func (f *fixture) officeHours() slaSetup {
	f.t.Helper()
	weekly, err := json.Marshal(map[string]any{
		"mon": []map[string]string{{"start": "09:00", "end": "17:00"}}, "tue": []map[string]string{{"start": "09:00", "end": "17:00"}},
		"wed": []map[string]string{{"start": "09:00", "end": "17:00"}}, "thu": []map[string]string{{"start": "09:00", "end": "17:00"}},
		"fri": []map[string]string{{"start": "09:00", "end": "17:00"}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	hours := f.id(`INSERT INTO business_hours (name, timezone, weekly, is_default) VALUES ('Kantoor', 'Europe/Amsterdam', $1, true) RETURNING id`, weekly)
	policy := f.id(`INSERT INTO sla_policies (name, first_response_minutes, resolution_minutes, at_risk_percent, business_hours_id)
		VALUES ('Standaard', 60, 480, 80, $1) RETURNING id`, hours)
	return slaSetup{hours: hours, policy: policy}
}

type slaRow struct {
	Policy                       pgtype.UUID
	State                        string
	FirstDue, ResolutionDue, Met pgtype.Timestamptz
	Paused, Resumed, Finalized   pgtype.Timestamptz
}

func (f *fixture) sla(conv pgtype.UUID) slaRow {
	f.t.Helper()
	var r slaRow
	err := f.pool.QueryRow(context.Background(), `SELECT sla_policy_id, sla_state, first_response_due_at, resolution_due_at, first_response_met_at,
		sla_paused_at, sla_resumed_at, sla_finalized_at FROM conversations WHERE id = $1`, conv).
		Scan(&r.Policy, &r.State, &r.FirstDue, &r.ResolutionDue, &r.Met, &r.Paused, &r.Resumed, &r.Finalized)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func sameInstant(got pgtype.Timestamptz, want time.Time) bool {
	return got.Valid && got.Time.Equal(want)
}

func (f *fixture) sweep() {
	f.t.Helper()
	if err := f.e.SweepSLA(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) slaConversation(created time.Time) pgtype.UUID {
	f.t.Helper()
	conv, msg := f.conversation(inbound{subject: "Vraag", receivedAt: created})
	f.exec(`UPDATE conversations SET created_at = $2 WHERE id = $1`, conv, created)
	f.created(conv, msg)
	return conv
}

func TestMailboxDefaultPolicyIsAppliedToNewConversations(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-06", 16, 30) // Friday afternoon

	conv := f.slaConversation(f.at)

	r := f.sla(conv)
	if r.Policy != s.policy || r.State != "ok" {
		t.Fatalf("sla = %+v", r)
	}
	// One hour of business time from Friday 16:30 ends Monday 09:30; a day of business time
	// ends Monday 16:30.
	if !sameInstant(r.FirstDue, at("2026-03-09", 9, 30)) || !sameInstant(r.ResolutionDue, at("2026-03-09", 16, 30)) {
		t.Fatalf("due = %v / %v", r.FirstDue.Time.In(ams), r.ResolutionDue.Time.In(ams))
	}
}

func TestConversationsWithoutPolicyAreNotTracked(t *testing.T) {
	f := newFixture(t)
	conv := f.slaConversation(time.Now())
	if r := f.sla(conv); r.Policy.Valid || r.State != "none" {
		t.Fatalf("sla = %+v", r)
	}
	f.sweep()
	if f.count(`SELECT count(*) FROM conversation_events WHERE type LIKE 'sla_%'`) != 0 {
		t.Fatal("events for an untracked conversation")
	}
}

func TestRuleAppliesPolicyOnPolicyless24x7Calendar(t *testing.T) {
	f := newFixture(t)
	policy := f.id(`INSERT INTO sla_policies (name, resolution_minutes) VALUES ('Rond de klok', 120) RETURNING id`)
	f.at = at("2026-03-07", 23, 0) // Saturday night: calendar time keeps counting
	f.rule("SLA", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"apply_sla","policy_id":"`+policy.String()+`"}`), false)
	conv := f.slaConversation(f.at)
	r := f.sla(conv)
	if r.FirstDue.Valid || !sameInstant(r.ResolutionDue, at("2026-03-08", 1, 0)) {
		t.Fatalf("sla = %+v", r)
	}
}

func TestSweepMovesThroughAtRiskToBreachedWithEventsNotificationsAndRules(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0) // Monday
	conv := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET assignee_user_id = $2 WHERE id = $1`, conv, f.agentA)
	atRisk := f.rule("Risico", jobs.TriggerSLAAtRisk, allMatch, actions(`{"type":"set_priority","priority":"high"}`), false)
	breached := f.rule("Overschreden", jobs.TriggerSLABreached, allMatch, actions(`{"type":"set_priority","priority":"urgent"}`), false)

	f.at = at("2026-03-02", 10, 30) // 30 of 60 minutes
	f.sweep()
	if r := f.sla(conv); r.State != "ok" {
		t.Fatalf("state = %s at half time", r.State)
	}

	f.at = at("2026-03-02", 10, 49) // more than 80 percent
	f.sweep()
	if r := f.sla(conv); r.State != "at_risk" {
		t.Fatalf("state = %s, want at_risk", r.State)
	}
	f.sweep()
	f.sweep()
	if n := f.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'sla_at_risk'`, conv); n != 1 {
		t.Fatalf("%d sla_at_risk events, want exactly one", n)
	}
	var target, source string
	if err := f.pool.QueryRow(context.Background(), `SELECT data->>'target', data->>'source' FROM conversation_events WHERE type = 'sla_at_risk'`).Scan(&target, &source); err != nil {
		t.Fatal(err)
	}
	if target != "first_response" || source != "system" {
		t.Fatalf("event data target=%q source=%q", target, source)
	}
	if f.count(`SELECT count(*) FROM notifications WHERE user_id = $1 AND conversation_id = $2 AND kind = 'sla'`, f.agentA, conv) != 1 {
		t.Fatal("the assignee was not notified once")
	}
	if f.count(`SELECT count(*) FROM river_job WHERE kind = 'rules.evaluate' AND args->>'trigger' = 'sla_at_risk'`) != 1 {
		t.Fatal("the sla_at_risk rules were not queued once")
	}

	f.at = at("2026-03-02", 11, 1) // past the hour
	f.sweep()
	if r := f.sla(conv); r.State != "breached" {
		t.Fatalf("state = %s, want breached", r.State)
	}
	f.sweep()
	if n := f.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'sla_breached'`, conv); n != 1 {
		t.Fatalf("%d sla_breached events", n)
	}
	if f.count(`SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sla'`, f.agentA) != 2 {
		t.Fatal("expected a notification for at risk and one for breached")
	}

	// The queued jobs run the trigger's rules.
	f.evaluate(jobs.TriggerSLAAtRisk, conv, pgtype.UUID{})
	f.evaluate(jobs.TriggerSLABreached, conv, pgtype.UUID{})
	if len(f.runs(atRisk)) != 1 || len(f.runs(breached)) != 1 || f.state(conv).Priority != "urgent" {
		t.Fatal("the SLA trigger rules did not run")
	}
}

func TestFirstReplyByAPersonMeetsTheFirstResponse(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0)
	conv := f.slaConversation(f.at)

	// What the send worker does when the first reply is delivered.
	f.exec(`UPDATE conversations SET first_responded_at = $2, first_response_met_at = $2 WHERE id = $1`, conv, at("2026-03-02", 10, 20))
	f.at = at("2026-03-02", 11, 30)
	f.sweep()

	if r := f.sla(conv); r.State != "ok" {
		t.Fatalf("state = %s: the first response was met in time and the day is not over", r.State)
	}
	if f.count(`SELECT count(*) FROM conversation_events WHERE type LIKE 'sla_%'`) != 0 {
		t.Fatal("events although nothing is at risk")
	}
}

func TestLateFirstResponseIsABreach(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0)
	conv := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET first_responded_at = $2, first_response_met_at = $2 WHERE id = $1`, conv, at("2026-03-02", 11, 30))
	f.at = at("2026-03-02", 11, 31)
	f.sweep()
	if r := f.sla(conv); r.State != "breached" {
		t.Fatalf("state = %s", r.State)
	}
}

func TestPolicyAppliedAfterTheFirstReplyCountsThatReply(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.at = at("2026-03-02", 10, 0)
	conv, msg := f.conversation(inbound{subject: "x", receivedAt: f.at})
	f.exec(`UPDATE conversations SET first_responded_at = $2 WHERE id = $1`, conv, at("2026-03-02", 10, 5))
	f.rule("SLA", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"apply_sla","policy_id":"`+s.policy.String()+`"}`), false)
	f.created(conv, msg)
	r := f.sla(conv)
	if !sameInstant(r.Met, at("2026-03-02", 10, 5)) {
		t.Fatalf("first response met at %v", r.Met)
	}
}

func TestWaitingPausesTheResolutionClock(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 9, 0) // Monday 09:00: the day's resolution ends at 17:00
	conv := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET first_response_met_at = $2 WHERE id = $1`, conv, at("2026-03-02", 9, 5))
	if r := f.sla(conv); !sameInstant(r.ResolutionDue, at("2026-03-02", 17, 0)) {
		t.Fatalf("resolution due %v", r.ResolutionDue.Time.In(ams))
	}

	// Waiting from 12:00. The trigger stamps real time, so set the moments explicitly.
	f.setStatus(conv, "waiting")
	f.exec(`UPDATE conversations SET sla_paused_at = $2 WHERE id = $1`, conv, at("2026-03-02", 12, 0))

	// Days later the resolution deadline has long passed on the calendar, but the clock is stopped.
	f.at = at("2026-03-05", 12, 0)
	f.sweep()
	if r := f.sla(conv); r.State != "ok" || r.Finalized.Valid {
		t.Fatalf("state = %s while waiting", r.State)
	}
	if f.count(`SELECT count(*) FROM conversation_events WHERE type = 'sla_breached'`) != 0 {
		t.Fatal("breach recorded while the clock was stopped")
	}

	// Reopened on Thursday 09:00. The clock stood still for 21 business hours: 5 on Monday
	// afternoon and 8 each on Tuesday and Wednesday.
	f.setStatus(conv, "open")
	f.exec(`UPDATE conversations SET sla_resumed_at = $2 WHERE id = $1`, conv, at("2026-03-05", 9, 0))
	f.at = at("2026-03-05", 9, 0)
	f.sweep()
	r := f.sla(conv)
	// Five business hours were left on Monday at 12:00, so the deadline is now Thursday 14:00.
	if !sameInstant(r.ResolutionDue, at("2026-03-05", 14, 0)) {
		t.Fatalf("resolution due %v, want Thursday 14:00", r.ResolutionDue.Time.In(ams))
	}
	if r.Paused.Valid || r.Resumed.Valid || r.State != "ok" {
		t.Fatalf("pause not cleared: %+v", r)
	}

	f.at = at("2026-03-05", 14, 1)
	f.sweep()
	if r := f.sla(conv); r.State != "breached" {
		t.Fatalf("state = %s after the shifted deadline", r.State)
	}
}

func TestWaitingStatusIsTrackedByTheDatabase(t *testing.T) {
	f := newFixture(t)
	conv, _ := f.conversation(inbound{subject: "x"})
	f.setStatus(conv, "waiting")
	r := f.sla(conv)
	if !r.Paused.Valid || r.Resumed.Valid {
		t.Fatalf("after waiting: %+v", r)
	}
	f.setStatus(conv, "open")
	r = f.sla(conv)
	if !r.Paused.Valid || !r.Resumed.Valid || r.Resumed.Time.Before(r.Paused.Time) {
		t.Fatalf("after reopening: %+v", r)
	}
	f.setStatus(conv, "waiting")
	r = f.sla(conv)
	if !r.Paused.Valid || r.Resumed.Valid {
		t.Fatalf("after waiting again: %+v", r)
	}
	f.setStatus(conv, "closed")
	if r = f.sla(conv); !r.Resumed.Valid {
		t.Fatal("closing ends the pause")
	}
}

func TestClosingInTimeFinalizesAsOK(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0)
	conv := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET first_response_met_at = $2 WHERE id = $1`, conv, at("2026-03-02", 10, 10))
	f.setStatus(conv, "closed")
	f.exec(`UPDATE conversations SET resolved_at = $2 WHERE id = $1`, conv, at("2026-03-02", 12, 0))

	f.at = at("2026-03-04", 12, 0) // long after the deadline
	f.sweep()
	r := f.sla(conv)
	if r.State != "ok" || !r.Finalized.Valid {
		t.Fatalf("sla = %+v", r)
	}
	if f.count(`SELECT count(*) FROM conversation_events WHERE type LIKE 'sla_%'`) != 0 {
		t.Fatal("a resolved conversation raised an SLA event")
	}
	// Final states are not swept again.
	f.exec(`UPDATE conversations SET sla_state = 'none' WHERE id = $1`, conv)
	f.sweep()
	if f.sla(conv).State != "none" {
		t.Fatal("a finalized conversation was evaluated again")
	}
}

func TestClosingLateFinalizesAsBreached(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0)
	conv := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET first_response_met_at = $2 WHERE id = $1`, conv, at("2026-03-02", 10, 10))
	f.setStatus(conv, "closed")
	f.exec(`UPDATE conversations SET resolved_at = $2 WHERE id = $1`, conv, at("2026-03-03", 9, 30))

	f.at = at("2026-03-04", 12, 0)
	f.sweep()
	// Created Monday 10:00 with 480 minutes: 7h on Monday, 1h on Tuesday, so due Tuesday 10:00.
	// Resolved Tuesday 09:30 is in time.
	if r := f.sla(conv); r.State != "ok" || !r.Finalized.Valid {
		t.Fatalf("sla = %+v", r)
	}

	f.at = at("2026-03-02", 10, 0)
	conv2 := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET first_response_met_at = $2 WHERE id = $1`, conv2, at("2026-03-02", 10, 10))
	f.setStatus(conv2, "closed")
	f.exec(`UPDATE conversations SET resolved_at = $2 WHERE id = $1`, conv2, at("2026-03-03", 10, 30))
	f.at = at("2026-03-04", 12, 0)
	f.sweep()
	if r := f.sla(conv2); r.State != "breached" || !r.Finalized.Valid {
		t.Fatalf("sla = %+v", r)
	}
	if f.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'sla_breached'`, conv2) != 1 {
		t.Fatal("the late close was not recorded")
	}
}

func TestReopenedConversationGetsFreshClocks(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0)
	conv := f.slaConversation(f.at)
	f.exec(`UPDATE conversations SET first_responded_at = $2, first_response_met_at = $2 WHERE id = $1`, conv, at("2026-03-02", 10, 5))
	f.setStatus(conv, "closed")
	f.exec(`UPDATE conversations SET resolved_at = $2 WHERE id = $1`, conv, at("2026-03-02", 11, 0))
	f.at = at("2026-03-02", 12, 0)
	f.sweep()
	if !f.sla(conv).Finalized.Valid {
		t.Fatal("not finalized")
	}

	// The customer writes again on Wednesday morning; ingest reopens and queues the rules.
	f.at = at("2026-03-04", 9, 30)
	f.setStatus(conv, "open")
	msg := f.id(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, subject, received_at)
		VALUES ($1, $2, 'email', 'in', 'again@acme.example', 'jane@acme.example', 'Re: Vraag', $3) RETURNING id`, conv, f.mailbox, f.at)
	f.evaluate(jobs.TriggerMessageReceived, conv, msg)

	r := f.sla(conv)
	if r.Finalized.Valid || r.Met.Valid || r.State != "ok" {
		t.Fatalf("sla = %+v", r)
	}
	if !sameInstant(r.FirstDue, at("2026-03-04", 10, 30)) || !sameInstant(r.ResolutionDue, at("2026-03-05", 9, 30)) {
		t.Fatalf("due = %v / %v", r.FirstDue.Time.In(ams), r.ResolutionDue.Time.In(ams))
	}
	f.at = at("2026-03-04", 10, 31)
	f.sweep()
	if r := f.sla(conv); r.State != "breached" {
		t.Fatalf("state = %s: the new first response is overdue although the first one was met", r.State)
	}
}

func TestSweepSkipsLockedConversationsAndSurvivesMissingPolicy(t *testing.T) {
	f := newFixture(t)
	s := f.officeHours()
	f.exec(`UPDATE mailboxes SET default_sla_policy_id = $2 WHERE id = $1`, f.mailbox, s.policy)
	f.at = at("2026-03-02", 10, 0)
	conv := f.slaConversation(f.at)
	f.exec(`DELETE FROM sla_policies WHERE id = $1`, s.policy)
	if f.sla(conv).Policy.Valid {
		t.Fatal("deleting a policy must detach its conversations")
	}
	f.at = at("2026-03-05", 10, 0)
	f.sweep()
	if r := f.sla(conv); r.State != "ok" {
		t.Fatalf("state = %s", r.State)
	}
}
