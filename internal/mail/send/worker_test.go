package send

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"echoo/internal/mail"
	"echoo/internal/netguard"
)

func TestSendSuccess(t *testing.T) {
	for name, mode := range map[string]tlsMode{"implicit TLS": serverImplicit, "STARTTLS": serverStartTLS} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, envOpts{smtpMode: mode})
			key, sha, err := e.store.Put(context.Background(), []byte("attachment bytes"))
			if err != nil {
				t.Fatal(err)
			}
			p := e.params(newKey(t))
			p.Attachments = []AttachmentRef{{Filename: "note.txt", ContentType: "text/plain", Size: 16, SHA256: sha, BlobKey: key}}
			enq := e.enqueue(p)

			if err := e.work(enq.MessageID); err != nil {
				t.Fatal(err)
			}

			ob := e.outbound(enq.MessageID)
			if ob.Status != "sent" || ob.Attempt != 1 || !ob.SentAt.Valid || !strings.HasPrefix(ob.SmtpResponse, "250 ") {
				t.Errorf("outbound = %+v", ob)
			}
			if e.count(`SELECT count(*) FROM messages WHERE id = $1 AND sent_at IS NOT NULL`, enq.MessageID) != 1 {
				t.Error("messages.sent_at not set")
			}
			got := e.smtp.deliveries()
			if len(got) != 1 {
				t.Fatalf("deliveries = %d, want 1", len(got))
			}
			d := got[0]
			if d.from != "help@example.com" || len(d.rcpt) != 2 || d.rcpt[0] != "jan@example.org" || d.rcpt[1] != "audit@example.org" {
				t.Errorf("envelope = %s -> %v", d.from, d.rcpt)
			}
			if !bytes.Contains(d.data, []byte("Message-Id: <"+enq.MessageIDHeader+">")) || bytes.Contains(d.data, []byte("audit@example.org")) {
				t.Errorf("unexpected message:\n%s", d.data)
			}
			if !bytes.Contains(d.data, []byte("YXR0YWNobWVudCBieXRlcw==")) {
				t.Errorf("attachment missing from message")
			}
			found, err := InSentFolder(context.Background(), e.imapConfig(), enq.MessageIDHeader)
			if err != nil || !found {
				t.Errorf("message not appended to Sent: found=%v err=%v", found, err)
			}
		})
	}
}

func TestSendWithoutSentFolderStillSends(t *testing.T) {
	e := newEnv(t, envOpts{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET sent_folder = ''`); err != nil {
		t.Fatal(err)
	}
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "sent" {
		t.Errorf("status = %s", st)
	}
}

func TestSentFolderAppendFailureKeepsStatus(t *testing.T) {
	e := newEnv(t, envOpts{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET sent_folder = 'DoesNotExist'`); err != nil {
		t.Fatal(err)
	}
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "sent" {
		t.Errorf("status = %s, want sent", st)
	}
}

func TestEnqueueIdempotent(t *testing.T) {
	e := newEnv(t, envOpts{})
	key := newKey(t)
	first := e.enqueue(e.params(key))
	second := e.enqueue(e.params(key))

	if first.Duplicate || !second.Duplicate || first.MessageID != second.MessageID || first.MessageIDHeader != second.MessageIDHeader {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
	// The same key twice inside one transaction must not poison it either.
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		p := e.params(newKey(t))
		a, err := Enqueue(context.Background(), tx, e.rc, p)
		if err != nil {
			return err
		}
		b, err := Enqueue(context.Background(), tx, e.rc, p)
		if err != nil {
			return err
		}
		if !b.Duplicate || a.MessageID != b.MessageID {
			t.Errorf("in-transaction duplicate = %+v vs %+v", a, b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := e.count(`SELECT count(*) FROM messages WHERE kind = 'email' AND direction = 'out'`); n != 2 {
		t.Errorf("messages = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM outbound`); n != 2 {
		t.Errorf("outbound rows = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM river_job WHERE kind = 'mail.send'`); n != 2 {
		t.Errorf("jobs = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM thread_refs WHERE conversation_id = $1`, e.conv); n != 2 {
		t.Errorf("thread refs = %d, want 2", n)
	}

	for i := 0; i < 2; i++ {
		if err := e.work(first.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.smtp.deliveries()); n != 1 {
		t.Errorf("deliveries = %d, want 1", n)
	}
}

func TestEnqueueSchedulesAfterUndoDelay(t *testing.T) {
	e := newEnv(t, envOpts{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET send_delay_seconds = 20`); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	enq := e.enqueue(e.params(newKey(t)))
	ob := e.outbound(enq.MessageID)
	if d := ob.ScheduledAt.Time.Sub(before); d < 19*time.Second || d > 22*time.Second {
		t.Errorf("scheduled %v after enqueue, want ~20s", d)
	}
	var jobAt time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT scheduled_at FROM river_job WHERE kind = 'mail.send'`).Scan(&jobAt); err != nil {
		t.Fatal(err)
	}
	if !jobAt.Equal(ob.ScheduledAt.Time) {
		t.Errorf("job scheduled_at %v != outbound %v", jobAt, ob.ScheduledAt.Time)
	}
	e.clock.advance(-time.Minute)
	err := e.work(enq.MessageID)
	if !isSnooze(err) || len(e.smtp.deliveries()) != 0 {
		t.Errorf("job ran before its time: err=%v", err)
	}
}

func TestEnqueueRejectsHeaderInjection(t *testing.T) {
	e := newEnv(t, envOpts{})
	p := e.params(newKey(t))
	p.Subject = "hi\r\nBcc: evil@example.net"
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		_, err := Enqueue(context.Background(), tx, e.rc, p)
		return err
	})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	if e.count(`SELECT count(*) FROM messages`) != 0 {
		t.Error("message was stored")
	}
}

func TestTemporaryFailureBacksOffThenFails(t *testing.T) {
	e := newEnv(t, envOpts{script: []behavior{temp4xx}})
	enq := e.enqueue(e.params(newKey(t)))

	delays := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	for i, delay := range delays {
		err := e.work(enq.MessageID)
		var snooze *river.JobSnoozeError
		if !errors.As(err, &snooze) || snooze.Duration != delay {
			t.Fatalf("attempt %d: err = %v, want snooze %v", i+1, err, delay)
		}
		ob := e.outbound(enq.MessageID)
		if ob.Status != "retry" || int(ob.Attempt) != i+1 || !strings.HasPrefix(ob.SmtpResponse, "451 4.3.0") {
			t.Fatalf("attempt %d: outbound = %+v", i+1, ob)
		}
		if want := e.clock.Now().Add(delay); !ob.NextAttemptAt.Time.Equal(want) {
			t.Fatalf("attempt %d: next_attempt_at = %v, want %v", i+1, ob.NextAttemptAt.Time, want)
		}
		// Too early: the row is not claimed and nothing is sent.
		if err := e.work(enq.MessageID); !isSnooze(err) {
			t.Fatalf("attempt %d: early run err = %v, want snooze", i+1, err)
		}
		if got := e.outbound(enq.MessageID).Attempt; int(got) != i+1 {
			t.Fatalf("early run changed attempt to %d", got)
		}
		e.clock.advance(delay)
	}
	if err := e.work(enq.MessageID); err != nil {
		t.Fatalf("sixth attempt: %v", err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "failed" || ob.Attempt != 6 {
		t.Errorf("outbound = %+v, want failed after 6 attempts", ob)
	}
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, data := e.smtp.counts(); data != 6 {
		t.Errorf("DATA calls = %d, want 6", data)
	}
	if n := len(e.smtp.deliveries()); n != 0 {
		t.Errorf("deliveries = %d, want 0", n)
	}
}

func TestTemporaryFailureThenSuccess(t *testing.T) {
	e := newEnv(t, envOpts{script: []behavior{temp4xx, accept}})
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); !isSnooze(err) {
		t.Fatalf("err = %v", err)
	}
	e.clock.advance(30 * time.Second)
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "sent" || ob.Attempt != 2 {
		t.Errorf("outbound = %+v", ob)
	}
}

func TestPermanentFailure(t *testing.T) {
	e := newEnv(t, envOpts{script: []behavior{perm5xx}})
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "failed" || ob.Attempt != 1 || !strings.HasPrefix(ob.SmtpResponse, "550 5.7.1 rejected") {
		t.Errorf("outbound = %+v", ob)
	}
	e.clock.advance(24 * time.Hour)
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, data := e.smtp.counts(); data != 1 {
		t.Errorf("DATA calls = %d, want 1", data)
	}
}

func TestConnectionDroppedAfterDataIsUncertain(t *testing.T) {
	e := newEnv(t, envOpts{script: []behavior{dropAfterData, accept}})
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "uncertain" {
		t.Fatalf("status = %s, want uncertain", st)
	}
	e.clock.advance(24 * time.Hour)
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, data := e.smtp.counts(); data != 1 {
		t.Errorf("DATA calls = %d, want exactly 1: an uncertain message must never be resent", data)
	}
	if n := len(e.smtp.deliveries()); n != 0 {
		t.Errorf("deliveries = %d", n)
	}
}

func markSending(t *testing.T, e *env, enq Enqueued, lastAttempt time.Time) {
	t.Helper()
	_, err := e.pool.Exec(context.Background(),
		`UPDATE outbound SET status = 'sending', attempt = 1, last_attempt_at = $2 WHERE message_id = $1`, enq.MessageID, lastAttempt)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStaleSendingFoundInSentFolder(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	markSending(t, e, enq, e.clock.Now().Add(-11*time.Minute))

	raw, err := Build(Outgoing{
		From: mail.Address{Address: "help@example.com"}, To: []mail.Address{{Address: "jan@example.org"}},
		Subject: "x", MessageID: enq.MessageIDHeader, Date: e.clock.Now(), Text: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendToSent(context.Background(), e.imapConfig(), raw, e.clock.Now()); err != nil {
		t.Fatal(err)
	}

	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "sent" || !ob.SentAt.Valid {
		t.Errorf("outbound = %+v, want sent", ob)
	}
	if _, data := e.smtp.counts(); data != 0 {
		t.Error("a stale claim must never be resent")
	}
}

func TestStaleSendingNotInSentFolder(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	markSending(t, e, enq, e.clock.Now().Add(-11*time.Minute))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "uncertain" {
		t.Errorf("status = %s, want uncertain", st)
	}
	if _, data := e.smtp.counts(); data != 0 {
		t.Error("a stale claim must never be resent")
	}
}

func TestStaleSendingWithoutSentFolder(t *testing.T) {
	e := newEnv(t, envOpts{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET sent_folder = ''`); err != nil {
		t.Fatal(err)
	}
	enq := e.enqueue(e.params(newKey(t)))
	markSending(t, e, enq, e.clock.Now().Add(-time.Hour))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "uncertain" {
		t.Errorf("status = %s, want uncertain", st)
	}
}

func TestFreshSendingClaimIsLeftAlone(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	markSending(t, e, enq, e.clock.Now().Add(-time.Minute))
	err := e.work(enq.MessageID)
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) || snooze.Duration != 9*time.Minute {
		t.Fatalf("err = %v, want snooze for the rest of the 10 minutes", err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "sending" {
		t.Errorf("status = %s", st)
	}
}

func TestCancelledMessageIsNeverSent(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		return Cancel(context.Background(), tx, enq.MessageID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "cancelled" {
		t.Errorf("status = %s", st)
	}
	if _, data := e.smtp.counts(); data != 0 {
		t.Error("cancelled message was delivered")
	}
}

func TestCancelOnlyWhileQueued(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		return Cancel(context.Background(), tx, enq.MessageID)
	})
	if !errors.Is(err, ErrNotCancellable) {
		t.Errorf("err = %v, want ErrNotCancellable", err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "sent" {
		t.Errorf("status = %s", st)
	}
}

func TestStartTLSUnsupportedNeverSendsPlaintext(t *testing.T) {
	e := newEnv(t, envOpts{smtpMode: serverPlaintext})
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); !isSnooze(err) {
		t.Fatalf("err = %v, want snooze (retry)", err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "retry" || !strings.Contains(ob.Error, "STARTTLS") {
		t.Errorf("outbound = %+v", ob)
	}
	if mails, data := e.smtp.counts(); mails != 0 || data != 0 {
		t.Errorf("server saw MAIL=%d DATA=%d over plaintext", mails, data)
	}
}

func TestUntrustedCertificateIsRejected(t *testing.T) {
	e := newEnv(t, envOpts{noTrust: true})
	enq := e.enqueue(e.params(newKey(t)))
	if err := e.work(enq.MessageID); !isSnooze(err) {
		t.Fatalf("err = %v, want snooze (retry)", err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "retry" || !strings.Contains(ob.Error, "certificate") {
		t.Errorf("outbound = %+v", ob)
	}
	if mails, _ := e.smtp.counts(); mails != 0 {
		t.Error("server was used despite an untrusted certificate")
	}
}

func TestUnsendableMessageFailsWithoutContactingServer(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	if _, err := e.pool.Exec(context.Background(), `UPDATE messages SET subject = E'a\nBcc: x@example.net' WHERE id = $1`, enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "failed" || !strings.Contains(ob.Error, "Subject") {
		t.Errorf("outbound = %+v", ob)
	}
	if mails, _ := e.smtp.counts(); mails != 0 {
		t.Error("server was contacted")
	}
}

func TestDisabledMailboxFails(t *testing.T) {
	e := newEnv(t, envOpts{})
	enq := e.enqueue(e.params(newKey(t)))
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET disabled_at = now()`); err != nil {
		t.Fatal(err)
	}
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	if st := e.outbound(enq.MessageID).Status; st != "failed" {
		t.Errorf("status = %s", st)
	}
}

func TestStaleSendingIMAPUnreachableBecomesUncertain(t *testing.T) {
	e := newEnv(t, envOpts{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET imap_port = 1`); err != nil {
		t.Fatal(err)
	}
	enq := e.enqueue(e.params(newKey(t)))
	markSending(t, e, enq, e.clock.Now().Add(-11*time.Minute))

	for attempt := 1; attempt < maxSentChecks; attempt++ {
		if err := e.workAttempt(enq.MessageID, attempt); err == nil || isSnooze(err) {
			t.Fatalf("attempt %d: err = %v, want a retryable error", attempt, err)
		}
		if st := e.outbound(enq.MessageID).Status; st != "sending" {
			t.Fatalf("attempt %d: status = %s, want sending", attempt, st)
		}
	}
	if err := e.workAttempt(enq.MessageID, maxSentChecks); err != nil {
		t.Fatal(err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "uncertain" || !strings.HasPrefix(ob.Error, "Sent folder could not be checked: ") || strings.Contains(ob.Error, imapPass) {
		t.Errorf("outbound = %+v", ob)
	}
	if _, data := e.smtp.counts(); data != 0 {
		t.Error("a stale claim must never be resent")
	}
}

func TestStaleSendingUndecryptableIMAPSecretIsUncertainAtOnce(t *testing.T) {
	e := newEnv(t, envOpts{})
	if _, err := e.pool.Exec(context.Background(), `UPDATE mailboxes SET imap_secret_enc = '\x00'::bytea`); err != nil {
		t.Fatal(err)
	}
	enq := e.enqueue(e.params(newKey(t)))
	markSending(t, e, enq, e.clock.Now().Add(-11*time.Minute))
	if err := e.work(enq.MessageID); err != nil {
		t.Fatal(err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "uncertain" || !strings.HasPrefix(ob.Error, "Sent folder could not be checked: ") {
		t.Errorf("outbound = %+v", ob)
	}
	if _, data := e.smtp.counts(); data != 0 {
		t.Error("a stale claim must never be resent")
	}
}

func TestInternalDestinationWithoutOptInIsNeverContacted(t *testing.T) {
	e := newEnv(t, envOpts{blockInternal: true})
	enq := e.enqueue(e.params(newKey(t)))

	if err := e.work(enq.MessageID); !isSnooze(err) {
		t.Fatalf("err = %v, want snooze (temporary failure)", err)
	}
	ob := e.outbound(enq.MessageID)
	if ob.Status != "retry" || !strings.Contains(ob.Error, netguard.ErrInternal.Error()) {
		t.Errorf("outbound = %+v", ob)
	}
	if n := e.smtp.conns.Load(); n != 0 {
		t.Errorf("SMTP server saw %d connections, want 0", n)
	}
	if _, err := InSentFolder(context.Background(), IMAPConfig{
		Host: "127.0.0.1", Port: e.imap.port, TLS: TLSImplicit, Username: imapUser, Password: imapPass,
		Folder: "Sent", TLSConfig: &tls.Config{RootCAs: e.roots},
	}, "x"); !errors.Is(err, netguard.ErrInternal) {
		t.Errorf("Sent folder check err = %v, want ErrInternal", err)
	}
	if n := e.imap.conns.Load(); n != 0 {
		t.Errorf("IMAP server saw %d connections, want 0", n)
	}
}
