package send

import (
	"context"
	"testing"
)

func (e *env) subscribeWebhook() {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO webhooks (url, secret_enc, events) VALUES
		('https://hooks.example.com/x', '\x00', ARRAY['message.sent', 'message.failed'])`)
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestSendRecordsWebhookOutcome(t *testing.T) {
	for name, tc := range map[string]struct {
		script []behavior
		want   string
	}{
		"delivered":         {script: []behavior{accept}, want: "message.sent"},
		"permanent failure": {script: []behavior{perm5xx}, want: "message.failed"},
		"temporary failure": {script: []behavior{temp4xx}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, envOpts{script: tc.script})
			e.subscribeWebhook()
			enq := e.enqueue(e.params(newKey(t)))
			_ = e.work(enq.MessageID)

			if tc.want == "" {
				if n := e.count(`SELECT count(*) FROM webhook_events`); n != 0 {
					t.Errorf("events = %d, want none for a retry", n)
				}
				return
			}
			if n := e.count(`SELECT count(*) FROM webhook_events WHERE type = $1 AND message_id = $2 AND conversation_id = $3`, tc.want, enq.MessageID, e.conv); n != 1 {
				t.Errorf("%s events = %d, want 1", tc.want, n)
			}
		})
	}
}
