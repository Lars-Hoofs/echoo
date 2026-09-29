package threading

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, pool: testdb.New(t)}
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) id(sql string, args ...any) ID {
	f.t.Helper()
	var id ID
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) mailbox(address string) ID {
	return f.id(`INSERT INTO mailboxes (name, email_address) VALUES ($1, $1) RETURNING id`, address)
}

type convSpec struct {
	subject    string
	status     string
	lastAt     time.Time
	resolvedAt *time.Time
	deleted    bool
}

func (f *fixture) conversation(mailbox ID, c convSpec) ID {
	if c.status == "" {
		c.status = "open"
	}
	var deletedAt *time.Time
	if c.deleted {
		deletedAt = &c.lastAt
	}
	return f.id(`INSERT INTO conversations (mailbox_id, subject, subject_normalized, status, last_message_at, resolved_at, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		mailbox, c.subject, NormalizeSubject(c.subject), c.status, c.lastAt, c.resolvedAt, deletedAt)
}

type msgSpec struct {
	kind    string
	from    string
	to, cc  string
	bcc     string
	deleted bool
}

func (f *fixture) message(conv, mailbox ID, m msgSpec) {
	if m.kind == "" {
		m.kind = "email"
	}
	var direction *string
	if m.kind == "email" {
		in := "in"
		direction = &in
	}
	var deletedAt *time.Time
	if m.deleted {
		now := time.Now()
		deletedAt = &now
	}
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, to_addrs, cc_addrs, bcc_addrs, deleted_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8::jsonb, $9)`,
		conv, mailbox, m.kind, direction, m.from, addrJSON(m.to), addrJSON(m.cc), addrJSON(m.bcc), deletedAt)
}

func addrJSON(a string) string {
	if a == "" {
		return "[]"
	}
	return `[{"name": "", "address": "` + a + `"}]`
}

func (f *fixture) ref(mailbox ID, messageID string, conv ID) {
	f.exec(`INSERT INTO thread_refs (mailbox_id, message_id_hash, conversation_id) VALUES ($1, $2, $3)`,
		mailbox, mail.HashMessageID(messageID), conv)
}

func TestPGLookupConversationsByRefs(t *testing.T) {
	f := newFixture(t)
	a, b := f.mailbox("a@echoo.test"), f.mailbox("b@echoo.test")
	convA := f.conversation(a, convSpec{subject: "S", lastAt: now})
	convB := f.conversation(b, convSpec{subject: "S", lastAt: now})
	f.ref(a, "one@x.nl", convA)
	f.ref(a, "two@x.nl", convA)
	f.ref(b, "one@x.nl", convB)
	lookup := NewPGLookup(dbq.New(f.pool))
	ctx := context.Background()

	got, err := lookup.ConversationsByRefs(ctx, a, [][]byte{
		mail.HashMessageID("one@x.nl"), mail.HashMessageID("two@x.nl"), mail.HashMessageID("missing@x.nl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[string(mail.HashMessageID("one@x.nl"))] != convA || got[string(mail.HashMessageID("two@x.nl"))] != convA {
		t.Errorf("mailbox A refs = %v", got)
	}

	got, err = lookup.ConversationsByRefs(ctx, b, [][]byte{mail.HashMessageID("one@x.nl"), mail.HashMessageID("two@x.nl")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[string(mail.HashMessageID("one@x.nl"))] != convB {
		t.Errorf("mailbox B refs = %v, want only its own conversation", got)
	}

	got, err = lookup.ConversationsByRefs(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty hash list returned %v", got)
	}
}

func TestPGLookupConversationInMailbox(t *testing.T) {
	f := newFixture(t)
	a, b := f.mailbox("a@echoo.test"), f.mailbox("b@echoo.test")
	convA := f.conversation(a, convSpec{subject: "S", lastAt: now})
	lookup := NewPGLookup(dbq.New(f.pool))

	tests := []struct {
		name    string
		mailbox ID
		conv    ID
		want    bool
	}{
		{"own mailbox", a, convA, true},
		{"other mailbox", b, convA, false},
		{"unknown conversation", a, ID{9}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := lookup.ConversationInMailbox(context.Background(), tt.mailbox, tt.conv)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPGLookupSubjectCandidates(t *testing.T) {
	f := newFixture(t)
	a, b := f.mailbox("a@echoo.test"), f.mailbox("b@echoo.test")
	resolved := daysAgo(3)

	recent := f.conversation(a, convSpec{subject: "Re: Printer stuk", lastAt: daysAgo(1)})
	f.message(recent, a, msgSpec{from: "Klant@Cust.nl", to: "support@echoo.test", cc: "collega@cust.nl", bcc: "hidden@cust.nl"})
	f.message(recent, a, msgSpec{from: "support@echoo.test", to: "klant@cust.nl"})
	f.message(recent, a, msgSpec{kind: "note", from: "note-author@echoo.test"})
	f.message(recent, a, msgSpec{from: "deleted@cust.nl", deleted: true})

	closed := f.conversation(a, convSpec{subject: "Printer stuk", status: "closed", lastAt: daysAgo(4), resolvedAt: &resolved})
	f.conversation(a, convSpec{subject: "Printer stuk", lastAt: daysAgo(40)})
	f.conversation(a, convSpec{subject: "Printer stuk", status: "spam", lastAt: daysAgo(1)})
	f.conversation(a, convSpec{subject: "Printer stuk", lastAt: daysAgo(1), deleted: true})
	f.conversation(a, convSpec{subject: "Scanner stuk", lastAt: daysAgo(1)})
	f.conversation(b, convSpec{subject: "Printer stuk", lastAt: daysAgo(1)})

	lookup := NewPGLookup(dbq.New(f.pool))
	got, err := lookup.SubjectCandidates(context.Background(), a, "printer stuk", daysAgo(30))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	if got[0].ID != recent || got[1].ID != closed {
		t.Errorf("order = %v, %v; want most recent first", got[0].ID, got[1].ID)
	}
	if want := []string{"collega@cust.nl", "klant@cust.nl", "support@echoo.test"}; !equalUnordered(got[0].Participants, want) {
		t.Errorf("participants = %v, want %v", got[0].Participants, want)
	}
	if got[0].Status != "open" || !got[0].ResolvedAt.IsZero() {
		t.Errorf("open candidate = %+v", got[0])
	}
	if got[1].Status != "closed" || !got[1].ResolvedAt.Equal(resolved) {
		t.Errorf("closed candidate = %+v, want resolved_at %v", got[1], resolved)
	}
	if len(got[1].Participants) != 0 {
		t.Errorf("conversation without messages has participants %v", got[1].Participants)
	}
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]int{}
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		set[s]--
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestPGLookupResolveInTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b := f.mailbox("support@echoo.test"), f.mailbox("sales@echoo.test")
	convA := f.conversation(a, convSpec{subject: "Printer stuk", lastAt: daysAgo(2)})
	f.message(convA, a, msgSpec{from: "klant@cust.nl", to: "support@echoo.test"})
	f.ref(a, "q1@cust.nl", convA)
	convB := f.conversation(b, convSpec{subject: "Offerte", lastAt: daysAgo(2)})

	tests := []struct {
		name string
		msg  *mail.Parsed
		want ID
		why  Reason
	}{
		{"In-Reply-To", msg("q2@cust.nl", "Re: Printer stuk", "klant@cust.nl", irt("q1@cust.nl")), convA, ReasonInReplyTo},
		{"subject and participant", msg("q3@cust.nl", "Re: Printer stuk", "klant@cust.nl"), convA, ReasonSubject},
		{"outbound token", msg("q4@cust.nl", "Anders", "klant@cust.nl", irt(outboundID(t, convA))), convA, ReasonOutboundToken},
		{"forged token from other mailbox", msg("q5@evil.example", "Anders", "evil@evil.example", irt(outboundID(t, convB))), ID{}, ReasonNewConversation},
		{"forward", msg("q6@cust.nl", "Fwd: Printer stuk", "klant@cust.nl"), ID{}, ReasonNewConversation},
		{"other customer", msg("q7@else.example", "Printer stuk", "ander@else.example"), ID{}, ReasonNewConversation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			in := Input{MailboxID: a, OwnAddresses: []string{"support@echoo.test"}, Message: tt.msg, ReceivedAt: now}
			got, err := Resolve(ctx, NewPGLookup(dbq.New(f.pool).WithTx(tx)), in)
			if err != nil {
				t.Fatal(err)
			}
			if got.ConversationID != tt.want || got.Reason != tt.why {
				t.Errorf("Resolve = %v %q, want %v %q", got.ConversationID, got.Reason, tt.want, tt.why)
			}
		})
	}
}
