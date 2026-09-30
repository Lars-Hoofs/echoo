package retention

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/db/dbq"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *dbq.Queries
	store *storage.FS
	svc   *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t)
	return &env{t: t, pool: pool, q: dbq.New(pool), store: store, svc: NewService(pool, store)}
}

func (e *env) id(sql string, args ...any) pgtype.UUID {
	e.t.Helper()
	var id pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) mailbox(name string) pgtype.UUID {
	return e.id(`INSERT INTO mailboxes (name, email_address) VALUES ($1, $2) RETURNING id`, name, strings.ToLower(name)+"@example.com")
}

func days(n int) time.Time { return time.Now().AddDate(0, 0, -n) }

// conversation inserts a conversation whose last activity was age days ago.
func (e *env) conversation(mailbox pgtype.UUID, status string, age int) pgtype.UUID {
	return e.id(`INSERT INTO conversations (mailbox_id, subject, subject_normalized, status, last_message_at, resolved_at)
		VALUES ($1, 'Onderwerp', 'onderwerp', $2::text, $3::timestamptz, CASE WHEN $2::text = 'closed' THEN $3::timestamptz END) RETURNING id`, mailbox, status, days(age))
}

type seeded struct {
	message pgtype.UUID
	raw     string
	files   []string
}

// message adds an email with a raw copy and attachments, all stored in the blob store. Content
// is what makes blob keys unique; passing the same content twice shares the blob.
func (e *env) message(conv, mailbox pgtype.UUID, age int, content string, attachments ...string) seeded {
	e.t.Helper()
	ctx := context.Background()
	rawKey, sum, err := e.store.Put(ctx, []byte("raw "+content))
	if err != nil {
		e.t.Fatal(err)
	}
	raw := e.id(`INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key, uidvalidity, uid, parse_status)
		VALUES ($1, 'imap', $2, 10, $3, 1, $4, 'parsed') RETURNING id`, mailbox, sum, rawKey, time.Now().UnixNano())
	msg := e.id(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, raw_message_id, message_id_hash, from_addr, subject, body_text, received_at)
		VALUES ($1, $2, 'email', 'in', $3, sha256($4::bytea), 'klant@example.com', 'Onderwerp', $5, $6) RETURNING id`,
		conv, mailbox, raw, []byte(content), "Tekst "+content, days(age))
	out := seeded{message: msg, raw: rawKey}
	for _, a := range attachments {
		key, sum, err := e.store.Put(ctx, []byte(a))
		if err != nil {
			e.t.Fatal(err)
		}
		e.exec(`INSERT INTO attachments (message_id, filename, sniffed_type, size_bytes, sha256, blob_key, disposition)
			VALUES ($1, 'bijlage.pdf', 'application/pdf', $2, $3, $4, 'attachment')`, msg, len(a), sum, key)
		out.files = append(out.files, key)
	}
	e.exec(`UPDATE conversations SET has_attachments = $2, message_count = 1 WHERE id = $1`, conv, len(attachments) > 0)
	return out
}

func (e *env) blobExists(key string) bool {
	rc, err := e.store.Open(context.Background(), key)
	if err != nil {
		return false
	}
	_ = rc.Close()
	return true
}

func (e *env) auditCount(action string) int {
	return e.count(`SELECT count(*) FROM audit_log WHERE action = $1`, action)
}

func (e *env) setSettings(s Settings) {
	e.t.Helper()
	if err := Save(context.Background(), e.q, s, pgtype.UUID{}); err != nil {
		e.t.Fatal(err)
	}
}

func months(n int) *int { return &n }

func TestClosedConversationsAreDeletedWithTheirFiles(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	shared := "gedeeld pdf"

	oldClosed := e.conversation(box, "closed", 300)
	old := e.message(oldClosed, box, 300, "oud", "alleen oud", shared)
	recentClosed := e.conversation(box, "closed", 20)
	recent := e.message(recentClosed, box, 20, "recent", shared)
	oldOpen := e.conversation(box, "open", 400)
	open := e.message(oldOpen, box, 400, "open", "open bijlage")
	oldSpam := e.conversation(box, "spam", 400)
	e.message(oldSpam, box, 400, "spam")

	e.setSettings(Settings{Global: Periods{ClosedConversationMonths: months(6)}, Mailboxes: []MailboxPeriods{}})
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ClosedConversations != 1 || res.SpamConversations != 0 {
		t.Fatalf("result = %+v", res)
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1`, oldClosed) != 0 || e.count(`SELECT count(*) FROM messages WHERE id = $1`, old.message) != 0 {
		t.Error("the old closed conversation and its messages should be gone")
	}
	for id, name := range map[pgtype.UUID]string{recentClosed: "recent closed", oldOpen: "old open", oldSpam: "old spam"} {
		if e.count(`SELECT count(*) FROM conversations WHERE id = $1`, id) != 1 {
			t.Errorf("%s conversation must stay", name)
		}
	}
	if e.blobExists(old.raw) || e.blobExists(old.files[0]) {
		t.Error("raw message and attachment of the deleted conversation should be deleted from storage")
	}
	if !e.blobExists(old.files[1]) {
		t.Error("a file that a kept conversation also uses must stay (content addressed)")
	}
	if !e.blobExists(recent.raw) || !e.blobExists(open.raw) || !e.blobExists(open.files[0]) {
		t.Error("files of kept conversations must stay")
	}
	if e.count(`SELECT count(*) FROM raw_messages WHERE blob_key = ''`) != 1 {
		t.Error("the raw row stays, blanked, so IMAP sync does not import the message again")
	}
	if e.count(`SELECT count(*) FROM pending_blob_deletions`) != 0 {
		t.Error("the deletion queue should be drained")
	}
	var meta string
	if err := e.pool.QueryRow(context.Background(), `SELECT metadata::text FROM audit_log WHERE action = $1`, audit.RetentionPurged).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"conversations": 1`, `"messages": 1`, `"attachments": 2`, `"kind": "closed_conversations"`} {
		if !strings.Contains(meta, want) {
			t.Errorf("audit metadata %s lacks %s", meta, want)
		}
	}
}

func TestMailboxOverrideBeatsWorkspacePeriod(t *testing.T) {
	e := newEnv(t)
	plain, kept := e.mailbox("Plain"), e.mailbox("Kept")
	a := e.conversation(plain, "closed", 400)
	b := e.conversation(kept, "closed", 400)
	c := e.conversation(kept, "spam", 40)
	e.setSettings(Settings{
		Global:    Periods{ClosedConversationMonths: months(6), SpamDays: months(30)},
		Mailboxes: []MailboxPeriods{{MailboxID: kept, Periods: Periods{SpamDays: months(30)}}},
	})
	if _, err := e.svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1`, a) != 0 {
		t.Error("the workspace period applies to a mailbox without override")
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1`, b) != 1 {
		t.Error("an override without a closed period keeps closed conversations forever")
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1`, c) != 0 {
		t.Error("the override's own spam period applies")
	}
}

func TestAttachmentsAreDeletedAndTextStays(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	conv := e.conversation(box, "open", 5)
	old := e.message(conv, box, 200, "oud", "grote bijlage")
	recent := e.message(conv, box, 10, "nieuw", "verse bijlage")

	e.setSettings(Settings{Global: Periods{AttachmentMonths: months(3)}, Mailboxes: []MailboxPeriods{}})
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Attachments != 1 {
		t.Fatalf("result = %+v", res)
	}
	if e.count(`SELECT count(*) FROM messages WHERE id = $1 AND body_text = 'Tekst oud'`, old.message) != 1 {
		t.Error("the message text must stay")
	}
	if e.count(`SELECT count(*) FROM attachments WHERE message_id = $1`, old.message) != 0 || e.blobExists(old.files[0]) {
		t.Error("the old attachment row and file should be gone")
	}
	if e.blobExists(old.raw) {
		t.Error("the raw copy of the message still contains the file and should be gone")
	}
	if e.count(`SELECT count(*) FROM attachments WHERE message_id = $1`, recent.message) != 1 || !e.blobExists(recent.files[0]) || !e.blobExists(recent.raw) {
		t.Error("recent attachments and raw copies must stay")
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1 AND has_attachments`, conv) != 1 {
		t.Error("has_attachments stays true while another message still has files")
	}
}

func TestHasAttachmentsClearedWhenLastFileGoes(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	conv := e.conversation(box, "open", 5)
	e.message(conv, box, 200, "oud", "bijlage")
	e.setSettings(Settings{Global: Periods{AttachmentMonths: months(3)}, Mailboxes: []MailboxPeriods{}})
	if _, err := e.svc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id = $1 AND NOT has_attachments`, conv) != 1 {
		t.Error("has_attachments should turn false")
	}
}

func TestSpamIsDeletedAfterDays(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	old := e.conversation(box, "spam", 20)
	fresh := e.conversation(box, "spam", 3)
	e.setSettings(Settings{Global: Periods{SpamDays: months(14)}, Mailboxes: []MailboxPeriods{}})
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.SpamConversations != 1 || e.count(`SELECT count(*) FROM conversations WHERE id = $1`, old) != 0 || e.count(`SELECT count(*) FROM conversations WHERE id = $1`, fresh) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestBatchesOfThousand(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	e.exec(`INSERT INTO conversations (mailbox_id, subject, subject_normalized, status, last_message_at)
		SELECT $1, 'x', 'x', 'closed', now() - interval '400 days' FROM generate_series(1, 2050)`, box)
	e.setSettings(Settings{Global: Periods{ClosedConversationMonths: months(6)}, Mailboxes: []MailboxPeriods{}})
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ClosedConversations != 2050 || e.count(`SELECT count(*) FROM conversations`) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if n := e.auditCount(audit.RetentionPurged); n != 3 {
		t.Errorf("a batch is audited on its own: %d audit entries, want 3", n)
	}
}

func TestPreviewMatchesWhatRunDeletes(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	closed := e.conversation(box, "closed", 300)
	e.message(closed, box, 300, "een", "pdf een")
	e.message(closed, box, 299, "twee")
	spam := e.conversation(box, "spam", 120)
	e.message(spam, box, 120, "spam", "bijlage spam")
	open := e.conversation(box, "open", 5)
	e.message(open, box, 200, "open", "oude bijlage")
	e.exec(`INSERT INTO audit_log (at, action) SELECT now() - interval '500 days', 'x' FROM generate_series(1, 7)`)

	set := Settings{Global: Periods{ClosedConversationMonths: months(6), SpamDays: months(30), AttachmentMonths: months(3)}, AuditMonths: months(12), Mailboxes: []MailboxPeriods{}}
	p, err := e.svc.Preview(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	if p.ClosedConversations.Conversations != 1 || p.ClosedConversations.Messages != 2 || p.ClosedConversations.Attachments != 1 {
		t.Errorf("closed preview = %+v", p.ClosedConversations)
	}
	if p.SpamConversations.Conversations != 1 || p.SpamConversations.Attachments != 1 {
		t.Errorf("spam preview = %+v", p.SpamConversations)
	}
	if p.Attachments.Attachments != 3 || p.AuditEntries != 7 {
		t.Errorf("attachments %+v audit %d", p.Attachments, p.AuditEntries)
	}
	if e.count(`SELECT count(*) FROM conversations`) != 3 {
		t.Fatal("a preview must not delete anything")
	}

	e.setSettings(set)
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ClosedConversations != p.ClosedConversations.Conversations || res.SpamConversations != p.SpamConversations.Conversations || res.AuditEntries != p.AuditEntries {
		t.Errorf("run %+v differs from preview %+v", res, p)
	}
}

func TestConversationsWithMailInFlightAndLockedRowsAreSkipped(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	sending := e.conversation(box, "closed", 400)
	msg := e.message(sending, box, 400, "uitgaand")
	e.exec(`UPDATE messages SET direction = 'out' WHERE id = $1`, msg.message)
	e.exec(`INSERT INTO outbound (message_id, idempotency_key, status) VALUES ($1, uuidv7(), 'retry')`, msg.message)
	locked := e.conversation(box, "closed", 400)
	free := e.conversation(box, "closed", 400)

	tx, err := e.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT 1 FROM conversations WHERE id = $1 FOR UPDATE`, locked); err != nil {
		t.Fatal(err)
	}

	e.setSettings(Settings{Global: Periods{ClosedConversationMonths: months(6)}, Mailboxes: []MailboxPeriods{}})
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ClosedConversations != 1 || e.count(`SELECT count(*) FROM conversations WHERE id = $1`, free) != 0 {
		t.Fatalf("only the unlocked conversation without pending mail goes: %+v", res)
	}
	if e.count(`SELECT count(*) FROM conversations WHERE id IN ($1, $2)`, sending, locked) != 2 {
		t.Error("pending mail and locked rows must be left alone")
	}
}

func TestUploadsPurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	user, err := e.q.CreateUser(ctx, dbq.CreateUserParams{Email: "u@example.com", Name: "U", Role: "agent", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	put := func(content string) (string, []byte) {
		key, sum, err := e.store.Put(ctx, []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		return key, sum
	}
	upload := func(content string, expires string) string {
		key, sum := put(content)
		e.exec(`INSERT INTO uploads (user_id, filename, content_type, size_bytes, sha256, blob_key, content_id, created_at, expires_at)
			VALUES ($1, 'f', 'text/plain', 1, $2, $3, $4, now() - interval '48 hours', now() + $5::interval)`, user.ID, sum, key, content+"@cid", expires)
		return key
	}
	expired := upload("verlopen", "-24 hours")
	usedElsewhere := upload("ook bijlage", "-24 hours")
	fresh := upload("vers", "1 hour")
	box := e.mailbox("Support")
	conv := e.conversation(box, "open", 1)
	msg := e.message(conv, box, 1, "bericht")
	_, sum := put("ook bijlage")
	e.exec(`INSERT INTO attachments (message_id, filename, sniffed_type, size_bytes, sha256, blob_key, disposition) VALUES ($1, 'a', 'text/plain', 1, $2, $3, 'attachment')`, msg.message, sum, usedElsewhere)

	n, err := e.svc.PurgeUploads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || e.count(`SELECT count(*) FROM uploads`) != 1 {
		t.Fatalf("purged %d, %d uploads left", n, e.count(`SELECT count(*) FROM uploads`))
	}
	if e.blobExists(expired) {
		t.Error("the file of an expired upload should be deleted")
	}
	if !e.blobExists(usedElsewhere) || !e.blobExists(fresh) {
		t.Error("files that are still referenced must stay")
	}
	if e.auditCount(audit.UploadsPurged) != 1 {
		t.Error("the purge is audited with its count")
	}
}

func TestValidate(t *testing.T) {
	box := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	bad := Settings{
		Global:      Periods{ClosedConversationMonths: months(0), SpamDays: months(4000)},
		AuditMonths: months(1),
		Mailboxes:   []MailboxPeriods{{MailboxID: box}, {MailboxID: box}, {MailboxID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}}},
	}
	problems := Validate(bad, []pgtype.UUID{box})
	for _, key := range []string{"global.closed_conversation_months", "global.spam_days", "audit_months", "mailboxes[1].mailbox_id", "mailboxes[2].mailbox_id"} {
		if problems[key] == "" {
			t.Errorf("no problem reported for %s: %v", key, problems)
		}
	}
	if got := Validate(Settings{AuditMonths: months(12), Mailboxes: []MailboxPeriods{{MailboxID: box}}}, []pgtype.UUID{box}); len(got) != 0 {
		t.Errorf("valid settings rejected: %v", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	def, err := Load(ctx, e.q)
	if err != nil {
		t.Fatal(err)
	}
	if def.AuditMonths == nil || *def.AuditMonths != DefaultAuditMonths || def.Global.ClosedConversationMonths != nil {
		t.Fatalf("defaults = %+v", def)
	}
	box := e.mailbox("Support")
	want := Settings{Global: Periods{SpamDays: months(30)}, AuditMonths: nil, Mailboxes: []MailboxPeriods{{MailboxID: box, Periods: Periods{AttachmentMonths: months(6)}}}}
	e.setSettings(want)
	got, err := Load(ctx, e.q)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuditMonths != nil || *got.Global.SpamDays != 30 || len(got.Mailboxes) != 1 || *got.Mailboxes[0].AttachmentMonths != 6 || got.Mailboxes[0].SpamDays != nil {
		t.Fatalf("loaded = %+v", got)
	}
}

var createRole sync.Once

// restricted returns a pool whose connections act as the role docker/postgres-init creates for
// the server: data access on tables and nothing else. The test database's own user is a
// superuser, which would bypass every check that matters here.
func (e *env) restricted() *pgxpool.Pool {
	e.t.Helper()
	ctx := context.Background()
	createRole.Do(func() {
		_, err := e.pool.Exec(ctx, `CREATE ROLE echoo_app_test NOLOGIN`)
		var pgErr *pgconn.PgError
		if err != nil && (!errors.As(err, &pgErr) || pgErr.Code != "42710") {
			e.t.Fatal(err)
		}
	})
	for _, stmt := range []string{
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO echoo_app_test`,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO echoo_app_test`,
		`GRANT EXECUTE ON FUNCTION audit_log_purge(interval, integer) TO echoo_app_test`,
	} {
		e.exec(stmt)
	}
	cfg := e.pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE echoo_app_test`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(pool.Close)
	return pool
}

func TestAuditLogStaysAppendOnlyExceptThroughThePurgeFunction(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	app := e.restricted()
	e.exec(`INSERT INTO audit_log (at, action) SELECT now() - interval '400 days', 'old' FROM generate_series(1, 2500)`)
	e.exec(`INSERT INTO audit_log (at, action) SELECT now() - interval '10 days', 'recent' FROM generate_series(1, 5)`)

	mustFail := func(who string, pool *pgxpool.Pool, sql, want string) {
		t.Helper()
		_, err := pool.Exec(ctx, sql)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %s: err = %v, want %q", who, sql, err, want)
		}
	}
	mustFail("app", app, `DELETE FROM audit_log`, "append-only")
	mustFail("app", app, `UPDATE audit_log SET action = 'x'`, "append-only")
	mustFail("app", app, `TRUNCATE audit_log`, "permission denied")
	// Setting the flag is not enough: the trigger also requires the table owner.
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('echoo.audit_purge', 'on', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM audit_log`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("app with the flag set: err = %v", err)
	}
	_ = tx.Rollback(ctx)
	mustFail("owner", e.pool, `DELETE FROM audit_log`, "append-only")
	mustFail("app", app, `SELECT audit_log_purge(interval '30 days', 100)`, "never purged")
	mustFail("app", app, `SELECT audit_log_purge(interval '12 months', 0)`, "batch_limit")

	var n int
	if err := app.QueryRow(ctx, `SELECT audit_log_purge(interval '12 months', 1000)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1000 {
		t.Fatalf("first batch deleted %d, want 1000", n)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'audit.purged' AND (metadata->>'count')::int = 1000`) != 1 {
		t.Error("the batch must be logged with its count")
	}

	res, err := NewService(app, e.store).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.AuditEntries != 1500 {
		t.Fatalf("service purged %d, want the remaining 1500", res.AuditEntries)
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'old'`) != 0 || e.count(`SELECT count(*) FROM audit_log WHERE action = 'recent'`) != 5 {
		t.Error("only entries past the period may go")
	}
	if got := e.auditCount(audit.AuditPurged); got != 3 {
		t.Errorf("%d purge entries, want one per batch (1000, 1000, 500)", got)
	}
}

func TestWholePurgeRunsAsRestrictedRole(t *testing.T) {
	e := newEnv(t)
	app := e.restricted()
	box := e.mailbox("Support")
	conv := e.conversation(box, "closed", 400)
	old := e.message(conv, box, 400, "oud", "bijlage")
	e.setSettings(Settings{Global: Periods{ClosedConversationMonths: months(6), AttachmentMonths: months(3)}, AuditMonths: months(12), Mailboxes: []MailboxPeriods{}})
	if _, err := NewService(app, e.store).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM conversations`) != 0 || e.blobExists(old.files[0]) {
		t.Error("the purge must work with data-access rights only")
	}
}

func TestPurgeWorkerRecordsTheRun(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	e.conversation(box, "closed", 400)
	e.setSettings(Settings{Global: Periods{ClosedConversationMonths: months(6)}, AuditMonths: months(12), Mailboxes: []MailboxPeriods{}})
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.recordRun(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	last, err := LoadLastRun(context.Background(), e.q)
	if err != nil || last == nil || last.ClosedConversation != 1 {
		t.Fatalf("last run = %+v, %v", last, err)
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

func TestAFileThatCannotBeDeletedIsReportedAndDoesNotBlockOtherMailboxes(t *testing.T) {
	e := newEnv(t)
	a, b := e.mailbox("Alpha"), e.mailbox("Beta")
	stuck := e.message(e.conversation(a, "closed", 400), a, 400, "vast", "kapot")
	second := e.conversation(b, "closed", 400)
	e.message(second, b, 400, "los")
	e.setSettings(Settings{Global: Periods{ClosedConversationMonths: months(6)}, Mailboxes: []MailboxPeriods{}})

	svc := NewService(e.pool, failingDeleter{FS: e.store, key: stuck.files[0]})
	res, err := svc.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not be deleted") {
		t.Fatalf("the failure must be reported: %v", err)
	}
	if res.ClosedConversations != 2 || e.count(`SELECT count(*) FROM conversations`) != 0 {
		t.Fatalf("both mailboxes are purged: %+v", res)
	}
	if e.count(`SELECT count(*) FROM pending_blob_deletions WHERE blob_key = $1`, stuck.files[0]) != 1 || !e.blobExists(stuck.files[0]) {
		t.Error("the stuck file stays queued for the hourly retry")
	}
}

func TestTrashIsEmptiedAfterDays(t *testing.T) {
	e := newEnv(t)
	box := e.mailbox("Support")
	old := e.conversation(box, "open", 1)
	e.message(old, box, 1, "weg", "bijlage weg")
	e.exec(`UPDATE conversations SET deleted_at = now() - interval '40 days' WHERE id = $1`, old)
	recent := e.conversation(box, "closed", 400)
	e.exec(`UPDATE conversations SET deleted_at = now() - interval '5 days' WHERE id = $1`, recent)
	notTrashed := e.conversation(box, "open", 400)

	set := Settings{Global: Periods{TrashDays: months(30)}, Mailboxes: []MailboxPeriods{}}
	p, err := e.svc.Preview(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	if p.TrashConversations.Conversations != 1 || p.TrashConversations.Attachments != 1 {
		t.Errorf("trash preview = %+v", p.TrashConversations)
	}
	e.setSettings(set)
	res, err := e.svc.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.TrashConversations != 1 || e.count(`SELECT count(*) FROM conversations WHERE id = $1`, old) != 0 {
		t.Fatalf("result = %+v", res)
	}
	for _, id := range []pgtype.UUID{recent, notTrashed} {
		if e.count(`SELECT count(*) FROM conversations WHERE id = $1`, id) != 1 {
			t.Errorf("conversation %s was deleted", id)
		}
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND metadata->>'kind' = 'trash_conversations'`, audit.RetentionPurged); n != 1 {
		t.Errorf("trash purge audit entries = %d", n)
	}
}

func TestTrashDaysDefaultTo30(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	load := func() Settings {
		t.Helper()
		s, err := Load(ctx, e.q)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := load(); s.Global.TrashDays == nil || *s.Global.TrashDays != DefaultTrashDays {
		t.Fatalf("never saved: trash_days = %v", s.Global.TrashDays)
	}
	// Settings saved before the trash existed have no trash_days key.
	err := e.q.UpsertSetting(ctx, dbq.UpsertSettingParams{Key: settingsKey, Value: []byte(`{"global":{"spam_days":14},"audit_months":12}`)})
	if err != nil {
		t.Fatal(err)
	}
	if s := load(); s.Global.TrashDays == nil || *s.Global.TrashDays != DefaultTrashDays || *s.Global.SpamDays != 14 {
		t.Fatalf("saved without the key: %+v", s.Global)
	}
	e.setSettings(Settings{Global: Periods{TrashDays: nil}, Mailboxes: []MailboxPeriods{}})
	if s := load(); s.Global.TrashDays != nil {
		t.Fatalf("an explicit null must keep the trash forever, got %d", *s.Global.TrashDays)
	}
	if got := Validate(Settings{Global: Periods{TrashDays: months(0)}, Mailboxes: []MailboxPeriods{}}, nil); got["global.trash_days"] == "" {
		t.Errorf("trash_days 0 accepted: %v", got)
	}
}
