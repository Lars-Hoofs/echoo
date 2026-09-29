package automation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/jobs"
)

func (f *fixture) tick() {
	f.t.Helper()
	if err := f.e.Tick(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) waitingConversation(silentFor time.Duration) pgtype.UUID {
	f.t.Helper()
	conv, _ := f.conversation(inbound{subject: "Vraag", receivedAt: time.Now().Add(-silentFor - time.Hour)})
	f.exec(`UPDATE conversations SET status = 'waiting', last_message_at = $2, last_outbound_at = $2 WHERE id = $1`, conv, time.Now().Add(-silentFor))
	return conv
}

func TestIdleRuleFiresOncePerSilence(t *testing.T) {
	f := newFixture(t)
	rule := f.rawRule(RuleDef{
		Name: "Herinnering", Trigger: TriggerCustomerIdle, IdleHours: 48, Conditions: []byte(allMatch),
		Actions: []byte(actions(addLabel(f.labelVIP))), Enabled: true,
	})
	young := f.waitingConversation(24 * time.Hour)
	silent := f.waitingConversation(49 * time.Hour)
	open, _ := f.conversation(inbound{subject: "open", receivedAt: time.Now().Add(-72 * time.Hour)})
	f.exec(`UPDATE conversations SET last_message_at = now() - interval '72 hours' WHERE id = $1`, open)

	f.tick()
	f.tick()

	if len(f.state(silent).Labels) != 1 {
		t.Fatal("the rule did not fire for a conversation silent for 49 hours")
	}
	if len(f.state(young).Labels) != 0 || len(f.state(open).Labels) != 0 {
		t.Fatal("the rule fired for a conversation that is not silent long enough or not waiting")
	}
	runs := f.runs(rule)
	if len(runs) != 1 || runs[0].Trigger != TriggerCustomerIdle || !runs[0].Matched {
		t.Fatalf("runs = %+v, want exactly one", runs)
	}

	// A message after the previous run starts a new silence, in which the rule fires again.
	f.exec(`UPDATE rule_runs SET created_at = now() - interval '60 hours' WHERE rule_id = $1`, rule)
	f.tick()
	if len(f.runs(rule)) != 2 {
		t.Fatalf("%d runs, want a second one for the new silence", len(f.runs(rule)))
	}
}

func TestIdleRuleUnmatchedConversationIsNotReevaluated(t *testing.T) {
	f := newFixture(t)
	rule := f.rawRule(RuleDef{
		Name: "Alleen VIP", Trigger: TriggerCustomerIdle, IdleHours: 24,
		Conditions: []byte(group("all", cond("label", "has_any", `["`+f.labelVIP.String()+`"]`))),
		Actions:    []byte(actions(`{"type":"set_priority","priority":"high"}`)), Enabled: true,
	})
	conv := f.waitingConversation(30 * time.Hour)
	f.tick()
	f.exec(`INSERT INTO conversation_labels (conversation_id, label_id) VALUES ($1, $2)`, conv, f.labelVIP)
	f.tick()
	runs := f.runs(rule)
	if len(runs) != 1 || runs[0].Matched || f.state(conv).Priority != "none" {
		t.Fatalf("runs = %+v: the non-matching run is recorded and the conversation is not looked at again during this silence", runs)
	}
}

func TestIdleRuleScopedToMailboxAndStopProcessing(t *testing.T) {
	f := newFixture(t)
	other := f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Other', 'other@shop.example') RETURNING id`)
	scoped := f.rawRule(RuleDef{Name: "Andere", Trigger: TriggerCustomerIdle, IdleHours: 1, MailboxID: other, Conditions: []byte(allMatch), Actions: []byte(actions(`{"type":"set_priority","priority":"low"}`)), Enabled: true})
	first := f.rawRule(RuleDef{Name: "Eerst", Trigger: TriggerCustomerIdle, IdleHours: 1, Conditions: []byte(allMatch), Actions: []byte(actions(`{"type":"set_priority","priority":"high"}`)), StopProcessing: true, Enabled: true})
	second := f.rawRule(RuleDef{Name: "Daarna", Trigger: TriggerCustomerIdle, IdleHours: 1, Conditions: []byte(allMatch), Actions: []byte(actions(`{"type":"set_priority","priority":"urgent"}`)), Enabled: true})
	conv := f.waitingConversation(2 * time.Hour)
	f.tick()
	if f.state(conv).Priority != "high" {
		t.Fatalf("priority = %s", f.state(conv).Priority)
	}
	if len(f.runs(scoped)) != 0 || len(f.runs(first)) != 1 || len(f.runs(second)) != 0 {
		t.Fatalf("runs: scoped %d, first %d, second %d", len(f.runs(scoped)), len(f.runs(first)), len(f.runs(second)))
	}
}

func TestIdleRuleIgnoresVeryOldConversations(t *testing.T) {
	f := newFixture(t)
	rule := f.rawRule(RuleDef{Name: "Herinnering", Trigger: TriggerCustomerIdle, IdleHours: 24, Conditions: []byte(allMatch), Actions: []byte(actions(`{"type":"set_priority","priority":"high"}`)), Enabled: true})
	f.waitingConversation(24*time.Hour + idleGrace + time.Hour)
	f.tick()
	if len(f.runs(rule)) != 0 {
		t.Fatal("a conversation far past the threshold was picked up")
	}
}

func TestAutoResolveClosesConversationsThatWaitedLongEnough(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO settings (key, value) VALUES ('automation', '{"auto_resolve_days": 3}')`)
	stale := f.waitingConversation(4 * 24 * time.Hour)
	fresh := f.waitingConversation(2 * 24 * time.Hour)
	repliedRecently := f.waitingConversation(4 * 24 * time.Hour)
	f.exec(`UPDATE conversations SET last_inbound_at = now() - interval '1 day' WHERE id = $1`, repliedRecently)
	open, _ := f.conversation(inbound{subject: "open", receivedAt: time.Now().Add(-10 * 24 * time.Hour)})
	f.exec(`UPDATE conversations SET last_message_at = now() - interval '10 days' WHERE id = $1`, open)

	f.tick()

	if f.state(stale).Status != "closed" {
		t.Fatal("the stale conversation was not closed")
	}
	for name, id := range map[string]pgtype.UUID{"fresh": fresh, "recent reply": repliedRecently, "open": open} {
		if f.state(id).Status == "closed" {
			t.Errorf("the %s conversation was closed", name)
		}
	}
	var noActor bool
	var source string
	err := f.pool.QueryRow(context.Background(), `SELECT actor_user_id IS NULL, data->>'source' FROM conversation_events WHERE conversation_id = $1 AND type = 'resolved'`, stale).Scan(&noActor, &source)
	if err != nil || !noActor || source != "system" {
		t.Fatalf("resolved event: noActor=%v source=%q err=%v", noActor, source, err)
	}
	if f.queuedRules() != 0 {
		t.Error("auto-resolve queued rules")
	}
}

func TestAutoResolveIsOffByDefaultAndWhenZero(t *testing.T) {
	f := newFixture(t)
	stale := f.waitingConversation(60 * 24 * time.Hour)
	f.tick()
	if f.state(stale).Status != "waiting" {
		t.Fatal("closed without the setting")
	}
	f.exec(`INSERT INTO settings (key, value) VALUES ('automation', '{"auto_resolve_days": 0}')`)
	f.tick()
	if f.state(stale).Status != "waiting" {
		t.Fatal("closed with the setting at zero")
	}
}

func TestPurgeRemovesOldRunsAndReplyClaims(t *testing.T) {
	f := newFixture(t)
	rule := f.rule("Alles", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"set_priority","priority":"high"}`), false)
	conv, msg := f.conversation(inbound{subject: "x"})
	f.created(conv, msg)
	f.exec(`INSERT INTO rule_runs (rule_id, conversation_id, trigger, matched, created_at) VALUES ($1, $2, 'x', true, now() - interval '31 days')`, rule, conv)
	f.exec(`INSERT INTO automation_replies (rule_id, address, sent_at) VALUES ($1, 'old@x.example', now() - interval '25 hours'), ($1, 'new@x.example', now())`, rule)

	if err := f.e.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.runs(rule)); n != 1 {
		t.Fatalf("%d runs left, want the recent one only", n)
	}
	if f.count(`SELECT count(*) FROM automation_replies`) != 1 {
		t.Fatal("expired reply claims were not removed")
	}
}

func TestOnlyOneTickRunsAtATime(t *testing.T) {
	f := newFixture(t)
	f.exec(`INSERT INTO settings (key, value) VALUES ('automation', '{"auto_resolve_days": 1}')`)
	stale := f.waitingConversation(3 * 24 * time.Hour)
	acquired, err := f.e.withLock(context.Background(), lockTick, false, func() error {
		f.tick() // a second tick while the first one runs must do nothing
		return nil
	})
	if err != nil || !acquired {
		t.Fatalf("acquired %v err %v", acquired, err)
	}
	if f.state(stale).Status != "waiting" {
		t.Fatal("a concurrent tick ran")
	}
	f.tick()
	if f.state(stale).Status != "closed" {
		t.Fatal("the tick did not run once the lock was free")
	}
}
