package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// inboxFixture has two mailboxes: the Support team (agent, readonly) may access only A;
// admin sees both.
type inboxFixture struct {
	h                      *harness
	mailboxA, mailboxB     string
	team, otherTeam        string
	agent, readonly, admin *client
	agentID                string
}

func (f *inboxFixture) exec(sql string, args ...any) {
	f.h.t.Helper()
	if _, err := f.h.pool.Exec(context.Background(), sql, args...); err != nil {
		f.h.t.Fatal(err)
	}
}

func (f *inboxFixture) queryID(sql string, args ...any) string {
	f.h.t.Helper()
	var id pgtype.UUID
	if err := f.h.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.h.t.Fatal(err)
	}
	return id.String()
}

func (f *inboxFixture) mailbox(name, address string) string {
	return f.queryID(`INSERT INTO mailboxes (name, email_address) VALUES ($1, $2) RETURNING id`, name, address)
}

type convOpt struct {
	mailbox, status, assignee, team, contact string
	lastMessageAt                            time.Time
}

func (f *inboxFixture) conversation(subject string, o convOpt) string {
	if o.status == "" {
		o.status = "open"
	}
	if o.lastMessageAt.IsZero() {
		o.lastMessageAt = time.Now()
	}
	return f.queryID(`INSERT INTO conversations (mailbox_id, subject, status, assignee_user_id, assignee_team_id, contact_id, last_message_at)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7) RETURNING id`,
		o.mailbox, subject, o.status, o.assignee, o.team, o.contact, o.lastMessageAt)
}

func newInboxFixture(t *testing.T) *inboxFixture {
	t.Helper()
	h := newHarness(t)
	f := &inboxFixture{h: h}
	f.mailboxA = f.mailbox("Alpha", "alpha@example.com")
	f.mailboxB = f.mailbox("Bravo", "bravo@example.com")
	f.team = f.queryID(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	f.otherTeam = f.queryID(`INSERT INTO teams (name) VALUES ('Billing') RETURNING id`)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, f.mailboxA, f.team)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'read')`, f.mailboxB, f.otherTeam)

	agent, agentUser := h.loggedIn("agent")
	readonly, readonlyUser := h.loggedIn("readonly")
	admin, _ := h.loggedIn("admin")
	f.agent, f.readonly, f.admin, f.agentID = agent, readonly, admin, agentUser.ID.String()
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($1, $3)`, f.team, agentUser.ID, readonlyUser.ID)
	return f
}

type listResponse struct {
	Conversations []struct {
		ID            string  `json:"id"`
		Subject       string  `json:"subject"`
		Status        string  `json:"status"`
		LastDirection *string `json:"last_direction"`
		Mailbox       struct{ ID, Name string }
		Contact       *struct{ ID, Name, Email string }
		Assignee      *struct{ ID, Name string }
		Team          *struct{ ID, Name string }
		MessageCount  int `json:"message_count"`
	} `json:"conversations"`
	NextCursor *string `json:"next_cursor"`
}

func (f *inboxFixture) list(c *client, query string) listResponse {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/conversations"+query, nil)
	expect(f.h.t, r, 200, "")
	var out listResponse
	if err := json.Unmarshal(r.raw, &out); err != nil {
		f.h.t.Fatal(err)
	}
	return out
}

func subjects(l listResponse) string {
	names := make([]string, len(l.Conversations))
	for i, c := range l.Conversations {
		names[i] = c.Subject
	}
	return strings.Join(names, ",")
}

func TestInboxSummaryIsScoped(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	f.conversation("a-open-mine", convOpt{mailbox: f.mailboxA, assignee: f.agentID, team: f.team})
	f.conversation("a-open-unassigned", convOpt{mailbox: f.mailboxA})
	f.conversation("a-closed", convOpt{mailbox: f.mailboxA, status: "closed"})
	f.conversation("b-open", convOpt{mailbox: f.mailboxB, team: f.otherTeam})
	f.conversation("b-open-2", convOpt{mailbox: f.mailboxB, lastMessageAt: now})
	deleted := f.conversation("a-deleted", convOpt{mailbox: f.mailboxA})
	f.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, deleted)

	type summary struct {
		Mailboxes []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			EmailAddress string `json:"email_address"`
			OpenCount    int    `json:"open_count"`
		} `json:"mailboxes"`
		Teams []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			OpenCount int    `json:"open_count"`
		} `json:"teams"`
		Counts struct{ Mine, Unassigned, All int } `json:"counts"`
	}
	get := func(c *client) summary {
		r := c.do("GET", "/api/v1/inbox/summary", nil)
		expect(t, r, 200, "")
		var s summary
		if err := json.Unmarshal(r.raw, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	for name, c := range map[string]*client{"agent": f.agent, "readonly": f.readonly} {
		s := get(c)
		if len(s.Mailboxes) != 1 || s.Mailboxes[0].ID != f.mailboxA || s.Mailboxes[0].OpenCount != 2 || s.Mailboxes[0].EmailAddress != "alpha@example.com" {
			t.Errorf("%s mailboxes: %+v", name, s.Mailboxes)
		}
		if len(s.Teams) != 1 || s.Teams[0].ID != f.team || s.Teams[0].OpenCount != 1 {
			t.Errorf("%s teams: %+v", name, s.Teams)
		}
		if name == "agent" && (s.Counts.Mine != 1 || s.Counts.Unassigned != 1 || s.Counts.All != 2) {
			t.Errorf("agent counts: %+v", s.Counts)
		}
	}
	if s := get(f.readonly); s.Counts.Mine != 0 || s.Counts.Unassigned != 1 || s.Counts.All != 2 {
		t.Errorf("readonly counts: %+v", s.Counts)
	}

	s := get(f.admin)
	if len(s.Mailboxes) != 2 || s.Mailboxes[0].Name != "Alpha" || s.Mailboxes[1].Name != "Bravo" || s.Mailboxes[1].OpenCount != 2 {
		t.Errorf("admin mailboxes: %+v", s.Mailboxes)
	}
	if len(s.Teams) != 2 || s.Teams[0].Name != "Billing" || s.Teams[0].OpenCount != 1 || s.Teams[1].OpenCount != 1 {
		t.Errorf("admin teams: %+v", s.Teams)
	}
	if s.Counts.Mine != 0 || s.Counts.Unassigned != 3 || s.Counts.All != 4 {
		t.Errorf("admin counts: %+v", s.Counts)
	}

	f.exec(`UPDATE mailboxes SET disabled_at = now() WHERE id = $1`, f.mailboxB)
	if s := get(f.admin); len(s.Mailboxes) != 1 {
		t.Errorf("disabled mailbox listed: %+v", s.Mailboxes)
	}
}

func TestConversationListScopeViewsAndFilters(t *testing.T) {
	f := newInboxFixture(t)
	base := time.Now().Add(-time.Hour)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	f.conversation("a-mine", convOpt{mailbox: f.mailboxA, assignee: f.agentID, team: f.team, lastMessageAt: at(4)})
	f.conversation("a-unassigned", convOpt{mailbox: f.mailboxA, lastMessageAt: at(3)})
	f.conversation("a-team", convOpt{mailbox: f.mailboxA, team: f.team, lastMessageAt: at(2)})
	f.conversation("a-waiting", convOpt{mailbox: f.mailboxA, status: "waiting", lastMessageAt: at(1)})
	f.conversation("a-closed", convOpt{mailbox: f.mailboxA, status: "closed", lastMessageAt: at(1)})
	f.conversation("a-spam", convOpt{mailbox: f.mailboxA, status: "spam", lastMessageAt: at(1)})
	f.conversation("b-open", convOpt{mailbox: f.mailboxB, lastMessageAt: at(5)})
	f.conversation("b-mine", convOpt{mailbox: f.mailboxB, assignee: f.agentID, lastMessageAt: at(6)})
	f.conversation("b-closed", convOpt{mailbox: f.mailboxB, status: "closed", lastMessageAt: at(6)})
	deleted := f.conversation("a-deleted", convOpt{mailbox: f.mailboxA, lastMessageAt: at(9)})
	f.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, deleted)

	cases := []struct{ query, agent, admin string }{
		{"", "a-mine,a-unassigned,a-team", "b-mine,b-open,a-mine,a-unassigned,a-team"},
		{"?view=all&status=open", "a-mine,a-unassigned,a-team", "b-mine,b-open,a-mine,a-unassigned,a-team"},
		{"?view=mine", "a-mine", "" /* admin has no assigned conversations */},
		{"?view=unassigned", "a-unassigned,a-team", "b-open,a-unassigned,a-team"},
		{"?status=waiting", "a-waiting", "a-waiting"},
		{"?status=closed", "a-closed", "b-closed,a-closed"},
		{"?status=spam", "a-spam", "a-spam"},
		{"?team_id=" + f.team, "a-mine,a-team", "a-mine,a-team"},
		{"?team_id=" + f.team + "&view=unassigned", "a-team", "a-team"},
		{"?mailbox_id=" + f.mailboxA, "a-mine,a-unassigned,a-team", "a-mine,a-unassigned,a-team"},
		{"?mailbox_id=" + f.mailboxB, "", "b-mine,b-open"},
		{"?mailbox_id=" + f.mailboxB + "&view=mine", "", ""},
		{"?mailbox_id=" + f.mailboxB + "&status=closed&view=unassigned", "", "b-closed"},
	}
	for _, tc := range cases {
		if got := subjects(f.list(f.agent, tc.query)); got != tc.agent {
			t.Errorf("agent %q: got %q, want %q", tc.query, got, tc.agent)
		}
		wantReadonly := tc.agent
		if strings.Contains(tc.query, "view=mine") {
			wantReadonly = ""
		}
		if got := subjects(f.list(f.readonly, tc.query)); got != wantReadonly {
			t.Errorf("readonly %q: got %q, want %q", tc.query, got, wantReadonly)
		}
		if got := subjects(f.list(f.admin, tc.query)); got != tc.admin {
			t.Errorf("admin %q: got %q, want %q", tc.query, got, tc.admin)
		}
	}

	// A conversation of mailbox B assigned to the agent stays invisible: assignment does not widen scope.
	if got := subjects(f.list(f.agent, "?view=mine&mailbox_id="+f.mailboxB)); got != "" {
		t.Errorf("agent saw B through mine: %q", got)
	}
}

func TestConversationListItemShape(t *testing.T) {
	f := newInboxFixture(t)
	org := f.queryID(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id`)
	contact := f.queryID(`INSERT INTO contacts (name, organization_id) VALUES ('Jane', $1) RETURNING id`, org)
	f.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'jane-old@acme.test', false), ($1, 'jane@acme.test', true)`, contact)
	conv := f.conversation("shape", convOpt{mailbox: f.mailboxA, assignee: f.agentID, team: f.team, contact: contact})
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, received_at) VALUES
		($1, $2, 'email', 'in', now() - interval '2 minutes'),
		($1, $2, 'email', 'out', now() - interval '1 minute'),
		($1, $2, 'note', NULL, now())`, conv, f.mailboxA)
	bare := f.conversation("bare", convOpt{mailbox: f.mailboxA, lastMessageAt: time.Now().Add(-time.Hour)})

	l := f.list(f.agent, "")
	if len(l.Conversations) != 2 {
		t.Fatalf("got %d conversations", len(l.Conversations))
	}
	c := l.Conversations[0]
	if c.ID != conv || c.Mailbox.Name != "Alpha" || c.Mailbox.ID != f.mailboxA ||
		c.Contact == nil || c.Contact.Email != "jane@acme.test" || c.Contact.Name != "Jane" ||
		c.Assignee == nil || c.Assignee.ID != f.agentID || c.Team == nil || c.Team.Name != "Support" ||
		c.LastDirection == nil || *c.LastDirection != "out" {
		t.Errorf("unexpected item: %+v", c)
	}
	b := l.Conversations[1]
	if b.ID != bare || b.Contact != nil || b.Assignee != nil || b.Team != nil || b.LastDirection != nil {
		t.Errorf("unexpected bare item: %+v", b)
	}
	if !strings.Contains(string(f.agent.do("GET", "/api/v1/conversations", nil).raw), `"last_direction":null`) {
		t.Error("last_direction must be an explicit null")
	}
}

func TestConversationPaginationVisitsEveryRowOnce(t *testing.T) {
	f := newInboxFixture(t)
	ts := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	want := map[string]bool{}
	// Groups of seven share one timestamp, so the id tiebreak decides the page boundaries.
	for i := range 120 {
		mailbox := f.mailboxA
		if i%3 == 0 {
			mailbox = f.mailboxB
		}
		id := f.conversation(fmt.Sprintf("c%03d", i), convOpt{mailbox: mailbox, lastMessageAt: ts.Add(time.Duration(i/7) * time.Minute)})
		want[id] = true
	}

	for _, view := range []string{"all", "unassigned"} {
		seen := map[string]bool{}
		cursor, pages := "", 0
		for {
			q := "?view=" + view + "&limit=13"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			l := f.list(f.admin, q)
			pages++
			for _, c := range l.Conversations {
				if seen[c.ID] {
					t.Fatalf("%s: %s returned twice", view, c.ID)
				}
				seen[c.ID] = true
			}
			if l.NextCursor == nil {
				break
			}
			if len(l.Conversations) != 13 {
				t.Fatalf("short page %d before the end", len(l.Conversations))
			}
			cursor = *l.NextCursor
		}
		if len(seen) != len(want) || pages != 10 {
			t.Errorf("%s: saw %d rows in %d pages, want %d in 10", view, len(seen), pages, len(want))
		}
		for id := range want {
			if !seen[id] {
				t.Errorf("%s: %s never returned", view, id)
			}
		}
	}

	// The agent pages through A only, in the same way.
	seen, cursor := 0, ""
	for {
		l := f.list(f.agent, "?limit=25&cursor="+url.QueryEscape(cursor))
		seen += len(l.Conversations)
		if l.NextCursor == nil {
			break
		}
		cursor = *l.NextCursor
	}
	if seen != 80 {
		t.Errorf("agent saw %d rows, want 80", seen)
	}

	// mine pages through the assignee index.
	for i := range 30 {
		f.conversation(fmt.Sprintf("m%02d", i), convOpt{mailbox: f.mailboxA, assignee: f.agentID, lastMessageAt: ts})
	}
	mine, cursor := map[string]bool{}, ""
	for {
		l := f.list(f.agent, "?view=mine&limit=8&cursor="+url.QueryEscape(cursor))
		for _, c := range l.Conversations {
			if mine[c.ID] {
				t.Fatalf("mine: %s returned twice", c.ID)
			}
			mine[c.ID] = true
		}
		if l.NextCursor == nil {
			break
		}
		cursor = *l.NextCursor
	}
	if len(mine) != 30 {
		t.Errorf("mine saw %d rows, want 30", len(mine))
	}
}

func TestConversationListRejectsBadParameters(t *testing.T) {
	f := newInboxFixture(t)
	valid := base64.RawURLEncoding.EncodeToString([]byte("1700000000000000.0199a000-0000-7000-8000-000000000000"))
	cursors := []string{
		"!!!", valid + "=", base64.RawURLEncoding.EncodeToString([]byte("nodot")),
		base64.RawURLEncoding.EncodeToString([]byte("x.0199a000-0000-7000-8000-000000000000")),
		base64.RawURLEncoding.EncodeToString([]byte("1700000000000000.not-a-uuid")),
		base64.RawURLEncoding.EncodeToString([]byte("9223372036854775807.0199a000-0000-7000-8000-000000000000")),
	}
	var queries []string
	for _, c := range cursors {
		queries = append(queries, "?cursor="+url.QueryEscape(c))
	}
	queries = append(queries, "?view=everything", "?status=archived", "?limit=0", "?limit=101", "?limit=-1", "?limit=abc",
		"?mailbox_id=nope", "?team_id=nope")
	for _, q := range queries {
		expect(t, f.agent.do("GET", "/api/v1/conversations"+q, nil), 400, "invalid_request")
	}
	expect(t, f.agent.do("GET", "/api/v1/conversations?cursor="+valid+"&limit=100", nil), 200, "")
}

func TestConversationDetail(t *testing.T) {
	f := newInboxFixture(t)
	org := f.queryID(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id`)
	contact := f.queryID(`INSERT INTO contacts (name, organization_id) VALUES ('Jane', $1) RETURNING id`, org)
	f.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'jane@acme.test', true)`, contact)
	conv := f.conversation("detail", convOpt{mailbox: f.mailboxA, contact: contact})
	f.conversation("elsewhere", convOpt{mailbox: f.mailboxB, contact: contact})
	f.conversation("also here", convOpt{mailbox: f.mailboxA, contact: contact})
	deleted := f.conversation("gone", convOpt{mailbox: f.mailboxA, contact: contact})
	f.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, deleted)

	in := f.queryID(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, to_addrs, cc_addrs, subject, body_text, body_html, sent_at, received_at)
		VALUES ($1, $2, 'email', 'in', 'jane@acme.test', 'Jane', '[{"name":"Support","address":"alpha@example.com"}]', '[{"name":"","address":"cc@acme.test"}]',
		'Hello', 'plain body', '<b>SECRET-HTML</b>', now() - interval '3 minutes', now() - interval '3 minutes') RETURNING id`, conv, f.mailboxA)
	out := f.queryID(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, to_addrs, subject, body_text, body_html, author_user_id, received_at)
		VALUES ($1, $2, 'email', 'out', 'alpha@example.com', 'Support', '[{"name":"Jane","address":"jane@acme.test"}]', 'Re: Hello', 'reply', '<p>SECRET-HTML</p>', $3, now() - interval '2 minutes') RETURNING id`, conv, f.mailboxA, f.agentID)
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, from_addr, subject, body_text, received_at)
		VALUES ($1, $2, 'note', '', '', 'internal note', now() - interval '1 minute')`, conv, f.mailboxA)
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, body_text, deleted_at)
		VALUES ($1, $2, 'email', 'in', 'deleted message', now())`, conv, f.mailboxA)
	f.exec(`INSERT INTO attachments (message_id, filename, sniffed_type, size_bytes, sha256, blob_key, disposition)
		VALUES ($1, 'invoice.pdf', 'application/pdf', 2048, '\x00', 'k1', 'attachment'), ($1, 'logo.png', 'image/png', 10, '\x01', 'k2', 'inline')`, in)
	f.exec(`INSERT INTO outbound (message_id, idempotency_key, status, error) VALUES ($1, uuidv7(), 'failed', 'mailbox full')`, out)

	type detail struct {
		Conversation struct {
			ID               string     `json:"id"`
			CreatedAt        time.Time  `json:"created_at"`
			FirstRespondedAt *time.Time `json:"first_responded_at"`
			ResolvedAt       *time.Time `json:"resolved_at"`
			CanWrite         bool       `json:"can_write"`
			Mailbox          struct{ ID string }
		} `json:"conversation"`
		Messages []struct {
			Kind      string  `json:"kind"`
			Direction *string `json:"direction"`
			From      struct{ Name, Address string }
			To, Cc    []struct{ Name, Address string }
			BodyText  string  `json:"body_text"`
			SentAt    *string `json:"sent_at"`
			Author    *struct{ ID, Name string }
			Attach    []struct {
				Filename    string
				Size        int64
				SniffedType string `json:"sniffed_type"`
				Inline      bool
			} `json:"attachments"`
			OutboundStatus *string `json:"outbound_status"`
			OutboundError  *string `json:"outbound_error"`
		} `json:"messages"`
		Contact *struct {
			ID, Name, Email   string
			Organization      *struct{ ID, Name string }
			ConversationCount int `json:"conversation_count"`
		} `json:"contact"`
	}
	get := func(c *client) (detail, response) {
		r := c.do("GET", "/api/v1/conversations/"+conv, nil)
		expect(t, r, 200, "")
		var d detail
		if err := json.Unmarshal(r.raw, &d); err != nil {
			t.Fatal(err)
		}
		return d, r
	}

	d, r := get(f.agent)
	if strings.Contains(string(r.raw), "SECRET-HTML") || strings.Contains(string(r.raw), "body_html") {
		t.Fatalf("body_html leaked: %s", r.raw)
	}
	if strings.Contains(string(r.raw), "deleted message") {
		t.Fatal("deleted message returned")
	}
	if d.Conversation.ID != conv || d.Conversation.CreatedAt.IsZero() || d.Conversation.FirstRespondedAt != nil || d.Conversation.ResolvedAt != nil {
		t.Errorf("conversation: %+v", d.Conversation)
	}
	if len(d.Messages) != 3 {
		t.Fatalf("got %d messages", len(d.Messages))
	}
	m0, m1, m2 := d.Messages[0], d.Messages[1], d.Messages[2]
	if m0.Kind != "email" || m0.Direction == nil || *m0.Direction != "in" || m0.From.Address != "jane@acme.test" || m0.From.Name != "Jane" ||
		len(m0.To) != 1 || m0.To[0].Address != "alpha@example.com" || len(m0.Cc) != 1 || m0.BodyText != "plain body" || m0.SentAt == nil ||
		m0.OutboundStatus != nil || m0.OutboundError != nil || m0.Author != nil {
		t.Errorf("inbound message: %+v", m0)
	}
	if len(m0.Attach) != 2 || m0.Attach[0].Filename != "invoice.pdf" || m0.Attach[0].Size != 2048 || m0.Attach[0].SniffedType != "application/pdf" || m0.Attach[0].Inline || !m0.Attach[1].Inline {
		t.Errorf("attachments: %+v", m0.Attach)
	}
	if m1.Direction == nil || *m1.Direction != "out" || m1.OutboundStatus == nil || *m1.OutboundStatus != "failed" ||
		m1.OutboundError == nil || *m1.OutboundError != "mailbox full" || m1.Author == nil || m1.Author.ID != f.agentID ||
		len(m1.Attach) != 0 || len(m1.Cc) != 0 {
		t.Errorf("outbound message: %+v", m1)
	}
	if m2.Kind != "note" || m2.Direction != nil || m2.SentAt != nil {
		t.Errorf("note: %+v", m2)
	}
	if !strings.Contains(string(r.raw), `"attachments":[]`) || !strings.Contains(string(r.raw), `"cc":[]`) {
		t.Error("empty lists must serialize as []")
	}
	if d.Contact == nil || d.Contact.Email != "jane@acme.test" || d.Contact.Organization == nil || d.Contact.Organization.Name != "Acme" || d.Contact.ConversationCount != 2 {
		t.Errorf("agent contact: %+v", d.Contact)
	}

	// The conversation count must not reveal conversations in mailboxes the caller cannot read.
	if d, _ := get(f.admin); d.Contact == nil || d.Contact.ConversationCount != 3 {
		t.Errorf("admin contact: %+v", d.Contact)
	}
	if d, _ := get(f.readonly); len(d.Messages) != 3 || d.Conversation.CanWrite {
		t.Errorf("readonly messages: %d, can_write %v", len(d.Messages), d.Conversation.CanWrite)
	}
	if !d.Conversation.CanWrite {
		t.Error("agent with write access got can_write false")
	}
}

func TestConversationDetailOutOfScopeIsNotFound(t *testing.T) {
	f := newInboxFixture(t)
	inB := f.conversation("b", convOpt{mailbox: f.mailboxB})
	deleted := f.conversation("a", convOpt{mailbox: f.mailboxA})
	f.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, deleted)

	for _, id := range []string{inB, deleted, "0199a000-0000-7000-8000-000000000000", "not-a-uuid"} {
		for name, c := range map[string]*client{"agent": f.agent, "readonly": f.readonly} {
			r := c.do("GET", "/api/v1/conversations/"+id, nil)
			if r.status != 404 || r.errCode() != "not_found" {
				t.Errorf("%s %s: got %d %s", name, id, r.status, r.raw)
			}
		}
	}
	expect(t, f.admin.do("GET", "/api/v1/conversations/"+inB, nil), 200, "")
	expect(t, f.admin.do("GET", "/api/v1/conversations/"+deleted, nil), 404, "not_found")
	expect(t, f.h.client().do("GET", "/api/v1/conversations/"+inB, nil), 401, "")
	expect(t, f.h.client().do("GET", "/api/v1/conversations", nil), 401, "")
	expect(t, f.h.client().do("GET", "/api/v1/inbox/summary", nil), 401, "")
}
