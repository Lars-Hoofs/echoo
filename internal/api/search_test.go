package api

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type searchResponse struct {
	Results []struct {
		Conversation struct {
			ID      string `json:"id"`
			Subject string `json:"subject"`
			Number  int64  `json:"number"`
		} `json:"conversation"`
		Snippet string `json:"snippet"`
	} `json:"results"`
	NextCursor *string `json:"next_cursor"`
}

func (r searchResponse) subjects() string {
	out := make([]string, len(r.Results))
	for i, res := range r.Results {
		out[i] = res.Conversation.Subject
	}
	return strings.Join(out, ",")
}

func (f *inboxFixture) search(c *client, q string, extra ...string) searchResponse {
	f.h.t.Helper()
	path := "/api/v1/search?q=" + url.QueryEscape(q)
	for _, e := range extra {
		path += "&" + e
	}
	r := c.do("GET", path, nil)
	expect(f.h.t, r, 200, "")
	var out searchResponse
	if err := json.Unmarshal(r.raw, &out); err != nil {
		f.h.t.Fatal(err)
	}
	return out
}

type searchMsg struct {
	direction, fromAddr, fromName, to string
	at                                time.Time
}

// message adds an e-mail to a conversation; the generated fts column indexes it.
func (f *inboxFixture) searchMessage(conv, mailbox, subject, body string, o searchMsg) {
	f.h.t.Helper()
	if o.direction == "" {
		o.direction = "in"
	}
	if o.fromAddr == "" {
		o.fromAddr = "klant@example.org"
	}
	if o.at.IsZero() {
		o.at = time.Now()
	}
	to := "[]"
	if o.to != "" {
		to = `[{"name":"","address":"` + o.to + `"}]`
	}
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, to_addrs, subject, body_text, received_at)
		VALUES ($1, $2, 'email', $3, $4, $5, $6::jsonb, $7, $8, $9)`,
		conv, mailbox, o.direction, o.fromAddr, o.fromName, to, subject, body, o.at)
}

func TestSearchIsScopedToReadableMailboxes(t *testing.T) {
	f := newInboxFixture(t)
	a := f.conversation("Offerte Alpha", convOpt{mailbox: f.mailboxA})
	f.searchMessage(a, f.mailboxA, "Offerte Alpha", "De geheime zin over zaaknummer 8842 staat hier.", searchMsg{})
	b := f.conversation("Offerte Bravo", convOpt{mailbox: f.mailboxB})
	f.searchMessage(b, f.mailboxB, "Offerte Bravo", "De geheime zin over zaaknummer 8842 staat ook hier.", searchMsg{})

	for _, q := range []string{`"geheime zin"`, "zaaknummer", "Bravo", "mailbox:Bravo zaaknummer", "aan:", `"zaaknummer 8842"`} {
		got := f.search(f.agent, q)
		if strings.Contains(got.subjects(), "Bravo") {
			t.Errorf("agent found mailbox B content with %q: %s", q, got.subjects())
		}
	}
	if got := f.search(f.agent, `"geheime zin"`); got.subjects() != "Offerte Alpha" {
		t.Errorf("agent results = %q, want only Alpha", got.subjects())
	}
	if got := f.search(f.admin, `"geheime zin"`); len(got.Results) != 2 {
		t.Errorf("admin results = %q, want both", got.subjects())
	}
	var number int64
	if err := f.h.pool.QueryRow(t.Context(), `SELECT number FROM conversations WHERE id = $1`, b).Scan(&number); err != nil {
		t.Fatal(err)
	}
	if got := f.search(f.agent, "#"+strconv.FormatInt(number, 10)); len(got.Results) != 0 {
		t.Errorf("agent found mailbox B by number: %s", got.subjects())
	}
	if got := f.search(f.admin, "#"+strconv.FormatInt(number, 10)); got.subjects() != "Offerte Bravo" {
		t.Errorf("admin by number = %q", got.subjects())
	}
}

func TestSearchRequiresSession(t *testing.T) {
	h := newHarness(t)
	expect(t, h.client().do("GET", "/api/v1/search?q=x", nil), 401, "")
}

func TestSearchEmptyAndInvalid(t *testing.T) {
	f := newInboxFixture(t)
	if got := f.search(f.agent, "   "); len(got.Results) != 0 || got.NextCursor != nil {
		t.Errorf("empty query returned %+v", got)
	}
	expect(t, f.agent.do("GET", "/api/v1/search?q=x&cursor=bogus", nil), 400, "invalid_request")
	expect(t, f.agent.do("GET", "/api/v1/search?q=x&limit=0", nil), 400, "invalid_request")
	expect(t, f.agent.do("GET", "/api/v1/search?q=x&limit=51", nil), 400, "invalid_request")
}

func TestSearchRanking(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	weak := f.conversation("Algemene vraag", convOpt{mailbox: f.mailboxA, lastMessageAt: now})
	f.searchMessage(weak, f.mailboxA, "Algemene vraag", strings.Repeat("Vul hier wat opvulling in. ", 40)+"Verder nog een keer factuur genoemd.", searchMsg{})
	strong := f.conversation("Factuur betaling", convOpt{mailbox: f.mailboxA, lastMessageAt: now.Add(-48 * time.Hour)})
	f.searchMessage(strong, f.mailboxA, "Factuur betaling", "Factuur factuur, de factuur is niet betaald.", searchMsg{})
	newer := f.conversation("Nieuwe factuur", convOpt{mailbox: f.mailboxA, lastMessageAt: now})
	f.searchMessage(newer, f.mailboxA, "Nieuwe factuur", "Factuur factuur, de factuur is niet betaald.", searchMsg{})

	got := f.search(f.agent, "factuur")
	if got.subjects() != "Nieuwe factuur,Factuur betaling,Algemene vraag" {
		t.Errorf("ranking = %q, want equal scores by recency, weak match last", got.subjects())
	}
}

func TestSearchPagination(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	want := []string{}
	for i := 0; i < 7; i++ {
		subject := "Pagina " + string(rune('A'+i))
		c := f.conversation(subject, convOpt{mailbox: f.mailboxA, lastMessageAt: now.Add(-time.Duration(i) * time.Minute)})
		f.searchMessage(c, f.mailboxA, subject, "zoekwoord paginatest", searchMsg{})
		want = append(want, subject)
	}
	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		extra := []string{"limit=3"}
		if cursor != "" {
			extra = append(extra, "cursor="+url.QueryEscape(cursor))
		}
		got := f.search(f.agent, "paginatest", extra...)
		for _, r := range got.Results {
			seen = append(seen, r.Conversation.Subject)
		}
		if got.NextCursor == nil {
			break
		}
		cursor = *got.NextCursor
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("paged results = %v, want %v", seen, want)
	}
}

func TestSearchFilters(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	old := now.AddDate(0, 0, -30)

	open := f.conversation("Open factuurvraag", convOpt{mailbox: f.mailboxA, assignee: f.agentID, team: f.team, lastMessageAt: now})
	f.searchMessage(open, f.mailboxA, "Open factuurvraag", "Wanneer komt de zending?", searchMsg{fromAddr: "jan@bedrijf.nl", fromName: "Jan Jansen", to: "info@alpha.example"})
	f.exec(`UPDATE conversations SET priority = 'high', has_attachments = true WHERE id = $1`, open)
	label := f.queryID(`INSERT INTO labels (name, color_token) VALUES ('Factuur', 'blue') RETURNING id`)
	f.exec(`INSERT INTO conversation_labels (conversation_id, label_id) VALUES ($1, $2)`, open, label)

	closed := f.conversation("Gesloten klacht", convOpt{mailbox: f.mailboxA, status: "closed", lastMessageAt: old})
	f.searchMessage(closed, f.mailboxA, "Gesloten klacht", "Wanneer komt de zending?", searchMsg{fromAddr: "piet@ander.nl", to: "support@alpha.example", at: old})

	snoozed := f.conversation("Uitgestelde vraag", convOpt{mailbox: f.mailboxA, lastMessageAt: now.Add(-time.Minute)})
	f.searchMessage(snoozed, f.mailboxA, "Uitgestelde vraag", "Wanneer komt de zending?", searchMsg{fromAddr: "anna@bedrijf.nl"})
	f.exec(`UPDATE conversations SET snoozed_until = now() + interval '1 day' WHERE id = $1`, snoozed)

	tests := []struct {
		name, q, want string
	}{
		{"text only", "zending", "Open factuurvraag,Uitgestelde vraag,Gesloten klacht"},
		{"status open excludes snoozed", "zending status:open", "Open factuurvraag"},
		{"status dutch", "zending status:gesloten", "Gesloten klacht"},
		{"status snoozed", "zending status:uitgesteld", "Uitgestelde vraag"},
		{"from domain", "zending van:@bedrijf.nl", "Open factuurvraag,Uitgestelde vraag"},
		{"from address english", "zending from:piet@ander.nl", "Gesloten klacht"},
		{"from address part", "zending van:jan", "Open factuurvraag"},
		{"to", "zending aan:support@alpha.example", "Gesloten klacht"},
		{"label", "zending label:factuur", "Open factuurvraag"},
		{"unknown label matches nothing", "zending label:bestaatniet", ""},
		{"assignee me", "zending toegewezen:me", "Open factuurvraag"},
		{"assignee none", "zending assignee:none", "Uitgestelde vraag,Gesloten klacht"},
		{"assignee name prefix", "zending toegewezen:Test", "Open factuurvraag"},
		{"team", "zending team:support", "Open factuurvraag"},
		{"priority", "zending prioriteit:hoog", "Open factuurvraag"},
		{"attachment", "zending heeft:bijlage", "Open factuurvraag"},
		{"before", "zending voor:" + now.AddDate(0, 0, -10).Format(time.DateOnly), "Gesloten klacht"},
		{"after", "zending na:" + now.AddDate(0, 0, -10).Format(time.DateOnly), "Open factuurvraag,Uitgestelde vraag"},
		{"mailbox by name", "zending mailbox:alpha", "Open factuurvraag,Uitgestelde vraag,Gesloten klacht"},
		{"unknown mailbox", "zending mailbox:nergens", ""},
		{"filters only", "status:gesloten", "Gesloten klacht"},
		{"invalid filter is text", "zending status:bogus", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.search(f.agent, tc.q).subjects(); got != tc.want {
				t.Errorf("search %q = %q, want %q", tc.q, got, tc.want)
			}
		})
	}
}

func TestSearchMatchesSubjectAndContact(t *testing.T) {
	f := newInboxFixture(t)
	contact := f.queryID(`INSERT INTO contacts (name) VALUES ('Marieke Vermeulen') RETURNING id`)
	f.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'marieke@vermeulen-bv.nl', true)`, contact)
	byContact := f.conversation("Iets heel anders", convOpt{mailbox: f.mailboxA, contact: contact})
	f.searchMessage(byContact, f.mailboxA, "Iets heel anders", "geen relevante tekst", searchMsg{})
	// No message text matches, only the subject does.
	f.conversation("Verlengingsaanvraag Zwaluwstraat", convOpt{mailbox: f.mailboxA})

	if got := f.search(f.agent, "Vermeulen").subjects(); got != "Iets heel anders" {
		t.Errorf("contact name search = %q", got)
	}
	if got := f.search(f.agent, "vermeulen-bv").subjects(); got != "Iets heel anders" {
		t.Errorf("contact address search = %q", got)
	}
	got := f.search(f.agent, "Zwaluwstraat")
	if got.subjects() != "Verlengingsaanvraag Zwaluwstraat" || got.Results[0].Snippet != "" {
		t.Errorf("subject search = %+v", got)
	}
}

func TestSearchSnippetAndStemming(t *testing.T) {
	f := newInboxFixture(t)
	c := f.conversation("Vraag", convOpt{mailbox: f.mailboxA})
	f.searchMessage(c, f.mailboxA, "Vraag", "Mijn bestelling ⟦nep⟧ is nog niet aangekomen, wilt u dat controleren?", searchMsg{})

	got := f.search(f.agent, "bestellingen")
	if len(got.Results) != 1 {
		t.Fatalf("stemmed search found %d results", len(got.Results))
	}
	snippet := got.Results[0].Snippet
	if !strings.Contains(snippet, "⟦bestelling⟧") {
		t.Errorf("snippet %q does not mark the match", snippet)
	}
	if strings.Contains(snippet, "⟦nep⟧") {
		t.Errorf("snippet %q kept marker characters from the message", snippet)
	}
	if got := f.search(f.agent, `"niet aangekomen"`); len(got.Results) != 1 {
		t.Errorf("phrase search found %d results", len(got.Results))
	}
	if got := f.search(f.agent, `"aangekomen niet"`); len(got.Results) != 0 {
		t.Errorf("reversed phrase found %d results", len(got.Results))
	}
}

func TestSearchIgnoresDeletedConversationsAndMessages(t *testing.T) {
	f := newInboxFixture(t)
	c := f.conversation("Verwijderd", convOpt{mailbox: f.mailboxA})
	f.searchMessage(c, f.mailboxA, "Verwijderd", "unieketermxyz", searchMsg{})
	f.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, c)
	if got := f.search(f.agent, "unieketermxyz"); len(got.Results) != 0 {
		t.Errorf("deleted conversation found: %s", got.subjects())
	}
}
