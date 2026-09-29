package mailflow

import (
	"bytes"
	"context"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	echoomail "echoo/internal/mail"
	"echoo/internal/mail/send"
)

const (
	customerAddr = "jane@customer.example"
	firstSubject = "Where is my parcel?"
	firstBody    = "Hello,\r\n\r\nMy parcel 1234 has not arrived yet.\r\nCan you check?\r\n"
	firstID      = "cust-1@customer.example"
)

func rfc822(headers []string, body string) []byte {
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body)
}

func customerMail(id, subject, body string, extra ...string) []byte {
	h := []string{
		"Message-ID: <" + id + ">",
		"From: Jane Customer <" + customerAddr + ">",
		"To: " + mailboxAddr,
		"Subject: " + subject,
		"Date: Mon, 02 Mar 2026 10:00:00 +0000",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
	}
	return rfc822(append(h, extra...), body)
}

// firstContact runs the whole inbound path for the first customer email and returns the
// conversation it created.
func (e *env) firstContact() pgtype.UUID {
	e.t.Helper()
	e.deliver(customerMail(firstID, firstSubject, firstBody))
	e.waitIngested(1)
	return e.conversationOf(firstID)
}

// agentReply enqueues a reply the way the API does: to the latest message of the conversation.
func (e *env) agentReply(conv pgtype.UUID, text string) send.Enqueued {
	e.t.Helper()
	var key pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()`).Scan(&key); err != nil {
		e.t.Fatal(err)
	}
	return e.agentReplyWithKey(conv, text, key)
}

func (e *env) agentReplyWithKey(conv pgtype.UUID, text string, key pgtype.UUID) send.Enqueued {
	e.t.Helper()
	parent, parentRefs := e.latestMessage(conv)
	var res send.Enqueued
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		var err error
		res, err = send.Enqueue(context.Background(), tx, e.river, send.EnqueueParams{
			ConversationID: conv,
			MailboxID:      e.mailID,
			IdempotencyKey: key,
			To:             []echoomail.Address{{Name: "Jane Customer", Address: customerAddr}},
			Subject:        "Re: " + firstSubject,
			Text:           text,
			InReplyTo:      parent,
			References:     append(append([]string(nil), parentRefs...), parent),
		})
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) outboundStatus(msg pgtype.UUID) string {
	e.t.Helper()
	var s string
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM outbound WHERE message_id = $1`, msg).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// sendReply enqueues a reply and waits until it left through SMTP and was filed in Sent.
func (e *env) sendReply(conv pgtype.UUID, text string) (send.Enqueued, *mail.Message, []byte) {
	e.t.Helper()
	return e.sendReplyWithKey(conv, text, nil)
}

func (e *env) sendReplyWithKey(conv pgtype.UUID, text string, key *pgtype.UUID) (send.Enqueued, *mail.Message, []byte) {
	e.t.Helper()
	var res send.Enqueued
	if key == nil {
		res = e.agentReply(conv, text)
	} else {
		res = e.agentReplyWithKey(conv, text, *key)
	}
	waitFor(e.t, "outbound sent", func() bool { return e.outboundStatus(res.MessageID) == "sent" })
	waitFor(e.t, "copy in Sent folder", func() bool { return e.sentFolderCount() > 0 })
	waitFor(e.t, "send job completion", func() bool {
		return e.count(`SELECT count(*) FROM river_job WHERE kind = 'mail.send' AND state <> 'completed'`) == 0
	})
	raws := e.smtp.messages()
	if len(raws) != 1 {
		e.t.Fatalf("SMTP server accepted %d messages, want exactly 1", len(raws))
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raws[0]))
	if err != nil {
		e.t.Fatal(err)
	}
	return res, msg, raws[0]
}

func TestInboundEmailBecomesConversation(t *testing.T) {
	e := newEnv(t)
	e.startSync()
	conv := e.firstContact()

	var subject, preview, status string
	var contact pgtype.UUID
	var msgCount int
	err := e.pool.QueryRow(context.Background(),
		`SELECT subject, preview, status, contact_id, message_count FROM conversations WHERE id = $1`, conv).
		Scan(&subject, &preview, &status, &contact, &msgCount)
	if err != nil {
		t.Fatal(err)
	}
	if subject != firstSubject {
		t.Errorf("subject = %q, want %q", subject, firstSubject)
	}
	if want := "Hello, My parcel 1234 has not arrived yet. Can you check?"; preview != want {
		t.Errorf("preview = %q, want %q", preview, want)
	}
	if status != "open" || msgCount != 1 {
		t.Errorf("status = %s, message_count = %d, want open and 1", status, msgCount)
	}
	if got := e.count(`SELECT count(*) FROM conversations`); got != 1 {
		t.Errorf("conversations = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM messages WHERE direction = 'in' AND from_addr = $1`, customerAddr); got != 1 {
		t.Errorf("inbound messages = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM contact_addresses WHERE email = $1 AND contact_id = $2`, customerAddr, contact); got != 1 {
		t.Errorf("contact for %s linked to conversation = %d, want 1", customerAddr, got)
	}
	e.waitCount("connected sync state", 1, `SELECT count(*) FROM mailboxes WHERE sync_state = 'connected'`)
	e.requireNoSecrets()
}

func TestAgentReplyIsSentOnceAndFiledInSent(t *testing.T) {
	e := newEnv(t)
	e.startSync()
	conv := e.firstContact()

	var key pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	res, msg, raw := e.sendReplyWithKey(conv, "It ships tomorrow.", &key)

	if got, want := msg.Header.Get("Message-Id"), "<"+res.MessageIDHeader+">"; got != want {
		t.Errorf("Message-ID = %q, want %q", got, want)
	}
	gotConv, ok := echoomail.ConversationFromMessageID(echoomail.NormalizeMessageID(res.MessageIDHeader))
	if !ok || gotConv != conv.Bytes {
		t.Errorf("Message-ID %q does not carry conversation %s", res.MessageIDHeader, conv.String())
	}
	if got, want := msg.Header.Get("In-Reply-To"), "<"+firstID+">"; got != want {
		t.Errorf("In-Reply-To = %q, want %q", got, want)
	}
	if refs := msg.Header.Get("References"); !strings.Contains(refs, "<"+firstID+">") {
		t.Errorf("References = %q, want it to contain <%s>", refs, firstID)
	}
	if to := msg.Header.Get("To"); !strings.Contains(to, customerAddr) {
		t.Errorf("To = %q, want %s", to, customerAddr)
	}
	e.smtp.mu.Lock()
	rcpts := e.smtp.rcpts
	e.smtp.mu.Unlock()
	if len(rcpts) != 1 || len(rcpts[0]) != 1 || rcpts[0][0] != customerAddr {
		t.Errorf("envelope recipients = %v, want [[%s]]", rcpts, customerAddr)
	}
	if got := e.sentFolderCount(); got != 1 {
		t.Errorf("Sent folder holds %d messages, want 1", got)
	}
	found, err := send.InSentFolder(context.Background(), e.sentConfig(), res.MessageIDHeader)
	if err != nil || !found {
		t.Errorf("InSentFolder = %v, %v; want true", found, err)
	}
	if !bytes.Contains(raw, []byte("It ships tomorrow.")) {
		t.Error("SMTP data lacks the reply text")
	}

	// A delivered reply counts on the conversation and sets the first response time.
	if got := e.count(`SELECT count(*) FROM conversations WHERE id = $1 AND message_count = 2
		AND last_outbound_at IS NOT NULL AND first_responded_at = last_outbound_at
		AND preview = 'It ships tomorrow.'`, conv); got != 1 {
		t.Error("conversation counters were not updated for the delivered reply")
	}

	// A retried request with the same idempotency key must not queue or send anything again.
	again := e.agentReplyWithKey(conv, "It ships tomorrow.", key)
	if !again.Duplicate || again.MessageID != res.MessageID {
		t.Errorf("retry with same key = %+v, want duplicate of %s", again, res.MessageID.String())
	}
	time.Sleep(300 * time.Millisecond)
	if got := len(e.smtp.messages()); got != 1 {
		t.Errorf("SMTP server accepted %d messages after retry, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM river_job WHERE kind = 'mail.send'`); got != 1 {
		t.Errorf("send jobs = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM messages WHERE direction = 'out'`); got != 1 {
		t.Errorf("outbound messages = %d, want 1", got)
	}
	e.requireNoSecrets()
}

func TestCustomerReplyThreadsIntoSameConversation(t *testing.T) {
	variants := []struct {
		name  string
		build func(ourID, ourRefs string) []byte
	}{
		{"in-reply-to and references", func(ourID, ourRefs string) []byte {
			return customerMail("cust-2@customer.example", "Re: "+firstSubject, "Thanks, will wait.\r\n",
				"In-Reply-To: "+ourID, "References: "+ourRefs+" "+ourID)
		}},
		{"only references, subject changed", func(ourID, ourRefs string) []byte {
			return customerMail("cust-2@customer.example", "Quick follow-up", "Thanks, will wait.\r\n",
				"References: "+ourRefs+" "+ourID)
		}},
		{"only our message id in references, subject changed", func(ourID, _ string) []byte {
			return customerMail("cust-2@customer.example", "Quick follow-up", "Thanks, will wait.\r\n",
				"References: "+ourID)
		}},
		{"no threading headers, same subject", func(string, string) []byte {
			return customerMail("cust-2@customer.example", "Re: "+firstSubject, "Thanks, will wait.\r\n")
		}},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			e := newEnv(t)
			e.startSync()
			conv := e.firstContact()
			res, msg, _ := e.sendReply(conv, "It ships tomorrow.")

			e.exec(`UPDATE conversations SET status = 'closed', resolved_at = now() WHERE id = $1`, conv)
			e.deliver(v.build("<"+res.MessageIDHeader+">", msg.Header.Get("References")))
			e.waitIngested(2)

			if got := e.count(`SELECT count(*) FROM conversations`); got != 1 {
				t.Fatalf("conversations = %d, want 1 (reply started a new thread)", got)
			}
			if got := e.conversationOf("cust-2@customer.example"); got != conv {
				t.Fatalf("reply landed in %s, want %s", got.String(), conv.String())
			}
			var status string
			var inbound, total int
			err := e.pool.QueryRow(context.Background(), `
				SELECT c.status, count(*) FILTER (WHERE m.direction = 'in'), count(*)
				FROM conversations c JOIN messages m ON m.conversation_id = c.id
				WHERE c.id = $1 GROUP BY c.status`, conv).Scan(&status, &inbound, &total)
			if err != nil {
				t.Fatal(err)
			}
			if status != "open" {
				t.Errorf("status = %s, want open (closed conversation must reopen)", status)
			}
			if inbound != 2 || total != 3 {
				t.Errorf("inbound = %d, total = %d, want 2 and 3", inbound, total)
			}
			if got := e.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'reopened'`, conv); got != 1 {
				t.Errorf("reopened events = %d, want 1", got)
			}
			e.requireNoSecrets()
		})
	}
}

func dsnFor(originalID string) []byte {
	return rfc822([]string{
		"Message-ID: <dsn-1@mx.customer.example>",
		"From: MAILER-DAEMON@mx.customer.example (Mail Delivery System)",
		"To: " + mailboxAddr,
		"Subject: Undelivered Mail Returned to Sender",
		"Date: Mon, 02 Mar 2026 11:00:00 +0000",
		"Auto-Submitted: auto-replied",
		"MIME-Version: 1.0",
		"Content-Type: multipart/report; report-type=delivery-status; boundary=RR",
	}, strings.Join([]string{
		"--RR",
		"Content-Type: text/plain; charset=us-ascii",
		"",
		"Your message could not be delivered.",
		"--RR",
		"Content-Type: message/delivery-status",
		"",
		"Reporting-MTA: dns; mx.customer.example",
		"",
		"Final-Recipient: rfc822; " + customerAddr,
		"Action: failed",
		"Status: 5.1.1",
		"Diagnostic-Code: smtp; 550 5.1.1 User unknown",
		"",
		"--RR",
		"Content-Type: text/rfc822-headers",
		"",
		"Message-ID: " + originalID,
		"Subject: Re: " + firstSubject,
		"--RR--",
		"",
	}, "\r\n"))
}

func TestBounceMarksOutboundBouncedWithoutNewMessage(t *testing.T) {
	e := newEnv(t)
	e.startSync()
	conv := e.firstContact()
	res, msg, _ := e.sendReply(conv, "It ships tomorrow.")

	e.deliver(dsnFor(msg.Header.Get("Message-Id")))
	waitFor(t, "outbound bounced", func() bool { return e.outboundStatus(res.MessageID) == "bounced" })
	e.waitIngested(2)

	var dsnStatus, diagnostic string
	err := e.pool.QueryRow(context.Background(), `SELECT dsn_status, error FROM outbound WHERE message_id = $1`, res.MessageID).Scan(&dsnStatus, &diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if dsnStatus != "5.1.1" || !strings.Contains(diagnostic, "User unknown") {
		t.Errorf("dsn_status = %q, error = %q", dsnStatus, diagnostic)
	}
	if got := e.count(`SELECT count(*) FROM messages`); got != 2 {
		t.Errorf("messages = %d, want 2 (original and reply, no bounce message)", got)
	}
	if got := e.count(`SELECT count(*) FROM messages WHERE direction = 'in'`); got != 1 {
		t.Errorf("inbound messages = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM conversations`); got != 1 {
		t.Errorf("conversations = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'bounced'`, conv); got != 1 {
		t.Errorf("bounced events = %d, want 1", got)
	}
}

func TestManagerRestartCreatesNoDuplicates(t *testing.T) {
	e := newEnv(t)
	m := e.startSync()
	e.deliver(customerMail("cust-a@customer.example", "First question", "Body one.\r\n"))
	e.deliver(customerMail("cust-b@customer.example", "Second question", "Body two.\r\n"))
	e.waitIngested(2)

	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE mailboxes SET sync_state = 'disabled' WHERE id = $1`, e.mailID)
	e.startSync()
	e.waitCount("connected sync state", 1, `SELECT count(*) FROM mailboxes WHERE sync_state = 'connected'`)

	e.deliver(customerMail("cust-c@customer.example", "Third question", "Body three.\r\n"))
	e.waitIngested(3)
	// Give a duplicate fetch of the older messages the chance to show up.
	time.Sleep(500 * time.Millisecond)

	if got := e.count(`SELECT count(*) FROM raw_messages`); got != 3 {
		t.Errorf("raw messages = %d, want 3", got)
	}
	if got := e.count(`SELECT count(*) FROM messages`); got != 3 {
		t.Errorf("messages = %d, want 3", got)
	}
	if got := e.count(`SELECT count(*) FROM conversations`); got != 3 {
		t.Errorf("conversations = %d, want 3", got)
	}
	if got := e.count(`SELECT count(*) FROM river_job WHERE kind = 'mail.parse'`); got != 3 {
		t.Errorf("parse jobs = %d, want 3", got)
	}
	e.requireNoSecrets()
}

func TestIdenticalCopyIsNotStoredTwice(t *testing.T) {
	e := newEnv(t)
	e.startSync()
	raw := customerMail(firstID, firstSubject, firstBody)
	e.deliver(raw)
	e.waitIngested(1)

	e.deliver(raw)
	e.waitIngested(2)

	if got := e.count(`SELECT count(*) FROM messages`); got != 1 {
		t.Errorf("messages = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM conversations`); got != 1 {
		t.Errorf("conversations = %d, want 1", got)
	}
	var count int
	if err := e.pool.QueryRow(context.Background(), `SELECT message_count FROM conversations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("conversation message_count = %d, want 1", count)
	}
	e.requireNoSecrets()
}
