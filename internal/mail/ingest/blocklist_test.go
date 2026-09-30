package ingest

import (
	"testing"
	"time"

	"echoo/internal/mail/parse"
)

func TestBlockedSenderStartsAsSpamWithoutRules(t *testing.T) {
	for _, pattern := range []string{"spammer@junk.example", "junk.example"} {
		t.Run(pattern, func(t *testing.T) {
			e := newEnv(t, parse.Limits{})
			e.exec(`INSERT INTO blocked_senders (mailbox_id, pattern) VALUES ($1, $2)`, e.mailbox, pattern)

			e.ingest(inbound("b1@junk.example", "Spammer <Spammer@Junk.example>", "Win", "Click"), 0)
			conv := e.convOf("b1@junk.example")
			if status, _, _ := e.convStatus(conv); status != "spam" {
				t.Errorf("status = %s, want spam", status)
			}
			if n := e.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'created' AND data->>'blocked_sender' = 'true'`, conv); n != 1 {
				t.Errorf("created events marked blocked_sender = %d, want 1", n)
			}
			if n := e.count(`SELECT count(*) FROM river_job WHERE kind = 'rules.evaluate'`); n != 0 {
				t.Errorf("rules jobs = %d, want none for a blocked sender", n)
			}
		})
	}
}

func TestBlocklistIsPerMailboxAndLeavesOthersAlone(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	var other string
	e.scan(&other, `INSERT INTO mailboxes (name, email_address) VALUES ('Sales', 'sales@shop.example') RETURNING id::text`)
	e.exec(`INSERT INTO blocked_senders (mailbox_id, pattern) VALUES ($1, 'junk.example')`, other)

	e.ingest(inbound("c1@junk.example", "x@junk.example", "Hello", "Hi"), 0)
	// A subdomain is not the blocked domain.
	e.exec(`INSERT INTO blocked_senders (mailbox_id, pattern) VALUES ($1, 'junk.example')`, e.mailbox)
	e.ingest(inbound("c2@mail.junk.example", "y@mail.junk.example", "Hello", "Hi"), time.Minute)

	for _, id := range []string{"c1@junk.example", "c2@mail.junk.example"} {
		if status, _, _ := e.convStatus(e.convOf(id)); status != "open" {
			t.Errorf("%s: status = %s, want open", id, status)
		}
	}
}

func TestBlockingDoesNotMoveOngoingConversation(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("d1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	conv := e.convOf("d1@acme.example")
	e.exec(`INSERT INTO blocked_senders (mailbox_id, pattern) VALUES ($1, 'jane@acme.example')`, e.mailbox)

	e.ingest(inbound("d2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More",
		"In-Reply-To: <d1@acme.example>"), time.Hour)
	if e.convOf("d2@acme.example") != conv {
		t.Fatal("reply did not join its conversation")
	}
	if status, _, _ := e.convStatus(conv); status != "open" {
		t.Errorf("status = %s, want open", status)
	}
}

func TestReplyToTrashedConversationStartsANewOne(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("t1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	trashed := e.convOf("t1@acme.example")
	e.setConversation(trashed, "deleted_at = now()")

	e.ingest(inbound("t2@acme.example", "Jane <jane@acme.example>", "Re: Order", "Hello?",
		"In-Reply-To: <t1@acme.example>"), time.Hour)
	fresh := e.convOf("t2@acme.example")
	if fresh == trashed {
		t.Fatal("reply went into the trashed conversation")
	}
	// The next reply threads onto the new conversation, not into a third one.
	e.ingest(inbound("t3@acme.example", "Jane <jane@acme.example>", "Re: Order", "Anyone?",
		"In-Reply-To: <t2@acme.example>", "References: <t1@acme.example> <t2@acme.example>"), 2*time.Hour)
	if got := e.convOf("t3@acme.example"); got != fresh {
		t.Errorf("third message went to %s, want %s", got, fresh)
	}
}
