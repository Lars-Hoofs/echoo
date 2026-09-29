package send

import (
	"testing"
)

func TestAutoReplyDoesNotMeetTheFirstResponse(t *testing.T) {
	e := newEnv(t, envOpts{smtpMode: serverImplicit})
	auto := e.params(newKey(t))
	auto.AutoReplied = true
	if err := e.work(e.enqueue(auto).MessageID); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1 AND first_responded_at IS NULL AND first_response_met_at IS NULL AND message_count = 1`, e.conv) != 1 {
		t.Fatal("an automatic reply must count as a message but not as the first response")
	}

	if err := e.work(e.enqueue(e.params(newKey(t))).MessageID); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1 AND first_responded_at IS NOT NULL AND first_response_met_at = first_responded_at`, e.conv) != 1 {
		t.Fatal("the first reply by a person must set first_responded_at and first_response_met_at")
	}
}
