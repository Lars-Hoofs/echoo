package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/contacts"
	"echoo/internal/jobs"
	"echoo/internal/mail/ingest"
	"echoo/internal/storage"
)

type contactFixture struct {
	*inboxFixture
	store    *storage.FS
	adminID  string
	customer map[string]string // name -> contact id
}

func newContactFixture(t *testing.T) *contactFixture {
	t.Helper()
	f := &contactFixture{inboxFixture: newInboxFixture(t), customer: map[string]string{}}
	rc, err := river.NewClient(riverpgxv5.New(f.h.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.h.srv.jobs = rc
	if f.store, err = storage.NewFS(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	f.h.srv.store = f.store
	f.adminID = f.queryID(`SELECT id FROM users WHERE role = 'admin'`)
	return f
}

// contactIn creates a contact with one address and one open conversation in mailbox.
func (f *contactFixture) contactIn(name, email, mailbox string) string {
	id := f.queryID(`INSERT INTO contacts (name) VALUES ($1) RETURNING id`, name)
	f.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, $2, true)`, id, email)
	if mailbox != "" {
		f.conversation("Vraag van "+name, convOpt{mailbox: mailbox, contact: id})
	}
	f.customer[name] = id
	return id
}

func contactNames(r response) []string {
	var out struct {
		Contacts []struct{ Name string } `json:"contacts"`
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		panic(err)
	}
	names := make([]string, len(out.Contacts))
	for i, c := range out.Contacts {
		names[i] = c.Name
	}
	slices.Sort(names)
	return names
}

func (f *contactFixture) multipart(c *client, path string, parts map[string]string, file []byte) response {
	f.h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if file != nil {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="file"; filename="contacten.csv"`)
		hdr.Set("Content-Type", "text/csv")
		part, err := mw.CreatePart(hdr)
		if err != nil {
			f.h.t.Fatal(err)
		}
		if _, err := part.Write(file); err != nil {
			f.h.t.Fatal(err)
		}
	}
	for name, value := range parts {
		if err := mw.WriteField(name, value); err != nil {
			f.h.t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		f.h.t.Fatal(err)
	}
	req, err := http.NewRequest("POST", f.h.ts.URL+path, &buf)
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
	if len(raw) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			f.h.t.Fatalf("decode: %v: %s", err, raw)
		}
	}
	return out
}

func idOf(t *testing.T, r response, key string) string {
	t.Helper()
	obj, ok := r.body[key].(map[string]any)
	if !ok {
		t.Fatalf("no %s in %s", key, r.raw)
	}
	id, _ := obj["id"].(string)
	return id
}

func TestContactsAreScopedToReadableMailboxes(t *testing.T) {
	f := newContactFixture(t)
	visible := f.contactIn("Zichtbaar", "zicht@customer.nl", f.mailboxA)
	hidden := f.contactIn("Verborgen", "verborgen@customer.nl", f.mailboxB)
	both := f.contactIn("Beide", "beide@customer.nl", f.mailboxA)
	f.conversation("Tweede vraag", convOpt{mailbox: f.mailboxB, contact: both})
	f.contactIn("Zonder gesprek", "leeg@customer.nl", "")
	org := f.queryID(`INSERT INTO organizations (name, domains) VALUES ('Alleen B', '{alleen-b.nl}') RETURNING id`)
	f.exec(`UPDATE contacts SET organization_id = $1 WHERE id = $2`, org, hidden)
	f.exec(`INSERT INTO crm_notes (contact_id, body) VALUES ($1, 'geheim voor B')`, hidden)

	t.Run("agent sees only readable contacts", func(t *testing.T) {
		if got := contactNames(f.agent.do("GET", "/api/v1/contacts", nil)); !slices.Equal(got, []string{"Beide", "Zichtbaar"}) {
			t.Fatalf("agent list = %q", got)
		}
		if got := contactNames(f.admin.do("GET", "/api/v1/contacts", nil)); len(got) != 4 {
			t.Fatalf("admin list = %q", got)
		}
	})
	t.Run("search cannot reveal a hidden contact", func(t *testing.T) {
		for _, q := range []string{"Verborgen", "verborgen@customer", "alleen-b", "Alleen B"} {
			if got := contactNames(f.agent.do("GET", "/api/v1/contacts?q="+url.QueryEscape(q), nil)); len(got) != 0 {
				t.Errorf("search %q leaks %q", q, got)
			}
		}
		filter := `{"conditions":[{"field":"name","op":"contains","value":"Verborgen"}]}`
		if got := contactNames(f.agent.do("GET", "/api/v1/contacts?filter="+url.QueryEscape(filter), nil)); len(got) != 0 {
			t.Errorf("filter leaks %q", got)
		}
	})
	t.Run("every contact endpoint answers 404 for a hidden contact", func(t *testing.T) {
		for _, req := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/v1/contacts/" + hidden, nil},
			{"PATCH", "/api/v1/contacts/" + hidden, map[string]any{"name": "x"}},
			{"GET", "/api/v1/contacts/" + hidden + "/conversations", nil},
			{"GET", "/api/v1/contacts/" + hidden + "/timeline", nil},
			{"GET", "/api/v1/contacts/" + hidden + "/notes", nil},
			{"POST", "/api/v1/contacts/" + hidden + "/notes", map[string]any{"body": "x"}},
			{"POST", "/api/v1/contacts/" + hidden + "/merge", map[string]any{"source_id": visible}},
			{"POST", "/api/v1/contacts/" + visible + "/merge", map[string]any{"source_id": hidden}},
			{"GET", "/api/v1/organizations/" + org, nil},
			{"PATCH", "/api/v1/organizations/" + org, map[string]any{"name": "x"}},
			{"GET", "/api/v1/organizations/" + org + "/notes", nil},
			{"GET", "/api/v1/organizations/" + org + "/conversations", nil},
		} {
			expect(t, f.agent.do(req.method, req.path, req.body), 404, "not_found")
		}
		if got := f.agent.do("GET", "/api/v1/organizations", nil); strings.Contains(string(got.raw), "Alleen B") {
			t.Errorf("organization list leaks: %s", got.raw)
		}
	})
	t.Run("counts and conversations only cover readable mailboxes", func(t *testing.T) {
		r := f.agent.do("GET", "/api/v1/contacts/"+both, nil)
		expect(t, r, 200, "")
		if n := r.body["contact"].(map[string]any)["conversation_count"]; n != 1.0 {
			t.Errorf("agent conversation_count = %v", n)
		}
		convs := f.agent.do("GET", "/api/v1/contacts/"+both+"/conversations", nil)
		expect(t, convs, 200, "")
		if got := len(convs.body["conversations"].([]any)); got != 1 {
			t.Errorf("agent sees %d conversations of Beide, want 1", got)
		}
		if n := f.admin.do("GET", "/api/v1/contacts/"+both, nil).body["contact"].(map[string]any)["conversation_count"]; n != 2.0 {
			t.Errorf("admin conversation_count = %v", n)
		}
	})
	t.Run("export contains only visible contacts", func(t *testing.T) {
		r := f.agent.do("GET", "/api/v1/contacts/export", nil)
		expect(t, r, 200, "")
		out := string(r.raw)
		if !strings.Contains(out, "Zichtbaar") || strings.Contains(out, "Verborgen") || strings.Contains(out, "alleen-b") {
			t.Errorf("export = %s", out)
		}
	})
	t.Run("a contact an agent created is theirs until it has conversations", func(t *testing.T) {
		r := f.agent.do("POST", "/api/v1/contacts", map[string]any{"name": "Nieuw", "emails": []map[string]any{{"email": "nieuw@customer.nl"}}})
		expect(t, r, 201, "")
		id := idOf(t, r, "contact")
		if got := contactNames(f.agent.do("GET", "/api/v1/contacts", nil)); !slices.Contains(got, "Nieuw") {
			t.Errorf("creator does not see own contact: %q", got)
		}
		other, _ := f.h.loggedIn("agent")
		expect(t, other.do("GET", "/api/v1/contacts/"+id, nil), 404, "not_found")
		expect(t, f.admin.do("GET", "/api/v1/contacts/"+id, nil), 200, "")
	})
	t.Run("readonly users can read but not change", func(t *testing.T) {
		expect(t, f.readonly.do("GET", "/api/v1/contacts/"+visible, nil), 200, "")
		expect(t, f.readonly.do("PATCH", "/api/v1/contacts/"+visible, map[string]any{"name": "x"}), 403, "forbidden")
		expect(t, f.readonly.do("POST", "/api/v1/contacts", map[string]any{"emails": []map[string]any{{"email": "r@customer.nl"}}}), 403, "forbidden")
		expect(t, f.readonly.do("POST", "/api/v1/contacts/"+visible+"/notes", map[string]any{"body": "x"}), 403, "forbidden")
		expect(t, f.readonly.do("POST", "/api/v1/contacts/"+visible+"/merge", map[string]any{"source_id": both}), 403, "forbidden")
		expect(t, f.readonly.do("POST", "/api/v1/organizations", map[string]any{"name": "x"}), 403, "forbidden")
		expect(t, f.readonly.do("POST", "/api/v1/contact-segments", map[string]any{"name": "x", "filter": map[string]any{"conditions": []any{}}}), 403, "forbidden")
	})
}

func TestContactCreateAndEdit(t *testing.T) {
	f := newContactFixture(t)
	org := f.queryID(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id`)
	f.exec(`INSERT INTO custom_attribute_defs (entity, key, label, type, options) VALUES
		('contact', 'tier', 'Niveau', 'list', '{gold,silver}'), ('contact', 'seats', 'Plekken', 'number', '{}')`)

	created := f.admin.do("POST", "/api/v1/contacts", map[string]any{
		"name": "Anna", "phone": "0612345678", "organization_id": org,
		"emails":            []map[string]any{{"email": "Anna@Acme.nl"}, {"email": "anna@home.example"}},
		"custom_attributes": map[string]any{"tier": "gold", "seats": 12},
	})
	expect(t, created, 201, "")
	id := idOf(t, created, "contact")
	c := created.body["contact"].(map[string]any)
	if emails := c["emails"].([]any); len(emails) != 2 || emails[0].(map[string]any)["email"] != "anna@acme.nl" || emails[0].(map[string]any)["primary"] != true {
		t.Errorf("emails = %v (lower-cased, first is primary)", emails)
	}
	if c["organization"].(map[string]any)["name"] != "Acme" || c["custom_attributes"].(map[string]any)["seats"] != 12.0 {
		t.Errorf("contact = %v", c)
	}
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.created' AND target_id = $1`, id) != 1 {
		t.Error("creation was not audited")
	}

	t.Run("validation", func(t *testing.T) {
		email := func(e string) []map[string]any { return []map[string]any{{"email": e}} }
		for name, body := range map[string]map[string]any{
			"no email":             {"name": "x"},
			"invalid email":        {"emails": email("nope")},
			"duplicate email":      {"emails": []map[string]any{{"email": "a@b.nl"}, {"email": "A@B.nl"}}},
			"two primaries":        {"emails": []map[string]any{{"email": "a@b.nl", "primary": true}, {"email": "c@d.nl", "primary": true}}},
			"unknown attribute":    {"emails": email("a@b.nl"), "custom_attributes": map[string]any{"nope": "x"}},
			"attribute wrong type": {"emails": email("a@b.nl"), "custom_attributes": map[string]any{"seats": "veel"}},
			"attribute not option": {"emails": email("a@b.nl"), "custom_attributes": map[string]any{"tier": "bronze"}},
			"unknown organization": {"emails": email("a@b.nl"), "organization_id": "0199a000-0000-7000-8000-000000000000"},
			"name too long":        {"emails": email("a@b.nl"), "name": strings.Repeat("x", 201)},
		} {
			if r := f.admin.do("POST", "/api/v1/contacts", body); r.status != 422 || r.errCode() != "validation_failed" {
				t.Errorf("%s: got %d %s", name, r.status, r.raw)
			}
		}
		expect(t, f.admin.do("POST", "/api/v1/contacts", map[string]any{"emails": email("anna@acme.nl")}), 409, "email_in_use")
		expect(t, f.admin.do("POST", "/api/v1/contacts", map[string]any{"emails": email("a@b.nl"), "unknown_field": 1}), 400, "invalid_request")
	})

	t.Run("edit fields, addresses, primary and attributes", func(t *testing.T) {
		r := f.admin.do("PATCH", "/api/v1/contacts/"+id, map[string]any{
			"name": "Anna de Vries", "organization_id": nil,
			"emails":            []map[string]any{{"email": "anna@home.example", "primary": true}, {"email": "a.devries@acme.nl"}},
			"custom_attributes": map[string]any{"tier": nil, "seats": 20},
		})
		expect(t, r, 200, "")
		c := r.body["contact"].(map[string]any)
		emails := c["emails"].([]any)
		if len(emails) != 2 || emails[0].(map[string]any)["email"] != "anna@home.example" || emails[0].(map[string]any)["primary"] != true {
			t.Errorf("emails = %v", emails)
		}
		if c["name"] != "Anna de Vries" || c["organization"] != nil || c["phone"] != "0612345678" {
			t.Errorf("contact = %v", c)
		}
		attrs := c["custom_attributes"].(map[string]any)
		if _, has := attrs["tier"]; has || attrs["seats"] != 20.0 {
			t.Errorf("attributes = %v", attrs)
		}
		if f.h.count(`SELECT count(*) FROM contact_addresses WHERE email = 'anna@acme.nl'`) != 0 {
			t.Error("removed address still exists")
		}
	})
	t.Run("addresses of another contact cannot be taken", func(t *testing.T) {
		f.contactIn("Bram", "bram@customer.nl", "")
		expect(t, f.admin.do("PATCH", "/api/v1/contacts/"+id, map[string]any{"emails": []map[string]any{{"email": "bram@customer.nl"}}}), 409, "email_in_use")
		expect(t, f.admin.do("PATCH", "/api/v1/contacts/"+id, map[string]any{"emails": []map[string]any{}}), 422, "validation_failed")
		if f.h.count(`SELECT count(*) FROM contact_addresses WHERE contact_id = $1`, id) != 2 {
			t.Error("a failed edit changed the addresses")
		}
	})
	t.Run("attribute validation on edit leaves the contact untouched", func(t *testing.T) {
		expect(t, f.admin.do("PATCH", "/api/v1/contacts/"+id, map[string]any{"name": "Nieuw", "custom_attributes": map[string]any{"seats": "abc"}}), 422, "validation_failed")
		if got := f.admin.do("GET", "/api/v1/contacts/"+id, nil).body["contact"].(map[string]any)["name"]; got != "Anna de Vries" {
			t.Errorf("name = %v", got)
		}
	})
}

func TestContactListFiltersSortsAndPages(t *testing.T) {
	f := newContactFixture(t)
	for _, n := range []string{"Ada", "Bea", "Cor", "Dirk", "Eva"} {
		f.contactIn(n, strings.ToLower(n)+"@acme.nl", f.mailboxA)
	}
	r := f.agent.do("GET", "/api/v1/contacts?sort=name&limit=2", nil)
	expect(t, r, 200, "")
	seen := []string{}
	for range 5 {
		var page struct {
			Contacts   []struct{ Name string }
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(r.raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Contacts {
			seen = append(seen, c.Name)
		}
		if page.NextCursor == nil {
			break
		}
		r = f.agent.do("GET", "/api/v1/contacts?sort=name&limit=2&cursor="+url.QueryEscape(*page.NextCursor), nil)
		expect(t, r, 200, "")
	}
	if !slices.Equal(seen, []string{"Ada", "Bea", "Cor", "Dirk", "Eva"}) {
		t.Fatalf("paged names = %v", seen)
	}
	for _, bad := range []string{"sort=email", "dir=up", "limit=0", "limit=101", "cursor=abc", "organization_id=x", "segment_id=x", "filter=%7B"} {
		if r := f.agent.do("GET", "/api/v1/contacts?"+bad, nil); r.status != 400 && r.status != 422 {
			t.Errorf("%s: status %d", bad, r.status)
		}
	}
	filter := url.QueryEscape(`{"conditions":[{"field":"domain","op":"equals","value":"acme.nl"}]}`)
	seg := f.agent.do("POST", "/api/v1/contact-segments", map[string]any{"name": "Acme", "filter": json.RawMessage(`{"match":"all","conditions":[{"field":"name","op":"starts_with","value":"d"}]}`)})
	expect(t, seg, 201, "")
	if got := contactNames(f.agent.do("GET", "/api/v1/contacts?segment_id="+idOf(t, seg, "segment"), nil)); !slices.Equal(got, []string{"Dirk"}) {
		t.Errorf("segment result = %q", got)
	}
	expect(t, f.agent.do("GET", "/api/v1/contacts?segment_id="+idOf(t, seg, "segment")+"&filter="+filter, nil), 400, "invalid_request")
	if got := contactNames(f.agent.do("GET", "/api/v1/contacts?filter="+filter, nil)); len(got) != 5 {
		t.Errorf("filter result = %q", got)
	}
}

func TestContactNotes(t *testing.T) {
	f := newContactFixture(t)
	id := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	other, otherUser := f.h.loggedIn("agent")
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, f.mailboxA, f.otherTeam)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.otherTeam, otherUser.ID)

	created := f.agent.do("POST", "/api/v1/contacts/"+id+"/notes", map[string]any{"body": "Belt woensdag terug\nmet vraag over factuur"})
	expect(t, created, 201, "")
	note := created.body["note"].(map[string]any)
	noteID := note["id"].(string)
	if note["can_edit"] != true || note["author"].(map[string]any)["id"] != f.agentID {
		t.Errorf("note = %v", note)
	}
	expect(t, f.agent.do("POST", "/api/v1/contacts/"+id+"/notes", map[string]any{"body": "  "}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/contacts/"+id+"/notes", map[string]any{"body": strings.Repeat("x", 10001)}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/contacts/"+id+"/notes", map[string]any{"body": "a\x00b"}), 422, "validation_failed")

	listed := f.readonly.do("GET", "/api/v1/contacts/"+id+"/notes", nil)
	expect(t, listed, 200, "")
	if notes := listed.body["notes"].([]any); len(notes) != 1 || notes[0].(map[string]any)["can_edit"] != false {
		t.Errorf("readonly sees %v", listed.body["notes"])
	}
	path := "/api/v1/contacts/" + id + "/notes/" + noteID
	// Another agent in the same mailbox may read but not edit or delete someone else's note.
	expect(t, other.do("GET", "/api/v1/contacts/"+id+"/notes", nil), 200, "")
	expect(t, other.do("PATCH", path, map[string]any{"body": "overschreven"}), 403, "forbidden")
	expect(t, other.do("DELETE", path, nil), 403, "forbidden")
	expect(t, f.agent.do("PATCH", path, map[string]any{"body": "Gewijzigd"}), 200, "")
	expect(t, f.admin.do("PATCH", path, map[string]any{"body": "Door beheerder"}), 200, "")
	expect(t, f.agent.do("PATCH", "/api/v1/contacts/"+id+"/notes/0199a000-0000-7000-8000-000000000000", map[string]any{"body": "x"}), 404, "not_found")
	// A note is only reachable through its own contact.
	otherContact := f.contactIn("Bram", "bram@customer.nl", f.mailboxA)
	expect(t, f.agent.do("PATCH", "/api/v1/contacts/"+otherContact+"/notes/"+noteID, map[string]any{"body": "x"}), 404, "not_found")
	expect(t, f.agent.do("DELETE", path, nil), 204, "")
	if f.h.count(`SELECT count(*) FROM crm_notes`) != 0 {
		t.Error("note not deleted")
	}

	t.Run("notes show up in the timeline", func(t *testing.T) {
		expect(t, f.agent.do("POST", "/api/v1/contacts/"+id+"/notes", map[string]any{"body": "In de tijdlijn"}), 201, "")
		r := f.agent.do("GET", "/api/v1/contacts/"+id+"/timeline", nil)
		expect(t, r, 200, "")
		if !strings.Contains(string(r.raw), "In de tijdlijn") || !strings.Contains(string(r.raw), `"kind":"note"`) {
			t.Errorf("timeline = %s", r.raw)
		}
	})
}

func TestOrganizationsDomainsAndNotes(t *testing.T) {
	f := newContactFixture(t)
	created := f.agent.do("POST", "/api/v1/organizations", map[string]any{"name": "Acme BV", "domains": []string{"Acme.nl", "acme.com"}})
	expect(t, created, 201, "")
	id := idOf(t, created, "organization")
	if d := created.body["organization"].(map[string]any)["domains"].([]any); d[0] != "acme.nl" {
		t.Errorf("domains = %v", d)
	}
	expect(t, f.agent.do("POST", "/api/v1/organizations", map[string]any{"name": "Dubbel", "domains": []string{"acme.nl"}}), 409, "domain_in_use")
	for _, bad := range [][]string{{"gmail.com"}, {"not a domain"}, {"acme.nl", "ACME.nl"}, {"localhost"}} {
		expect(t, f.agent.do("POST", "/api/v1/organizations", map[string]any{"name": "X", "domains": bad}), 422, "validation_failed")
	}
	expect(t, f.agent.do("POST", "/api/v1/organizations", map[string]any{"name": ""}), 422, "validation_failed")
	expect(t, f.agent.do("PATCH", "/api/v1/organizations/"+id, map[string]any{"name": "Acme Group", "domains": []string{"acme.nl"}}), 200, "")

	member := f.contactIn("Anna", "anna@acme.nl", f.mailboxA)
	f.exec(`UPDATE contacts SET organization_id = $1 WHERE id = $2`, id, member)
	other := f.contactIn("Bram", "bram@acme.nl", f.mailboxB)
	f.exec(`UPDATE contacts SET organization_id = $1 WHERE id = $2`, id, other)
	r := f.agent.do("GET", "/api/v1/organizations/"+id, nil)
	expect(t, r, 200, "")
	o := r.body["organization"].(map[string]any)
	if o["contact_count"] != 1.0 || len(o["contacts"].([]any)) != 1 {
		t.Errorf("agent sees %v contacts of the organization, want only the readable one", o["contact_count"])
	}
	if f.admin.do("GET", "/api/v1/organizations/"+id, nil).body["organization"].(map[string]any)["contact_count"] != 2.0 {
		t.Error("admin count")
	}
	convs := f.agent.do("GET", "/api/v1/organizations/"+id+"/conversations", nil)
	expect(t, convs, 200, "")
	if len(convs.body["conversations"].([]any)) != 1 {
		t.Errorf("organization conversations = %s", convs.raw)
	}
	expect(t, f.agent.do("POST", "/api/v1/organizations/"+id+"/notes", map[string]any{"body": "Sleutelklant"}), 201, "")
	if !strings.Contains(string(f.agent.do("GET", "/api/v1/organizations/"+id+"/notes", nil).raw), "Sleutelklant") {
		t.Error("organization note missing")
	}
	search := f.agent.do("GET", "/api/v1/organizations?q=group", nil)
	if !strings.Contains(string(search.raw), "Acme Group") {
		t.Errorf("search = %s", search.raw)
	}
}

func TestCustomAttributeDefinitions(t *testing.T) {
	f := newContactFixture(t)
	def := func(body map[string]any) response { return f.admin.do("POST", "/api/v1/custom-attributes", body) }

	created := def(map[string]any{"entity": "contact", "key": "tier", "label": "Niveau", "type": "list", "options": []string{"gold", "silver"}})
	expect(t, created, 201, "")
	id := idOf(t, created, "attribute")
	for name, body := range map[string]map[string]any{
		"bad entity":            {"entity": "user", "key": "a", "label": "A", "type": "text"},
		"key with caps":         {"entity": "contact", "key": "Tier", "label": "A", "type": "text"},
		"key with dash":         {"entity": "contact", "key": "a-b", "label": "A", "type": "text"},
		"key too long":          {"entity": "contact", "key": strings.Repeat("a", 41), "label": "A", "type": "text"},
		"key starts with digit": {"entity": "contact", "key": "1a", "label": "A", "type": "text"},
		"no label":              {"entity": "contact", "key": "a", "label": " ", "type": "text"},
		"bad type":              {"entity": "contact", "key": "a", "label": "A", "type": "file"},
		"list w/o options":      {"entity": "contact", "key": "a", "label": "A", "type": "list"},
		"options on text":       {"entity": "contact", "key": "a", "label": "A", "type": "text", "options": []string{"x"}},
	} {
		if r := def(body); r.status != 422 {
			t.Errorf("%s: status %d", name, r.status)
		}
	}
	if r := def(map[string]any{"entity": "contact", "key": "tier", "label": "Nog een", "type": "text"}); r.status != 422 || !strings.Contains(string(r.raw), "taken") {
		t.Errorf("duplicate key: %d %s", r.status, r.raw)
	}
	expect(t, def(map[string]any{"entity": "organization", "key": "tier", "label": "Zelfde key, andere entiteit", "type": "text"}), 201, "")

	agentList := f.agent.do("GET", "/api/v1/custom-attributes?entity=contact", nil)
	expect(t, agentList, 200, "")
	if n := len(agentList.body["attributes"].([]any)); n != 1 {
		t.Errorf("entity filter returned %d", n)
	}
	expect(t, f.agent.do("GET", "/api/v1/custom-attributes?entity=nope", nil), 400, "invalid_request")

	expect(t, f.admin.do("PATCH", "/api/v1/custom-attributes/"+id, map[string]any{"type": "text"}), 422, "validation_failed")
	expect(t, f.admin.do("PATCH", "/api/v1/custom-attributes/"+id, map[string]any{"label": "Klantniveau", "options": []string{"gold", "silver", "bronze"}}), 200, "")

	contact := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	expect(t, f.agent.do("PATCH", "/api/v1/contacts/"+contact, map[string]any{"custom_attributes": map[string]any{"tier": "bronze"}}), 200, "")
	expect(t, f.admin.do("DELETE", "/api/v1/custom-attributes/"+id, nil), 204, "")
	got := f.agent.do("GET", "/api/v1/contacts/"+contact, nil).body["contact"].(map[string]any)["custom_attributes"].(map[string]any)
	if len(got) != 0 {
		t.Errorf("values of a deleted definition remain: %v", got)
	}
	expect(t, f.agent.do("PATCH", "/api/v1/contacts/"+contact, map[string]any{"custom_attributes": map[string]any{"tier": "gold"}}), 422, "validation_failed")
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action IN ('custom_attribute.created', 'custom_attribute.updated', 'custom_attribute.deleted')`) != 4 {
		t.Error("definition changes are not audited")
	}
}

func TestConversationAttributes(t *testing.T) {
	f := newContactFixture(t)
	f.exec(`INSERT INTO custom_attribute_defs (entity, key, label, type, options) VALUES
		('conversation', 'kanaal', 'Kanaal', 'list', '{telefoon,mail}'), ('conversation', 'ticket', 'Ticket', 'text', '{}')`)
	inA := f.conversation("In A", convOpt{mailbox: f.mailboxA})
	inB := f.conversation("In B", convOpt{mailbox: f.mailboxB})

	r := f.agent.do("PATCH", "/api/v1/conversations/"+inA+"/attributes", map[string]any{"attributes": map[string]any{"kanaal": "telefoon", "ticket": "T-1"}})
	expect(t, r, 200, "")
	if a := r.body["attributes"].(map[string]any); a["kanaal"] != "telefoon" || a["ticket"] != "T-1" {
		t.Errorf("attributes = %v", a)
	}
	expect(t, f.agent.do("PATCH", "/api/v1/conversations/"+inA+"/attributes", map[string]any{"attributes": map[string]any{"ticket": nil}}), 200, "")
	got := f.agent.do("GET", "/api/v1/conversations/"+inA+"/attributes", nil)
	expect(t, got, 200, "")
	if a := got.body["attributes"].(map[string]any); len(a) != 1 || a["kanaal"] != "telefoon" {
		t.Errorf("after removal: %v", a)
	}
	expect(t, f.agent.do("PATCH", "/api/v1/conversations/"+inA+"/attributes", map[string]any{"attributes": map[string]any{"kanaal": "fax"}}), 422, "validation_failed")
	expect(t, f.agent.do("PATCH", "/api/v1/conversations/"+inA+"/attributes", map[string]any{"attributes": map[string]any{"onbekend": "x"}}), 422, "validation_failed")
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+inB+"/attributes", nil), 404, "not_found")
	expect(t, f.agent.do("PATCH", "/api/v1/conversations/"+inB+"/attributes", map[string]any{"attributes": map[string]any{"ticket": "x"}}), 404, "not_found")
	expect(t, f.readonly.do("PATCH", "/api/v1/conversations/"+inA+"/attributes", map[string]any{"attributes": map[string]any{"ticket": "x"}}), 403, "forbidden")
	expect(t, f.readonly.do("GET", "/api/v1/conversations/"+inA+"/attributes", nil), 200, "")

	contact := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	f.exec(`UPDATE conversations SET contact_id = $1 WHERE id = $2`, contact, inA)
	filter := `{"conditions":[{"field":"conversation_attribute","key":"kanaal","op":"equals","value":"telefoon"}]}`
	if got := contactNames(f.agent.do("GET", "/api/v1/contacts?filter="+url.QueryEscape(filter), nil)); !slices.Equal(got, []string{"Anna"}) {
		t.Errorf("segment on a conversation attribute = %q", got)
	}
}

func TestSegmentsArePersonalOrShared(t *testing.T) {
	f := newContactFixture(t)
	filter := map[string]any{"match": "all", "conditions": []map[string]any{{"field": "name", "op": "contains", "value": "a"}}}
	personal := f.agent.do("POST", "/api/v1/contact-segments", map[string]any{"name": "Persoonlijk", "filter": filter})
	expect(t, personal, 201, "")
	shared := f.agent.do("POST", "/api/v1/contact-segments", map[string]any{"name": "Gedeeld", "shared": true, "filter": filter})
	expect(t, shared, 201, "")
	personalID, sharedID := idOf(t, personal, "segment"), idOf(t, shared, "segment")

	names := func(c *client) []string {
		r := c.do("GET", "/api/v1/contact-segments", nil)
		expect(t, r, 200, "")
		var out []string
		for _, s := range r.body["segments"].([]any) {
			out = append(out, s.(map[string]any)["name"].(string))
		}
		return out
	}
	if got := names(f.agent); !slices.Equal(got, []string{"Gedeeld", "Persoonlijk"}) {
		t.Errorf("owner sees %q", got)
	}
	other, _ := f.h.loggedIn("agent")
	if got := names(other); !slices.Equal(got, []string{"Gedeeld"}) {
		t.Errorf("another agent sees %q", got)
	}
	if got := names(f.admin); !slices.Equal(got, []string{"Gedeeld"}) {
		t.Errorf("an admin does not see personal segments of others: %q", got)
	}
	expect(t, other.do("PATCH", "/api/v1/contact-segments/"+personalID, map[string]any{"name": "Gekaapt"}), 404, "not_found")
	expect(t, other.do("PATCH", "/api/v1/contact-segments/"+sharedID, map[string]any{"name": "Gekaapt"}), 403, "forbidden")
	expect(t, other.do("DELETE", "/api/v1/contact-segments/"+sharedID, nil), 403, "forbidden")
	expect(t, other.do("GET", "/api/v1/contacts?segment_id="+personalID, nil), 404, "not_found")
	expect(t, other.do("GET", "/api/v1/contacts?segment_id="+sharedID, nil), 200, "")
	expect(t, f.admin.do("PATCH", "/api/v1/contact-segments/"+sharedID, map[string]any{"name": "Beheerd"}), 200, "")
	expect(t, f.agent.do("POST", "/api/v1/contact-segments", map[string]any{"name": "persoonlijk", "filter": filter}), 422, "validation_failed")
	for name, body := range map[string]map[string]any{
		"no name":      {"filter": filter},
		"no filter":    {"name": "x"},
		"bad field":    {"name": "x", "filter": map[string]any{"conditions": []map[string]any{{"field": "password", "op": "equals", "value": "x"}}}},
		"unknown attr": {"name": "x", "filter": map[string]any{"conditions": []map[string]any{{"field": "attribute", "key": "nope", "op": "is_set"}}}},
	} {
		if r := f.agent.do("POST", "/api/v1/contact-segments", body); r.status != 422 {
			t.Errorf("%s: status %d", name, r.status)
		}
	}
	expect(t, f.agent.do("DELETE", "/api/v1/contact-segments/"+personalID, nil), 204, "")
}

func TestMergeOverHTTP(t *testing.T) {
	f := newContactFixture(t)
	primary := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	secondary := f.contactIn("A. de Vries", "adv@home.example", f.mailboxA)
	f.exec(`INSERT INTO crm_notes (contact_id, body) VALUES ($1, 'oude notitie')`, secondary)

	expect(t, f.agent.do("POST", "/api/v1/contacts/"+primary+"/merge", map[string]any{"source_id": primary}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/contacts/"+primary+"/merge", map[string]any{"source_id": "nope"}), 422, "validation_failed")
	r := f.agent.do("POST", "/api/v1/contacts/"+primary+"/merge", map[string]any{"source_id": secondary})
	expect(t, r, 200, "")
	c := r.body["contact"].(map[string]any)
	if len(c["emails"].([]any)) != 2 || c["conversation_count"] != 2.0 {
		t.Errorf("merged contact = %v", c)
	}
	merged := r.body["merged"].(map[string]any)
	if merged["addresses"] != 1.0 || merged["conversations"] != 1.0 || merged["notes"] != 1.0 {
		t.Errorf("merged = %v", merged)
	}
	expect(t, f.agent.do("GET", "/api/v1/contacts/"+secondary, nil), 404, "not_found")
	if !strings.Contains(string(f.agent.do("GET", "/api/v1/contacts/"+primary+"/timeline", nil).raw), "contact_merged") {
		t.Error("the merge is missing from the timeline")
	}
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.merged' AND target_id = $1`, primary) != 1 {
		t.Error("merge not audited")
	}
}

func TestContactExportCSV(t *testing.T) {
	f := newContactFixture(t)
	f.exec(`INSERT INTO custom_attribute_defs (entity, key, label, type) VALUES ('contact', 'notitie', 'Notitie', 'text')`)
	id := f.contactIn("=HYPERLINK(\"http://evil\")", "formule@customer.nl", f.mailboxA)
	f.exec(`UPDATE contacts SET phone = '+31612345678', custom_attributes = '{"notitie":"@SUM(1)"}' WHERE id = $1`, id)
	f.exec(`INSERT INTO contact_addresses (contact_id, email) VALUES ($1, 'tweede@customer.nl')`, id)
	f.contactIn("Anna; \"de\" Vries", "anna@customer.nl", f.mailboxA)

	r := f.admin.do("GET", "/api/v1/contacts/export?sort=name", nil)
	expect(t, r, 200, "")
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") || !strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("headers = %v", r.header)
	}
	if !bytes.HasPrefix(r.raw, []byte("\xEF\xBB\xBF")) {
		t.Error("no BOM")
	}
	table, err := contacts.ParseCSV(r.raw, ";")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(table.Header[:3], []string{"Naam", "E-mail", "Overige e-mailadressen"}) || table.Header[len(table.Header)-1] != "Notitie" {
		t.Fatalf("header = %v", table.Header)
	}
	byEmail := map[string][]string{}
	for _, row := range table.Rows {
		byEmail[row[1]] = row
	}
	formula := byEmail["formule@customer.nl"]
	if len(formula) == 0 {
		t.Fatalf("rows = %v", table.Rows)
	}
	if !strings.HasPrefix(formula[0], "'=HYPERLINK") || formula[3] != "'+31612345678" || formula[len(formula)-1] != "'@SUM(1)" || formula[2] != "tweede@customer.nl" {
		t.Errorf("row = %q: every formula-like cell must be neutralised", formula)
	}
	if got := byEmail["anna@customer.nl"][0]; got != `Anna; "de" Vries` {
		t.Errorf("delimiters and quotes must survive: %q", got)
	}
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.exported' AND metadata ->> 'rows' = '2'`) != 1 {
		t.Error("export not audited with its row count")
	}
}

func importOptions(dedupe string, targets ...string) string {
	cols := make([]map[string]any, len(targets))
	for i, t := range targets {
		cols[i] = map[string]any{"index": i, "target": t}
	}
	b, err := json.Marshal(map[string]any{"dedupe": dedupe, "columns": cols})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f *contactFixture) runQueuedImport(id string) {
	f.h.t.Helper()
	w := contacts.NewImportWorker(contacts.ImportDeps{Pool: f.h.pool, Blobs: f.store, IsFreeMailDomain: ingest.IsFreeMailDomain})
	err := w.Work(f.h.t.Context(), &river.Job[jobs.ContactImport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactImport{ImportID: id}})
	if err != nil {
		f.h.t.Fatal(err)
	}
}

func TestContactImportOverHTTP(t *testing.T) {
	f := newContactFixture(t)
	csv := []byte("E-mail;Naam\nanna@acme.nl;Anna\nkapot;Fout\nbram@acme.nl;Bram\n")

	expect(t, f.multipart(f.agent, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "email", "name")}, csv), 403, "forbidden")
	expect(t, f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "email", "name")}, nil), 422, "validation_failed")
	expect(t, f.multipart(f.admin, "/api/v1/contact-imports", nil, csv), 422, "validation_failed")
	expect(t, f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "name", "email")[:20]}, csv), 422, "validation_failed")
	if r := f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "name", "phone")}, csv); r.status != 422 || !strings.Contains(string(r.raw), "options") {
		t.Errorf("mapping without email: %d %s", r.status, r.raw)
	}
	if r := f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "email", "name")}, []byte("E-mail,Naam\n\xff,x\n")); r.status != 422 || !strings.Contains(string(r.raw), "not_utf8") {
		t.Errorf("latin-1 file: %d %s", r.status, r.raw)
	}
	var big strings.Builder
	big.WriteString("email\n")
	for i := range contacts.MaxImportRows + 1 {
		fmt.Fprintf(&big, "u%d@example.com\n", i)
	}
	if r := f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "email")}, []byte(big.String())); r.status != 422 || !strings.Contains(string(r.raw), "too_many_rows") {
		t.Errorf("50001 rows: %d %.200s", r.status, r.raw)
	}
	if r := f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "email")}, []byte("email\n")); r.status != 422 || !strings.Contains(string(r.raw), "empty") {
		t.Errorf("header only: %d %s", r.status, r.raw)
	}
	if f.h.count(`SELECT count(*) FROM contact_imports`) != 0 || f.h.count(`SELECT count(*) FROM river_job WHERE kind = 'contacts.import'`) != 0 {
		t.Fatal("a rejected upload queued work")
	}

	r := f.multipart(f.admin, "/api/v1/contact-imports", map[string]string{"options": importOptions("skip", "email", "name")}, csv)
	expect(t, r, 202, "")
	id := idOf(t, r, "import")
	if f.h.count(`SELECT count(*) FROM river_job WHERE kind = 'contacts.import' AND args->>'import_id' = $1`, id) != 1 {
		t.Fatal("import job not enqueued")
	}
	if imp := r.body["import"].(map[string]any); imp["status"] != "queued" || imp["total_rows"] != 3.0 {
		t.Errorf("import = %v", imp)
	}
	f.runQueuedImport(id)

	status := f.admin.do("GET", "/api/v1/contact-imports/"+id, nil)
	expect(t, status, 200, "")
	imp := status.body["import"].(map[string]any)
	if imp["status"] != "done" || imp["created_count"] != 2.0 || imp["failed_count"] != 1.0 || imp["processed_rows"] != 3.0 || imp["has_errors"] != true {
		t.Fatalf("import = %v", imp)
	}
	expect(t, f.agent.do("GET", "/api/v1/contact-imports/"+id, nil), 403, "forbidden")
	report := f.admin.do("GET", "/api/v1/contact-imports/"+id+"/errors", nil)
	expect(t, report, 200, "")
	if !strings.HasPrefix(report.header.Get("Content-Type"), "text/csv") || !strings.Contains(string(report.raw), "kapot") {
		t.Errorf("report = %s", report.raw)
	}
	if got := contactNames(f.admin.do("GET", "/api/v1/contacts", nil)); !slices.Equal(got, []string{"Anna", "Bram"}) {
		t.Errorf("imported = %q", got)
	}
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.import_started'`) != 1 {
		t.Error("import not audited")
	}
	expect(t, f.admin.do("GET", "/api/v1/contact-imports/0199a000-0000-7000-8000-000000000000/errors", nil), 404, "not_found")
}

func TestContactDataExportIsDownloadableOnce(t *testing.T) {
	f := newContactFixture(t)
	id := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, subject, body_text)
		SELECT id, mailbox_id, 'email', 'in', 'anna@customer.nl', 'Anna', 'Vraag', 'Mijn vraag over de factuur' FROM conversations WHERE contact_id = $1`, id)

	expect(t, f.agent.do("POST", "/api/v1/contacts/"+id+"/data-export", nil), 403, "forbidden")
	r := f.admin.do("POST", "/api/v1/contacts/"+id+"/data-export", nil)
	expect(t, r, 202, "")
	exportID := idOf(t, r, "export")
	if f.h.count(`SELECT count(*) FROM river_job WHERE kind = 'contacts.export' AND args->>'export_id' = $1`, exportID) != 1 {
		t.Fatal("export job not enqueued")
	}
	expect(t, f.admin.do("GET", "/api/v1/data-exports/"+exportID+"/download", nil), 409, "not_ready")

	w := contacts.NewExportWorker(f.h.pool, f.store)
	if err := w.Work(t.Context(), &river.Job[jobs.ContactExport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactExport{ExportID: exportID}}); err != nil {
		t.Fatal(err)
	}
	status := f.admin.do("GET", "/api/v1/data-exports/"+exportID, nil)
	expect(t, status, 200, "")
	if status.body["export"].(map[string]any)["status"] != "ready" {
		t.Fatalf("status = %s", status.raw)
	}

	otherAdmin, _ := f.h.loggedIn("admin")
	expect(t, otherAdmin.do("GET", "/api/v1/data-exports/"+exportID, nil), 404, "not_found")
	expect(t, otherAdmin.do("GET", "/api/v1/data-exports/"+exportID+"/download", nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/data-exports/"+exportID+"/download", nil), 403, "forbidden")

	dl := f.admin.do("GET", "/api/v1/data-exports/"+exportID+"/download", nil)
	expect(t, dl, 200, "")
	if dl.header.Get("Content-Type") != "application/zip" || !strings.Contains(dl.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("headers = %v", dl.header)
	}
	zr, err := zip.NewReader(bytes.NewReader(dl.raw), int64(len(dl.raw)))
	if err != nil {
		t.Fatal(err)
	}
	var doc string
	for _, zf := range zr.File {
		if zf.Name == "contact.json" {
			rc, err := zf.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(rc)
			_ = rc.Close()
			doc = string(b)
		}
	}
	if !strings.Contains(doc, "Mijn vraag over de factuur") || !strings.Contains(doc, "anna@customer.nl") {
		t.Errorf("contact.json = %s", doc)
	}
	expect(t, f.admin.do("GET", "/api/v1/data-exports/"+exportID+"/download", nil), 410, "gone")
	if f.h.count(`SELECT count(*) FROM data_exports WHERE id = $1 AND blob_key = ''`, exportID) != 1 {
		t.Error("the export file is still referenced after the download")
	}
	if f.h.count(`SELECT count(*) FROM pending_blob_deletions`) != 0 {
		t.Error("the export file was not deleted after the download")
	}
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action IN ('contact.data_export_requested', 'contact.data_export_downloaded')`) != 2 {
		t.Error("export request and download must both be audited")
	}
}

func TestEraseContactOverHTTP(t *testing.T) {
	f := newContactFixture(t)
	id := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, subject, body_text)
		SELECT id, mailbox_id, 'email', 'in', 'anna@customer.nl', 'Anna', 'Vraag', 'Persoonlijke tekst' FROM conversations WHERE contact_id = $1`, id)

	expect(t, f.agent.do("POST", "/api/v1/contacts/"+id+"/erase", map[string]any{"confirmation": "anna@customer.nl"}), 403, "forbidden")
	expect(t, f.admin.do("POST", "/api/v1/contacts/"+id+"/erase", map[string]any{"confirmation": "anna"}), 422, "validation_failed")
	expect(t, f.admin.do("POST", "/api/v1/contacts/"+id+"/erase", map[string]any{}), 422, "validation_failed")
	if f.h.count(`SELECT count(*) FROM contacts WHERE id = $1`, id) != 1 {
		t.Fatal("a refused erasure removed the contact")
	}
	r := f.admin.do("POST", "/api/v1/contacts/"+id+"/erase", map[string]any{"confirmation": " ANNA@customer.nl "})
	expect(t, r, 200, "")
	erased := r.body["erased"].(map[string]any)
	if erased["conversations_erased"] != 1.0 || erased["messages_blanked"] != 1.0 {
		t.Errorf("erased = %v", erased)
	}
	expect(t, f.admin.do("GET", "/api/v1/contacts/"+id, nil), 404, "not_found")
	expect(t, f.admin.do("POST", "/api/v1/contacts/"+id+"/erase", map[string]any{"confirmation": "anna@customer.nl"}), 404, "not_found")
	if f.h.count(`SELECT count(*) FROM messages WHERE body_text <> '' AND from_addr = 'anna@customer.nl'`) != 0 {
		t.Error("message text remains")
	}
	if f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.erased' AND target_id = $1`, id) != 1 {
		t.Error("erasure not audited")
	}
}
