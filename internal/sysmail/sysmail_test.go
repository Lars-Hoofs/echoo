package sysmail

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/config"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/sysmail/sinktest"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	keys *keyring.Keyring
	sink *sinktest.Sink
	jobs *river.Client[pgx.Tx]
	// delivered counts the queued jobs the test has already run, standing in for River.
	delivered int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t)
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, pool: pool, keys: keys, sink: sinktest.Start(t), jobs: rc}
}

func (e *env) relaySender() *Sender {
	cfg := &config.Config{SystemSMTP: &config.SMTPRelay{Host: "127.0.0.1", Port: e.sink.Port, TLS: "implicit", From: "noreply@echoo.test", AllowInternal: true}}
	return New(e.pool, e.keys, nil, cfg, e.sink.Roots)
}

func (e *env) mailboxSender(address string) *Sender {
	return New(e.pool, e.keys, nil, &config.Config{SystemMailbox: address}, e.sink.Roots)
}

// addMailbox inserts a password mailbox whose SMTP server is the sink.
func (e *env) addMailbox(address string, disabled bool) {
	e.t.Helper()
	var id pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), "SELECT uuidv7()").Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	enc, err := e.keys.Encrypt([]byte("pw"), keyring.AAD("mailboxes", "smtp_secret_enc", id.String()))
	if err != nil {
		e.t.Fatal(err)
	}
	_, err = e.pool.Exec(context.Background(), `
		INSERT INTO mailboxes (id, name, email_address, display_name, smtp_host, smtp_port, smtp_tls, smtp_username, smtp_secret_enc, allow_internal_host, disabled_at)
		VALUES ($1, 'system', $2, 'Echoo', '127.0.0.1', $3, 'implicit', 'help', $4, true, CASE WHEN $5::boolean THEN now() END)`,
		id, address, e.sink.Port, enc, disabled)
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestSendThroughARelay(t *testing.T) {
	e := newEnv(t)
	err := e.relaySender().Send(t.Context(), "sanne@example.com", "Uitnodiging voor Echoo", "Open https://echoo.test/uitnodiging/abc en kies een wachtwoord.\n", "<p>hoi</p>")
	if err != nil {
		t.Fatal(err)
	}
	msgs := e.sink.Messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	m := msgs[0]
	if m.From != "noreply@echoo.test" || len(m.To) != 1 || m.To[0] != "sanne@example.com" {
		t.Errorf("envelope %q -> %v", m.From, m.To)
	}
	if m.Subject() != "Uitnodiging voor Echoo" {
		t.Errorf("subject %q", m.Subject())
	}
	if m.Link("https://echoo.test/uitnodiging/") != "https://echoo.test/uitnodiging/abc" {
		t.Errorf("text %q", m.Text())
	}
	if !strings.Contains(string(m.Raw), "Auto-Submitted:") {
		t.Error("system mail must be marked as automatic so auto-responders stay quiet")
	}
}

func TestSendThroughAMailbox(t *testing.T) {
	e := newEnv(t)
	e.addMailbox("help@example.com", false)
	if err := e.mailboxSender("help@example.com").Send(t.Context(), "sanne@example.com", "Onderwerp", "tekst", ""); err != nil {
		t.Fatal(err)
	}
	msgs := e.sink.Messages()
	if len(msgs) != 1 || msgs[0].From != "help@example.com" {
		t.Fatalf("messages: %+v", msgs)
	}
}

func TestUndeliverableSetupsDoNotRetry(t *testing.T) {
	e := newEnv(t)
	e.addMailbox("off@example.com", true)
	for name, s := range map[string]*Sender{
		"missing mailbox":  e.mailboxSender("nobody@example.com"),
		"disabled mailbox": e.mailboxSender("off@example.com"),
	} {
		if err := s.Send(t.Context(), "a@example.com", "x", "y", ""); !errors.Is(err, errUndeliverable) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if err := New(e.pool, e.keys, nil, &config.Config{}, nil).Send(t.Context(), "a@example.com", "x", "y", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("unconfigured: got %v", err)
	}
	if n := len(e.sink.Messages()); n != 0 {
		t.Errorf("%d messages were sent", n)
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	e.addMailbox("on@example.com", false)
	e.addMailbox("off@example.com", true)
	tests := []struct {
		name   string
		s      *Sender
		want   bool
		source string
	}{
		{"relay", e.relaySender(), true, SourceSMTP},
		{"mailbox", e.mailboxSender("on@example.com"), true, SourceMailbox},
		{"disabled mailbox", e.mailboxSender("off@example.com"), false, SourceMailbox},
		{"unknown mailbox", e.mailboxSender("nobody@example.com"), false, SourceMailbox},
		{"nothing configured", New(e.pool, e.keys, nil, &config.Config{}, nil), false, ""},
	}
	for _, tc := range tests {
		st, err := tc.s.Status(t.Context())
		if err != nil || st.Available != tc.want || st.Source != tc.source {
			t.Errorf("%s: %+v, %v", tc.name, st, err)
		}
	}
}

func (e *env) queued() []*river.Job[jobs.SysmailSend] {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT args FROM river_job WHERE kind = 'sysmail.send' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []*river.Job[jobs.SysmailSend]
	for rows.Next() {
		var args jobs.SysmailSend
		if err := rows.Scan(&args); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, &river.Job[jobs.SysmailSend]{Args: args})
	}
	return out
}

func TestEnqueuedMailIsSealedAndDeliveredByTheWorker(t *testing.T) {
	e := newEnv(t)
	s := e.relaySender()
	tx, err := e.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(t.Context(), tx, e.jobs, Message{To: "sanne@example.com", Subject: "Onderwerp", Text: "geheime link https://echoo.test/x/topsecret"}); err != nil {
		t.Fatal(err)
	}
	// Nothing is queued before the caller's transaction commits.
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.queued()); n != 0 {
		t.Fatalf("%d jobs survived a rollback", n)
	}

	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		return s.Enqueue(t.Context(), tx, e.jobs, Message{To: "sanne@example.com", Subject: "Onderwerp", Text: "geheime link https://echoo.test/x/topsecret"})
	}); err != nil {
		t.Fatal(err)
	}
	queued := e.queued()
	if len(queued) != 1 {
		t.Fatalf("%d jobs", len(queued))
	}
	if strings.Contains(string(queued[0].Args.Payload), "topsecret") || strings.Contains(string(queued[0].Args.Payload), "sanne@") {
		t.Error("the job arguments must not hold the message in the clear")
	}
	var stored string
	if err := e.pool.QueryRow(t.Context(), `SELECT args::text FROM river_job WHERE kind = 'sysmail.send'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "topsecret") || strings.Contains(stored, "sanne@") {
		t.Error("the stored job holds the message in the clear")
	}

	if err := NewWorker(s).Work(t.Context(), queued[0]); err != nil {
		t.Fatal(err)
	}
	if msgs := e.sink.Messages(); len(msgs) != 1 || msgs[0].Link("https://echoo.test/x/") != "https://echoo.test/x/topsecret" {
		t.Fatalf("messages: %+v", msgs)
	}
}

func TestWorkerCancelsWhatCannotBeDelivered(t *testing.T) {
	e := newEnv(t)
	s := e.mailboxSender("nobody@example.com")
	if err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		return s.Enqueue(t.Context(), tx, e.jobs, Message{To: "a@example.com", Subject: "x", Text: "y"})
	}); err != nil {
		t.Fatal(err)
	}
	err := NewWorker(s).Work(t.Context(), e.queued()[0])
	var cancel *river.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("got %v, want a cancelled job", err)
	}

	bad := &river.Job[jobs.SysmailSend]{Args: jobs.SysmailSend{Payload: []byte("garbage")}}
	if err := NewWorker(s).Work(t.Context(), bad); !errors.As(err, &cancel) {
		t.Fatalf("garbage payload: got %v, want a cancelled job", err)
	}

	if err := New(e.pool, e.keys, nil, &config.Config{}, nil).Enqueue(t.Context(), nil, e.jobs, Message{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("enqueue without configuration: %v", err)
	}
}

func (e *env) user(email, name string, mentions, assignments bool) pgtype.UUID {
	e.t.Helper()
	var id pgtype.UUID
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO users (email, name, role, password_hash, email_notify_mentions, email_notify_assignments)
		VALUES ($1, $2, 'agent', 'x', $3, $4) RETURNING id`, email, name, mentions, assignments).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) conversation() (pgtype.UUID, int64) {
	e.t.Helper()
	e.addMailbox("conv-"+strings.ToLower(rand.Text())+"@example.com", false)
	var id pgtype.UUID
	var number int64
	err := e.pool.QueryRow(context.Background(), `
		INSERT INTO conversations (mailbox_id, subject) VALUES ((SELECT id FROM mailboxes ORDER BY created_at DESC LIMIT 1), 'Vraag') RETURNING id, number`).Scan(&id, &number)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, number
}

func (e *env) notify(user, actor, conv pgtype.UUID, kind string) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO notifications (user_id, kind, conversation_id, actor_id) VALUES ($1, $2, $3, $4)`, user, kind, conv, actor); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) runNotifications(s *Sender) {
	e.t.Helper()
	if err := NewNotificationWorker(e.pool, s, "https://echoo.test").run(context.Background(), e.jobs); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) pendingNotifications() int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE email_handled_at IS NULL`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) deliverQueued(s *Sender) {
	e.t.Helper()
	queued := e.queued()
	for _, j := range queued[e.delivered:] {
		if err := NewWorker(s).Work(context.Background(), j); err != nil {
			e.t.Fatal(err)
		}
	}
	e.delivered = len(queued)
}

func TestNotificationMailForOptedInUsers(t *testing.T) {
	e := newEnv(t)
	s := e.relaySender()
	sanne := e.user("sanne@example.com", "Sanne de Vries", false, false)
	optedIn := e.user("piet@example.com", "Piet", true, true)
	mentionsOnly := e.user("mo@example.com", "Mo", true, false)
	conv, number := e.conversation()

	e.notify(optedIn, sanne, conv, "mention")
	e.notify(optedIn, sanne, conv, "assigned")
	e.notify(mentionsOnly, sanne, conv, "assigned") // did not opt in to assignments
	e.notify(mentionsOnly, sanne, conv, "reply")    // no email kind
	e.notify(sanne, sanne, conv, "mention")         // did not opt in at all

	e.runNotifications(s)
	if n := e.pendingNotifications(); n != 0 {
		t.Fatalf("%d notifications left unhandled", n)
	}
	e.deliverQueued(s)

	msgs := e.sink.Messages()
	if len(msgs) != 2 {
		t.Fatalf("%d emails, want 2 (mention and assignment for the opted-in user)", len(msgs))
	}
	subjects := map[string]bool{}
	for _, m := range msgs {
		if len(m.To) != 1 || m.To[0] != "piet@example.com" {
			t.Errorf("sent to %v", m.To)
		}
		subjects[m.Subject()] = true
		if m.Link("https://echoo.test/inbox/alle/") != "https://echoo.test/inbox/alle/"+conv.String() {
			t.Errorf("link in %q", m.Text())
		}
	}
	wantMention := "Sanne de Vries noemde je in gesprek #" + itoa(number)
	wantAssigned := "Sanne de Vries heeft gesprek #" + itoa(number) + " aan je toegewezen"
	if !subjects[wantMention] || !subjects[wantAssigned] {
		t.Errorf("subjects %v, want %q and %q", subjects, wantMention, wantAssigned)
	}

	// A second run finds nothing to do.
	e.runNotifications(s)
	e.deliverQueued(s)
	if len(e.sink.Messages()) != 2 {
		t.Error("a second run mailed again")
	}
}

func TestReplyNotificationMailOnlyForOptedInUsers(t *testing.T) {
	e := newEnv(t)
	s := e.relaySender()
	optedIn := e.user("piet@example.com", "Piet", false, false)
	other := e.user("mo@example.com", "Mo", true, true)
	if _, err := e.pool.Exec(t.Context(), `UPDATE users SET email_notify_replies = true WHERE id = $1`, optedIn); err != nil {
		t.Fatal(err)
	}
	conv, number := e.conversation()

	e.notify(optedIn, pgtype.UUID{}, conv, "reply")
	e.notify(other, pgtype.UUID{}, conv, "reply")

	e.runNotifications(s)
	e.deliverQueued(s)
	msgs := e.sink.Messages()
	if len(msgs) != 1 || len(msgs[0].To) != 1 || msgs[0].To[0] != "piet@example.com" {
		t.Fatalf("emails: %d, want one for the user who opted in to replies", len(msgs))
	}
	if want := "Nieuw antwoord van de klant in gesprek #" + itoa(number); msgs[0].Subject() != want {
		t.Errorf("subject %q, want %q", msgs[0].Subject(), want)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestNotificationMailSkipsReadAndOldNotificationsAndWaitsForSystemMail(t *testing.T) {
	e := newEnv(t)
	actor := e.user("a@example.com", "Sanne", false, false)
	piet := e.user("piet@example.com", "Piet", true, true)
	conv, _ := e.conversation()

	e.notify(piet, actor, conv, "mention")
	if _, err := e.pool.Exec(t.Context(), `UPDATE notifications SET read_at = now()`); err != nil {
		t.Fatal(err)
	}
	e.notify(piet, actor, conv, "mention")
	if _, err := e.pool.Exec(t.Context(), `UPDATE notifications SET created_at = now() - interval '2 hours' WHERE read_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	e.notify(piet, actor, conv, "assigned")

	// Without system mail nothing is claimed, so a later configuration still sends the fresh one.
	unconfigured := New(e.pool, e.keys, nil, &config.Config{}, nil)
	e.runNotifications(unconfigured)
	if n := e.pendingNotifications(); n != 3 {
		t.Fatalf("%d pending before system mail exists, want all 3", n)
	}

	e.runNotifications(e.relaySender())
	if n := e.pendingNotifications(); n != 0 {
		t.Fatalf("%d pending after the run: read and stale rows must be marked handled too", n)
	}
	e.deliverQueued(e.relaySender())
	if msgs := e.sink.Messages(); len(msgs) != 1 || !strings.Contains(msgs[0].Subject(), "toegewezen") {
		t.Fatalf("emails: %d", len(msgs))
	}
}
