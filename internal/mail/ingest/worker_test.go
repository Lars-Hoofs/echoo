package ingest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/jobs"
	"echoo/internal/mail"
	"echoo/internal/mail/parse"
	"echoo/internal/scan"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const mailboxAddr = "support@shop.example"

type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	store   *storage.FS
	worker  *Worker
	mailbox string
	base    time.Time
}

func newEnv(t *testing.T, limits parse.Limits) *env {
	t.Helper()
	pool := testdb.New(t)
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{
		t: t, pool: pool, store: store,
		worker: NewWorker(Deps{Pool: pool, Store: store, Limits: limits, Jobs: client}),
		base:   time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour),
	}
	e.scan(&e.mailbox, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', $1) RETURNING id::text`, mailboxAddr)
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) scan(dst any, sql string, args ...any) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(dst); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	e.scan(&n, sql, args...)
	return n
}

// addRaw stores the bytes as a raw message that arrived offset after the test's base time.
func (e *env) addRaw(data string, offset time.Duration) string {
	e.t.Helper()
	key, sum, err := e.store.Put(context.Background(), []byte(data))
	if err != nil {
		e.t.Fatal(err)
	}
	var id string
	e.scan(&id, `INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key, received_at)
		VALUES ($1, 'webhook', $2, $3, $4, $5) RETURNING id::text`,
		e.mailbox, sum, len(data), key, e.base.Add(offset))
	return id
}

func (e *env) work(rawID string) error {
	return e.worker.Work(context.Background(), &river.Job[jobs.ParseRaw]{Args: jobs.ParseRaw{RawMessageID: rawID}})
}

func (e *env) ingest(data string, offset time.Duration) string {
	e.t.Helper()
	id := e.addRaw(data, offset)
	if err := e.work(id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) rawStatus(id string) (status, detail string) {
	e.t.Helper()
	var s, d string
	e.scan(&s, `SELECT parse_status FROM raw_messages WHERE id = $1`, id)
	e.scan(&d, `SELECT parse_error FROM raw_messages WHERE id = $1`, id)
	return s, d
}

func (e *env) convOf(messageID string) string {
	e.t.Helper()
	var id string
	e.scan(&id, `SELECT conversation_id::text FROM messages WHERE message_id_header = $1`, messageID)
	return id
}

func (e *env) events(conv, typ string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = $2`, conv, typ)
}

// msg builds a plain text message with CRLF line endings.
func msg(headers []string, body string) string {
	return strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n"
}

func inbound(id, from, subject, body string, extra ...string) string {
	h := []string{
		"Message-ID: <" + id + ">",
		"From: " + from,
		"To: " + mailboxAddr,
		"Subject: " + subject,
		"Date: Mon, 02 Mar 2026 10:00:00 +0000",
		"Content-Type: text/plain; charset=utf-8",
	}
	return msg(append(h, extra...), body)
}

func TestFirstMessageCreatesConversationContactOrganization(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	raw := e.ingest(inbound("a1@acme.example", "Jane Doe <Jane@Acme.example>", "Order 1234", "Where is my parcel?",
		"Cc: Bob <bob@acme.example>"), 0)

	if st, _ := e.rawStatus(raw); st != "parsed" {
		t.Fatalf("raw status = %s", st)
	}
	var subject, status, preview string
	var count int
	var hasAtt bool
	var lastMsg, lastIn time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT subject, status, preview, message_count, has_attachments, last_message_at, last_inbound_at
		FROM conversations`).Scan(&subject, &status, &preview, &count, &hasAtt, &lastMsg, &lastIn); err != nil {
		t.Fatal(err)
	}
	if subject != "Order 1234" || status != "open" || preview != "Where is my parcel?" || count != 1 || hasAtt {
		t.Errorf("conversation = %q %q %q %d %v", subject, status, preview, count, hasAtt)
	}
	if !lastMsg.Equal(e.base) || !lastIn.Equal(e.base) {
		t.Errorf("last_message_at = %v, last_inbound_at = %v, want %v", lastMsg, lastIn, e.base)
	}

	var name, org string
	e.scan(&name, `SELECT c.name FROM contacts c JOIN contact_addresses a ON a.contact_id = c.id WHERE a.email = 'jane@acme.example'`)
	e.scan(&org, `SELECT o.name FROM organizations o JOIN contacts c ON c.organization_id = o.id JOIN contact_addresses a ON a.contact_id = c.id WHERE a.email = 'jane@acme.example'`)
	if name != "Jane Doe" || org != "acme.example" {
		t.Errorf("contact name = %q, organization = %q", name, org)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE contact_id = (SELECT contact_id FROM contact_addresses WHERE email = 'jane@acme.example')`); n != 1 {
		t.Errorf("conversation not linked to contact")
	}

	var kind, dir, from, to string
	var sentAt time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT kind, direction, from_addr, to_addrs::text, sent_at FROM messages`).Scan(&kind, &dir, &from, &to, &sentAt); err != nil {
		t.Fatal(err)
	}
	if kind != "email" || dir != "in" || from != "jane@acme.example" || !strings.Contains(to, mailboxAddr) {
		t.Errorf("message = %s %s %s %s", kind, dir, from, to)
	}
	if want := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC); !sentAt.Equal(want) {
		t.Errorf("sent_at = %v", sentAt)
	}
	conv := e.convOf("a1@acme.example")
	if e.events(conv, "created") != 1 {
		t.Errorf("created events = %d", e.events(conv, "created"))
	}
	var reason string
	e.scan(&reason, `SELECT data ->> 'reason' FROM conversation_events WHERE conversation_id = $1`, conv)
	if reason != "new_conversation" {
		t.Errorf("event reason = %q", reason)
	}
	if n := e.count(`SELECT count(*) FROM thread_refs WHERE conversation_id = $1`, conv); n != 1 {
		t.Errorf("thread refs = %d", n)
	}
}

func TestFreeMailSenderGetsNoOrganization(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("g1@gmail.example", "Piet <piet@gmail.com>", "Vraag over levering", "Hallo"), 0)
	if n := e.count(`SELECT count(*) FROM organizations`); n != 0 {
		t.Errorf("organizations = %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM contacts WHERE organization_id IS NULL AND name = 'Piet'`); n != 1 {
		t.Errorf("contact without organization not found")
	}
}

func TestExistingOrganizationIsReused(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.exec(`INSERT INTO organizations (name, domains) VALUES ('ACME BV', ARRAY['acme.example', 'acme.nl'])`)
	e.ingest(inbound("o1@acme.example", "Jane <jane@acme.example>", "Eerste vraag", "Hallo"), 0)
	if n := e.count(`SELECT count(*) FROM organizations`); n != 1 {
		t.Errorf("organizations = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM contacts c JOIN organizations o ON o.id = c.organization_id WHERE o.name = 'ACME BV'`); n != 1 {
		t.Errorf("contact not linked to existing organization")
	}
}

func TestOwnAddressGetsNoContact(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("s1@shop.example", "Support <"+mailboxAddr+">", "Test", "Body"), 0)
	if n := e.count(`SELECT count(*) FROM contacts`); n != 0 {
		t.Errorf("contacts = %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE contact_id IS NULL`); n != 1 {
		t.Errorf("conversation should exist without contact")
	}
}

func TestReplyJoinsThread(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order 1234", "First"), 0)
	var v1 int
	e.scan(&v1, `SELECT version FROM conversations`)

	e.ingest(inbound("a2@acme.example", "JANE D <jane@acme.example>", "Re: Order 1234",
		"Thanks for the quick   answer.\n> quoted line\n\n  Second   line",
		"In-Reply-To: <a1@acme.example>", "References: <a1@acme.example>"), time.Hour)

	if n := e.count(`SELECT count(*) FROM conversations`); n != 1 {
		t.Fatalf("conversations = %d, want 1", n)
	}
	var count, version int
	var preview string
	var lastMsg time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT message_count, version, preview, last_message_at FROM conversations`).Scan(&count, &version, &preview, &lastMsg); err != nil {
		t.Fatal(err)
	}
	if count != 2 || version != v1+1 || preview != "Thanks for the quick answer. Second line" || !lastMsg.Equal(e.base.Add(time.Hour)) {
		t.Errorf("conversation = %d %d %q %v", count, version, preview, lastMsg)
	}
	if e.convOf("a1@acme.example") != e.convOf("a2@acme.example") {
		t.Error("messages in different conversations")
	}
	if n := e.count(`SELECT count(*) FROM contacts`); n != 1 {
		t.Errorf("contacts = %d, want 1", n)
	}
	var name string
	e.scan(&name, `SELECT name FROM contacts`)
	if name != "Jane" {
		t.Errorf("contact name overwritten: %q", name)
	}
	if n := e.count(`SELECT count(*) FROM conversation_events WHERE type = 'created'`); n != 1 {
		t.Errorf("created events = %d, want 1", n)
	}
}

func TestOlderMessageKeepsPreviewAndLastMessage(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("b2@acme.example", "Jane <jane@acme.example>", "Order 1234", "Newest",
		"In-Reply-To: <b1@acme.example>"), time.Hour)
	e.ingest(inbound("b1@acme.example", "Jane <jane@acme.example>", "Order 1234", "Oldest"), 0)

	var preview string
	var count int
	var lastMsg time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT preview, message_count, last_message_at FROM conversations`).Scan(&preview, &count, &lastMsg); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM conversations`); n != 1 {
		t.Fatalf("conversations = %d, want 1 (parent arriving late must join through thread_refs)", n)
	}
	if preview != "Newest" || count != 2 || !lastMsg.Equal(e.base.Add(time.Hour)) {
		t.Errorf("conversation = %q %d %v", preview, count, lastMsg)
	}
}

func TestSameJobTwiceIsNoOp(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	raw := e.ingest(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	if err := e.work(raw); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM messages`); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
	var count, version int
	e.scan(&count, `SELECT message_count FROM conversations`)
	e.scan(&version, `SELECT version FROM conversations`)
	if count != 1 || version != 2 {
		t.Errorf("message_count = %d, version = %d", count, version)
	}
}

func TestRefetchedIdenticalRawCreatesNoSecondMessage(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	data := inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello")
	first := e.ingest(data, 0)
	second := e.ingest(data, time.Minute)

	if n := e.count(`SELECT count(*) FROM messages`); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM conversations`); n != 1 {
		t.Errorf("conversations = %d, want 1", n)
	}
	for _, id := range []string{first, second} {
		if st, _ := e.rawStatus(id); st != "parsed" {
			t.Errorf("raw %s status = %s", id, st)
		}
	}
}

func TestSameMessageIDDifferentContentIsKept(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("dup@acme.example", "Jane <jane@acme.example>", "Order", "Version one"), 0)
	e.ingest(inbound("dup@acme.example", "Jane <jane@acme.example>", "Order", "Version two"), time.Minute)
	if n := e.count(`SELECT count(*) FROM messages`); n != 2 {
		t.Errorf("messages = %d, want 2", n)
	}
}

func TestAttachments(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	pdf := "%PDF-1.4\n1 0 obj\n<<>>\nendobj\n"
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	raw := msg([]string{
		"Message-ID: <att@acme.example>",
		"From: Jane <jane@acme.example>",
		"To: " + mailboxAddr,
		"Subject: Invoice",
		"MIME-Version: 1.0",
		"Content-Type: multipart/mixed; boundary=XX",
	}, strings.Join([]string{
		"--XX",
		"Content-Type: text/html; charset=utf-8",
		"",
		`<p>See <img src="cid:logo@acme"></p>`,
		"--XX",
		"Content-Type: image/png; name=logo.png",
		"Content-Disposition: inline; filename=logo.png",
		"Content-ID: <logo@acme>",
		"Content-Transfer-Encoding: base64",
		"",
		enc(png),
		"--XX",
		"Content-Type: application/octet-stream; name=invoice.pdf",
		"Content-Disposition: attachment; filename=invoice.pdf",
		"Content-Transfer-Encoding: base64",
		"",
		enc(pdf),
		"--XX--",
	}, "\r\n"))
	e.ingest(raw, 0)

	var hasAtt bool
	e.scan(&hasAtt, `SELECT has_attachments FROM conversations`)
	if !hasAtt {
		t.Error("has_attachments = false")
	}
	rows, err := e.pool.Query(context.Background(), `SELECT filename, declared_type, sniffed_type, disposition, content_id, size_bytes, blob_key FROM attachments ORDER BY filename`)
	if err != nil {
		t.Fatal(err)
	}
	type att struct {
		name, declared, sniffed, disposition, cid, key string
		size                                           int64
	}
	var got []att
	for rows.Next() {
		var a att
		if err := rows.Scan(&a.name, &a.declared, &a.sniffed, &a.disposition, &a.cid, &a.size, &a.key); err != nil {
			t.Fatal(err)
		}
		got = append(got, a)
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("attachments = %+v", got)
	}
	if a := got[0]; a.name != "invoice.pdf" || a.declared != "application/octet-stream" || a.sniffed != "application/pdf" || a.disposition != "attachment" || a.cid != "" || a.size != int64(len(pdf)) {
		t.Errorf("pdf = %+v", a)
	}
	if a := got[1]; a.name != "logo.png" || a.sniffed != "image/png" || a.disposition != "inline" || a.cid != "logo@acme" {
		t.Errorf("png = %+v", a)
	}
	rc, err := e.store.Open(context.Background(), got[0].key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil || buf.String() != pdf {
		t.Errorf("stored blob = %q, %v", buf.String(), err)
	}
}

func (e *env) setConversation(id, set string) {
	e.t.Helper()
	e.exec(`UPDATE conversations SET `+set+` WHERE id = $1`, id)
}

func (e *env) convStatus(id string) (status string, resolved, snoozed bool) {
	e.t.Helper()
	e.scan(&status, `SELECT status FROM conversations WHERE id = $1`, id)
	e.scan(&resolved, `SELECT resolved_at IS NOT NULL FROM conversations WHERE id = $1`, id)
	e.scan(&snoozed, `SELECT snoozed_until IS NOT NULL FROM conversations WHERE id = $1`, id)
	return status, resolved, snoozed
}

func TestCustomerReplyReopens(t *testing.T) {
	for _, from := range []string{"closed", "waiting"} {
		t.Run(from, func(t *testing.T) {
			e := newEnv(t, parse.Limits{})
			e.ingest(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
			conv := e.convOf("a1@acme.example")
			e.setConversation(conv, fmt.Sprintf("status = '%s', resolved_at = now(), snoozed_until = now() + interval '1 day'", from))

			e.ingest(inbound("a2@acme.example", "Jane <jane@acme.example>", "Re: Order", "Still broken",
				"In-Reply-To: <a1@acme.example>"), time.Hour)

			status, resolved, snoozed := e.convStatus(conv)
			if status != "open" || resolved || snoozed {
				t.Errorf("status = %s, resolved = %v, snoozed = %v", status, resolved, snoozed)
			}
			if e.events(conv, "reopened") != 1 || e.events(conv, "woke") != 1 {
				t.Errorf("reopened = %d, woke = %d", e.events(conv, "reopened"), e.events(conv, "woke"))
			}
		})
	}
}

func TestSnoozedOpenConversationWakes(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	conv := e.convOf("a1@acme.example")
	e.setConversation(conv, "snoozed_until = now() + interval '1 day'")
	e.ingest(inbound("a2@acme.example", "Jane <jane@acme.example>", "Re: Order", "Hi again",
		"In-Reply-To: <a1@acme.example>"), time.Hour)
	if _, _, snoozed := e.convStatus(conv); snoozed {
		t.Error("snooze not cleared")
	}
	if e.events(conv, "woke") != 1 || e.events(conv, "reopened") != 0 {
		t.Errorf("woke = %d, reopened = %d", e.events(conv, "woke"), e.events(conv, "reopened"))
	}
}

func TestAutoReplyDoesNotReopen(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	conv := e.convOf("a1@acme.example")
	e.setConversation(conv, "status = 'closed', resolved_at = now(), snoozed_until = now() + interval '1 day'")

	e.ingest(inbound("a2@acme.example", "Jane <jane@acme.example>", "Automatic reply: Order", "I am out of office",
		"In-Reply-To: <a1@acme.example>", "Auto-Submitted: auto-replied"), time.Hour)

	status, resolved, snoozed := e.convStatus(conv)
	if status != "closed" || !resolved || !snoozed {
		t.Errorf("status = %s, resolved = %v, snoozed = %v", status, resolved, snoozed)
	}
	if e.events(conv, "reopened") != 0 || e.events(conv, "woke") != 0 {
		t.Error("unexpected reopen events")
	}
	var auto bool
	e.scan(&auto, `SELECT auto_submitted FROM messages WHERE message_id_header = 'a2@acme.example'`)
	if !auto {
		t.Error("auto_submitted not stored")
	}
	var count int
	e.scan(&count, `SELECT message_count FROM conversations WHERE id = $1`, conv)
	if count != 2 {
		t.Errorf("message_count = %d, want 2", count)
	}
}

func TestSpamStaysSpam(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	conv := e.convOf("a1@acme.example")
	e.setConversation(conv, "status = 'spam'")
	e.ingest(inbound("a2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More",
		"In-Reply-To: <a1@acme.example>"), time.Hour)
	if status, _, _ := e.convStatus(conv); status != "spam" {
		t.Errorf("status = %s, want spam", status)
	}
	if e.events(conv, "reopened") != 0 {
		t.Error("spam conversation reopened")
	}
}

func dsnMessage(original, action string) string {
	return msg([]string{
		"Message-ID: <dsn1@mx.shop.example>",
		"From: MAILER-DAEMON@mx.shop.example (Mail Delivery System)",
		"To: " + mailboxAddr,
		"Subject: Undelivered Mail Returned to Sender",
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
		"Reporting-MTA: dns; mx.shop.example",
		"",
		"Final-Recipient: rfc822; nobody@example.net",
		"Action: " + action,
		"Status: 5.1.1",
		"Diagnostic-Code: smtp; 550 5.1.1 User unknown",
		"",
		"--RR",
		"Content-Type: text/rfc822-headers",
		"",
		"Message-ID: <" + original + ">",
		"Subject: Hello",
		"--RR--",
	}, "\r\n"))
}

// seedOutbound creates a conversation with one outbound message in the given state.
func (e *env) seedOutbound(messageID, status string) (conv, msgID string) {
	e.t.Helper()
	e.scan(&conv, `INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 'Hello') RETURNING id::text`, e.mailbox)
	e.scan(&msgID, `INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, message_id_hash, from_addr)
		VALUES ($1, $2, 'email', 'out', $3, $4, $5) RETURNING id::text`,
		conv, e.mailbox, messageID, mail.HashMessageID(messageID), mailboxAddr)
	e.exec(`INSERT INTO outbound (message_id, idempotency_key, status) VALUES ($1, uuidv7(), $2)`, msgID, status)
	return conv, msgID
}

func TestDSNMarksOutboundBounced(t *testing.T) {
	for _, from := range []string{"sent", "uncertain"} {
		t.Run(from, func(t *testing.T) {
			e := newEnv(t, parse.Limits{})
			conv, msgID := e.seedOutbound("echoo.out1@shop.example", from)
			raw := e.ingest(dsnMessage("echoo.out1@shop.example", "failed"), 0)

			var status, dsn, diag string
			if err := e.pool.QueryRow(context.Background(), `SELECT status, dsn_status, error FROM outbound WHERE message_id = $1`, msgID).Scan(&status, &dsn, &diag); err != nil {
				t.Fatal(err)
			}
			if status != "bounced" || dsn != "5.1.1" || !strings.Contains(diag, "User unknown") {
				t.Errorf("outbound = %s %s %q", status, dsn, diag)
			}
			if n := e.count(`SELECT count(*) FROM messages WHERE direction = 'in'`); n != 0 {
				t.Errorf("inbound messages = %d, want 0", n)
			}
			if e.events(conv, "bounced") != 1 {
				t.Errorf("bounced events = %d", e.events(conv, "bounced"))
			}
			if n := e.count(`SELECT count(*) FROM conversations`); n != 1 {
				t.Errorf("conversations = %d, want 1", n)
			}
			if st, _ := e.rawStatus(raw); st != "parsed" {
				t.Errorf("raw status = %s", st)
			}
		})
	}
}

func TestDSNDoesNotOverwriteOtherStates(t *testing.T) {
	cases := []struct{ name, status, action string }{
		{"delayed", "sent", "delayed"},
		{"already bounced", "bounced", "failed"},
		{"cancelled", "cancelled", "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, parse.Limits{})
			conv, msgID := e.seedOutbound("echoo.out1@shop.example", c.status)
			e.ingest(dsnMessage("echoo.out1@shop.example", c.action), 0)
			var status string
			e.scan(&status, `SELECT status FROM outbound WHERE message_id = $1`, msgID)
			if status != c.status {
				t.Errorf("status = %s, want %s", status, c.status)
			}
			if e.events(conv, "bounced") != 0 {
				t.Error("bounced event recorded")
			}
			if n := e.count(`SELECT count(*) FROM messages WHERE direction = 'in'`); n != 0 {
				t.Errorf("inbound messages = %d, want 0", n)
			}
		})
	}
}

func TestDSNWithoutMatchBecomesMessage(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.seedOutbound("echoo.other@shop.example", "sent")
	e.ingest(dsnMessage("unknown@elsewhere.example", "failed"), 0)

	if n := e.count(`SELECT count(*) FROM messages WHERE direction = 'in' AND message_id_header = 'dsn1@mx.shop.example'`); n != 1 {
		t.Errorf("dsn messages = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM outbound WHERE status = 'sent'`); n != 1 {
		t.Error("unrelated outbound changed")
	}
	if n := e.count(`SELECT count(*) FROM conversation_events WHERE type = 'bounced'`); n != 0 {
		t.Errorf("bounced events = %d", n)
	}
}

func TestParseLimitSkips(t *testing.T) {
	e := newEnv(t, parse.Limits{MaxMessageBytes: 200})
	raw := e.ingest(inbound("big@acme.example", "Jane <jane@acme.example>", "Big", strings.Repeat("x", 500)), 0)
	st, detail := e.rawStatus(raw)
	if st != "skipped" || !strings.Contains(detail, "too large") {
		t.Errorf("status = %s, detail = %q", st, detail)
	}
	if n := e.count(`SELECT count(*) FROM conversations`); n != 0 {
		t.Errorf("conversations = %d, want 0", n)
	}
	if err := e.work(raw); err != nil {
		t.Fatal(err)
	}
}

func TestStructuralLimitSkips(t *testing.T) {
	e := newEnv(t, parse.Limits{MaxDepth: 2})
	nested := msg([]string{
		"Message-ID: <deep@acme.example>", "From: jane@acme.example", "Subject: Deep",
		"Content-Type: multipart/mixed; boundary=A",
	}, strings.Join([]string{
		"--A", "Content-Type: multipart/mixed; boundary=B", "",
		"--B", "Content-Type: multipart/mixed; boundary=C", "",
		"--C", "Content-Type: text/plain", "", "x", "--C--", "--B--", "--A--",
	}, "\r\n"))
	raw := e.ingest(nested, 0)
	if st, _ := e.rawStatus(raw); st != "skipped" {
		t.Errorf("status = %s, want skipped", st)
	}
}

func TestParseFailureIsRecordedNotRetried(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.worker.parse = func([]byte, parse.Limits) (*mail.Parsed, error) {
		return nil, errors.New("parse: read header: corrupt")
	}
	raw := e.addRaw("garbage", 0)
	if err := e.work(raw); err != nil {
		t.Fatalf("Work returned %v, want nil", err)
	}
	st, detail := e.rawStatus(raw)
	if st != "failed" || detail != "parse: read header: corrupt" {
		t.Errorf("status = %s, detail = %q", st, detail)
	}
	if n := e.count(`SELECT count(*) FROM conversations`); n != 0 {
		t.Errorf("conversations = %d, want 0", n)
	}
}

func TestFailedRawCanBeRetried(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	real := e.worker.parse
	e.worker.parse = func([]byte, parse.Limits) (*mail.Parsed, error) { return nil, errors.New("boom") }
	raw := e.addRaw(inbound("a1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	if err := e.work(raw); err != nil {
		t.Fatal(err)
	}
	e.worker.parse = real
	if err := e.work(raw); err != nil {
		t.Fatal(err)
	}
	st, detail := e.rawStatus(raw)
	if st != "parsed" || detail != "" {
		t.Errorf("status = %s, detail = %q", st, detail)
	}
	if n := e.count(`SELECT count(*) FROM messages`); n != 1 {
		t.Errorf("messages = %d, want 1", n)
	}
}

func TestMissingBlobIsRetryableError(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	var id string
	e.scan(&id, `INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key)
		VALUES ($1, 'webhook', $2, 1, 'missing/blob') RETURNING id::text`, e.mailbox, make([]byte, 32))
	err := e.work(id)
	if err == nil || !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Work = %v, want storage.ErrNotFound", err)
	}
	if st, _ := e.rawStatus(id); st != "pending" {
		t.Errorf("status = %s, want pending", st)
	}
}

func TestUnknownRawIsCancelled(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	var cancel *river.JobCancelError
	if err := e.work("0190f3a0-0000-7000-8000-000000000000"); !errors.As(err, &cancel) {
		t.Errorf("Work = %v, want job cancel", err)
	}
	if err := e.work("not-a-uuid"); !errors.As(err, &cancel) {
		t.Errorf("Work = %v, want job cancel", err)
	}
}

func TestUnparsableFromFallsBackToReplyTo(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	raw := msg([]string{
		"Message-ID: <rt@client.example>",
		"From: this is not an address",
		"Reply-To: Help Desk <Help@Client.example>",
		"To: " + mailboxAddr,
		"Subject: Question",
	}, "Hello")
	e.ingest(raw, 0)

	var from, name string
	e.scan(&from, `SELECT from_addr FROM messages`)
	e.scan(&name, `SELECT c.name FROM contacts c JOIN contact_addresses a ON a.contact_id = c.id WHERE a.email = 'help@client.example'`)
	if from != "help@client.example" || name != "Help Desk" {
		t.Errorf("from_addr = %q, contact name = %q", from, name)
	}
	if n := e.count(`SELECT count(*) FROM organizations WHERE domains = ARRAY['client.example']`); n != 1 {
		t.Error("organization for reply-to domain missing")
	}
}

func TestSenderHeaderFallbackBeforeReplyTo(t *testing.T) {
	p := &mail.Parsed{
		Sender:  &mail.Address{Address: "sender@x.example"},
		ReplyTo: []mail.Address{{Address: "reply@x.example"}},
	}
	if got := senderOf(p).Address; got != "sender@x.example" {
		t.Errorf("senderOf = %q", got)
	}
	p.Sender = nil
	p.ReplyTo = []mail.Address{{}, {Address: "reply@x.example"}}
	if got := senderOf(p).Address; got != "reply@x.example" {
		t.Errorf("senderOf = %q", got)
	}
}

func TestPreview(t *testing.T) {
	long := strings.Repeat("é", 300)
	cases := []struct{ in, want string }{
		{"", ""},
		{"> only quote\n>> more", ""},
		{"Hi\n> quoted\n  there  ", "Hi there"},
		{long, strings.Repeat("é", 200)},
	}
	for _, c := range cases {
		if got := preview(c.in); got != c.want {
			t.Errorf("preview(%.20q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCustomerMessageNotifiesTheAssignee(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	var agent string
	e.scan(&agent, `INSERT INTO users (email, name, role, password_hash) VALUES ('agent@shop.example', 'Agent', 'agent', 'x') RETURNING id::text`)
	notifications := func() int {
		return e.count(`SELECT count(*) FROM notifications WHERE kind = 'reply' AND user_id = $1`, agent)
	}

	e.ingest(inbound("n1@acme.example", "Jane <jane@acme.example>", "Order", "Hello"), 0)
	conv := e.convOf("n1@acme.example")
	if n := notifications(); n != 0 {
		t.Fatalf("an unassigned conversation notified %d users", n)
	}

	e.exec(`UPDATE conversations SET assignee_user_id = $1 WHERE id = $2`, agent, conv)
	e.ingest(inbound("n2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More", "In-Reply-To: <n1@acme.example>"), time.Minute)
	if n := notifications(); n != 1 {
		t.Fatalf("reply notifications = %d, want 1", n)
	}
	var msgOK bool
	e.scan(&msgOK, `SELECT message_id = (SELECT id FROM messages WHERE message_id_header = 'n2@acme.example') AND conversation_id = $1::uuid
		FROM notifications WHERE kind = 'reply'`, conv)
	if !msgOK {
		t.Error("the notification does not point at the new message")
	}

	e.ingest(inbound("n3@acme.example", "Jane <jane@acme.example>", "Re: Order", "Out of office", "In-Reply-To: <n1@acme.example>", "Auto-Submitted: auto-replied"), 2*time.Minute)
	if n := notifications(); n != 1 {
		t.Fatalf("an auto-reply added a notification: %d", n)
	}

	e.ingest(inbound("n2@acme.example", "Jane <jane@acme.example>", "Re: Order", "More", "In-Reply-To: <n1@acme.example>"), time.Minute)
	if n := notifications(); n != 1 {
		t.Fatalf("a duplicate delivery added a notification: %d", n)
	}
}

type stubScanner map[string]scan.Result

func (s stubScanner) Scan(_ context.Context, data []byte) scan.Result { return s[string(data)] }

func TestAttachmentsAreScannedAtIngest(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.worker.d.Scanner = stubScanner{
		"clean file":    {Status: scan.StatusClean},
		"virus file":    {Status: scan.StatusInfected, Detail: "Eicar-Test-Signature"},
		"unknown state": {Status: scan.StatusError, Detail: "connect to clamd: refused"},
	}
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	part := func(name, data string) []string {
		return []string{
			"--XX",
			"Content-Type: application/octet-stream; name=" + name,
			"Content-Disposition: attachment; filename=" + name,
			"Content-Transfer-Encoding: base64",
			"",
			enc(data),
		}
	}
	body := append(append(part("a.bin", "clean file"), part("b.bin", "virus file")...), part("c.bin", "unknown state")...)
	e.ingest(msg([]string{
		"Message-ID: <scan@acme.example>", "From: Jane <jane@acme.example>", "To: " + mailboxAddr,
		"Subject: Files", "MIME-Version: 1.0", "Content-Type: multipart/mixed; boundary=XX",
	}, strings.Join(append(body, "--XX--"), "\r\n")), 0)

	rows, err := e.pool.Query(context.Background(), `SELECT filename, scan_status, scan_detail FROM attachments ORDER BY filename`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name, status, detail string
		if err := rows.Scan(&name, &status, &detail); err != nil {
			t.Fatal(err)
		}
		got = append(got, name+"|"+status+"|"+detail)
	}
	want := []string{"a.bin|clean|", "b.bin|infected|Eicar-Test-Signature", "c.bin|error|connect to clamd: refused"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAttachmentsStayNotScannedWithoutScanner(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	e.ingest(msg([]string{
		"Message-ID: <noscan@acme.example>", "From: Jane <jane@acme.example>", "To: " + mailboxAddr,
		"Subject: File", "MIME-Version: 1.0", "Content-Type: multipart/mixed; boundary=XX",
	}, strings.Join([]string{
		"--XX", "Content-Type: application/octet-stream; name=a.bin", "Content-Disposition: attachment; filename=a.bin",
		"Content-Transfer-Encoding: base64", "", base64.StdEncoding.EncodeToString([]byte("data")), "--XX--",
	}, "\r\n")), 0)
	if n := e.count(`SELECT count(*) FROM attachments WHERE scan_status = 'not_scanned'`); n != 1 {
		t.Fatalf("not_scanned attachments = %d, want 1", n)
	}
}

// hangingScanner blocks until its context ends, like a clamd that accepts and never answers.
type hangingScanner struct{ calls int }

func (h *hangingScanner) Scan(ctx context.Context, _ []byte) scan.Result {
	h.calls++
	<-ctx.Done()
	return scan.Result{Status: scan.StatusError, Detail: ctx.Err().Error()}
}

func TestHangingScannerCannotStallIngest(t *testing.T) {
	e := newEnv(t, parse.Limits{})
	h := &hangingScanner{}
	e.worker.d.Scanner = h
	e.worker.scanBudget = 200 * time.Millisecond
	enc := base64.StdEncoding.EncodeToString([]byte("payload"))
	var body []string
	for _, n := range []string{"a.bin", "b.bin", "c.bin"} {
		body = append(body, "--XX", "Content-Type: application/octet-stream; name="+n, "Content-Disposition: attachment; filename="+n,
			"Content-Transfer-Encoding: base64", "", enc)
	}
	started := time.Now()
	e.ingest(msg([]string{
		"Message-ID: <hang@acme.example>", "From: Jane <jane@acme.example>", "To: " + mailboxAddr,
		"Subject: Files", "MIME-Version: 1.0", "Content-Type: multipart/mixed; boundary=XX",
	}, strings.Join(append(body, "--XX--"), "\r\n")), 0)
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("ingest took %s with a hanging scanner", took)
	}
	if h.calls != 1 {
		t.Errorf("scanner called %d times, want once: the budget ends the rest", h.calls)
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM attachments WHERE scan_status = 'error'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("attachments with scan error = %d (%v), want 3", n, err)
	}
}
