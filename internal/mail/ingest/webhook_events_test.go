package ingest

import (
	"testing"

	"echoo/internal/mail/parse"
)

func (e *env) subscribeWebhook() {
	e.t.Helper()
	e.exec(`INSERT INTO webhooks (url, secret_enc, events) VALUES ('https://hooks.example.com/x', '\x00',
		ARRAY['conversation.created', 'message.created', 'contact.created'])`)
}

func (e *env) webhookEvents(typ string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM webhook_events WHERE type = $1`, typ)
}

func TestIngestRecordsWebhookEvents(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.subscribeWebhook()

	e.ingest(inbound("w1@acme.example", "Jane <jane@acme.example>", "Order 1", "First"), 0)
	for typ, want := range map[string]int{"conversation.created": 1, "message.created": 1, "contact.created": 1} {
		if got := e.webhookEvents(typ); got != want {
			t.Errorf("%s events = %d, want %d", typ, got, want)
		}
	}
	var msgID, convID string
	e.scan(&msgID, `SELECT message_id::text FROM webhook_events WHERE type = 'message.created'`)
	e.scan(&convID, `SELECT conversation_id::text FROM webhook_events WHERE type = 'message.created'`)
	if msgID == "" || convID != e.convOf("w1@acme.example") {
		t.Errorf("message event refers to message %q conversation %q", msgID, convID)
	}

	// A reply from the same sender in the same thread adds a message only.
	e.ingest(inbound("w2@acme.example", "Jane <jane@acme.example>", "Re: Order 1", "Second",
		"In-Reply-To: <w1@acme.example>", "References: <w1@acme.example>"), 60)
	for typ, want := range map[string]int{"conversation.created": 1, "message.created": 2, "contact.created": 1} {
		if got := e.webhookEvents(typ); got != want {
			t.Errorf("after reply: %s events = %d, want %d", typ, got, want)
		}
	}
}

func TestIngestRecordsNothingWithoutSubscribers(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("w3@acme.example", "Jane <jane@acme.example>", "Order 3", "Hi"), 0)
	if n := e.count(`SELECT count(*) FROM webhook_events`); n != 0 {
		t.Errorf("webhook_events = %d, want 0", n)
	}
}
