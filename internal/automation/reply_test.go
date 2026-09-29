package automation

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/jobs"
)

func (f *fixture) replies() int {
	return f.count(`SELECT count(*) FROM messages WHERE kind = 'email' AND direction = 'out'`)
}

func TestAutoReplyIsQueuedAsAnAutomaticMessage(t *testing.T) {
	f := newFixture(t)
	rule := f.rule("Bevestiging", jobs.TriggerConversationCreated, allMatch,
		actions(`{"type":"auto_reply","text":"Bedankt voor je bericht <b>Jane</b>.\n\nWe reageren binnen een werkdag.","subject":"Ontvangen"}`), false)
	conv, msg := f.conversation(inbound{subject: "Vraag", from: "jane@acme.example"})

	f.created(conv, msg)

	if f.replies() != 1 {
		t.Fatalf("replies = %d", f.replies())
	}
	var to, subject, text, html, inReplyTo string
	var auto bool
	var author pgtype.UUID
	err := f.pool.QueryRow(context.Background(), `SELECT to_addrs->0->>'address', subject, body_text, body_html, in_reply_to, auto_submitted, author_user_id
		FROM messages WHERE conversation_id = $1 AND direction = 'out'`, conv).Scan(&to, &subject, &text, &html, &inReplyTo, &auto, &author)
	if err != nil {
		t.Fatal(err)
	}
	if to != "jane@acme.example" || subject != "Ontvangen" || !auto || author.Valid {
		t.Errorf("reply = to %q subject %q auto %v author %v", to, subject, auto, author)
	}
	if strings.Contains(html, "<b>") || !strings.Contains(html, "We reageren binnen een werkdag.") || !strings.Contains(text, "werkdag") {
		t.Errorf("html %q text %q", html, text)
	}
	if inReplyTo == "" {
		t.Error("the reply does not thread onto the customer's message")
	}
	if f.count(`SELECT count(*) FROM river_job WHERE kind = 'mail.send'`) != 1 {
		t.Error("no send job queued")
	}
	if r := f.runs(rule); r[0].Actions[0].Result != "applied" {
		t.Errorf("run = %+v", r)
	}
}

func TestAutoReplyFromCannedResponseFillsVariables(t *testing.T) {
	f := newFixture(t)
	tpl := f.id(`INSERT INTO templates (name, scope, subject, body_html) VALUES ('Ontvangst', 'global', 'Uw vraag #{{conversation.number}}',
		'<p>Hallo {{contact.first_name}}, groet {{agent.first_name}}. {{ unknown.thing }}</p>') RETURNING id`)
	f.rule("Sjabloon", jobs.TriggerConversationCreated, allMatch,
		actions(fmt.Sprintf(`{"type":"auto_reply","template_id":%q}`, tpl.String())), false)
	conv, msg := f.conversation(inbound{subject: "Vraag"})
	f.created(conv, msg)

	var subject, html string
	if err := f.pool.QueryRow(context.Background(), `SELECT subject, body_html FROM messages WHERE conversation_id = $1 AND direction = 'out'`, conv).Scan(&subject, &html); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(subject, "Uw vraag #") || strings.Contains(html, "{{") || !strings.Contains(html, "Hallo Jane") || !strings.Contains(html, "groet Shop") {
		t.Errorf("subject %q html %q", subject, html)
	}
}

func TestAutoReplyIsSuppressed(t *testing.T) {
	tests := []struct {
		name   string
		msg    inbound
		reason string
	}{
		{"auto-submitted mail", inbound{subject: "Afwezig", autoSubmitted: true}, "auto-submitted"},
		{"bulk or list mail", inbound{subject: "Nieuwsbrief", bulk: true}, "bulk"},
		{"noreply address", inbound{subject: "Bon", from: "noreply@shop.example.org"}, "does not accept"},
		{"no-reply address", inbound{subject: "Bon", from: "No-Reply@shop.example.org"}, "does not accept"},
		{"mailer daemon", inbound{subject: "Bounce", from: "MAILER-DAEMON@mx.example.org"}, "does not accept"},
		{"our own mailbox", inbound{subject: "Loop", from: "support@shop.example"}, "our mailboxes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rule := f.rule("Bevestiging", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"auto_reply","text":"Ontvangen."}`), false)
			conv, msg := f.conversation(tc.msg)
			f.created(conv, msg)
			if f.replies() != 0 {
				t.Fatalf("a reply was queued")
			}
			a := f.runs(rule)[0].Actions[0]
			if a.Result != "skipped" || !strings.Contains(a.Detail, tc.reason) {
				t.Fatalf("action = %+v", a)
			}
			if f.count(`SELECT count(*) FROM automation_replies`) != 0 {
				t.Error("a suppressed reply used up the 24 hour allowance")
			}
		})
	}
}

func TestAutoReplyIsSuppressedForSpam(t *testing.T) {
	f := newFixture(t)
	rule := f.rule("Eerst spam", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"mark_spam"}`, `{"type":"auto_reply","text":"Ontvangen."}`), false)
	conv, msg := f.conversation(inbound{subject: "Win een prijs"})
	f.created(conv, msg)
	if f.state(conv).Status != "spam" || f.replies() != 0 {
		t.Fatalf("status %s, replies %d", f.state(conv).Status, f.replies())
	}
	if a := f.runs(rule)[0].Actions[1]; a.Result != "skipped" || !strings.Contains(a.Detail, "spam") {
		t.Fatalf("action = %+v", a)
	}
}

func TestAutoReplyOncePerContactPerRulePer24Hours(t *testing.T) {
	f := newFixture(t)
	rule := f.rule("Bevestiging", jobs.TriggerMessageReceived, allMatch, actions(`{"type":"auto_reply","text":"Ontvangen."}`), false)
	other := f.rule("Tweede regel", jobs.TriggerMessageReceived, allMatch, actions(`{"type":"auto_reply","text":"Nog een keer."}`), false)
	conv, msg := f.conversation(inbound{subject: "Vraag"})

	f.evaluate(jobs.TriggerMessageReceived, conv, msg)
	if f.replies() != 2 {
		t.Fatalf("each rule replies once, got %d replies", f.replies())
	}

	f.evaluate(jobs.TriggerMessageReceived, conv, msg)
	if f.replies() != 2 {
		t.Fatalf("a second evaluation within 24 hours replied again (%d)", f.replies())
	}
	a := f.runs(rule)[1].Actions[0]
	if a.Result != "skipped" || !strings.Contains(a.Detail, "24 hours") {
		t.Fatalf("second run = %+v", a)
	}

	f.exec(`UPDATE automation_replies SET sent_at = now() - interval '25 hours' WHERE rule_id = $1`, rule)
	f.evaluate(jobs.TriggerMessageReceived, conv, msg)
	if f.replies() != 3 {
		t.Fatalf("after 24 hours the rule may reply again, got %d replies", f.replies())
	}
	_ = other
}

func TestAutoReplyToAnotherContactIsAllowed(t *testing.T) {
	f := newFixture(t)
	f.rule("Bevestiging", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"auto_reply","text":"Ontvangen."}`), false)
	for _, from := range []string{"a@acme.example", "b@acme.example"} {
		conv, msg := f.conversation(inbound{subject: "Vraag", from: from})
		f.created(conv, msg)
	}
	if f.replies() != 2 {
		t.Fatalf("replies = %d, want one per contact", f.replies())
	}
}

func TestAutoReplyWithMissingTemplateFails(t *testing.T) {
	f := newFixture(t)
	gone := f.id(`SELECT uuidv7()`)
	rule := f.rule("Sjabloon", jobs.TriggerConversationCreated, allMatch,
		actions(fmt.Sprintf(`{"type":"auto_reply","template_id":%q}`, gone.String())), false)
	conv, msg := f.conversation(inbound{subject: "Vraag"})
	f.created(conv, msg)
	a := f.runs(rule)[0].Actions[0]
	if a.Result != "failed" || !strings.Contains(a.Detail, "no longer exists") || f.replies() != 0 {
		t.Fatalf("action = %+v", a)
	}
}

func TestAutoReplyOnlySendsTemplatesOfItsMailbox(t *testing.T) {
	foreign := map[string]func(f *fixture) pgtype.UUID{
		"personal": func(f *fixture) pgtype.UUID {
			return f.id(`INSERT INTO templates (name, scope, owner_user_id, subject, body_html) VALUES ('P', 'personal', $1, 's', '<p>secret</p>') RETURNING id`, f.agentA)
		},
		"team": func(f *fixture) pgtype.UUID {
			return f.id(`INSERT INTO templates (name, scope, team_id, subject, body_html) VALUES ('T', 'team', $1, 's', '<p>secret</p>') RETURNING id`, f.team)
		},
		"other mailbox": func(f *fixture) pgtype.UUID {
			other := f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Other', 'other@shop.example') RETURNING id`)
			return f.id(`INSERT INTO templates (name, scope, mailbox_id, subject, body_html) VALUES ('M', 'mailbox', $1, 's', '<p>secret</p>') RETURNING id`, other)
		},
	}
	for name, make := range foreign {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			rule := f.rule("Sjabloon", jobs.TriggerConversationCreated, allMatch,
				actions(fmt.Sprintf(`{"type":"auto_reply","template_id":%q}`, make(f).String())), false)
			conv, msg := f.conversation(inbound{subject: "Vraag"})
			f.created(conv, msg)
			a := f.runs(rule)[0].Actions[0]
			if a.Result != "failed" || !strings.Contains(a.Detail, "not available") || f.replies() != 0 {
				t.Fatalf("action = %+v", a)
			}
		})
	}
	f := newFixture(t)
	own := f.id(`INSERT INTO templates (name, scope, mailbox_id, subject, body_html) VALUES ('M', 'mailbox', $1, 's', '<p>ok</p>') RETURNING id`, f.mailbox)
	f.rule("Eigen", jobs.TriggerConversationCreated, allMatch, actions(fmt.Sprintf(`{"type":"auto_reply","template_id":%q}`, own.String())), false)
	conv, msg := f.conversation(inbound{subject: "Vraag"})
	f.created(conv, msg)
	if f.replies() != 1 {
		t.Fatal("a template of the conversation's own mailbox must be sent")
	}
}
