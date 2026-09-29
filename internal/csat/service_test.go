package csat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *Service
	signer  *Signer
	mailbox pgtype.UUID
	agent   pgtype.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, pool: pool, signer: testSigner()}
	f.svc = NewService(Deps{Pool: pool, Signer: f.signer, BaseURL: "https://echoo.test", Jobs: client})
	f.mailbox = f.id(`INSERT INTO mailboxes (name, email_address, display_name) VALUES ('Support', 'support@shop.example', 'Shop Support') RETURNING id`)
	f.agent = f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('a@shop.example', 'Anna', 'agent', 'x') RETURNING id`)
	return f
}

func (f *fixture) id(sql string, args ...any) pgtype.UUID {
	f.t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) enable(delayHours int) {
	f.t.Helper()
	f.exec(`INSERT INTO csat_settings (mailbox_id, enabled, delay_hours, enabled_at) VALUES ($1, true, $2, now() - interval '30 days')
		ON CONFLICT (mailbox_id) DO UPDATE SET enabled = true, delay_hours = $2`, f.mailbox, delayHours)
}

type resolved struct {
	ago      time.Duration // how long ago the conversation was resolved
	from     string
	fromName string
	auto     bool
	bounce   bool
	bulk     bool
	noInbox  bool // only an outbound message
	status   string
	assignee pgtype.UUID
}

func (f *fixture) conversation(r resolved) pgtype.UUID {
	f.t.Helper()
	if r.from == "" {
		r.from = "jane@acme.example"
	}
	if r.status == "" {
		r.status = "closed"
	}
	at := time.Now().Add(-r.ago)
	id := f.id(`INSERT INTO conversations (mailbox_id, subject, status, resolved_at, assignee_user_id, created_at)
		VALUES ($1, 'Factuur', $2, $3, $4, $5) RETURNING id`, f.mailbox, r.status, at, r.assignee, at.Add(-24*time.Hour))
	if r.noInbox {
		f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, received_at)
			VALUES ($1, $2, 'email', 'out', 'out-' || uuidv7()::text || '@shop.example', 'support@shop.example', $3)`, id, f.mailbox, at.Add(-time.Hour))
		return id
	}
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, received_at, auto_submitted, is_bounce, is_bulk)
		VALUES ($1, $2, 'email', 'in', 'in-' || uuidv7()::text || '@acme.example', $3, $4, $5, $6, $7, $8)`,
		id, f.mailbox, r.from, r.fromName, at.Add(-time.Hour), r.auto, r.bounce, r.bulk)
	return id
}

func (f *fixture) sweep() int {
	f.t.Helper()
	n, err := f.svc.Sweep(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) surveyed(conv pgtype.UUID) bool {
	return f.count(`SELECT count(*) FROM csat_requests WHERE conversation_id = $1 AND sent_at IS NOT NULL`, conv) == 1
}

func (f *fixture) skipReason(conv pgtype.UUID) string {
	f.t.Helper()
	var reason string
	if err := f.pool.QueryRow(context.Background(), `SELECT skipped_reason FROM csat_requests WHERE conversation_id = $1`, conv).Scan(&reason); err != nil {
		f.t.Fatalf("no request row: %v", err)
	}
	return reason
}

func TestSurveyIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	c := f.conversation(resolved{ago: time.Hour})
	if f.sweep() != 0 || f.count(`SELECT count(*) FROM csat_requests`) != 0 || f.surveyed(c) {
		t.Error("a survey was queued for a mailbox that never enabled CSAT")
	}
	f.exec(`INSERT INTO csat_settings (mailbox_id, enabled, delay_hours, enabled_at) VALUES ($1, false, 0, NULL)`, f.mailbox)
	if f.sweep() != 0 {
		t.Error("a survey was queued for a mailbox with CSAT switched off")
	}
}

func TestSurveyMailIsQueuedOnceAsAnAutomatedMessage(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	c := f.conversation(resolved{ago: time.Hour, fromName: "Jane"})

	if f.sweep() != 1 || !f.surveyed(c) {
		t.Fatal("no survey queued")
	}
	var subject, text, html, to string
	var auto bool
	err := f.pool.QueryRow(context.Background(), `SELECT subject, body_text, body_html, auto_submitted, to_addrs -> 0 ->> 'address' FROM messages
		WHERE conversation_id = $1 AND direction = 'out'`, c).Scan(&subject, &text, &html, &auto, &to)
	if err != nil {
		t.Fatal(err)
	}
	if !auto || to != "jane@acme.example" || subject != "Hoe was uw contact met Shop Support?" {
		t.Errorf("auto %v, to %q, subject %q", auto, to, subject)
	}
	for i := 1; i <= 5; i++ {
		want := "https://echoo.test/tevredenheid/"
		if strings.Count(text, want) != 5 || !strings.Contains(text, "?r="+string(rune('0'+i))) || !strings.Contains(html, "?r="+string(rune('0'+i))) {
			t.Fatalf("rating link %d missing:\n%s", i, text)
		}
	}
	if strings.Contains(html, "<img") {
		t.Error("the survey mail must not carry a tracking pixel")
	}
	if f.count(`SELECT count(*) FROM outbound o JOIN messages m ON m.id = o.message_id WHERE m.conversation_id = $1`, c) != 1 {
		t.Error("the mail was not queued for sending")
	}
	var messageID pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT message_id FROM csat_requests WHERE conversation_id = $1`, c).Scan(&messageID); err != nil || !messageID.Valid {
		t.Errorf("request does not point at the mail: %v", err)
	}

	// Once per conversation, also after it is reopened and resolved again.
	if f.sweep() != 0 {
		t.Error("second sweep sent again")
	}
	f.exec(`UPDATE conversations SET status = 'open', resolved_at = NULL WHERE id = $1`, c)
	f.exec(`UPDATE conversations SET status = 'closed', resolved_at = now() - interval '1 minute' WHERE id = $1`, c)
	if f.sweep() != 0 || f.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'out'`, c) != 1 {
		t.Error("a resolved-again conversation was surveyed twice")
	}
}

func TestSurveyWaitsForTheDelay(t *testing.T) {
	f := newFixture(t)
	f.enable(2)
	recent := f.conversation(resolved{ago: time.Hour})
	due := f.conversation(resolved{ago: 3 * time.Hour})
	if f.sweep() != 1 || f.surveyed(recent) || !f.surveyed(due) {
		t.Error("only the conversation resolved longer ago than the delay is due")
	}
}

func TestSurveyNeverGoesToAStrangerWhoWroteLast(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	c := f.conversation(resolved{ago: time.Hour})
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, received_at)
		VALUES ($1, $2, 'email', 'in', 'forged-' || uuidv7()::text || '@evil.example', 'attacker@evil.example', 'Attacker', now() - interval '30 minutes')`, c, f.mailbox)

	if f.sweep() != 1 {
		t.Fatal("no survey queued")
	}
	var to string
	if err := f.pool.QueryRow(context.Background(), `SELECT to_addrs -> 0 ->> 'address' FROM messages WHERE conversation_id = $1 AND direction = 'out'`, c).Scan(&to); err != nil {
		t.Fatal(err)
	}
	if to != "jane@acme.example" {
		t.Errorf("survey sent to %q, want the original customer", to)
	}
}

func TestSurveySkipsWhatMustNotBeSurveyed(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	cases := map[string]struct {
		r      resolved
		reason string
	}{
		"auto reply":          {resolved{ago: time.Hour, auto: true}, "auto_submitted"},
		"bounce":              {resolved{ago: time.Hour, bounce: true}, "bounce"},
		"bulk":                {resolved{ago: time.Hour, bulk: true}, "bulk"},
		"no-reply address":    {resolved{ago: time.Hour, from: "noreply@acme.example"}, "no_reply_address"},
		"do-not-reply":        {resolved{ago: time.Hour, from: "Do-Not-Reply@acme.example"}, "no_reply_address"},
		"mailer daemon":       {resolved{ago: time.Hour, from: "MAILER-DAEMON@acme.example"}, "no_reply_address"},
		"our own mailbox":     {resolved{ago: time.Hour, from: "support@shop.example"}, "own_mailbox"},
		"never heard of them": {resolved{ago: time.Hour, noInbox: true}, "no_customer_message"},
	}
	ids := map[string]pgtype.UUID{}
	for name, c := range cases {
		ids[name] = f.conversation(c.r)
	}
	spam := f.conversation(resolved{ago: time.Hour, status: "spam"})
	open := f.conversation(resolved{ago: time.Hour, status: "open"})
	old := f.conversation(resolved{ago: 5 * 24 * time.Hour})
	ok := f.conversation(resolved{ago: time.Hour})

	if got := f.sweep(); got != 1 {
		t.Errorf("queued %d surveys, want 1", got)
	}
	for name, c := range cases {
		if f.surveyed(ids[name]) || f.skipReason(ids[name]) != c.reason {
			t.Errorf("%s: surveyed %v, reason %q, want %q", name, f.surveyed(ids[name]), f.skipReason(ids[name]), c.reason)
		}
	}
	for name, id := range map[string]pgtype.UUID{"spam": spam, "open": open, "too old": old} {
		if f.count(`SELECT count(*) FROM csat_requests WHERE conversation_id = $1`, id) != 0 {
			t.Errorf("%s conversation was picked up", name)
		}
	}
	if !f.surveyed(ok) {
		t.Error("the ordinary conversation was not surveyed")
	}
}

func TestConversationsResolvedBeforeEnablingAreNotSurveyed(t *testing.T) {
	f := newFixture(t)
	c := f.conversation(resolved{ago: 2 * time.Hour})
	f.exec(`INSERT INTO csat_settings (mailbox_id, enabled, delay_hours, enabled_at) VALUES ($1, true, 0, now() - interval '1 hour')`, f.mailbox)
	if f.sweep() != 0 || f.surveyed(c) {
		t.Error("a conversation resolved before CSAT was switched on was surveyed")
	}
}

// survey returns the token of a surveyed conversation by re-issuing it: tokens are deterministic
// for a conversation and expiry, and the stored hash proves it is the emailed one.
func (f *fixture) token(conv pgtype.UUID) string {
	f.t.Helper()
	var expires time.Time
	var hash []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT expires_at, token_hash FROM csat_requests WHERE conversation_id = $1`, conv).Scan(&expires, &hash); err != nil {
		f.t.Fatal(err)
	}
	token := f.signer.Issue(conv, expires)
	if string(Hash(token)) != string(hash) {
		f.t.Fatal("stored hash does not match the token")
	}
	return token
}

func TestAnswerIsStoredOnceAndCanBeChanged(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	c := f.conversation(resolved{ago: time.Hour, assignee: f.agent})
	f.sweep()
	token := f.token(c)
	ctx := context.Background()

	sv, err := f.svc.Lookup(ctx, token)
	if err != nil || sv.Rating != 0 || sv.Brand != "Shop Support" {
		t.Fatalf("%+v %v", sv, err)
	}
	if sv, err = f.svc.Submit(ctx, token, 4, " Snel geholpen "); err != nil || sv.Rating != 4 || sv.Comment != "Snel geholpen" {
		t.Fatalf("%+v %v", sv, err)
	}
	if sv, err = f.svc.Submit(ctx, token, 5, ""); err != nil || sv.Rating != 5 || sv.Comment != "" {
		t.Fatalf("changing the answer: %+v %v", sv, err)
	}
	if n := f.count(`SELECT count(*) FROM csat_responses WHERE conversation_id = $1`, c); n != 1 {
		t.Errorf("%d answers stored, want 1", n)
	}
	var hash []byte
	if err := f.pool.QueryRow(ctx, `SELECT token_hash FROM csat_responses WHERE conversation_id = $1`, c).Scan(&hash); err != nil || string(hash) != string(Hash(token)) {
		t.Errorf("the answer does not record the token hash: %v", err)
	}

	f.exec(`UPDATE csat_responses SET created_at = now() - interval '8 days' WHERE conversation_id = $1`, c)
	if _, err := f.svc.Submit(ctx, token, 1, "te laat"); !errors.Is(err, ErrLocked) {
		t.Errorf("after the change window: %v, want ErrLocked", err)
	}
	if sv, _ := f.svc.Lookup(ctx, token); sv.Rating != 5 || !sv.Locked {
		t.Errorf("locked answer changed: %+v", sv)
	}
}

func TestInvalidAnswersAreRejected(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	c := f.conversation(resolved{ago: time.Hour})
	f.sweep()
	token := f.token(c)
	for _, rating := range []int{0, 6, -1} {
		if _, err := f.svc.Submit(context.Background(), token, rating, ""); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("rating %d: %v", rating, err)
		}
	}
	if _, err := f.svc.Submit(context.Background(), token, 3, strings.Repeat("x", MaxComment+1)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("long comment: %v", err)
	}
	if f.count(`SELECT count(*) FROM csat_responses`) != 0 {
		t.Error("an invalid answer was stored")
	}
}

func TestTokensOfOtherSurveysAndExpiredTokensAreRefused(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	a := f.conversation(resolved{ago: time.Hour})
	b := f.conversation(resolved{ago: time.Hour})
	f.sweep()
	ctx := context.Background()

	// A correctly signed token that was never emailed: the stored hash does not match.
	if _, err := f.svc.Lookup(ctx, f.signer.Issue(a, time.Now().Add(time.Hour))); !errors.Is(err, ErrInvalid) {
		t.Errorf("unissued token: %v", err)
	}
	// A signed token for a conversation without a survey.
	unsurveyed := f.conversation(resolved{ago: time.Hour, status: "open"})
	if _, err := f.svc.Lookup(ctx, f.signer.Issue(unsurveyed, time.Now().Add(time.Hour))); !errors.Is(err, ErrInvalid) {
		t.Errorf("token without a survey: %v", err)
	}
	if _, err := f.svc.Lookup(ctx, f.token(b)); err != nil {
		t.Errorf("real token: %v", err)
	}
	if _, err := f.svc.Lookup(ctx, f.signer.Issue(b, time.Now().Add(-time.Second))); !errors.Is(err, ErrExpired) {
		t.Errorf("expired token: %v", err)
	}
}

func TestLowRatingAlertsTheAssigneeOnce(t *testing.T) {
	f := newFixture(t)
	f.enable(0)
	c := f.conversation(resolved{ago: time.Hour, assignee: f.agent})
	unassigned := f.conversation(resolved{ago: time.Hour})
	f.sweep()
	ctx := context.Background()
	alerts := func() int {
		return f.count(`SELECT count(*) FROM notifications WHERE kind = 'csat' AND user_id = $1 AND conversation_id = $2`, f.agent, c)
	}

	if _, err := f.svc.Submit(ctx, f.token(c), 3, ""); err != nil || alerts() != 0 {
		t.Fatalf("a neutral rating alerted: %d %v", alerts(), err)
	}
	if _, err := f.svc.Submit(ctx, f.token(c), 2, "hm"); err != nil || alerts() != 1 {
		t.Fatalf("a low rating did not alert: %d %v", alerts(), err)
	}
	if _, err := f.svc.Submit(ctx, f.token(c), 1, "slecht"); err != nil || alerts() != 1 {
		t.Fatalf("a low rating replaced by another low one alerted again: %d %v", alerts(), err)
	}
	if _, err := f.svc.Submit(ctx, f.token(c), 5, ""); err != nil || alerts() != 1 {
		t.Fatalf("recovering alerted: %d %v", alerts(), err)
	}
	if _, err := f.svc.Submit(ctx, f.token(c), 1, ""); err != nil || alerts() != 2 {
		t.Fatalf("dropping to low again should alert: %d %v", alerts(), err)
	}
	if _, err := f.svc.Submit(ctx, f.token(unassigned), 1, ""); err != nil {
		t.Errorf("unassigned conversation: %v", err)
	}
}
