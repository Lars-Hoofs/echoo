package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/storage"
)

// pngHeader is enough for content sniffing to report image/png.
var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

type composerFixture struct {
	*inboxFixture
	store       storage.Store
	conv        string // in mailbox A, with contact Jan and one inbound message
	contact     string
	inboundID   string
	agentUserID string
	readUser    *client // read-only access to B through otherTeam, role agent
	readUserID  string
	adminID     string
}

func newComposerFixture(t *testing.T) *composerFixture {
	t.Helper()
	f := &composerFixture{inboxFixture: newInboxFixture(t)}
	rc, err := river.NewClient(riverpgxv5.New(f.h.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.h.srv.jobs = rc
	if f.store, err = storage.NewFS(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	f.h.srv.store = f.store
	f.agentUserID = f.agentID

	f.contact = f.queryID(`INSERT INTO contacts (name) VALUES ('Jan de Vries') RETURNING id`)
	f.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'jan@customer.nl', true)`, f.contact)
	f.conv = f.conversation("Vraag over factuur", convOpt{mailbox: f.mailboxA, contact: f.contact, assignee: f.agentID})
	f.inboundID = f.queryID(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, to_addrs, cc_addrs, subject, body_text, received_at)
		VALUES ($1, $2, 'email', 'in', 'abc@customer.nl', 'jan@customer.nl', 'Jan de Vries', '[{"name":"","address":"alpha@example.com"}]', '[{"name":"Kim","address":"kim@customer.nl"}]', 'Vraag over factuur', 'Klopt mijn factuur wel?', now() - interval '1 hour') RETURNING id`, f.conv, f.mailboxA)

	c, u := f.h.loggedIn("agent")
	f.readUser, f.readUserID = c, u.ID.String()
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.otherTeam, u.ID)
	f.adminID = f.queryID(`SELECT id FROM users WHERE role = 'admin'`)
	return f
}

func (f *composerFixture) replyBody(extra map[string]any) map[string]any {
	body := map[string]any{"idempotency_key": newUUID(f.h.t), "html": "<p>Dank voor je bericht.</p>"}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func newUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}.String()
}

func (f *composerFixture) scalar(sql string, args ...any) string {
	f.h.t.Helper()
	var v any
	if err := f.h.pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		f.h.t.Fatal(err)
	}
	if id, ok := v.([16]byte); ok {
		return pgtype.UUID{Bytes: id, Valid: true}.String()
	}
	return fmt.Sprint(v)
}

func (f *composerFixture) send(c *client, conv string, body map[string]any) response {
	return c.do("POST", "/api/v1/conversations/"+conv+"/replies", body)
}

func messageOf(t *testing.T, r response) map[string]any {
	t.Helper()
	m, ok := r.body["message"].(map[string]any)
	if !ok {
		t.Fatalf("no message in %s", r.raw)
	}
	return m
}

func (f *composerFixture) upload(c *client, filename string, data []byte) response {
	f.h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	hdr.Set("Content-Type", "text/plain")
	part, err := mw.CreatePart(hdr)
	if err != nil {
		f.h.t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		f.h.t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		f.h.t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.h.ts.URL+"/api/v1/uploads", &buf)
	if err != nil {
		f.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", c.origin)
	req.Header.Set("X-Forwarded-For", c.ip)
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		f.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.h.t.Fatal(err)
	}
	out := response{status: resp.StatusCode, header: resp.Header, raw: raw}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			f.h.t.Fatalf("decode upload response: %v: %s", err, raw)
		}
	}
	return out
}

func TestReplyDefaults(t *testing.T) {
	f := newComposerFixture(t)
	r := f.agent.do("GET", "/api/v1/conversations/"+f.conv+"/reply-defaults", nil)
	expect(t, r, 200, "")
	var out struct {
		To      []struct{ Name, Address string }
		Cc      []struct{ Name, Address string }
		Subject string
		From    struct{ Address string }
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.To) != 1 || out.To[0].Address != "jan@customer.nl" {
		t.Errorf("to = %+v", out.To)
	}
	if len(out.Cc) != 1 || out.Cc[0].Address != "kim@customer.nl" {
		t.Errorf("cc = %+v (our own address must not appear)", out.Cc)
	}
	if out.Subject != "Re: Vraag over factuur" || out.From.Address != "alpha@example.com" {
		t.Errorf("subject %q from %q", out.Subject, out.From.Address)
	}
}

func TestReplyDefaultsNeverSwitchCustomerToStranger(t *testing.T) {
	f := newComposerFixture(t)
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, to_addrs, cc_addrs, subject, body_text, received_at)
		VALUES ($1, $2, 'email', 'in', 'forged@evil.example', 'attacker@evil.example', 'Attacker', '[{"name":"","address":"alpha@example.com"},{"name":"","address":"victim@evil.example"}]', '[]', 'Re: Vraag over factuur', 'Send it to me', now())`, f.conv, f.mailboxA)
	r := f.agent.do("GET", "/api/v1/conversations/"+f.conv+"/reply-defaults", nil)
	expect(t, r, 200, "")
	var out struct {
		To          []struct{ Address string }
		Cc          []struct{ Address string }
		SuggestedCc []struct{ Address string } `json:"suggested_cc"`
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.To) != 1 || out.To[0].Address != "jan@customer.nl" {
		t.Errorf("to = %+v, want the original customer", out.To)
	}
	for _, a := range out.Cc {
		if strings.HasSuffix(a.Address, "@evil.example") {
			t.Errorf("stranger %s in default cc", a.Address)
		}
	}
	if len(out.SuggestedCc) != 2 || out.SuggestedCc[0].Address != "attacker@evil.example" || out.SuggestedCc[1].Address != "victim@evil.example" {
		t.Errorf("suggested_cc = %+v", out.SuggestedCc)
	}
}

func TestReplyDefaultsIgnoreAStrangerHidingBehindReplyTo(t *testing.T) {
	f := newComposerFixture(t)
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, reply_to, to_addrs, cc_addrs, subject, body_text, received_at)
		VALUES ($1, $2, 'email', 'in', 'forged2@evil.example', 'a@evil.example', 'A', '[{"name":"","address":"jan@customer.nl"}]', '[{"name":"","address":"alpha@example.com"}]', '[{"name":"","address":"a@evil.example"}]', 'Re: Vraag over factuur', 'x', now())`, f.conv, f.mailboxA)
	r := f.agent.do("GET", "/api/v1/conversations/"+f.conv+"/reply-defaults", nil)
	expect(t, r, 200, "")
	var out struct {
		To          []struct{ Address string }
		Cc          []struct{ Address string }
		SuggestedCc []struct{ Address string } `json:"suggested_cc"`
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.To) != 1 || out.To[0].Address != "jan@customer.nl" {
		t.Errorf("to = %+v", out.To)
	}
	for _, a := range out.Cc {
		if a.Address == "a@evil.example" {
			t.Error("stranger in default cc")
		}
	}
	if len(out.SuggestedCc) != 1 || out.SuggestedCc[0].Address != "a@evil.example" {
		t.Errorf("suggested_cc = %+v", out.SuggestedCc)
	}
}

func TestSendReplyQueuesSanitizedMessage(t *testing.T) {
	f := newComposerFixture(t)
	key := newUUID(t)
	r := f.send(f.agent, f.conv, map[string]any{
		"idempotency_key": key,
		"html":            `<p onclick="x()">Hallo <strong>Jan</strong></p><script>alert(1)</script><img src="https://evil.test/track.png"><a href="javascript:alert(1)">klik</a>`,
	})
	expect(t, r, 201, "")
	msg := messageOf(t, r)
	if msg["outbound_status"] != "queued" || msg["direction"] != "out" {
		t.Fatalf("message = %v", msg)
	}
	if r.body["undo_until"] == nil {
		t.Error("undo_until missing")
	}
	id := msg["id"].(string)
	html := f.scalar(`SELECT body_html FROM messages WHERE id = $1`, id)
	for _, bad := range []string{"<script", "onclick", "evil.test", "javascript:"} {
		if strings.Contains(html, bad) {
			t.Errorf("stored HTML still contains %q: %s", bad, html)
		}
	}
	if !strings.Contains(html, "<strong>Jan</strong>") || !strings.Contains(html, "<table") {
		t.Errorf("stored HTML lacks content or layout: %s", html)
	}
	if text := f.scalar(`SELECT body_text FROM messages WHERE id = $1`, id); !strings.Contains(text, "Hallo Jan") || strings.Contains(text, "alert") {
		t.Errorf("text part = %q", text)
	}
	if got := f.scalar(`SELECT (to_addrs->0->>'address') || '|' || (cc_addrs->0->>'address') || '|' || in_reply_to || '|' || subject FROM messages WHERE id = $1`, id); got != "jan@customer.nl|kim@customer.nl|abc@customer.nl|Re: Vraag over factuur" {
		t.Errorf("defaults not applied: %s", got)
	}
	if got := f.scalar(`SELECT count(*) FROM river_job WHERE kind = 'mail.send'`); got != "1" {
		t.Errorf("send jobs = %s", got)
	}
	if got := f.scalar(`SELECT author_user_id FROM messages WHERE id = $1`, id); got != f.agentUserID {
		t.Errorf("author = %s", got)
	}
}

func TestSendReplyIsIdempotent(t *testing.T) {
	f := newComposerFixture(t)
	body := f.replyBody(nil)
	first := f.send(f.agent, f.conv, body)
	expect(t, first, 201, "")
	second := f.send(f.agent, f.conv, body)
	expect(t, second, 200, "")
	if messageOf(t, first)["id"] != messageOf(t, second)["id"] {
		t.Error("a repeated key must return the first message")
	}
	if got := f.scalar(`SELECT count(*) FROM messages WHERE direction = 'out'`); got != "1" {
		t.Errorf("outgoing messages = %s", got)
	}
	if got := f.scalar(`SELECT count(*) FROM river_job WHERE kind = 'mail.send'`); got != "1" {
		t.Errorf("send jobs = %s", got)
	}
	// Someone else must not be able to read a message by guessing its key.
	expect(t, f.admin.do("POST", "/api/v1/conversations/"+f.conv+"/replies", body), 409, "idempotency_conflict")
}

func TestSendReplyValidation(t *testing.T) {
	f := newComposerFixture(t)
	cases := map[string]struct {
		body  map[string]any
		field string
	}{
		"empty body":       {f.replyBody(map[string]any{"html": "<p> </p><script>x</script>"}), "html"},
		"bad address":      {f.replyBody(map[string]any{"to": []map[string]string{{"address": "nope"}}}), "to"},
		"header injection": {f.replyBody(map[string]any{"to": []map[string]string{{"address": "a@b.nl\r\nBcc: x@y.nl"}}}), "to"},
		"subject newline":  {f.replyBody(map[string]any{"subject": "a\r\nBcc: x@y.nl"}), "subject"},
		"bad key":          {f.replyBody(map[string]any{"idempotency_key": "x"}), "idempotency_key"},
		"bad status":       {f.replyBody(map[string]any{"status_after": "open"}), "status_after"},
		"unknown upload":   {f.replyBody(map[string]any{"attachment_ids": []string{newUUID(t)}}), "attachment_ids"},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			r := f.send(f.agent, f.conv, tt.body)
			expect(t, r, 422, "validation_failed")
			fields, _ := r.body["error"].(map[string]any)["fields"].(map[string]any)
			if _, ok := fields[tt.field]; !ok {
				t.Errorf("no error for %s: %s", tt.field, r.raw)
			}
		})
	}
	many := make([]map[string]string, 51)
	for i := range many {
		many[i] = map[string]string{"address": fmt.Sprintf("p%d@example.org", i)}
	}
	expect(t, f.send(f.agent, f.conv, f.replyBody(map[string]any{"to": many})), 422, "validation_failed")
	if got := f.scalar(`SELECT count(*) FROM messages WHERE direction = 'out'`); got != "0" {
		t.Errorf("a rejected request stored %s messages", got)
	}
}

func TestReplyAuthorization(t *testing.T) {
	f := newComposerFixture(t)
	convB := f.conversation("In B", convOpt{mailbox: f.mailboxB})
	body := f.replyBody(map[string]any{"to": []map[string]string{{"address": "x@example.org"}}})

	// readonly: can read A, must never write.
	expect(t, f.send(f.readonly, f.conv, body), 403, "forbidden")
	expect(t, f.readonly.do("POST", "/api/v1/conversations/"+f.conv+"/notes", map[string]any{"html": "<p>x</p>"}), 403, "forbidden")
	expect(t, f.readonly.do("PUT", "/api/v1/conversations/"+f.conv+"/draft", map[string]any{"html": "<p>x</p>"}), 403, "forbidden")
	expect(t, f.readonly.do("POST", "/api/v1/conversations", map[string]any{"mailbox_id": f.mailboxA, "subject": "s", "to": []map[string]string{{"address": "x@example.org"}}, "html": "<p>x</p>", "idempotency_key": newUUID(t)}), 403, "forbidden")
	expect(t, f.upload(f.readonly, "a.txt", []byte("x")), 403, "forbidden")
	// read-only grant on the mailbox: same.
	expect(t, f.send(f.readUser, convB, body), 403, "forbidden")
	// no access at all: the conversation does not exist.
	expect(t, f.send(f.agent, convB, body), 404, "not_found")
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+convB+"/notes", map[string]any{"html": "<p>x</p>"}), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+convB+"/reply-defaults", nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+convB+"/mentionable", nil), 404, "not_found")
	expect(t, f.agent.do("POST", "/api/v1/conversations", map[string]any{"mailbox_id": f.mailboxB, "subject": "s", "to": []map[string]string{{"address": "x@example.org"}}, "html": "<p>x</p>", "idempotency_key": newUUID(t)}), 404, "not_found")
	if got := f.scalar(`SELECT count(*) FROM messages WHERE direction = 'out' OR kind = 'note'`); got != "0" {
		t.Errorf("forbidden requests stored %s messages", got)
	}
}

func TestStatusAfterAndUndoSend(t *testing.T) {
	f := newComposerFixture(t)
	r := f.send(f.agent, f.conv, f.replyBody(map[string]any{"status_after": "closed"}))
	expect(t, r, 201, "")
	id := messageOf(t, r)["id"].(string)
	if got := f.scalar(`SELECT status FROM conversations WHERE id = $1`, f.conv); got != "closed" {
		t.Fatalf("status = %s", got)
	}
	if got := f.scalar(`SELECT string_agg(type, ',' ORDER BY id) FROM conversation_events WHERE conversation_id = $1`, f.conv); got != "resolved" {
		t.Errorf("events = %s, want the same resolved event the header action writes", got)
	}

	// Only the author may undo.
	expect(t, f.admin.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+id+"/cancel", nil), 403, "forbidden")
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+newUUID(t)+"/cancel", nil), 404, "not_found")
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+id+"/cancel", nil), 200, "")
	if got := f.scalar(`SELECT status FROM outbound WHERE message_id = $1`, id); got != "cancelled" {
		t.Errorf("outbound = %s", got)
	}
	if got := f.scalar(`SELECT status FROM conversations WHERE id = $1`, f.conv); got != "open" {
		t.Errorf("status after undo = %s, want open again", got)
	}
	if got := f.scalar(`SELECT string_agg(type, ',' ORDER BY id) FROM conversation_events WHERE conversation_id = $1`, f.conv); got != "resolved,reopened" {
		t.Errorf("events after undo = %s", got)
	}
	if got := f.scalar(`SELECT deleted_at IS NOT NULL FROM messages WHERE id = $1`, id); got != "true" {
		t.Error("a cancelled message must leave the thread")
	}
	// Cancelling twice is too late: nothing is queued any more.
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+id+"/cancel", nil), 404, "not_found")
}

func TestUndoSendGivesAttachmentsBack(t *testing.T) {
	f := newComposerFixture(t)
	up := f.upload(f.agent, "factuur.pdf", []byte("%PDF-1.4 test"))
	expect(t, up, 201, "")
	r := f.send(f.agent, f.conv, f.replyBody(map[string]any{"attachment_ids": []string{up.body["id"].(string)}}))
	expect(t, r, 201, "")
	id := messageOf(t, r)["id"].(string)
	if got := f.scalar(`SELECT count(*) FROM uploads`); got != "0" {
		t.Fatalf("uploads after send = %s", got)
	}

	r = f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+id+"/cancel", nil)
	expect(t, r, 200, "")
	restored, _ := r.body["attachments"].([]any)
	if len(restored) != 1 {
		t.Fatalf("restored = %s", r.raw)
	}
	back := restored[0].(map[string]any)
	if back["filename"] != "factuur.pdf" || back["id"] == up.body["id"] {
		t.Errorf("restored upload = %v", back)
	}
	// The restored file can be attached to the next attempt, and is the caller's own.
	expect(t, f.send(f.admin, f.conv, f.replyBody(map[string]any{"attachment_ids": []string{back["id"].(string)}})), 422, "validation_failed")
	expect(t, f.send(f.agent, f.conv, f.replyBody(map[string]any{"attachment_ids": []string{back["id"].(string)}})), 201, "")
}

func TestUndoSendTooLate(t *testing.T) {
	f := newComposerFixture(t)
	r := f.send(f.agent, f.conv, f.replyBody(nil))
	id := messageOf(t, r)["id"].(string)
	f.exec(`UPDATE outbound SET status = 'sending' WHERE message_id = $1`, id)
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+id+"/cancel", nil), 409, "too_late")
	if got := f.scalar(`SELECT deleted_at IS NULL FROM messages WHERE id = $1`, id); got != "true" {
		t.Error("a message that is being sent must stay in the thread")
	}
}

func TestUndoKeepsStatusChangedByAnotherAgent(t *testing.T) {
	f := newComposerFixture(t)
	id := messageOf(t, f.send(f.agent, f.conv, f.replyBody(map[string]any{"status_after": "waiting"})))["id"].(string)
	f.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, f.conv)
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/replies/"+id+"/cancel", nil), 200, "")
	if got := f.scalar(`SELECT status FROM conversations WHERE id = $1`, f.conv); got != "closed" {
		t.Errorf("status = %s; undo must not overwrite a later change", got)
	}
}

func TestUploads(t *testing.T) {
	f := newComposerFixture(t)

	t.Run("sniffs type and cleans name", func(t *testing.T) {
		r := f.upload(f.agent, `..\..\evil/plaatje.txt`, pngHeader)
		expect(t, r, 201, "")
		if r.body["content_type"] != "image/png" || r.body["filename"] != "plaatje.txt" {
			t.Errorf("upload = %v", r.body)
		}
		if r.body["expires_at"] == nil || r.body["content_id"] == "" {
			t.Errorf("upload = %v", r.body)
		}
	})
	t.Run("size limit", func(t *testing.T) {
		f.h.srv.cfg.MaxAttachmentMB = 1
		t.Cleanup(func() { f.h.srv.cfg.MaxAttachmentMB = 0 })
		expect(t, f.upload(f.agent, "ok.bin", bytes.Repeat([]byte("a"), 1<<20)), 201, "")
		expect(t, f.upload(f.agent, "big.bin", bytes.Repeat([]byte("a"), 1<<20+1)), 413, "file_too_large")
	})
	t.Run("empty file", func(t *testing.T) {
		expect(t, f.upload(f.agent, "empty.txt", nil), 422, "validation_failed")
	})
	t.Run("not multipart", func(t *testing.T) {
		expect(t, f.agent.do("POST", "/api/v1/uploads", map[string]string{}), 400, "invalid_request")
	})
	t.Run("ownership and expiry", func(t *testing.T) {
		mine := f.upload(f.agent, "mine.txt", []byte("hello"))
		expect(t, mine, 201, "")
		id := mine.body["id"].(string)
		// Another user, even with write access to the conversation, cannot attach it.
		expect(t, f.send(f.admin, f.conv, f.replyBody(map[string]any{"attachment_ids": []string{id}})), 422, "validation_failed")
		expect(t, f.admin.do("DELETE", "/api/v1/uploads/"+id, nil), 404, "not_found")

		f.exec(`UPDATE uploads SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
		expect(t, f.send(f.agent, f.conv, f.replyBody(map[string]any{"attachment_ids": []string{id}})), 422, "validation_failed")

		// Removing expired uploads and their files is the uploads.purge job's work
		// (internal/retention), not the next upload's.
		fresh := f.upload(f.agent, "fresh.txt", []byte("content nobody else has"))
		expect(t, fresh, 201, "")
		freshKey := f.scalar(`SELECT blob_key FROM uploads WHERE id = $1`, fresh.body["id"].(string))
		expect(t, f.agent.do("DELETE", "/api/v1/uploads/"+fresh.body["id"].(string), nil), 204, "")
		if rc, err := f.store.Open(context.Background(), freshKey); err == nil {
			_ = rc.Close()
			t.Error("deleting an upload must delete its file when nothing else uses it")
		}
	})
	t.Run("attachments are sent and released", func(t *testing.T) {
		a := f.upload(f.agent, "factuur.pdf", []byte("%PDF-1.4 test"))
		img := f.upload(f.agent, "logo.png", pngHeader)
		expect(t, a, 201, "")
		expect(t, img, 201, "")
		cid := img.body["content_id"].(string)
		r := f.send(f.agent, f.conv, f.replyBody(map[string]any{
			"html":           `<p>Zie bijlage</p><img src="cid:` + cid + `" alt="logo"><img src="cid:other@echoo.upload">`,
			"attachment_ids": []string{a.body["id"].(string), img.body["id"].(string)},
		}))
		expect(t, r, 201, "")
		id := messageOf(t, r)["id"].(string)
		rows := f.scalar(`SELECT string_agg(filename || ':' || disposition, ',' ORDER BY filename) FROM attachments WHERE message_id = $1`, id)
		if rows != "factuur.pdf:attachment,logo.png:inline" {
			t.Errorf("attachments = %s", rows)
		}
		html := f.scalar(`SELECT body_html FROM messages WHERE id = $1`, id)
		if !strings.Contains(html, "cid:"+cid) || strings.Contains(html, "other@echoo.upload") {
			t.Errorf("cid handling wrong: %s", html)
		}
		if got := f.scalar(`SELECT count(*) FROM uploads WHERE id = ANY($1::uuid[])`, []string{a.body["id"].(string), img.body["id"].(string)}); got != "0" {
			t.Errorf("uploads not released: %s", got)
		}
		if got := f.scalar(`SELECT has_attachments FROM conversations WHERE id = $1`, f.conv); got != "true" {
			t.Error("conversation must be flagged as having attachments")
		}
	})
}

func TestNotesMentionsAndNotifications(t *testing.T) {
	f := newComposerFixture(t)
	ctx := context.Background()
	conn, err := f.h.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `LISTEN echoo_events`); err != nil {
		t.Fatal(err)
	}

	// Mentionable users: the mailbox team, admins and the owner; not agents of other teams.
	r := f.agent.do("GET", "/api/v1/conversations/"+f.conv+"/mentionable", nil)
	expect(t, r, 200, "")
	if got := string(r.raw); !strings.Contains(got, f.agentUserID) || !strings.Contains(got, f.adminID) || strings.Contains(got, f.readUserID) {
		t.Errorf("mentionable = %s", got)
	}

	// Mention of someone without access to the mailbox is refused and stores nothing.
	r = f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/notes", map[string]any{"html": "<p>hoi</p>", "mentions": []string{f.readUserID}})
	expect(t, r, 422, "mention_no_access")
	if got := f.scalar(`SELECT count(*) FROM messages WHERE kind = 'note'`); got != "0" {
		t.Errorf("refused note was stored (%s)", got)
	}
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/notes", map[string]any{"html": "<p>hoi</p>", "mentions": []string{"x"}}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/notes", map[string]any{"html": "<p></p>"}), 422, "validation_failed")

	note := `<p>Kun jij dit <span data-type="mention" data-id="` + f.adminID + `" data-label="Test admin" onclick="x()">@Test admin</span> oppakken?</p><script>x</script>`
	r = f.agent.do("POST", "/api/v1/conversations/"+f.conv+"/notes", map[string]any{"html": note, "mentions": []string{f.adminID, f.adminID, f.agentUserID}})
	expect(t, r, 201, "")
	msg := messageOf(t, r)
	if msg["kind"] != "note" || msg["direction"] != nil || msg["outbound_status"] != nil {
		t.Errorf("note = %v", msg)
	}
	id := msg["id"].(string)
	html := f.scalar(`SELECT body_html FROM messages WHERE id = $1`, id)
	if strings.Contains(html, "script") || strings.Contains(html, "onclick") || !strings.Contains(html, `data-id="`+f.adminID+`"`) {
		t.Errorf("note HTML = %s", html)
	}
	if text := f.scalar(`SELECT body_text FROM messages WHERE id = $1`, id); text != "Kun jij dit @Test admin oppakken?" {
		t.Errorf("note text = %q", text)
	}
	if got := f.scalar(`SELECT count(*) FROM mentions WHERE message_id = $1`, id); got != "2" {
		t.Errorf("mentions = %s", got)
	}
	// A self-mention is stored but never notifies.
	if got := f.scalar(`SELECT count(*) FROM notifications`); got != "1" {
		t.Errorf("notifications = %s, want 1 (self-mention excluded)", got)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	found := false
	for !found {
		n, err := conn.Conn().WaitForNotification(waitCtx)
		if err != nil {
			t.Fatalf("no notification event: %v", err)
		}
		var ev struct {
			Type   string `json:"type"`
			UserID string `json:"user_id"`
		}
		if err := json.Unmarshal([]byte(n.Payload), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "notification" {
			found = true
			if ev.UserID != f.adminID {
				t.Errorf("event user = %s", ev.UserID)
			}
			if strings.Contains(n.Payload, "Kun jij") {
				t.Error("event must not carry content")
			}
		}
	}

	var list struct {
		Notifications []struct {
			ID, Kind           string
			ConversationID     string `json:"conversation_id"`
			ConversationNumber int    `json:"conversation_number"`
			Actor              *struct{ Name string }
			ReadAt             *string `json:"read_at"`
		}
		UnreadCount int `json:"unread_count"`
	}
	r = f.admin.do("GET", "/api/v1/notifications?unread=true", nil)
	expect(t, r, 200, "")
	if err := json.Unmarshal(r.raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Notifications) != 1 || list.Notifications[0].Kind != "mention" || list.Notifications[0].ConversationID != f.conv || list.Notifications[0].Actor == nil || list.UnreadCount != 1 {
		t.Fatalf("notifications = %s", r.raw)
	}
	// Notifications of conversations the user can no longer read stay hidden.
	convB := f.conversation("In B", convOpt{mailbox: f.mailboxB})
	f.exec(`INSERT INTO notifications (user_id, kind, conversation_id) VALUES ($1, 'assigned', $2)`, f.agentUserID, convB)
	r = f.agent.do("GET", "/api/v1/notifications", nil)
	expect(t, r, 200, "")
	if strings.Contains(string(r.raw), convB) || !strings.Contains(string(r.raw), `"unread_count":0`) {
		t.Errorf("hidden conversation leaked: %s", r.raw)
	}

	expect(t, f.admin.do("POST", "/api/v1/notifications/read", map[string]any{}), 422, "validation_failed")
	// Someone else's ids are ignored.
	expect(t, f.agent.do("POST", "/api/v1/notifications/read", map[string]any{"ids": []string{list.Notifications[0].ID}}), 200, "")
	if got := f.scalar(`SELECT read_at IS NULL FROM notifications WHERE user_id = $1 AND kind = 'mention'`, f.adminID); got != "true" {
		t.Error("marking read must be limited to the caller's notifications")
	}
	r = f.admin.do("POST", "/api/v1/notifications/read", map[string]any{"ids": []string{list.Notifications[0].ID}})
	expect(t, r, 200, "")
	if r.body["unread_count"] != float64(0) {
		t.Errorf("unread after read = %v", r.body["unread_count"])
	}
	r = f.admin.do("GET", "/api/v1/notifications?unread=true", nil)
	if strings.Contains(string(r.raw), `"kind":"mention"`) {
		t.Errorf("read notification still unread: %s", r.raw)
	}
	expect(t, f.admin.do("GET", "/api/v1/notifications?unread=maybe", nil), 400, "invalid_request")
}

func TestDrafts(t *testing.T) {
	f := newComposerFixture(t)
	path := "/api/v1/conversations/" + f.conv + "/draft"
	r := f.agent.do("GET", path, nil)
	expect(t, r, 200, "")
	if r.body["draft"] != nil {
		t.Fatalf("draft = %v", r.body["draft"])
	}
	up := f.upload(f.agent, "a.txt", []byte("x"))
	expect(t, up, 201, "")
	r = f.agent.do("PUT", path, map[string]any{
		"to": []map[string]string{{"address": "Jan@Customer.nl"}}, "subject": "Re: iets",
		"html": "<p>half</p><script>x</script>", "attachment_ids": []string{up.body["id"].(string), newUUID(t)},
	})
	expect(t, r, 200, "")
	r = f.agent.do("GET", path, nil)
	expect(t, r, 200, "")
	d := r.body["draft"].(map[string]any)
	if d["html"] != "<p>half</p>" || d["subject"] != "Re: iets" || len(d["attachments"].([]any)) != 1 || d["to"].([]any)[0].(map[string]any)["address"] != "jan@customer.nl" {
		t.Errorf("draft = %v", d)
	}
	// Drafts are private to their author.
	r = f.admin.do("GET", path, nil)
	expect(t, r, 200, "")
	if r.body["draft"] != nil {
		t.Error("another user saw the draft")
	}
	expect(t, f.agent.do("PUT", path, map[string]any{"to": []map[string]string{{"address": "nope"}}}), 422, "validation_failed")

	// Sending removes it.
	expect(t, f.send(f.agent, f.conv, f.replyBody(nil)), 201, "")
	if got := f.scalar(`SELECT count(*) FROM drafts`); got != "0" {
		t.Errorf("drafts after send = %s", got)
	}
	f.agent.do("PUT", path, map[string]any{"html": "<p>x</p>"})
	expect(t, f.agent.do("DELETE", path, nil), 204, "")
	if got := f.scalar(`SELECT count(*) FROM drafts`); got != "0" {
		t.Errorf("drafts after delete = %s", got)
	}
}

func TestNewConversation(t *testing.T) {
	f := newComposerFixture(t)
	body := map[string]any{
		"idempotency_key": newUUID(t), "mailbox_id": f.mailboxA, "subject": "Welkom bij Alpha",
		"to": []map[string]string{{"name": "Nieuwe Klant", "address": "Nieuw@Klant.nl"}}, "html": "<p>Welkom</p>",
	}
	r := f.agent.do("POST", "/api/v1/conversations", body)
	expect(t, r, 201, "")
	convID, _ := r.body["conversation_id"].(string)
	if convID == "" || messageOf(t, r)["outbound_status"] != "queued" {
		t.Fatalf("response = %s", r.raw)
	}
	if got := f.scalar(`SELECT subject || '|' || mailbox_id::text || '|' || status FROM conversations WHERE id = $1`, convID); got != "Welkom bij Alpha|"+f.mailboxA+"|open" {
		t.Errorf("conversation = %s", got)
	}
	if got := f.scalar(`SELECT c.name FROM conversations v JOIN contacts c ON c.id = v.contact_id JOIN contact_addresses a ON a.contact_id = c.id WHERE v.id = $1 AND a.email = 'nieuw@klant.nl'`, convID); got != "Nieuwe Klant" {
		t.Errorf("contact = %s", got)
	}
	if got := f.scalar(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'created'`, convID); got != "1" {
		t.Errorf("created events = %s", got)
	}
	// The same key again returns the first result and creates nothing.
	again := f.agent.do("POST", "/api/v1/conversations", body)
	expect(t, again, 200, "")
	if got := f.scalar(`SELECT count(*) FROM conversations WHERE subject = 'Welkom bij Alpha'`); got != "1" {
		t.Errorf("duplicate created %s conversations", got)
	}
	// An existing contact is reused.
	body["idempotency_key"] = newUUID(t)
	body["to"] = []map[string]string{{"address": "jan@customer.nl"}}
	expect(t, f.agent.do("POST", "/api/v1/conversations", body), 201, "")
	if got := f.scalar(`SELECT count(*) FROM contact_addresses WHERE email = 'jan@customer.nl'`); got != "1" {
		t.Errorf("contact addresses = %s", got)
	}
	delete(body, "subject")
	body["idempotency_key"] = newUUID(t)
	expect(t, f.agent.do("POST", "/api/v1/conversations", body), 422, "validation_failed")
	body["subject"] = "x"
	body["to"] = []map[string]string{}
	expect(t, f.agent.do("POST", "/api/v1/conversations", body), 422, "validation_failed")
	f.exec(`UPDATE mailboxes SET disabled_at = now() WHERE id = $1`, f.mailboxA)
	body["to"] = []map[string]string{{"address": "a@b.nl"}}
	expect(t, f.agent.do("POST", "/api/v1/conversations", body), 422, "mailbox_disabled")
}

func TestForwardAttachesOriginalAndQuotesEscapedText(t *testing.T) {
	f := newComposerFixture(t)
	ctx := context.Background()
	key, sum, err := f.store.Put(ctx, []byte("From: jan@customer.nl\r\n\r\nraw"))
	if err != nil {
		t.Fatal(err)
	}
	raw := f.queryID(`INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key) VALUES ($1, 'imap', $2, 28, $3) RETURNING id`, f.mailboxA, sum, key)
	f.exec(`UPDATE messages SET raw_message_id = $1, body_text = $2 WHERE id = $3`, raw, "Regel <b>een</b>\nregel twee\n\nAlinea", f.inboundID)

	path := "/api/v1/conversations/" + f.conv + "/forward"
	body := map[string]any{"idempotency_key": newUUID(t), "message_id": f.inboundID, "to": []map[string]string{{"address": "collega@example.org"}}, "html": ""}
	r := f.agent.do("POST", path, body)
	expect(t, r, 201, "")
	id := messageOf(t, r)["id"].(string)
	if got := f.scalar(`SELECT subject FROM messages WHERE id = $1`, id); got != "Fwd: Vraag over factuur" {
		t.Errorf("subject = %s", got)
	}
	html := f.scalar(`SELECT body_html FROM messages WHERE id = $1`, id)
	if !strings.Contains(html, "Regel &lt;b&gt;een&lt;/b&gt;<br>regel twee") || strings.Contains(html, "<b>een") || !strings.Contains(html, "jan@customer.nl") {
		t.Errorf("quote = %s", html)
	}
	if got := f.scalar(`SELECT filename || ':' || declared_type || ':' || blob_key FROM attachments WHERE message_id = $1`, id); got != "doorgestuurd-bericht.eml:message/rfc822:"+key {
		t.Errorf("attachment = %s", got)
	}
	if got := f.scalar(`SELECT in_reply_to FROM messages WHERE id = $1`, id); got != "" {
		t.Errorf("a forward must not set In-Reply-To, got %q", got)
	}
	// Whoever received the forward is not added to the next reply to the customer.
	r = f.agent.do("GET", "/api/v1/conversations/"+f.conv+"/reply-defaults", nil)
	expect(t, r, 200, "")
	if strings.Contains(string(r.raw), "collega@example.org") {
		t.Errorf("forward recipient in reply defaults: %s", r.raw)
	}
	body["idempotency_key"] = newUUID(t)
	body["message_id"] = newUUID(t)
	expect(t, f.agent.do("POST", path, body), 404, "not_found")
	body["message_id"] = f.inboundID
	body["to"] = []map[string]string{}
	expect(t, f.agent.do("POST", path, body), 422, "validation_failed")
}

func TestTemplatesVisibilityAndPermissions(t *testing.T) {
	f := newComposerFixture(t)
	tpl := func(c *client, body map[string]any) response {
		body["name"], body["body_html"] = fmt.Sprint(body["name"]), fmt.Sprint(body["body_html"])
		return c.do("POST", "/api/v1/templates", body)
	}
	mk := func(c *client, scope, name string, extra map[string]any) string {
		body := map[string]any{"name": name, "scope": scope, "body_html": "<p>" + name + "</p>"}
		for k, v := range extra {
			body[k] = v
		}
		r := tpl(c, body)
		expect(t, r, 201, "")
		return r.body["id"].(string)
	}
	mineAgent := mk(f.agent, "personal", "agent-personal", map[string]any{"shortcode": "hoi"})
	mineAdmin := mk(f.admin, "personal", "admin-personal", nil)
	teamSupport := mk(f.admin, "team", "team-support", map[string]any{"team_id": f.team})
	teamBilling := mk(f.admin, "team", "team-billing", map[string]any{"team_id": f.otherTeam})
	mbA := mk(f.admin, "mailbox", "mailbox-a", map[string]any{"mailbox_id": f.mailboxA})
	mbB := mk(f.admin, "mailbox", "mailbox-b", map[string]any{"mailbox_id": f.mailboxB})
	global := mk(f.admin, "global", "global", nil)

	names := func(c *client, query string) string {
		r := c.do("GET", "/api/v1/templates"+query, nil)
		expect(t, r, 200, "")
		var out struct{ Templates []struct{ Name string } }
		if err := json.Unmarshal(r.raw, &out); err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, x := range out.Templates {
			n = append(n, x.Name)
		}
		return strings.Join(n, ",")
	}
	if got := names(f.agent, ""); got != "agent-personal,global,mailbox-a,team-support" {
		t.Errorf("agent sees %s", got)
	}
	if got := names(f.readUser, ""); got != "global,mailbox-b,team-billing" {
		t.Errorf("read-only-mailbox agent sees %s", got)
	}
	if got := names(f.admin, ""); got != "admin-personal,global,mailbox-a,mailbox-b" {
		t.Errorf("admin sees %s", got)
	}
	if got := names(f.admin, "?manage=true"); got != "admin-personal,global,mailbox-a,mailbox-b,team-billing,team-support" {
		t.Errorf("admin manage view %s", got)
	}
	expect(t, f.agent.do("GET", "/api/v1/templates?manage=true", nil), 403, "forbidden")

	// Permissions.
	expect(t, tpl(f.agent, map[string]any{"name": "x", "scope": "global", "body_html": "<p>x</p>"}), 403, "forbidden")
	expect(t, tpl(f.readonly, map[string]any{"name": "x", "scope": "personal", "body_html": "<p>x</p>"}), 403, "forbidden")
	expect(t, tpl(f.admin, map[string]any{"name": "x", "scope": "team", "body_html": "<p>x</p>"}), 422, "validation_failed")
	expect(t, tpl(f.admin, map[string]any{"name": "x", "scope": "team", "team_id": newUUID(t), "body_html": "<p>x</p>"}), 422, "validation_failed")
	expect(t, tpl(f.agent, map[string]any{"name": "x", "scope": "personal", "shortcode": "Bad Code", "body_html": "<p>x</p>"}), 422, "validation_failed")
	expect(t, f.agent.do("PATCH", "/api/v1/templates/"+mineAdmin, map[string]any{"name": "x", "body_html": "<p>x</p>"}), 404, "not_found")
	expect(t, f.agent.do("DELETE", "/api/v1/templates/"+mineAdmin, nil), 404, "not_found")
	expect(t, f.agent.do("PATCH", "/api/v1/templates/"+global, map[string]any{"name": "x", "body_html": "<p>x</p>"}), 403, "forbidden")
	expect(t, f.agent.do("DELETE", "/api/v1/templates/"+teamSupport, nil), 403, "forbidden")
	r := f.agent.do("PATCH", "/api/v1/templates/"+mineAgent, map[string]any{"name": "renamed", "shortcode": "hi", "subject": "S", "body_html": "<p>nieuw</p><script>x</script>"})
	expect(t, r, 200, "")
	if r.body["body_html"] != "<p>nieuw</p>" || r.body["editable"] != true {
		t.Errorf("update = %v", r.body)
	}
	expect(t, f.agent.do("DELETE", "/api/v1/templates/"+mineAgent, nil), 204, "")
	expect(t, f.admin.do("PATCH", "/api/v1/templates/"+mbA, map[string]any{"name": "mailbox-a2", "body_html": "<p>x</p>"}), 200, "")
	expect(t, f.admin.do("DELETE", "/api/v1/templates/"+mbB, nil), 204, "")
	_ = teamBilling

	// Shared changes are audited; personal ones are not.
	if got := f.scalar(`SELECT count(*) FROM audit_log WHERE action LIKE 'template.%'`); got != "7" {
		t.Errorf("template audit entries = %s", got)
	}
}

func TestRenderTemplateEscapesAndFlagsUnknownVariables(t *testing.T) {
	f := newComposerFixture(t)
	f.exec(`UPDATE contacts SET name = $1 WHERE id = $2`, `<img src=x onerror=alert(1)> Jan`, f.contact)
	r := f.agent.do("POST", "/api/v1/templates", map[string]any{
		"name": "groet", "scope": "personal", "subject": "Re: {{contact.name}} #{{conversation.number}}",
		"body_html": `<p>Hoi {{contact.first_name}}, {{ agent.name }} van {{mailbox.name}} ({{contact.email}}). {{secret.token}} {{contact.name}}</p>`,
	})
	expect(t, r, 201, "")
	id := r.body["id"].(string)
	num := f.scalar(`SELECT number FROM conversations WHERE id = $1`, f.conv)

	r = f.agent.do("GET", "/api/v1/templates/"+id+"/render?conversation_id="+f.conv, nil)
	expect(t, r, 200, "")
	body := r.body["body_html"].(string)
	if strings.Contains(body, "<img") || !strings.Contains(body, "&lt;img src=x onerror=alert(1)&gt; Jan") {
		t.Errorf("contact name not escaped: %s", body)
	}
	if !strings.Contains(body, "Hoi &lt;img") || !strings.Contains(body, "Test agent van Alpha (jan@customer.nl)") || !strings.Contains(body, "{{secret.token}}") {
		t.Errorf("body = %s", body)
	}
	if r.body["subject"] != "Re: <img src=x onerror=alert(1)> Jan #"+num {
		t.Errorf("subject = %v", r.body["subject"])
	}
	un, _ := r.body["unresolved"].([]any)
	if len(un) != 1 || un[0] != "secret.token" {
		t.Errorf("unresolved = %v", r.body["unresolved"])
	}

	convB := f.conversation("In B", convOpt{mailbox: f.mailboxB})
	expect(t, f.agent.do("GET", "/api/v1/templates/"+id+"/render?conversation_id="+convB, nil), 404, "not_found")
	// Someone else's personal template is invisible.
	expect(t, f.admin.do("GET", "/api/v1/templates/"+id+"/render?conversation_id="+f.conv, nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/templates/"+id+"/render", nil), 400, "invalid_request")
}

func TestSignatureResolutionAndBrandedLayout(t *testing.T) {
	f := newComposerFixture(t)
	eff := func() (string, string) {
		r := f.agent.do("GET", "/api/v1/signatures/effective?mailbox_id="+f.mailboxA, nil)
		expect(t, r, 200, "")
		return r.body["body_html"].(string), r.body["source"].(string)
	}
	if body, src := eff(); body != "" || src != "" {
		t.Fatalf("effective without signatures = %q %q", body, src)
	}
	expect(t, f.agent.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/signature", map[string]any{"body_html": "<p>x</p>"}), 403, "forbidden")
	expect(t, f.admin.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/signature", map[string]any{"body_html": "<p>Team Alpha</p><script>x</script>"}), 200, "")
	if body, src := eff(); body != "<p>Team Alpha</p>" || src != "mailbox" {
		t.Errorf("mailbox default = %q %q", body, src)
	}
	expect(t, f.agent.do("PUT", "/api/v1/me/signatures", map[string]any{"mailbox_id": nil, "body_html": "<p>Groet, Agent</p>"}), 200, "")
	if body, src := eff(); body != "<p>Groet, Agent</p>" || src != "user" {
		t.Errorf("user default = %q %q", body, src)
	}
	expect(t, f.agent.do("PUT", "/api/v1/me/signatures", map[string]any{"mailbox_id": f.mailboxA, "body_html": "<p>Agent voor Alpha</p>"}), 200, "")
	if body, src := eff(); body != "<p>Agent voor Alpha</p>" || src != "user_mailbox" {
		t.Errorf("user+mailbox = %q %q", body, src)
	}
	// Signatures for a mailbox the user cannot read do not exist.
	expect(t, f.agent.do("PUT", "/api/v1/me/signatures", map[string]any{"mailbox_id": f.mailboxB, "body_html": "<p>x</p>"}), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/signatures/effective?mailbox_id="+f.mailboxB, nil), 404, "not_found")
	expect(t, f.readonly.do("PUT", "/api/v1/me/signatures", map[string]any{"body_html": "<p>x</p>"}), 403, "forbidden")

	r := f.agent.do("GET", "/api/v1/me/signatures", nil)
	expect(t, r, 200, "")
	if n := len(r.body["signatures"].([]any)); n != 2 {
		t.Errorf("signatures = %s", r.raw)
	}

	// Workspace footer and layout, applied server side.
	expect(t, f.agent.do("PUT", "/api/v1/settings/email", map[string]any{"footer_text": "x"}), 403, "forbidden")
	expect(t, f.admin.do("PUT", "/api/v1/settings/email", map[string]any{"footer_text": "a\nb"}), 422, "validation_failed")
	expect(t, f.admin.do("PUT", "/api/v1/settings/email", map[string]any{"footer_text": "Acme BV, Kade 1, Utrecht"}), 200, "")
	r = f.admin.do("GET", "/api/v1/settings/email", nil)
	expect(t, r, 200, "")
	if r.body["footer_text"] != "Acme BV, Kade 1, Utrecht" {
		t.Errorf("footer = %v", r.body)
	}
	f.exec(`UPDATE mailboxes SET display_name = 'Alpha Support' WHERE id = $1`, f.mailboxA)
	id := messageOf(t, f.send(f.agent, f.conv, f.replyBody(nil)))["id"].(string)
	html := f.scalar(`SELECT body_html FROM messages WHERE id = $1`, id)
	for _, want := range []string{"Agent voor Alpha", "Acme BV, Kade 1, Utrecht", "Alpha Support", "<table"} {
		if !strings.Contains(html, want) {
			t.Errorf("html lacks %q: %s", want, html)
		}
	}
	if strings.Contains(html, "Team Alpha") {
		t.Error("the mailbox signature must lose against the user's own")
	}
	text := f.scalar(`SELECT body_text FROM messages WHERE id = $1`, id)
	if text != "Dank voor je bericht.\n\n-- \nAgent voor Alpha\n\nAcme BV, Kade 1, Utrecht" {
		t.Errorf("text part = %q", text)
	}
	if got := f.scalar(`SELECT count(*) FROM audit_log WHERE action IN ('mailbox.signature_changed', 'settings.email_changed')`); got != "2" {
		t.Errorf("audit entries = %s", got)
	}

	// An empty signature removes it, falling back one level.
	expect(t, f.agent.do("PUT", "/api/v1/me/signatures", map[string]any{"mailbox_id": f.mailboxA, "body_html": "<p> </p>"}), 200, "")
	if _, src := eff(); src != "user" {
		t.Errorf("after delete source = %s", src)
	}
}

func TestAssigningNotifiesTheAssignee(t *testing.T) {
	f := newComposerFixture(t)
	// Assigning to yourself is your own doing and does not notify.
	expect(t, f.admin.do("PATCH", "/api/v1/conversations/"+f.conv, map[string]any{"assignee_user_id": f.adminID}), 200, "")
	if got := f.scalar(`SELECT count(*) FROM notifications`); got != "0" {
		t.Fatalf("notifications after self-assignment = %s", got)
	}
	expect(t, f.admin.do("PATCH", "/api/v1/conversations/"+f.conv, map[string]any{"assignee_user_id": f.agentUserID}), 200, "")
	r := f.agent.do("GET", "/api/v1/notifications?unread=true", nil)
	expect(t, r, 200, "")
	list, _ := r.body["notifications"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["kind"] != "assigned" {
		t.Fatalf("notifications = %s", r.raw)
	}
}
