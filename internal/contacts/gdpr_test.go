package contacts

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/storage"
	"echoo/internal/webhooks"
)

// captureReceiver records webhook request bodies.
type captureReceiver struct {
	mu     sync.Mutex
	bodies []string
}

func (c *captureReceiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, string(b))
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

type enqueueRecorder struct{ ids []string }

func (f *enqueueRecorder) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	f.ids = append(f.ids, args.(jobs.WebhookDeliver).DeliveryID)
	return &rivertype.JobInsertResult{}, nil
}

type eraseFixture struct {
	mailbox, jane, bob, conv1, conv2, conv3               pgtype.UUID
	janeMsg, replyMsg, noteMsg, sharedJane, kim, kimAlone pgtype.UUID
	sharedPDF                                             []byte
	passport                                              []byte
	rawJane, rawReply, rawSharedJane, rawKim              []byte
	rawBob                                                []byte
	exportKey                                             string
}

// erasable builds one contact with a conversation of their own (subject, bodies, raw copies,
// attachments, a note, a draft), one conversation shared with another customer, and a second
// contact (Bob) who received the very same PDF, so the blob is shared.
func erasable(t *testing.T, e *env, admin dbq.User) eraseFixture {
	t.Helper()
	f := eraseFixture{
		sharedPDF: []byte("%PDF-1.4 invoice that jane and bob both received"),
		passport:  []byte("PNG passport scan of jane only"),
		rawJane:   []byte("From: jane@example.org\r\n\r\nzebra secret-jane-word"),
		rawReply:  []byte("To: jane@example.org\r\n\r\nquokka reply"),
		rawKim:    []byte("From: kim@customer.nl\r\n\r\nkim words"),
		rawBob:    []byte("From: bob@example.net\r\n\r\nbob words"),
	}
	f.rawSharedJane = []byte("From: jane@example.org\r\n\r\nshared conversation jane words")
	f.mailbox = e.mailbox("Support", "help@example.com")
	f.jane = e.contact("Jane Doe", admin.ID, "jane@example.org", "jane.work@example.org")
	f.bob = e.contact("Bob", admin.ID, "bob@example.net")

	f.conv1 = e.conversation(f.mailbox, f.jane, "zebrasubject rocket", "open")
	f.janeMsg = e.message(f.conv1, f.mailbox, msgOpt{
		fromAddr: "jane@example.org", fromName: "Jane Doe", subject: "zebrasubject rocket", body: "zebra secret-jane-word",
		to: []string{"help@example.com"}, raw: f.rawJane,
		attachments: map[string][]byte{"invoice.pdf": f.sharedPDF, "passport.png": f.passport},
	})
	f.replyMsg = e.message(f.conv1, f.mailbox, msgOpt{
		direction: "out", fromAddr: "help@example.com", fromName: "Support", subject: "Re: zebrasubject rocket", body: "quokka reply",
		to: []string{"jane@example.org"}, raw: f.rawReply,
	})
	f.noteMsg = e.id(`INSERT INTO messages (conversation_id, mailbox_id, kind, body_text, body_html, from_name)
		VALUES ($1, $2, 'note', 'internal note: jane called about the zebra', '<p>jane</p>', 'Agent') RETURNING id`, f.conv1, f.mailbox)
	e.exec(`INSERT INTO outbound (message_id, idempotency_key, status, smtp_response, error)
		VALUES ($1, uuidv7(), 'sent', '250 ok queued for jane@example.org', '')`, f.replyMsg)
	e.exec(`INSERT INTO drafts (conversation_id, user_id, body_html, subject) VALUES ($1, $2, '<p>hi jane</p>', 'zebrasubject rocket')`, f.conv1, admin.ID)
	e.exec(`INSERT INTO csat_requests (conversation_id, mailbox_id) VALUES ($1, $2)`, f.conv1, f.mailbox)
	e.exec(`INSERT INTO csat_responses (conversation_id, mailbox_id, rating, comment, token_hash) VALUES ($1, $2, 4, 'zebra survey comment', '\x01')`, f.conv1, f.mailbox)
	e.exec(`INSERT INTO thread_refs (mailbox_id, message_id_hash, conversation_id) VALUES ($1, sha256('<zebra@example.org>'::bytea), $2)`, f.mailbox, f.conv1)

	f.conv2 = e.conversation(f.mailbox, f.jane, "Gedeeld gesprek", "open")
	f.sharedJane = e.message(f.conv2, f.mailbox, msgOpt{
		fromAddr: "jane@example.org", fromName: "Jane Doe", subject: "Gedeeld gesprek", body: "shared conversation jane words",
		to: []string{"help@example.com"}, raw: f.rawSharedJane,
	})
	f.kim = e.message(f.conv2, f.mailbox, msgOpt{
		fromAddr: "kim@customer.nl", fromName: "Kim", subject: "Re: Gedeeld gesprek", body: "kim words",
		to: []string{"help@example.com", "jane@example.org"}, raw: f.rawKim,
	})
	f.kimAlone = e.message(f.conv2, f.mailbox, msgOpt{
		fromAddr: "kim@customer.nl", fromName: "Kim", subject: "Re: Gedeeld gesprek", body: "kim alone words",
		to: []string{"help@example.com"}, raw: []byte("From: kim@customer.nl\r\n\r\nkim alone"),
	})

	f.conv3 = e.conversation(f.mailbox, f.bob, "Bob vraag", "open")
	e.message(f.conv3, f.mailbox, msgOpt{
		fromAddr: "bob@example.net", fromName: "Bob", subject: "Bob vraag", body: "bob words",
		to: []string{"help@example.com"}, raw: f.rawBob, attachments: map[string][]byte{"invoice.pdf": f.sharedPDF},
	})

	e.exec(`INSERT INTO crm_notes (contact_id, author_user_id, body) VALUES ($1, $2, 'jane wants a callback')`, f.jane, admin.ID)
	key, _, err := e.store.Put(context.Background(), []byte("PK old export of jane"))
	if err != nil {
		t.Fatal(err)
	}
	f.exportKey = key
	e.exec(`INSERT INTO data_exports (contact_id, requested_by, status, blob_key) VALUES ($1, $2, 'ready', $3)`, f.jane, admin.ID, key)
	e.exec(`WITH r AS (INSERT INTO rules (name, trigger, conditions, actions, position) VALUES ('r', 'message_received', '{}', '[]', 1) RETURNING id)
		INSERT INTO automation_replies (rule_id, address) SELECT id, 'jane@example.org' FROM r`)
	e.exec(`INSERT INTO sender_image_allowlist (pattern) VALUES ('jane@example.org'), ('@example.org')`)
	return f
}

func (e *env) blobKeyOf(sql string, args ...any) string {
	e.t.Helper()
	var key string
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&key); err != nil {
		e.t.Fatal(err)
	}
	return key
}

func TestEraseContact(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("admin")
	f := erasable(t, e, admin)

	sharedPDFKey := e.blobKeyOf(`SELECT blob_key FROM attachments WHERE filename = 'invoice.pdf' LIMIT 1`)
	passportKey := e.blobKeyOf(`SELECT blob_key FROM attachments WHERE filename = 'passport.png'`)
	janeRawKey := e.blobKeyOf(`SELECT blob_key FROM raw_messages WHERE id = (SELECT raw_message_id FROM messages WHERE id = $1)`, f.janeMsg)
	replyRawKey := e.blobKeyOf(`SELECT blob_key FROM raw_messages WHERE id = (SELECT raw_message_id FROM messages WHERE id = $1)`, f.replyMsg)
	sharedRawKey := e.blobKeyOf(`SELECT blob_key FROM raw_messages WHERE id = (SELECT raw_message_id FROM messages WHERE id = $1)`, f.sharedJane)
	kimRawKey := e.blobKeyOf(`SELECT blob_key FROM raw_messages WHERE id = (SELECT raw_message_id FROM messages WHERE id = $1)`, f.kim)
	kimAloneRawKey := e.blobKeyOf(`SELECT blob_key FROM raw_messages WHERE id = (SELECT raw_message_id FROM messages WHERE id = $1)`, f.kimAlone)

	// Webhook outbox: a subscribed endpoint with content, events fanned out before the erasure
	// but delivered after it, as happens with a retry.
	recv := &captureReceiver{}
	srv := httptest.NewServer(recv)
	t.Cleanup(srv.Close)
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(keyBytes))
	if err != nil {
		t.Fatal(err)
	}
	hookID, err := e.q.NewUUID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := keys.Encrypt([]byte("whsec"), webhooks.SecretAAD(hookID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.InsertWebhook(ctx, dbq.InsertWebhookParams{
		ID: hookID, Url: srv.URL, SecretEnc: enc, Events: []string{webhooks.ContactCreated, webhooks.MessageCreated}, IncludeContent: true, AllowHttp: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []webhooks.Event{
		{Type: webhooks.ContactCreated, ContactID: f.jane},
		{Type: webhooks.MessageCreated, MailboxID: f.mailbox, ConversationID: f.conv1, MessageID: f.janeMsg, ContactID: f.jane},
	} {
		if err := webhooks.Record(ctx, e.q, ev); err != nil {
			t.Fatal(err)
		}
	}
	enq := &enqueueRecorder{}
	if err := webhooks.NewFanoutWorker(e.pool).Run(ctx, enq); err != nil {
		t.Fatal(err)
	}
	if len(enq.ids) != 2 {
		t.Fatalf("deliveries = %d", len(enq.ids))
	}
	// A pending event that was not fanned out yet must not survive either.
	if err := webhooks.Record(ctx, e.q, webhooks.Event{Type: webhooks.ContactCreated, ContactID: f.jane}); err != nil {
		t.Fatal(err)
	}

	searchable := func(word string) int {
		return e.count(`SELECT count(*) FROM messages WHERE fts @@ plainto_tsquery('echoo_simple', $1) OR fts @@ plainto_tsquery('dutch', $1)`, word)
	}
	for _, w := range []string{"zebra", "quokka", "zebrasubject"} {
		if searchable(w) == 0 {
			t.Fatalf("precondition: %q is not in the search vectors", w)
		}
	}

	res, err := Erase(ctx, e.pool, e.store, Actor{UserID: admin.ID}, f.jane)
	if err != nil {
		t.Fatal(err)
	}
	if res.ConversationsErased != 1 || res.ConversationsKept != 1 || res.MessagesBlanked != 3 || res.AttachmentsDeleted != 2 || res.BlobDeletionsPending != 0 {
		t.Fatalf("result = %+v", res)
	}

	t.Run("contact rows are gone", func(t *testing.T) {
		for _, q := range []string{
			`SELECT count(*) FROM contacts WHERE id = $1`, `SELECT count(*) FROM contact_addresses WHERE contact_id = $1`,
			`SELECT count(*) FROM crm_notes WHERE contact_id = $1`, `SELECT count(*) FROM data_exports WHERE contact_id = $1`,
		} {
			if n := e.count(q, f.jane); n != 0 {
				t.Errorf("%s: %d rows left", q, n)
			}
		}
		if e.count(`SELECT count(*) FROM conversations WHERE contact_id = $1`, f.jane) != 0 {
			t.Error("conversations still point at the erased contact")
		}
		if e.count(`SELECT count(*) FROM automation_replies WHERE address = 'jane@example.org'`) != 0 ||
			e.count(`SELECT count(*) FROM sender_image_allowlist WHERE pattern = 'jane@example.org'`) != 0 {
			t.Error("address-keyed rows remain")
		}
		if e.count(`SELECT count(*) FROM sender_image_allowlist WHERE pattern = '@example.org'`) != 1 {
			t.Error("a domain-wide rule is not personal data of one contact and must stay")
		}
	})

	t.Run("bodies, subjects and search vectors of the sole conversation are gone", func(t *testing.T) {
		var subject, norm, preview string
		if err := e.pool.QueryRow(ctx, `SELECT subject, subject_normalized, preview FROM conversations WHERE id = $1`, f.conv1).Scan(&subject, &norm, &preview); err != nil {
			t.Fatal(err)
		}
		if subject != "" || norm != "" || preview != "" {
			t.Errorf("conversation kept text: %q %q %q", subject, norm, preview)
		}
		rows, err := e.pool.Query(ctx, `SELECT direction, from_addr, from_name, subject, body_text, body_html, to_addrs::text, message_id_header FROM messages WHERE conversation_id = $1`, f.conv1)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var dir pgtype.Text
			var from, name, subject, text, html, to, mid string
			if err := rows.Scan(&dir, &from, &name, &subject, &text, &html, &to, &mid); err != nil {
				t.Fatal(err)
			}
			n++
			if subject != "" || text != "" || html != "" || to != "[]" || mid != "" {
				t.Errorf("message kept content: %q %q %q %s %q", subject, text, html, to, mid)
			}
			if dir.String == "in" && (from != TombstoneAddress || name != TombstoneName) {
				t.Errorf("sender not tombstoned: %q %q", from, name)
			}
		}
		if n != 3 {
			t.Errorf("message rows = %d, want the 3 shells to remain", n)
		}
		for _, w := range []string{"zebra", "quokka", "zebrasubject", "secret-jane-word"} {
			if c := searchable(w); c != 0 {
				t.Errorf("%q still matches %d messages in the search vectors", w, c)
			}
		}
		if e.count(`SELECT count(*) FROM csat_responses WHERE comment <> ''`) != 0 {
			t.Error("the survey comment of the erased conversation remains")
		}
		if e.count(`SELECT count(*) FROM csat_responses WHERE rating = 4`) != 1 {
			t.Error("the rating is not personal text and stays for the reports")
		}
		if e.count(`SELECT count(*) FROM outbound WHERE smtp_response <> '' OR error <> ''`) != 0 {
			t.Error("delivery responses still name the recipient")
		}
		if e.count(`SELECT count(*) FROM drafts WHERE conversation_id = $1`, f.conv1) != 0 ||
			e.count(`SELECT count(*) FROM thread_refs WHERE conversation_id = $1`, f.conv1) != 0 {
			t.Error("drafts or thread references of the erased conversation remain (a later mail would thread into it)")
		}
	})

	t.Run("attachments and raw copies", func(t *testing.T) {
		if e.count(`SELECT count(*) FROM attachments WHERE message_id = $1`, f.janeMsg) != 0 {
			t.Error("attachment rows of the erased conversation remain")
		}
		if e.blobExists(passportKey) {
			t.Error("the attachment blob only Jane had is still in the store")
		}
		for name, key := range map[string]string{"jane raw": janeRawKey, "reply raw": replyRawKey, "shared-conversation raw from jane": sharedRawKey} {
			if e.blobExists(key) {
				t.Errorf("%s: the .eml blob is still in the store", name)
			}
		}
		var status, blob string
		if err := e.pool.QueryRow(ctx, `SELECT parse_status, blob_key FROM raw_messages WHERE id = (SELECT raw_message_id FROM messages WHERE id = $1)`, f.janeMsg).Scan(&status, &blob); err != nil {
			t.Fatal(err)
		}
		if status != "skipped" || blob != "" {
			t.Errorf("raw row = %q %q: it must stay (so IMAP does not fetch the mail again) without its blob", status, blob)
		}
		if e.blobExists(f.exportKey) {
			t.Error("an earlier GDPR export of the contact is still in the store")
		}
		if e.count(`SELECT count(*) FROM pending_blob_deletions`) != 0 {
			t.Error("blob deletion queue not drained")
		}
	})

	t.Run("a blob shared with another contact survives", func(t *testing.T) {
		if !e.blobExists(sharedPDFKey) {
			t.Fatal("the PDF that Bob also received was deleted from the store")
		}
		rc, err := e.store.Open(ctx, e.blobKeyOf(`SELECT blob_key FROM attachments WHERE message_id IN (SELECT id FROM messages WHERE conversation_id = $1)`, f.conv3))
		if err != nil {
			t.Fatalf("Bob's attachment is unreadable: %v", err)
		}
		defer func() { _ = rc.Close() }()
		got, err := io.ReadAll(rc)
		if err != nil || !bytes.Equal(got, f.sharedPDF) {
			t.Fatalf("Bob's attachment content changed: %q %v", got, err)
		}
		var bobText string
		if err := e.pool.QueryRow(ctx, `SELECT body_text FROM messages WHERE conversation_id = $1`, f.conv3).Scan(&bobText); err != nil || bobText != "bob words" {
			t.Errorf("Bob's message = %q %v", bobText, err)
		}
	})

	t.Run("a conversation shared with someone else keeps the other side", func(t *testing.T) {
		var subject string
		if err := e.pool.QueryRow(ctx, `SELECT subject FROM conversations WHERE id = $1`, f.conv2).Scan(&subject); err != nil || subject != "Gedeeld gesprek" {
			t.Fatalf("shared conversation subject = %q %v", subject, err)
		}
		var from, name, body string
		if err := e.pool.QueryRow(ctx, `SELECT from_addr, from_name, body_text FROM messages WHERE id = $1`, f.sharedJane).Scan(&from, &name, &body); err != nil {
			t.Fatal(err)
		}
		if from != TombstoneAddress || name != TombstoneName {
			t.Errorf("Jane's message in the shared conversation is not tombstoned: %q %q", from, name)
		}
		var kimFrom, kimBody, kimTo string
		if err := e.pool.QueryRow(ctx, `SELECT from_addr, body_text, to_addrs::text FROM messages WHERE id = $1`, f.kim).Scan(&kimFrom, &kimBody, &kimTo); err != nil {
			t.Fatal(err)
		}
		if kimFrom != "kim@customer.nl" || kimBody != "kim words" {
			t.Errorf("Kim's message changed: %q %q", kimFrom, kimBody)
		}
		if strings.Contains(kimTo, "jane") || !strings.Contains(kimTo, "help@example.com") || !strings.Contains(kimTo, TombstoneAddress) {
			t.Errorf("recipients of Kim's message = %s: Jane must be tombstoned, the mailbox kept", kimTo)
		}
		if e.blobExists(kimRawKey) {
			t.Error("the raw copy of a message that names Jane as recipient still holds her address")
		}
		if !e.blobExists(kimAloneRawKey) {
			t.Error("the raw copy of a message that never mentions Jane must stay")
		}
	})

	t.Run("audit holds counts and no personal data", func(t *testing.T) {
		var meta string
		if err := e.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'contact.erased' AND target_id = $1`, f.jane.String()).Scan(&meta); err != nil {
			t.Fatal(err)
		}
		for _, pii := range []string{"jane", "Jane", "zebra", "example.org"} {
			if strings.Contains(meta, pii) {
				t.Errorf("audit metadata contains %q: %s", pii, meta)
			}
		}
		var counts map[string]any
		if err := json.Unmarshal([]byte(meta), &counts); err != nil {
			t.Fatal(err)
		}
		if counts["conversations_erased"] != 1.0 || counts["messages_blanked"] != 3.0 || counts["attachments_deleted"] != 2.0 || counts["notes_deleted"] != 1.0 || counts["addresses"] != 2.0 {
			t.Errorf("counts = %v", counts)
		}
	})

	t.Run("webhook payloads carry no personal data after erasure", func(t *testing.T) {
		deliver := webhooks.NewDeliverWorker(webhooks.DeliverDeps{Pool: e.pool, Keyring: keys, Client: srv.Client(), AnyURL: true})
		for _, id := range enq.ids {
			err := deliver.Work(ctx, &river.Job[jobs.WebhookDeliver]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: jobs.WebhookDeliver{DeliveryID: id}})
			if err != nil {
				t.Fatal(err)
			}
		}
		recv.mu.Lock()
		defer recv.mu.Unlock()
		if len(recv.bodies) != 2 {
			t.Fatalf("deliveries received = %d", len(recv.bodies))
		}
		for _, body := range recv.bodies {
			for _, pii := range []string{"Jane", "jane@", "example.org", "zebra", "secret", "quokka"} {
				if strings.Contains(body, pii) {
					t.Errorf("payload contains %q: %s", pii, body)
				}
			}
			if strings.Contains(body, `"name"`) || strings.Contains(body, `"email"`) || strings.Contains(body, `"subject":"`+"a") || strings.Contains(body, `"preview":"`+"z") {
				t.Errorf("payload still has content fields: %s", body)
			}
			if !strings.Contains(body, f.jane.String()) && !strings.Contains(body, f.janeMsg.String()) {
				t.Errorf("payload lost its identifiers: %s", body)
			}
		}
		if e.count(`SELECT count(*) FROM webhook_events WHERE contact_id = $1 AND fanned_out_at IS NULL`, f.jane) != 0 {
			t.Error("an undelivered outbox event of the contact survived")
		}
	})
}

func TestEraseUnknownContact(t *testing.T) {
	e := newEnv(t)
	_, err := Erase(context.Background(), e.pool, e.store, Actor{}, pgtype.UUID{Bytes: [16]byte{1}, Valid: true})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestEraseRetainsBlobsThatCouldNotBeDeleted(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin")
	f := erasable(t, e, admin)
	passportKey := e.blobKeyOf(`SELECT blob_key FROM attachments WHERE filename = 'passport.png'`)

	res, err := Erase(context.Background(), e.pool, failingDeleter{e.store, passportKey}, Actor{UserID: admin.ID}, f.jane)
	if err != nil {
		t.Fatal(err)
	}
	if res.BlobDeletionsPending != 1 {
		t.Fatalf("pending = %d, want the one blob that failed", res.BlobDeletionsPending)
	}
	if e.count(`SELECT count(*) FROM pending_blob_deletions WHERE blob_key = $1`, passportKey) != 1 {
		t.Fatal("the failed deletion must stay queued so the purge job retries it")
	}
	purge := NewPurgeWorker(e.pool, e.store)
	if err := purge.Run(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if e.blobExists(passportKey) || e.count(`SELECT count(*) FROM pending_blob_deletions`) != 0 {
		t.Fatal("the purge job did not finish the deletion")
	}
}

type failingDeleter struct {
	*storage.FS
	key string
}

func (f failingDeleter) Delete(ctx context.Context, key string) error {
	if key == f.key {
		return errors.New("disk on fire")
	}
	return f.FS.Delete(ctx, key)
}

func TestBlobReferenceCheckCoversEveryBlobColumn(t *testing.T) {
	e := newEnv(t)
	rows, err := e.pool.Query(context.Background(), `SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name LIKE '%blob%' AND table_name <> 'pending_blob_deletions'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	covered := []string{
		"attachments.blob_key", "raw_messages.blob_key", "uploads.blob_key", "image_proxy_cache.blob_key",
		"data_exports.blob_key", "kb_images.blob_key",
	}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(covered, col) {
			t.Errorf("%s holds a blob key: add it to BlobIsReferenced (db/queries/contacts.sql), or erasure and purges can delete a file that is still in use", col)
		}
	}
}

func TestBuildExport(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("admin")
	f := erasable(t, e, admin)
	org := e.id(`INSERT INTO organizations (name, domains) VALUES ('Example Org', '{example.org}') RETURNING id`)
	e.exec(`UPDATE contacts SET organization_id = $2, phone = '0612345678', custom_attributes = '{"tier":"gold"}' WHERE id = $1`, f.jane, org)
	// A hostile filename must not escape the attachments directory, and a missing blob must not
	// fail the whole export.
	e.exec(`UPDATE attachments SET filename = '../../etc/passwd' WHERE filename = 'passport.png'`)
	e.exec(`INSERT INTO attachments (message_id, filename, size_bytes, sha256, blob_key, disposition)
		VALUES ($1, 'gone.pdf', 1, sha256('x'::bytea), 'sha256/aa/bb/' || repeat('a', 64), 'attachment')`, f.janeMsg)

	data, err := BuildExport(ctx, e.q, e.store, f.jane, allMailboxIDs(t, e))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, zf := range zr.File {
		if strings.Contains(zf.Name, "..") || strings.HasPrefix(zf.Name, "/") {
			t.Errorf("unsafe entry name %q", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[zf.Name], err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	var doc struct {
		Name          string   `json:"name"`
		Phone         string   `json:"phone"`
		Emails        []string `json:"email_addresses"`
		Organization  struct{ Name string }
		Attributes    map[string]any `json:"custom_attributes"`
		Notes         []struct{ Author, Text string }
		Conversations []struct {
			Subject string
			Survey  *struct {
				Rating  int
				Comment string
			} `json:"satisfaction_survey"`
			Messages []struct {
				Kind, Direction, Subject, Text string
				From                           struct{ Name, Address string }
				Attachments                    []struct{ Filename, File string }
			}
		}
	}
	if err := json.Unmarshal(files["contact.json"], &doc); err != nil {
		t.Fatalf("contact.json: %v", err)
	}
	if doc.Name != "Jane Doe" || doc.Phone != "0612345678" || doc.Organization.Name != "Example Org" || doc.Attributes["tier"] != "gold" {
		t.Errorf("profile = %+v", doc)
	}
	if !slices.Equal(doc.Emails, []string{"jane.work@example.org", "jane@example.org"}) && !slices.Equal(doc.Emails, []string{"jane@example.org", "jane.work@example.org"}) {
		t.Errorf("emails = %v", doc.Emails)
	}
	if len(doc.Notes) != 1 || doc.Notes[0].Text != "jane wants a callback" {
		t.Errorf("notes = %+v", doc.Notes)
	}
	if len(doc.Conversations) != 2 {
		t.Fatalf("conversations = %d", len(doc.Conversations))
	}
	var texts []string
	var attachments []string
	var surveys []string
	for _, c := range doc.Conversations {
		if c.Survey != nil {
			surveys = append(surveys, c.Survey.Comment)
		}
		for _, m := range c.Messages {
			texts = append(texts, m.Text)
			for _, a := range m.Attachments {
				attachments = append(attachments, a.Filename+"="+a.File)
			}
		}
	}
	for _, want := range []string{"zebra secret-jane-word", "quokka reply", "internal note: jane called about the zebra", "shared conversation jane words", "kim words"} {
		if !slices.Contains(texts, want) {
			t.Errorf("message text %q missing from export (got %q)", want, texts)
		}
	}
	if !slices.Equal(surveys, []string{"zebra survey comment"}) {
		t.Errorf("survey comments in export = %q", surveys)
	}
	var pdfFile string
	for _, a := range attachments {
		if name, file, _ := strings.Cut(a, "="); name == "invoice.pdf" {
			pdfFile = file
		}
		if strings.HasPrefix(a, "gone.pdf=") && !strings.HasSuffix(a, "=") {
			t.Errorf("a missing blob must be listed without a file: %s", a)
		}
	}
	if pdfFile == "" || !bytes.Equal(files[pdfFile], f.sharedPDF) {
		t.Errorf("attachment file %q missing or wrong content", pdfFile)
	}
	if bytes.Contains(files["contact.json"], []byte("Bob")) || bytes.Contains(files["contact.json"], []byte("bob words")) {
		t.Error("export contains another contact's data")
	}
}

func TestExportWorkerAndPurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("admin")
	contact := e.contact("Jane", admin.ID, "jane@example.org")

	exportID, err := e.q.InsertDataExport(ctx, dbq.InsertDataExportParams{ContactID: contact, RequestedBy: admin.ID})
	if err != nil {
		t.Fatal(err)
	}
	w := NewExportWorker(e.pool, e.store)
	if err := w.Work(ctx, &river.Job[jobs.ContactExport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactExport{ExportID: exportID.String()}}); err != nil {
		t.Fatal(err)
	}
	exp, err := e.q.GetDataExport(ctx, exportID)
	if err != nil || exp.Status != "ready" || exp.BlobKey == "" || exp.SizeBytes == 0 {
		t.Fatalf("export = %+v %v", exp, err)
	}
	if !e.blobExists(exp.BlobKey) {
		t.Fatal("export blob missing")
	}

	// Once claimed, the download is gone for a second request.
	claimed, err := e.q.ClaimDataExportDownload(ctx, dbq.ClaimDataExportDownloadParams{ID: exportID, RequestedBy: admin.ID})
	if err != nil || claimed.BlobKey != exp.BlobKey {
		t.Fatalf("claim = %+v %v", claimed, err)
	}
	if _, err := e.q.ClaimDataExportDownload(ctx, dbq.ClaimDataExportDownloadParams{ID: exportID, RequestedBy: admin.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second claim: %v", err)
	}
	other := e.user("admin")
	second, err := e.q.InsertDataExport(ctx, dbq.InsertDataExportParams{ContactID: contact, RequestedBy: admin.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Work(ctx, &river.Job[jobs.ContactExport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactExport{ExportID: second.String()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.q.ClaimDataExportDownload(ctx, dbq.ClaimDataExportDownloadParams{ID: second, RequestedBy: other.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another admin claimed the export: %v", err)
	}

	// Purge removes the downloaded export, the expired one and week-old import files.
	e.exec(`UPDATE data_exports SET expires_at = now() - interval '1 minute' WHERE id = $1`, second)
	secondKey := e.blobKeyOf(`SELECT blob_key FROM data_exports WHERE id = $1`, second)
	errKey, _, err := e.store.Put(ctx, []byte("error report"))
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO contact_imports (created_by, dedupe, delimiter, mapping, total_rows, status, error_key, finished_at)
		VALUES ($1, 'skip', ',', '{}', 1, 'done', $2, now() - interval '8 days')`, admin.ID, errKey)
	if err := NewPurgeWorker(e.pool, e.store).Run(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"downloaded export": exp.BlobKey, "expired export": secondKey, "old error report": errKey} {
		if e.blobExists(key) {
			t.Errorf("%s was not purged", name)
		}
	}
	if e.count(`SELECT count(*) FROM data_exports WHERE blob_key <> ''`) != 0 {
		t.Error("export rows still reference blobs")
	}
}

func allMailboxIDs(t *testing.T, e *env) []pgtype.UUID {
	t.Helper()
	ids, err := e.q.ListAllMailboxIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestBuildExportStaysInsideTheGivenMailboxes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("admin")
	visible, hidden := e.mailbox("Visible", "visible@example.com"), e.mailbox("Hidden", "hidden@example.com")
	jane := e.contact("Jane", admin.ID, "jane@example.org")
	e.conversation(visible, jane, "In view", "open")
	e.conversation(hidden, jane, "Out of view", "open")

	data, err := BuildExport(ctx, e.q, e.store, jane, []pgtype.UUID{visible})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("In view")) || bytes.Contains(body, []byte("Out of view")) {
		t.Errorf("export is not limited to the given mailboxes: %s", body)
	}
}

func TestExportWorkerRefusesRequestersWhoDoNotSeeEverything(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	agent := e.user("agent")
	contact := e.contact("Jane", agent.ID, "jane@example.org")
	exportID, err := e.q.InsertDataExport(ctx, dbq.InsertDataExportParams{ContactID: contact, RequestedBy: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	w := NewExportWorker(e.pool, e.store)
	job := &river.Job[jobs.ContactExport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactExport{ExportID: exportID.String()}}
	if err := w.Work(ctx, job); err == nil {
		t.Fatal("export for an agent must be refused")
	}
	exp, err := e.q.GetDataExport(ctx, exportID)
	if err != nil || exp.Status != "failed" || exp.Error != "forbidden" || exp.BlobKey != "" {
		t.Fatalf("export = %+v %v", exp, err)
	}
}
