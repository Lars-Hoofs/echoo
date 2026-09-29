package ingest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"echoo/internal/mail/parse"
	"echoo/internal/realtime"
)

// collectEvents LISTENs before fn runs and returns what was notified by the time fn returned.
func collectEvents(t *testing.T, e *env, fn func()) []realtime.Event {
	t.Helper()
	ctx := context.Background()
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+realtime.Channel); err != nil {
		t.Fatal(err)
	}
	fn()
	var out []realtime.Event
	for {
		waitCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		n, err := conn.Conn().WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			return out
		}
		var ev realtime.Event
		if err := json.Unmarshal([]byte(n.Payload), &ev); err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
}

func TestIngestNotifiesAboutNewMessagesAndConversations(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	var first, reply string
	events := collectEvents(t, e, func() {
		e.ingest(inbound("n1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
		first = e.convOf("n1@acme.example")
	})
	if len(events) != 2 || events[0].Type != realtime.TypeMessageCreated || events[1].Type != realtime.TypeConversationUpdated {
		t.Fatalf("new conversation events = %+v", events)
	}
	if events[0].ConversationID != first || events[0].MailboxID != e.mailbox {
		t.Errorf("event ids = %+v, want conversation %s mailbox %s", events[0], first, e.mailbox)
	}

	events = collectEvents(t, e, func() {
		e.ingest(inbound("n2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More", "In-Reply-To: <n1@acme.example>"), time.Minute)
		reply = e.convOf("n2@acme.example")
	})
	if reply != first {
		t.Fatalf("reply landed in %s, want %s", reply, first)
	}
	if len(events) != 1 || events[0].Type != realtime.TypeMessageCreated {
		t.Fatalf("reply to an open conversation should only announce the message, got %+v", events)
	}
}
