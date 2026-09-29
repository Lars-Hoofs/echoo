package send

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"echoo/internal/realtime"
)

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

func TestOutboundStatusChangesAreNotified(t *testing.T) {
	sent := newEnv(t, envOpts{})
	enq := sent.enqueue(sent.params(newKey(t)))
	events := collectEvents(t, sent, func() {
		if err := sent.work(enq.MessageID); err != nil {
			t.Fatal(err)
		}
	})
	if len(events) != 2 || events[0].Type != realtime.TypeMessageUpdated || events[1].Type != realtime.TypeConversationUpdated {
		t.Fatalf("delivery events = %+v", events)
	}
	if events[0].ConversationID != sent.conv.String() || events[0].MailboxID != sent.mailbox.String() {
		t.Errorf("event ids = %+v", events[0])
	}

	failed := newEnv(t, envOpts{script: []behavior{perm5xx}})
	enq = failed.enqueue(failed.params(newKey(t)))
	events = collectEvents(t, failed, func() {
		if err := failed.work(enq.MessageID); err != nil {
			t.Fatal(err)
		}
	})
	if len(events) != 1 || events[0].Type != realtime.TypeMessageUpdated {
		t.Fatalf("failure events = %+v", events)
	}
}
