package automation

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/inbox"
)

func (f *fixture) macroActor(user pgtype.UUID, mailboxes ...pgtype.UUID) inbox.Actor {
	return inbox.Actor{UserID: user, Read: mailboxes, Write: mailboxes}
}

func mustActions(t *testing.T, doc string) []Action {
	t.Helper()
	a, err := ParseActions([]byte(doc), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMacroRunsOnASelectionAsThePerson(t *testing.T) {
	f := newFixture(t)
	first, _ := f.conversation(inbound{subject: "een"})
	second, _ := f.conversation(inbound{subject: "twee"})
	acts := mustActions(t, actions(addLabel(f.labelVIP), `{"type":"set_priority","priority":"high"}`, `{"type":"add_note","text":"Macro gedraaid"}`))

	results, err := f.e.RunMacro(context.Background(), f.macroActor(f.agentA, f.mailbox), "VIP", acts, []pgtype.UUID{first, second, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Failed() || results[1].Failed() {
		t.Fatalf("results = %+v", results)
	}
	for _, conv := range []pgtype.UUID{first, second} {
		if s := f.state(conv); s.Priority != "high" || len(s.Labels) != 1 {
			t.Fatalf("state = %+v", s)
		}
	}
	var actor pgtype.UUID
	var source string
	if err := f.pool.QueryRow(context.Background(), `SELECT actor_user_id, data->>'source' FROM conversation_events WHERE conversation_id = $1 AND type = 'priority_changed'`, first).Scan(&actor, &source); err != nil {
		t.Fatal(err)
	}
	if actor != f.agentA || source != "macro" {
		t.Fatalf("event actor %v source %q, want the person and macro", actor, source)
	}
	if name := f.str(`SELECT from_name FROM messages WHERE conversation_id = $1 AND kind = 'note'`, first); name != "Macro: VIP" {
		t.Errorf("note author = %q", name)
	}
	// One evaluation of the update rules per conversation, not one per action.
	if n := f.count(`SELECT count(*) FROM river_job WHERE kind = 'rules.evaluate' AND args->>'trigger' = 'conversation_updated'`); n != 2 {
		t.Fatalf("%d rules jobs queued, want 2", n)
	}
}

func TestMacroRespectsTheActorsMailboxScope(t *testing.T) {
	f := newFixture(t)
	other := f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Other', 'other@shop.example') RETURNING id`)
	hidden := f.id(`INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 'geheim') RETURNING id`, other)
	readOnly := f.id(`INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 'lezen') RETURNING id`, other)
	mine, _ := f.conversation(inbound{subject: "mijn"})
	missing := f.id(`SELECT uuidv7()`)
	actor := inbox.Actor{UserID: f.agentA, Read: []pgtype.UUID{f.mailbox, other}, Write: []pgtype.UUID{f.mailbox}}
	hiddenFrom := inbox.Actor{UserID: f.agentA, Read: []pgtype.UUID{f.mailbox}, Write: []pgtype.UUID{f.mailbox}}
	acts := mustActions(t, actions(`{"type":"set_priority","priority":"urgent"}`, `{"type":"add_note","text":"x"}`))

	results, err := f.e.RunMacro(context.Background(), actor, "M", acts, []pgtype.UUID{mine, readOnly, missing})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Failed() || !errors.Is(results[1].Err, inbox.ErrForbidden) || !errors.Is(results[2].Err, inbox.ErrNotFound) {
		t.Fatalf("results = %+v", results)
	}
	results, err = f.e.RunMacro(context.Background(), hiddenFrom, "M", acts, []pgtype.UUID{hidden})
	if err != nil || !errors.Is(results[0].Err, inbox.ErrNotFound) {
		t.Fatalf("a conversation outside the read scope must look missing: %+v %v", results, err)
	}
	if f.count(`SELECT count(*) FROM messages WHERE kind = 'note' AND conversation_id = ANY($1)`, []pgtype.UUID{hidden, readOnly}) != 0 {
		t.Fatal("a note was added to a conversation the actor may not change")
	}
	if f.state(readOnly).Priority != "none" || f.state(hidden).Priority != "none" {
		t.Fatal("a conversation outside the write scope was changed")
	}
}

func TestMacroReportsFailingActionsPerConversation(t *testing.T) {
	f := newFixture(t)
	conv, _ := f.conversation(inbound{subject: "x"})
	gone := f.id(`SELECT uuidv7()`)
	acts := mustActions(t, actions(addLabel(gone), `{"type":"set_priority","priority":"low"}`))
	results, err := f.e.RunMacro(context.Background(), f.macroActor(f.agentA, f.mailbox), "M", acts, []pgtype.UUID{conv})
	if err != nil {
		t.Fatal(err)
	}
	r := results[0]
	if !r.Failed() || r.Err != nil || r.Actions[0].Result != "failed" || r.Actions[1].Result != "applied" {
		t.Fatalf("result = %+v", r)
	}
	if f.state(conv).Priority != "low" {
		t.Fatal("the second action did not run")
	}
}

func TestMacroAssignsRoundRobinAndRejectsTooManyIDs(t *testing.T) {
	f := newFixture(t)
	conv, _ := f.conversation(inbound{subject: "x"})
	acts := mustActions(t, actions(`{"type":"assign_round_robin"}`))
	if _, err := f.e.RunMacro(context.Background(), f.macroActor(f.admin, f.mailbox), "M", acts, []pgtype.UUID{conv}); err != nil {
		t.Fatal(err)
	}
	if a := f.state(conv).Assignee; a != f.agentA && a != f.agentB {
		t.Fatalf("assignee = %v", a)
	}
	ids := make([]pgtype.UUID, inbox.MaxBulk+1)
	for i := range ids {
		ids[i].Valid = true
		ids[i].Bytes[0] = byte(i)
		ids[i].Bytes[1] = byte(i >> 8)
	}
	if _, err := f.e.RunMacro(context.Background(), f.macroActor(f.admin, f.mailbox), "M", acts, ids); !errors.Is(err, inbox.ErrTooManyIDs) {
		t.Fatalf("err = %v", err)
	}
}
