package campaigns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/contacts"
	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	q       *dbq.Queries
	svc     *Service
	clock   *clock
	mailbox pgtype.UUID
	admin   dbq.User
	segment pgtype.UUID
}

const allContacts = `{"match":"all","conditions":[]}`

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, pool: pool, q: dbq.New(pool), clock: &clock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}}
	f.svc = NewService(Deps{Pool: pool, BaseURL: "https://echoo.test", Jobs: client, Now: f.clock.Now})
	f.mailbox = f.id(`INSERT INTO mailboxes (name, email_address, display_name, smtp_host) VALUES ('Support', 'support@shop.example', 'Shop Support', 'smtp.shop.example') RETURNING id`)
	f.admin = f.user("admin")
	f.segment = f.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('Everyone', $1, true, $2) RETURNING id`, f.admin.ID, allContacts)
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

func (f *fixture) user(role string) dbq.User {
	f.t.Helper()
	u, err := f.q.CreateUser(context.Background(), dbq.CreateUserParams{
		Email: fmt.Sprintf("%s-%d@example.com", role, time.Now().UnixNano()), Name: role, Role: role, PasswordHash: "x",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) contact(name string, emails ...string) pgtype.UUID {
	f.t.Helper()
	id := f.id(`INSERT INTO contacts (name, created_by) VALUES ($1, $2) RETURNING id`, name, f.admin.ID)
	for i, email := range emails {
		f.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, $2, $3)`, id, email, i == 0)
	}
	return id
}

func (f *fixture) contacts(n int) {
	f.t.Helper()
	f.exec(`WITH c AS (INSERT INTO contacts (name, created_by) SELECT 'Contact ' || g, $1 FROM generate_series(1, $2::int) g RETURNING id, name)
		INSERT INTO contact_addresses (contact_id, email, is_primary)
		SELECT id, lower(replace(name, ' ', '.')) || '@acme.example', true FROM c`, f.admin.ID, n)
}

type campaignOpt struct {
	creator pgtype.UUID
	segment pgtype.UUID
	rate    int
	subject string
	body    string
}

func (f *fixture) campaign(o campaignOpt) pgtype.UUID {
	f.t.Helper()
	if !o.creator.Valid {
		o.creator = f.admin.ID
	}
	if !o.segment.Valid {
		o.segment = f.segment
	}
	if o.rate == 0 {
		o.rate = 60
	}
	if o.subject == "" {
		o.subject = "Herfstactie voor {{contact.first_name}}"
	}
	if o.body == "" {
		o.body = "<p>Hallo {{contact.first_name}}, bekijk onze aanbieding.</p>"
	}
	return f.id(`INSERT INTO campaigns (name, mailbox_id, segment_id, segment_name, subject, body_html, rate_per_minute, created_by)
		VALUES ('Herfst', $1, $2, 'Everyone', $3, $4, $5, $6) RETURNING id`, f.mailbox, o.segment, o.subject, o.body, o.rate, o.creator)
}

// start starts the campaign as the user who created it.
func (f *fixture) start(id pgtype.UUID) {
	f.t.Helper()
	var creator pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT created_by FROM campaigns WHERE id = $1`, id).Scan(&creator); err != nil {
		f.t.Fatal(err)
	}
	starter, err := f.q.GetUser(context.Background(), creator)
	if err != nil {
		f.t.Fatal(err)
	}
	f.startAs(id, starter)
}

func (f *fixture) startAs(id pgtype.UUID, starter dbq.User) {
	f.t.Helper()
	err := pgx.BeginFunc(context.Background(), f.pool, func(tx pgx.Tx) error {
		_, err := f.svc.Start(context.Background(), tx, id, starter, nil)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) tick(times int) {
	f.t.Helper()
	for range times {
		if err := f.svc.Tick(context.Background()); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) inTx(fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(context.Background(), f.pool, fn)
}

func (f *fixture) status(id pgtype.UUID) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM campaigns WHERE id = $1`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) states(id pgtype.UUID) map[string]int {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT state || CASE WHEN skip_reason <> '' THEN ':' || skip_reason ELSE '' END, count(*) FROM campaign_recipients WHERE campaign_id = $1 GROUP BY 1`, id)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			f.t.Fatal(err)
		}
		out[k] = n
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// mailed lists the recipients that are not skipped, whether or not the dispatcher has queued
// their message yet.
func (f *fixture) mailed(id pgtype.UUID) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT email FROM campaign_recipients WHERE campaign_id = $1 AND state <> 'skipped' ORDER BY email`, id)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// visibilityFixture has an agent who reads one mailbox with two contacts in it (one seen, one
// created by the agent) and two contacts the agent cannot see.
func visibilityFixture(t *testing.T) (*fixture, dbq.User) {
	f := newFixture(t)
	mailboxB := f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Sales', 'sales@shop.example') RETURNING id`)
	agent := f.user("agent")
	team := f.id(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team, agent.ID)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'read')`, f.mailbox, team)

	inA := f.contact("In A", "in.a@acme.example")
	inB := f.contact("In B", "in.b@acme.example")
	own := f.contact("Own", "own@acme.example")
	f.exec(`UPDATE contacts SET created_by = $2 WHERE id = $1`, own, agent.ID)
	f.contact("Nobody", "nobody@acme.example")
	f.exec(`INSERT INTO conversations (mailbox_id, contact_id, subject) VALUES ($1, $2, 's'), ($3, $4, 's')`, f.mailbox, inA, mailboxB, inB)
	return f, agent
}

func TestMaterializeUsesTheVisibilityOfWhoStarts(t *testing.T) {
	f, agent := visibilityFixture(t)
	byAgent := f.campaign(campaignOpt{creator: agent.ID})
	byAdmin := f.campaign(campaignOpt{})
	f.start(byAgent)
	f.start(byAdmin)
	f.tick(1)

	if got, want := f.mailed(byAgent), []string{"in.a@acme.example", "own@acme.example"}; !slices.Equal(got, want) {
		t.Errorf("agent's campaign recipients = %v, want %v (only contacts the agent may see)", got, want)
	}
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1`, byAdmin); got != 4 {
		t.Errorf("admin's campaign recipients = %d, want all 4 contacts", got)
	}
}

// The draft's author does not decide: a draft the agent wrote and an admin starts reaches
// everyone, and one an admin wrote and the agent starts stays inside the agent's view.
func TestMaterializeIgnoresWhoWroteTheDraft(t *testing.T) {
	f, agent := visibilityFixture(t)
	draftByAgent := f.campaign(campaignOpt{creator: agent.ID})
	draftByAdmin := f.campaign(campaignOpt{})
	// Sent mail opens a conversation per recipient, which widens what the agent sees, so the
	// agent's campaign is resolved first.
	f.startAs(draftByAdmin, agent)
	f.tick(1)
	f.startAs(draftByAgent, f.admin)
	f.tick(1)
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1`, draftByAgent); got != 4 {
		t.Errorf("recipients of a draft the agent wrote and the admin started = %d, want 4", got)
	}
	if got, want := f.mailed(draftByAdmin), []string{"in.a@acme.example", "own@acme.example"}; !slices.Equal(got, want) {
		t.Errorf("recipients of a draft the admin wrote and the agent started = %v, want %v", got, want)
	}
}

func TestStartRefusesASegmentTheStarterMayNotUse(t *testing.T) {
	f := newFixture(t)
	agent := f.user("agent")
	personal := f.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('Mine', $1, false, $2) RETURNING id`, f.admin.ID, allContacts)
	c := f.campaign(campaignOpt{creator: f.admin.ID, segment: personal})
	var notReady *NotReadyError
	err := f.inTx(func(tx pgx.Tx) error {
		_, err := f.svc.Start(context.Background(), tx, c, agent, nil)
		return err
	})
	if !errors.As(err, &notReady) || notReady.Fields["segment_id"] != "unknown" {
		t.Fatalf("err = %v, want a NotReadyError about segment_id", err)
	}
	if f.status(c) != "draft" {
		t.Errorf("status = %s, want draft", f.status(c))
	}
	f.startAs(c, f.admin)
}

func TestReportOnlyListsRecipientsTheReaderMaySee(t *testing.T) {
	f, agent := visibilityFixture(t)
	c := f.campaign(campaignOpt{})
	f.exec(`INSERT INTO campaign_recipients (campaign_id, contact_id, email, name, state, skip_reason)
		SELECT $1, contact_id, email, '', 'pending', '' FROM contact_addresses`, c)
	f.exec(`INSERT INTO campaign_recipients (campaign_id, email, name, state, skip_reason) VALUES ($1, 'erased@acme.example', '', 'pending', '')`, c)

	report := func(v contacts.Viewer) string {
		var out bytes.Buffer
		if err := f.svc.WriteReport(context.Background(), c, v, &out, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	viewer, err := contacts.ViewerFor(context.Background(), f.q, agent)
	if err != nil {
		t.Fatal(err)
	}
	got := report(viewer)
	if !strings.Contains(got, "in.a@acme.example") || !strings.Contains(got, "own@acme.example") ||
		strings.Contains(got, "in.b@acme.example") || strings.Contains(got, "nobody@acme.example") || strings.Contains(got, "erased@acme.example") {
		t.Errorf("agent's report = %q, want only contacts the agent may see", got)
	}
	all := report(contacts.Viewer{Admin: true, UserID: f.admin.ID})
	for _, email := range []string{"in.a@acme.example", "in.b@acme.example", "nobody@acme.example", "erased@acme.example"} {
		if !strings.Contains(all, email) {
			t.Errorf("admin's report misses %s", email)
		}
	}
}

func TestSkippingRules(t *testing.T) {
	f := newFixture(t)
	f.contact("Ok", "ok@acme.example")
	unsub := f.contact("Unsubscribed", "unsub@acme.example")
	f.exec(`UPDATE contacts SET unsubscribed_at = now() WHERE id = $1`, unsub)
	both := f.contact("Unsubscribed and bounced", "both@acme.example")
	f.exec(`UPDATE contacts SET unsubscribed_at = now() WHERE id = $1`, both)
	f.exec(`UPDATE contact_addresses SET bounced_at = now() WHERE email = 'both@acme.example'`)
	f.contact("No address")
	f.contact("Bounced", "bounced@acme.example")
	f.exec(`UPDATE contact_addresses SET bounced_at = now() WHERE email = 'bounced@acme.example'`)
	f.contact("Second address", "first@acme.example", "second@acme.example")
	f.exec(`UPDATE contact_addresses SET bounced_at = now() WHERE email = 'first@acme.example'`)
	erased := f.contact("Erased", "erased@acme.example")
	f.exec(`UPDATE contacts SET erased_at = now() WHERE id = $1`, erased)

	want := map[string]int{"skipped:unsubscribed": 2, "skipped:no_address": 2, "skipped:bounced": 1}
	preview, err := f.svc.Preview(context.Background(), f.admin, f.segment)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != 7 || preview.Sendable != 2 || preview.Unsubscribed != 2 || preview.NoAddress != 2 || preview.Bounced != 1 {
		t.Errorf("preview = %+v", preview)
	}

	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	got := f.states(c)
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("states = %v, want %v", got, want)
			break
		}
	}
	if got["pending"]+got["queued"] != 2 {
		t.Errorf("mailed = %d, want 2", got["pending"]+got["queued"])
	}
	if emails := f.mailed(c); !slices.Contains(emails, "second@acme.example") || slices.Contains(emails, "first@acme.example") {
		t.Errorf("recipients = %v: a bounced primary address falls back to the next one", emails)
	}
}

func TestClassifyDuplicateAddress(t *testing.T) {
	seen := map[string]bool{}
	a := classify(dbq.CampaignCandidatesRow{Email: "x@acme.example", HasAddress: true}, seen)
	b := classify(dbq.CampaignCandidatesRow{Email: "x@acme.example", HasAddress: true}, seen)
	if a.skip != "" || b.skip != SkipDuplicate {
		t.Errorf("skips = %q and %q, want none and %q", a.skip, b.skip, SkipDuplicate)
	}
}

func TestUnsubscribeAfterStartIsHonouredBeforeSending(t *testing.T) {
	f := newFixture(t)
	f.contacts(20)
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	late := f.id(`SELECT contact_id FROM campaign_recipients WHERE campaign_id = $1 AND state = 'pending' ORDER BY id LIMIT 1`, c)
	f.exec(`UPDATE contacts SET unsubscribed_at = now() WHERE id = $1`, late)

	f.clock.advance(2 * time.Minute)
	f.tick(10)
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE contact_id = $1 AND state = 'skipped' AND skip_reason = 'unsubscribed'`, late); got != 1 {
		t.Errorf("late unsubscribe: skipped rows = %d, want 1", got)
	}
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE contact_id = $1 AND message_id IS NOT NULL`, late); got != 0 {
		t.Error("a contact who unsubscribed got a message")
	}
}

func TestPacingFollowsTheRateOverAFakeClock(t *testing.T) {
	f := newFixture(t)
	f.contacts(200)
	c := f.campaign(campaignOpt{rate: 60})
	f.start(c)

	f.tick(1)
	if got := f.states(c)["queued"]; got != 5 {
		t.Fatalf("after one tick: queued = %d, want 5 (one tick's share of 60 per minute)", got)
	}
	f.tick(40)
	if got := f.states(c)["queued"]; got != 60 {
		t.Fatalf("after many ticks in one minute: queued = %d, want exactly the rate, 60", got)
	}
	f.clock.advance(30 * time.Second)
	f.tick(10)
	if got := f.states(c)["queued"]; got != 60 {
		t.Fatalf("half a minute later: queued = %d, want still 60", got)
	}
	f.clock.advance(31 * time.Second)
	f.tick(40)
	if got := f.states(c)["queued"]; got != 120 {
		t.Fatalf("a minute after the first batch: queued = %d, want 120", got)
	}
}

func TestPacingIsSharedByCampaignsOnOneMailbox(t *testing.T) {
	f := newFixture(t)
	f.contacts(100)
	a, b := f.campaign(campaignOpt{rate: 60}), f.campaign(campaignOpt{rate: 60})
	f.start(a)
	f.start(b)
	f.tick(40)
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE state = 'queued'`); got != 60 {
		t.Errorf("two campaigns on one mailbox queued %d in a minute, want the mailbox's 60", got)
	}
}

func TestRateIsCappedByTheConfiguredMaximum(t *testing.T) {
	f := newFixture(t)
	f.contacts(100)
	c := f.campaign(campaignOpt{rate: 100})
	f.start(c)
	f.svc.d.MaxRate = 30 // lowered by the operator after the campaign started
	f.tick(40)
	if got := f.states(c)["queued"]; got != 30 {
		t.Errorf("queued = %d, want the configured maximum of 30", got)
	}
}

func TestPauseResumeAndCancel(t *testing.T) {
	f := newFixture(t)
	f.contacts(20)
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	if got := f.states(c)["queued"]; got != 5 {
		t.Fatalf("queued = %d, want 5", got)
	}

	if err := f.inTx(func(tx pgx.Tx) error { return f.svc.Pause(context.Background(), tx, c) }); err != nil {
		t.Fatal(err)
	}
	f.tick(10)
	if got := f.states(c)["queued"]; got != 5 || f.status(c) != "paused" {
		t.Fatalf("paused: queued = %d status = %s, want 5 and paused", got, f.status(c))
	}
	if err := f.inTx(func(tx pgx.Tx) error { return f.svc.Pause(context.Background(), tx, c) }); !errors.Is(err, ErrConflict) {
		t.Errorf("pausing a paused campaign: %v, want ErrConflict", err)
	}

	if err := f.inTx(func(tx pgx.Tx) error { return f.svc.Resume(context.Background(), tx, c) }); err != nil {
		t.Fatal(err)
	}
	f.tick(1)
	if got := f.states(c)["queued"]; got != 10 {
		t.Fatalf("resumed: queued = %d, want 10", got)
	}

	if err := f.inTx(func(tx pgx.Tx) error { return f.svc.Cancel(context.Background(), tx, c) }); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(time.Hour)
	f.tick(10)
	states := f.states(c)
	if f.status(c) != "cancelled" || states["skipped:cancelled"] != 20 || states["queued"] != 0 || states["pending"] != 0 {
		t.Errorf("cancelled: status = %s, states = %v, want every recipient skipped as cancelled", f.status(c), states)
	}
	if got := f.count(`SELECT count(*) FROM outbound WHERE status = 'cancelled'`); got != 10 {
		t.Errorf("cancelled outbound rows = %d, want the 10 messages still waiting", got)
	}
	if got := f.count(`SELECT count(*) FROM outbound WHERE status IN ('queued', 'retry')`); got != 0 {
		t.Errorf("%d messages of a cancelled campaign can still go out", got)
	}
	if err := f.inTx(func(tx pgx.Tx) error { return f.svc.Resume(context.Background(), tx, c) }); !errors.Is(err, ErrConflict) {
		t.Errorf("resuming a cancelled campaign: %v, want ErrConflict", err)
	}
}

func TestMessageBeingSentAtCancelIsNotRetriedAfterwards(t *testing.T) {
	f := newFixture(t)
	f.contacts(2)
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	messages := f.recipientMessages(c)
	f.exec(`UPDATE outbound SET status = 'sending' WHERE message_id = $1`, messages[0])

	if err := f.inTx(func(tx pgx.Tx) error { return f.svc.Cancel(context.Background(), tx, c) }); err != nil {
		t.Fatal(err)
	}
	if got := f.states(c)["queued"]; got != 1 {
		t.Fatalf("queued = %d, want the one message that is being delivered", got)
	}

	// The delivery attempt failed and the send worker scheduled a retry.
	f.exec(`UPDATE outbound SET status = 'retry', next_attempt_at = now() + interval '30 seconds' WHERE message_id = $1`, messages[0])
	f.tick(1)
	if got := f.count(`SELECT count(*) FROM outbound WHERE status = 'cancelled'`); got != 2 {
		t.Errorf("cancelled outbound rows = %d, want both messages", got)
	}
	if got := f.states(c)["skipped:cancelled"]; got != 2 {
		t.Errorf("states = %v, want both recipients skipped as cancelled", f.states(c))
	}
}

func TestRetryAfterACrashNeverQueuesARecipientTwice(t *testing.T) {
	f := newFixture(t)
	f.contacts(3)
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)

	// The dispatcher committed the message of one recipient but the recipient row still says
	// pending, as after a crash between the two writes of an older implementation.
	victim := f.id(`SELECT id FROM campaign_recipients WHERE campaign_id = $1 ORDER BY id LIMIT 1`, c)
	var messageID, conversationID pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT message_id, conversation_id FROM campaign_recipients WHERE id = $1`, victim).Scan(&messageID, &conversationID); err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE campaign_recipients SET state = 'pending', message_id = NULL, conversation_id = NULL, queued_at = NULL WHERE id = $1`, victim)

	f.clock.advance(time.Minute)
	f.tick(5)

	var linked pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT message_id FROM campaign_recipients WHERE id = $1`, victim).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != messageID {
		t.Errorf("recipient was linked to %s, want the message queued before the crash (%s)", linked, messageID)
	}
	if got := f.count(`SELECT count(*) FROM outbound`); got != 3 {
		t.Errorf("outbound rows = %d, want one per recipient", got)
	}
	if got := f.count(`SELECT count(*) FROM conversations`); got != 3 {
		t.Errorf("conversations = %d, want one per recipient", got)
	}
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE conversation_id = $1`, conversationID); got != 1 {
		t.Errorf("the earlier conversation is used by %d recipients, want 1", got)
	}
}

func TestIdempotencyKeyIsStablePerRecipient(t *testing.T) {
	var c, r1, r2 pgtype.UUID
	c.Bytes[0], r1.Bytes[0], r2.Bytes[0] = 1, 2, 3
	first, again := idempotencyKey(c, r1), idempotencyKey(c, r1)
	if first != again {
		t.Error("key changes between calls")
	}
	if idempotencyKey(c, r1) == idempotencyKey(c, r2) {
		t.Error("two recipients share a key")
	}
	if got := idempotencyKey(c, r1).Bytes[6] >> 4; got != 5 {
		t.Errorf("uuid version = %d, want 5", got)
	}
}

func TestQueuedMessageCarriesHeadersConversationAndEvent(t *testing.T) {
	f := newFixture(t)
	f.contact("Jan de Vries", "jan@acme.example")
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)

	var status, subject string
	var headers []byte
	err := f.pool.QueryRow(context.Background(), `
		SELECT cv.status, cv.subject, o.extra_headers
		FROM campaign_recipients r
		JOIN conversations cv ON cv.id = r.conversation_id
		JOIN outbound o ON o.message_id = r.message_id
		WHERE r.campaign_id = $1`, c).Scan(&status, &subject, &headers)
	if err != nil {
		t.Fatal(err)
	}
	if status != "closed" || subject != "Herfstactie voor Jan" {
		t.Errorf("conversation status = %q subject = %q", status, subject)
	}
	for _, want := range []string{"List-Unsubscribe", "List-Unsubscribe-Post", "One-Click", "Precedence", "bulk", "/afmelden/"} {
		if !bytes.Contains(headers, []byte(want)) {
			t.Errorf("extra headers lack %q: %s", want, headers)
		}
	}
	if got := f.count(`SELECT count(*) FROM conversation_events e JOIN campaign_recipients r ON r.conversation_id = e.conversation_id
		WHERE r.campaign_id = $1 AND e.type = 'campaign' AND e.data ->> 'name' = 'Herfst'`, c); got != 1 {
		t.Errorf("campaign events = %d, want 1", got)
	}
	var auto bool
	if err := f.pool.QueryRow(context.Background(), `SELECT m.auto_submitted FROM messages m JOIN campaign_recipients r ON r.message_id = m.id WHERE r.campaign_id = $1`, c).Scan(&auto); err != nil {
		t.Fatal(err)
	}
	if auto {
		t.Error("campaign mail is marked auto-submitted")
	}
}

func TestSyncCopiesFinalOutcomesAndFinishesTheCampaign(t *testing.T) {
	f := newFixture(t)
	f.contacts(3)
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	if f.status(c) != "sending" {
		t.Fatalf("status = %s while deliveries are open", f.status(c))
	}
	ids := f.recipientMessages(c)
	f.exec(`UPDATE outbound SET status = 'sent', sent_at = now() WHERE message_id = $1`, ids[0])
	f.exec(`UPDATE outbound SET status = 'failed', error = 'mailbox full' WHERE message_id = $1`, ids[1])
	f.exec(`UPDATE outbound SET status = 'uncertain', error = 'connection lost' WHERE message_id = $1`, ids[2])
	f.tick(1)

	states := f.states(c)
	if states["sent"] != 1 || states["failed"] != 2 || f.status(c) != "done" {
		t.Errorf("states = %v status = %s, want 1 sent, 2 failed and done", states, f.status(c))
	}
	if got := f.count(`SELECT count(*) FROM campaign_recipients WHERE error LIKE 'uncertain: connection lost'`); got != 1 {
		t.Error("an uncertain message must be reported as failed with its reason, never resent")
	}
}

func (f *fixture) recipientMessages(c pgtype.UUID) []pgtype.UUID {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT message_id FROM campaign_recipients WHERE campaign_id = $1 AND message_id IS NOT NULL ORDER BY id`, c)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestScheduledCampaignWaitsForItsTime(t *testing.T) {
	f := newFixture(t)
	f.contacts(2)
	c := f.campaign(campaignOpt{})
	at := f.clock.Now().Add(time.Hour)
	var scheduled bool
	err := f.inTx(func(tx pgx.Tx) error {
		var err error
		scheduled, err = f.svc.Start(context.Background(), tx, c, f.admin, &at)
		return err
	})
	if err != nil || !scheduled {
		t.Fatalf("scheduled = %v err = %v", scheduled, err)
	}
	f.tick(3)
	if f.status(c) != "scheduled" || f.count(`SELECT count(*) FROM campaign_recipients`) != 0 {
		t.Fatal("a scheduled campaign started early")
	}
	f.clock.advance(time.Hour + time.Second)
	f.tick(1)
	if f.status(c) != "sending" || f.states(c)["queued"] != 2 {
		t.Errorf("status = %s states = %v, want sending with 2 queued", f.status(c), f.states(c))
	}
}

func TestCampaignStopsWhenItsSegmentOrCreatorIsGone(t *testing.T) {
	f := newFixture(t)
	f.contacts(2)
	gone := f.campaign(campaignOpt{})
	f.start(gone)
	f.exec(`DELETE FROM contact_segments WHERE id = $1`, f.segment)
	f.tick(1)
	var reason string
	if err := f.pool.QueryRow(context.Background(), `SELECT error FROM campaigns WHERE id = $1`, gone).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if f.status(gone) != "cancelled" || reason != "segment_missing" || f.count(`SELECT count(*) FROM campaign_recipients`) != 0 {
		t.Errorf("status = %s error = %q, want cancelled by segment_missing with no recipients", f.status(gone), reason)
	}

	segment := f.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('All 2', $1, true, $2) RETURNING id`, f.admin.ID, allContacts)
	leaver := f.user("agent")
	orphaned := f.campaign(campaignOpt{creator: leaver.ID, segment: segment})
	f.start(orphaned)
	f.exec(`UPDATE users SET deactivated_at = now() WHERE id = $1`, leaver.ID)
	f.tick(1)
	if f.status(orphaned) != "cancelled" {
		t.Errorf("campaign of a deactivated creator is %s, want cancelled", f.status(orphaned))
	}
}

func TestDisabledMailboxPausesTheCampaign(t *testing.T) {
	f := newFixture(t)
	f.contacts(2)
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.exec(`UPDATE mailboxes SET disabled_at = now() WHERE id = $1`, f.mailbox)
	f.tick(1)
	if f.status(c) != "paused" {
		t.Errorf("status = %s, want paused", f.status(c))
	}
}

func TestStartRefusesAnIncompleteOrStartedCampaign(t *testing.T) {
	f := newFixture(t)
	incomplete := f.campaign(campaignOpt{subject: "x", body: "<p></p>"})
	var notReady *NotReadyError
	err := f.inTx(func(tx pgx.Tx) error {
		_, err := f.svc.Start(context.Background(), tx, incomplete, f.admin, nil)
		return err
	})
	if !errors.As(err, &notReady) || notReady.Fields["body_html"] != "required" {
		t.Errorf("err = %v, want a NotReadyError about body_html", err)
	}
	ok := f.campaign(campaignOpt{})
	f.start(ok)
	err = f.inTx(func(tx pgx.Tx) error {
		_, err := f.svc.Start(context.Background(), tx, ok, f.admin, nil)
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("starting twice: %v, want ErrConflict", err)
	}
}

func TestDraftValidation(t *testing.T) {
	mailbox := pgtype.UUID{Valid: true}
	segment := pgtype.UUID{Valid: true}
	valid := Draft{Name: "Herfst", MailboxID: mailbox, SegmentID: segment, Subject: "Hallo {{contact.name}}", BodyHTML: "<p>Hoi</p>", Rate: 60}
	for name, c := range map[string]struct {
		mutate   func(*Draft)
		complete bool
		field    string
	}{
		"missing name":         {func(d *Draft) { d.Name = " " }, false, "name"},
		"name too long":        {func(d *Draft) { d.Name = strings.Repeat("a", 101) }, false, "name"},
		"no mailbox":           {func(d *Draft) { d.MailboxID = pgtype.UUID{} }, false, "mailbox_id"},
		"rate above maximum":   {func(d *Draft) { d.Rate = 121 }, false, "rate_per_minute"},
		"rate zero":            {func(d *Draft) { d.Rate = 0 }, false, "rate_per_minute"},
		"subject with newline": {func(d *Draft) { d.Subject = "a\r\nBcc: x@y.z" }, false, "subject"},
		"unknown variable":     {func(d *Draft) { d.BodyHTML = "<p>{{conversation.number}}</p>" }, false, "body_html"},
		"subject variable":     {func(d *Draft) { d.Subject = "{{nope.thing}}" }, false, "subject"},
		"complete: subject":    {func(d *Draft) { d.Subject = "" }, true, "subject"},
		"complete: body":       {func(d *Draft) { d.BodyHTML = "<p><br></p>" }, true, "body_html"},
		"complete: segment":    {func(d *Draft) { d.SegmentID = pgtype.UUID{} }, true, "segment_id"},
	} {
		t.Run(name, func(t *testing.T) {
			d := valid
			c.mutate(&d)
			if got := d.Validate(120, c.complete); got[c.field] == "" {
				t.Errorf("fields = %v, want an error on %s", got, c.field)
			}
		})
	}
	if got := valid.Validate(120, true); len(got) != 0 {
		t.Errorf("a valid draft has errors: %v", got)
	}
	incomplete := valid
	incomplete.Subject, incomplete.BodyHTML, incomplete.SegmentID = "", "", pgtype.UUID{}
	if got := incomplete.Validate(120, false); len(got) != 0 {
		t.Errorf("a draft may be saved incomplete, got %v", got)
	}
}

func TestRenderFillsVariablesAndAddsUnsubscribe(t *testing.T) {
	c := Render("Actie voor {{contact.first_name}}", `<p>Hallo {{contact.name}}, van {{agent.first_name}} bij {{mailbox.name}}.</p>`,
		Sender{Brand: "Shop", Agent: "Sanne Bakker"}, mail.Address{Name: `Jan <b>de</b> Vries`, Address: "jan@acme.example"},
		"https://echoo.test/afmelden/TOKEN")
	if c.Subject != "Actie voor Jan" {
		t.Errorf("subject = %q", c.Subject)
	}
	if strings.Contains(c.HTML, "<b>de</b>") || !strings.Contains(c.HTML, "Jan &lt;b&gt;de&lt;/b&gt; Vries") {
		t.Errorf("contact name is not escaped in the HTML part: %s", c.HTML)
	}
	if !strings.Contains(c.HTML, `href="https://echoo.test/afmelden/TOKEN"`) || !strings.Contains(c.Text, "Afmelden: https://echoo.test/afmelden/TOKEN") {
		t.Error("the unsubscribe link is missing from the footer")
	}
	byName := map[string]string{}
	for _, h := range c.Headers {
		byName[h.Name] = h.Value
	}
	if byName["List-Unsubscribe"] != "<https://echoo.test/afmelden/TOKEN>" || byName["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || byName["Precedence"] != "bulk" {
		t.Errorf("headers = %v", byName)
	}
	if _, ok := byName["Auto-Submitted"]; ok {
		t.Error("campaign mail must not be marked Auto-Submitted")
	}
}

func TestRenderNeverLeaksAPlaceholder(t *testing.T) {
	c := Render("Hoi {{contact.first_name}}!", "<p>Beste {{ contact.first_name }},</p>", Sender{Brand: "Shop"}, mail.Address{Address: "jan@acme.example"}, "https://x/afmelden/T")
	if strings.Contains(c.Subject+c.HTML+c.Text, "{{") {
		t.Errorf("template syntax reached the customer: %q %q", c.Subject, c.Text)
	}
	if c.Subject != "Hoi !" {
		t.Errorf("subject = %q", c.Subject)
	}
}

func TestTokenTamperingAndExpiry(t *testing.T) {
	var recipient, secret, other pgtype.UUID
	recipient.Bytes[0], secret.Bytes[0], other.Bytes[0] = 1, 2, 3
	recipient.Valid = true
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	token := issueToken(recipient, secret, now.Add(time.Hour))

	parsed, err := parseToken(token)
	if err != nil || parsed.recipient != recipient {
		t.Fatalf("parse: %v", err)
	}
	if err := parsed.verify(secret, now); err != nil {
		t.Fatalf("a genuine token: %v", err)
	}
	if err := parsed.verify(other, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong secret: %v, want ErrInvalidToken", err)
	}
	if err := parsed.verify(secret, now.Add(2*time.Hour)); !errors.Is(err, ErrExpiredToken) {
		t.Errorf("after expiry: %v, want ErrExpiredToken", err)
	}

	// Changing any single character must break the token, wherever the change is.
	for i := range token {
		flipped := []byte(token)
		if flipped[i] == 'A' {
			flipped[i] = 'B'
		} else {
			flipped[i] = 'A'
		}
		p, err := parseToken(string(flipped))
		if err == nil {
			err = p.verify(secret, now)
		}
		if err == nil {
			t.Fatalf("token with character %d changed still verifies", i)
		}
	}
	for _, bad := range []string{"", "abc", token[:len(token)-2], token + "AA", "!!!" + token} {
		if _, err := parseToken(bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("parse(%q) = %v, want ErrInvalidToken", bad, err)
		}
	}

	forged := parsed
	forged.body = append([]byte(nil), parsed.body...)
	forged.body[15] ^= 1
	if err := forged.verify(secret, now); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("forged recipient: %v", err)
	}
}

func (f *fixture) unsubscribeToken(c pgtype.UUID, expires time.Time) (token string, contact pgtype.UUID) {
	f.t.Helper()
	var recipient, secret pgtype.UUID
	err := f.pool.QueryRow(context.Background(), `SELECT id, unsubscribe_secret, contact_id FROM campaign_recipients WHERE campaign_id = $1 ORDER BY id LIMIT 1`, c).Scan(&recipient, &secret, &contact)
	if err != nil {
		f.t.Fatal(err)
	}
	return issueToken(recipient, secret, expires), contact
}

func TestUnsubscribeLookupChangesNothingAndPostIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.contact("Jan", "jan@acme.example")
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	token, contact := f.unsubscribeToken(c, f.clock.Now().Add(TokenTTL))

	info, err := f.svc.LookupUnsubscribe(context.Background(), token)
	if err != nil || info.Done || info.Brand != "Shop Support" || info.Email != "j***@acme.example" {
		t.Fatalf("lookup = %+v, %v", info, err)
	}
	if got := f.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact); got != 0 {
		t.Fatal("opening the link unsubscribed the contact")
	}

	for range 2 {
		if info, err = f.svc.Unsubscribe(context.Background(), token); err != nil || !info.Done {
			t.Fatalf("unsubscribe = %+v, %v", info, err)
		}
	}
	if got := f.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at = $2`, contact, f.clock.Now()); got != 1 {
		t.Error("the contact is not unsubscribed at the click time")
	}
	if got := f.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.unsubscribed'`); got != 1 {
		t.Errorf("audit entries = %d, want exactly 1 for two clicks", got)
	}
	if info, err = f.svc.LookupUnsubscribe(context.Background(), token); err != nil || !info.Done {
		t.Errorf("lookup after unsubscribing = %+v, %v", info, err)
	}

	next := f.campaign(campaignOpt{})
	f.start(next)
	f.tick(1)
	if got := f.states(next)["skipped:unsubscribed"]; got != 1 {
		t.Errorf("the next campaign skipped %d unsubscribed, want 1", got)
	}
}

func TestUnsubscribeRejectsExpiredAndForeignTokens(t *testing.T) {
	f := newFixture(t)
	f.contact("Jan", "jan@acme.example")
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)

	expired, contact := f.unsubscribeToken(c, f.clock.Now().Add(-time.Second))
	if _, err := f.svc.Unsubscribe(context.Background(), expired); !errors.Is(err, ErrExpiredToken) {
		t.Errorf("expired token: %v, want ErrExpiredToken", err)
	}
	var unknown pgtype.UUID
	unknown.Bytes[0] = 9
	ghost := issueToken(unknown, unknown, f.clock.Now().Add(time.Hour))
	if _, err := f.svc.Unsubscribe(context.Background(), ghost); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("token of a recipient that does not exist: %v, want ErrInvalidToken", err)
	}
	if got := f.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact); got != 0 {
		t.Error("a rejected token unsubscribed the contact")
	}
}

func TestUnsubscribeFindsTheContactThroughTheAddressAfterAMerge(t *testing.T) {
	f := newFixture(t)
	f.contact("Jan", "jan@acme.example")
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	token, contact := f.unsubscribeToken(c, f.clock.Now().Add(TokenTTL))
	f.exec(`UPDATE campaign_recipients SET contact_id = NULL WHERE campaign_id = $1`, c)

	if _, err := f.svc.Unsubscribe(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if got := f.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact); got != 1 {
		t.Error("the unsubscribe did not reach the contact that owns the address")
	}
}

func TestReportIsFormulaSafe(t *testing.T) {
	f := newFixture(t)
	c := f.campaign(campaignOpt{})
	f.exec(`INSERT INTO campaign_recipients (campaign_id, email, name, state, skip_reason) VALUES
		($1, 'a@acme.example', '=HYPERLINK("http://evil","x")', 'pending', ''),
		($1, '-b@acme.example', '+SUM(1)', 'skipped', 'unsubscribed')`, c)

	var out bytes.Buffer
	if err := f.svc.WriteReport(context.Background(), c, contacts.Viewer{Admin: true, UserID: f.admin.ID}, &out, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	report := out.String()
	if !strings.HasPrefix(report, "\xEF\xBB\xBFE-mailadres;Naam;Status;") {
		t.Errorf("report header = %q", report[:min(len(report), 60)])
	}
	for _, line := range strings.Split(report, "\n") {
		for _, cell := range strings.Split(line, ";") {
			if strings.HasPrefix(cell, "=") || strings.HasPrefix(cell, "+") || strings.HasPrefix(cell, "-") || strings.HasPrefix(cell, "\"=") {
				t.Errorf("cell %q starts a formula", cell)
			}
		}
	}
	if !strings.Contains(report, "'=HYPERLINK") || !strings.Contains(report, "'-b@acme.example") || !strings.Contains(report, "unsubscribed") {
		t.Errorf("report = %q", report)
	}
}

func TestSendTestRefusesADisabledMailbox(t *testing.T) {
	f := newFixture(t)
	c := f.campaign(campaignOpt{})
	f.exec(`UPDATE mailboxes SET disabled_at = now() WHERE id = $1`, f.mailbox)
	err := f.svc.SendTest(context.Background(), c, mail.Address{Address: "me@example.com"})
	var failed *TestSendError
	if !errors.As(err, &failed) {
		t.Errorf("err = %v, want a TestSendError", err)
	}
}

func TestQueuedMessageBuildsIntoBulkMail(t *testing.T) {
	f := newFixture(t)
	f.contact("Jan", "jan@acme.example")
	c := f.campaign(campaignOpt{})
	f.start(c)
	f.tick(1)
	var extra []byte
	err := f.pool.QueryRow(context.Background(), `SELECT o.extra_headers FROM outbound o JOIN campaign_recipients r ON r.message_id = o.message_id WHERE r.campaign_id = $1`, c).Scan(&extra)
	if err != nil {
		t.Fatal(err)
	}
	var headers []send.Header
	if err := json.Unmarshal(extra, &headers); err != nil {
		t.Fatal(err)
	}
	raw, err := send.Build(send.Outgoing{
		From: mail.Address{Address: "support@shop.example"}, To: []mail.Address{{Address: "jan@acme.example"}},
		Subject: "s", MessageID: "x@shop.example", Date: f.clock.Now(), Text: "t", Headers: headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("List-Unsubscribe-Post: List-Unsubscribe=One-Click")) || bytes.Contains(raw, []byte("Auto-Submitted")) {
		t.Errorf("built message:\n%s", raw)
	}
}
