package api

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type campaignEnv struct {
	h                *harness
	admin, agent     *client
	adminID          string
	mailboxA, mailB  string
	segment, private string
	anon             *http.Client
}

func newCampaignEnv(t *testing.T) *campaignEnv {
	t.Helper()
	h := newHarness(t)
	rc, err := river.NewClient(riverpgxv5.New(h.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.jobs = rc
	e := &campaignEnv{h: h}
	admin, adminUser := h.loggedIn("admin")
	e.admin, e.adminID = admin, adminUser.ID.String()
	e.agent, _ = h.loggedIn("agent")
	e.mailboxA = e.id(`INSERT INTO mailboxes (name, email_address, display_name) VALUES ('Support', 'support@shop.example', 'Shop') RETURNING id`)
	e.mailB = e.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Sales', 'sales@shop.example') RETURNING id`)
	e.segment = e.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('Everyone', $1, true, '{"match":"all","conditions":[]}') RETURNING id`, e.adminID)
	e.private = e.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('Private', $1, false, '{"match":"all","conditions":[]}') RETURNING id`, e.adminID)

	tr := h.ts.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server certificate
	e.anon = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

func (e *campaignEnv) id(sql string, args ...any) string {
	e.h.t.Helper()
	var id pgtype.UUID
	if err := e.h.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		e.h.t.Fatal(err)
	}
	return id.String()
}

func (e *campaignEnv) exec(sql string, args ...any) {
	e.h.t.Helper()
	if _, err := e.h.pool.Exec(context.Background(), sql, args...); err != nil {
		e.h.t.Fatal(err)
	}
}

func (e *campaignEnv) count(sql string, args ...any) int {
	e.h.t.Helper()
	var n int
	if err := e.h.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.h.t.Fatal(err)
	}
	return n
}

func (e *campaignEnv) contacts(n int) {
	e.exec(`WITH c AS (INSERT INTO contacts (name, created_by) SELECT 'Contact ' || g, $1 FROM generate_series(1, $2::int) g RETURNING id, name)
		INSERT INTO contact_addresses (contact_id, email, is_primary)
		SELECT id, lower(replace(name, ' ', '.')) || '@acme.example', true FROM c`, e.adminID, n)
}

func (e *campaignEnv) body(extra map[string]any) map[string]any {
	b := map[string]any{
		"name": "Herfst", "mailbox_id": e.mailboxA, "segment_id": e.segment, "subject": "Actie voor {{contact.first_name}}",
		"body_html":       "<p>Hallo {{contact.first_name}}, <a href=\"https://shop.example\">bekijk de actie</a>.</p><script>alert(1)</script>",
		"rate_per_minute": 30,
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func (e *campaignEnv) create(extra map[string]any) string {
	e.h.t.Helper()
	r := e.admin.do("POST", "/api/v1/campaigns", e.body(extra))
	expect(e.h.t, r, 201, "")
	return r.body["campaign"].(map[string]any)["id"].(string)
}

func campaignOf(r response) map[string]any { return r.body["campaign"].(map[string]any) }

func TestCampaignLifecycle(t *testing.T) {
	e := newCampaignEnv(t)
	e.contacts(6)
	id := e.create(nil)

	r := e.admin.do("GET", "/api/v1/campaigns/"+id, nil)
	expect(t, r, 200, "")
	c := campaignOf(r)
	if c["status"] != "draft" || c["subject"] != "Actie voor {{contact.first_name}}" || c["rate_per_minute"] != float64(30) {
		t.Fatalf("campaign = %v", c)
	}
	if strings.Contains(c["body_html"].(string), "script") || !strings.Contains(c["body_html"].(string), "bekijk de actie") {
		t.Errorf("body was not sanitized: %v", c["body_html"])
	}
	if seg := c["segment"].(map[string]any); seg["name"] != "Everyone" {
		t.Errorf("segment = %v", seg)
	}

	r = e.admin.do("PATCH", "/api/v1/campaigns/"+id, map[string]any{"name": "Herfstactie", "rate_per_minute": 60})
	expect(t, r, 200, "")
	if c := campaignOf(r); c["name"] != "Herfstactie" || c["rate_per_minute"] != float64(60) || c["subject"] != "Actie voor {{contact.first_name}}" {
		t.Errorf("patched campaign = %v", c)
	}

	r = e.admin.do("POST", "/api/v1/campaigns/preview", map[string]any{"segment_id": e.segment})
	expect(t, r, 200, "")
	if r.body["total"] != float64(6) || r.body["sendable"] != float64(6) {
		t.Errorf("preview = %v", r.body)
	}

	r = e.admin.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{})
	expect(t, r, 200, "")
	if campaignOf(r)["status"] != "sending" {
		t.Fatalf("status = %v", campaignOf(r)["status"])
	}
	if got := e.count(`SELECT count(*) FROM river_job WHERE kind = 'campaigns.tick'`); got != 1 {
		t.Errorf("dispatcher jobs = %d, want 1 queued by the start", got)
	}
	expect(t, e.admin.do("PATCH", "/api/v1/campaigns/"+id, map[string]any{"name": "x"}), 409, "campaign_state")
	expect(t, e.admin.do("DELETE", "/api/v1/campaigns/"+id, nil), 409, "campaign_state")
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{}), 409, "campaign_state")

	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/resume", nil), 409, "campaign_state")
	r = e.admin.do("POST", "/api/v1/campaigns/"+id+"/pause", nil)
	expect(t, r, 200, "")
	if campaignOf(r)["status"] != "paused" {
		t.Errorf("status = %v", campaignOf(r)["status"])
	}
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/resume", nil), 200, "")
	r = e.admin.do("POST", "/api/v1/campaigns/"+id+"/cancel", nil)
	expect(t, r, 200, "")
	if campaignOf(r)["status"] != "cancelled" {
		t.Errorf("status = %v", campaignOf(r)["status"])
	}
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/cancel", nil), 409, "campaign_state")

	r = e.admin.do("GET", "/api/v1/campaigns", nil)
	expect(t, r, 200, "")
	list := r.body["campaigns"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != id {
		t.Fatalf("list = %v", list)
	}
	if limits := r.body["limits"].(map[string]any); limits["default_rate"] != float64(60) || limits["max_rate"] != float64(120) {
		t.Errorf("limits = %v", limits)
	}
	expect(t, e.admin.do("DELETE", "/api/v1/campaigns/"+id, nil), 204, "")
	expect(t, e.admin.do("GET", "/api/v1/campaigns/"+id, nil), 404, "not_found")

	for _, action := range []string{"campaign.created", "campaign.updated", "campaign.started", "campaign.paused", "campaign.resumed", "campaign.cancelled", "campaign.deleted"} {
		if got := e.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND target_id = $2`, action, id); got != 1 {
			t.Errorf("audit entries for %s = %d, want 1", action, got)
		}
	}
}

func TestCampaignValidation(t *testing.T) {
	e := newCampaignEnv(t)
	// An admin sees every mailbox, but a personal segment stays personal.
	other, _ := e.h.loggedIn("admin")

	for name, c := range map[string]struct {
		body  map[string]any
		field string
	}{
		"missing name":      {map[string]any{"name": ""}, "name"},
		"unknown variable":  {map[string]any{"body_html": "<p>{{conversation.number}}</p>"}, "body_html"},
		"rate above max":    {map[string]any{"rate_per_minute": 500}, "rate_per_minute"},
		"unknown mailbox":   {map[string]any{"mailbox_id": "0199a000-0000-7000-8000-000000000000"}, "mailbox_id"},
		"malformed mailbox": {map[string]any{"mailbox_id": "nope"}, "mailbox_id"},
		"unknown segment":   {map[string]any{"segment_id": "0199a000-0000-7000-8000-000000000000"}, "segment_id"},
		"subject newline":   {map[string]any{"subject": "a\r\nBcc: x@y.z"}, "subject"},
	} {
		t.Run(name, func(t *testing.T) {
			r := e.admin.do("POST", "/api/v1/campaigns", e.body(c.body))
			expect(t, r, 422, "validation_failed")
			fields := r.body["error"].(map[string]any)["fields"].(map[string]any)
			if fields[c.field] == nil {
				t.Errorf("fields = %v, want one on %s", fields, c.field)
			}
		})
	}
	r := e.admin.do("POST", "/api/v1/campaigns", e.body(map[string]any{"segment_id": e.private}))
	expect(t, r, 201, "")
	if r := other.do("POST", "/api/v1/campaigns", e.body(map[string]any{"segment_id": e.private})); r.status != 422 {
		t.Errorf("using someone else's personal segment: %d %s, want 422", r.status, r.raw)
	}

	incomplete := e.create(map[string]any{"subject": "", "body_html": "", "segment_id": ""})
	r = e.admin.do("POST", "/api/v1/campaigns/"+incomplete+"/start", map[string]any{})
	expect(t, r, 422, "validation_failed")
	fields := r.body["error"].(map[string]any)["fields"].(map[string]any)
	for _, f := range []string{"subject", "body_html", "segment_id"} {
		if fields[f] != "required" {
			t.Errorf("starting an incomplete campaign: fields = %v, want %s required", fields, f)
		}
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+e.create(nil)+"/start", map[string]any{"scheduled_at": past}), 422, "validation_failed")
}

func TestCampaignScheduling(t *testing.T) {
	e := newCampaignEnv(t)
	id := e.create(nil)
	at := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	r := e.admin.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{"scheduled_at": at})
	expect(t, r, 200, "")
	c := campaignOf(r)
	if c["status"] != "scheduled" || c["scheduled_at"] == nil {
		t.Errorf("campaign = %v", c)
	}
	if got := e.count(`SELECT count(*) FROM audit_log WHERE action = 'campaign.scheduled' AND target_id = $1`, id); got != 1 {
		t.Errorf("schedule audit entries = %d", got)
	}
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/cancel", nil), 200, "")
}

func TestCampaignMailboxScope(t *testing.T) {
	e := newCampaignEnv(t)
	_, user := e.h.loggedIn("agent")
	role := e.id(`INSERT INTO custom_roles (name, permissions) VALUES ('Marketing', ARRAY['conversations.read','conversations.write','contacts.read','campaigns.manage']) RETURNING id`)
	e.exec(`UPDATE users SET role = 'custom', custom_role_id = $1, permissions = ARRAY['conversations.read','conversations.write','contacts.read','campaigns.manage'] WHERE id = $2`, role, user.ID)
	team := e.id(`INSERT INTO teams (name) VALUES ('Marketing') RETURNING id`)
	e.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team, user.ID)
	e.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, e.mailboxA, team)
	// A second session: the first one loaded the agent role before the change.
	custom := e.h.client()
	if r := custom.login(user.Email, "correct horse agent"); r.status != 200 {
		t.Fatalf("login: %d %s", r.status, r.raw)
	}

	onB := e.create(map[string]any{"mailbox_id": e.mailB})
	onA := e.create(nil)

	r := custom.do("GET", "/api/v1/campaigns", nil)
	expect(t, r, 200, "")
	list := r.body["campaigns"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != onA {
		t.Errorf("a marketer sees %v, want only the campaign on their mailbox", list)
	}
	expect(t, custom.do("GET", "/api/v1/campaigns/"+onB, nil), 404, "not_found")
	expect(t, custom.do("POST", "/api/v1/campaigns/"+onB+"/cancel", nil), 404, "not_found")
	r = custom.do("POST", "/api/v1/campaigns", e.body(map[string]any{"mailbox_id": e.mailB}))
	expect(t, r, 422, "validation_failed")
	expect(t, custom.do("POST", "/api/v1/campaigns", e.body(nil)), 201, "")
}

func TestCampaignRecipientsAndReport(t *testing.T) {
	e := newCampaignEnv(t)
	e.contacts(5)
	unsub := e.id(`INSERT INTO contacts (name, unsubscribed_at) VALUES ('=cmd|calc', now()) RETURNING id`)
	e.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'off@acme.example', true)`, unsub)
	id := e.create(nil)
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{}), 200, "")
	if err := e.h.srv.campaignService().Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	r := e.admin.do("GET", "/api/v1/campaigns/"+id, nil)
	expect(t, r, 200, "")
	counts := campaignOf(r)["counts"].(map[string]any)
	if counts["total"] != float64(6) || counts["skipped"] != float64(1) || counts["skipped_reasons"].(map[string]any)["unsubscribed"] != float64(1) {
		t.Errorf("counts = %v", counts)
	}
	if counts["queued"].(float64)+counts["pending"].(float64) != 5 {
		t.Errorf("counts = %v, want 5 still to go", counts)
	}

	r = e.admin.do("GET", "/api/v1/campaigns/"+id+"/recipients?limit=4", nil)
	expect(t, r, 200, "")
	first := r.body["recipients"].([]any)
	if len(first) != 4 || r.body["next"] == nil {
		t.Fatalf("page 1 = %d rows, next = %v", len(first), r.body["next"])
	}
	r = e.admin.do("GET", "/api/v1/campaigns/"+id+"/recipients?limit=4&after="+r.body["next"].(string), nil)
	expect(t, r, 200, "")
	if second := r.body["recipients"].([]any); len(second) != 2 || r.body["next"] != nil {
		t.Errorf("page 2 = %d rows, next = %v", len(second), r.body["next"])
	}
	r = e.admin.do("GET", "/api/v1/campaigns/"+id+"/recipients?state=skipped", nil)
	expect(t, r, 200, "")
	if rows := r.body["recipients"].([]any); len(rows) != 1 || rows[0].(map[string]any)["skip_reason"] != "unsubscribed" {
		t.Errorf("skipped rows = %v", rows)
	}
	expect(t, e.admin.do("GET", "/api/v1/campaigns/"+id+"/recipients?state=nope", nil), 422, "validation_failed")

	r = e.admin.do("GET", "/api/v1/campaigns/"+id+"/report", nil)
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/csv") || !strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("report: %d %v", r.status, r.header)
	}
	report := string(r.raw)
	if strings.Count(report, "\n") != 7 || !strings.Contains(report, "'=cmd|calc") {
		t.Errorf("report = %q, want a header and 6 rows with the formula neutralised", report)
	}
	if got := e.count(`SELECT count(*) FROM audit_log WHERE action = 'campaign.report_exported'`); got != 1 {
		t.Errorf("report audit entries = %d", got)
	}
}

func TestCampaignTestSendReportsAFailure(t *testing.T) {
	e := newCampaignEnv(t)
	id := e.create(nil)
	r := e.admin.do("POST", "/api/v1/campaigns/"+id+"/test", map[string]any{})
	expect(t, r, 502, "test_send_failed")
	if reason := r.body["error"].(map[string]any)["fields"].(map[string]any)["reason"]; reason == "" || reason == nil {
		t.Errorf("no reason given: %v", r.body)
	}
	empty := e.create(map[string]any{"subject": "", "body_html": ""})
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+empty+"/test", map[string]any{}), 422, "validation_failed")
}

// unsubscribeLink issues the link of a real recipient the way the dispatcher does.
func (e *campaignEnv) unsubscribeLink(t *testing.T) (path, contact string) {
	t.Helper()
	e.contacts(1)
	id := e.create(nil)
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{}), 200, "")
	if err := e.h.srv.campaignService().Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var headers []byte
	err := e.h.pool.QueryRow(context.Background(), `SELECT o.extra_headers FROM outbound o JOIN campaign_recipients r ON r.message_id = o.message_id WHERE r.campaign_id = $1`, id).Scan(&headers)
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(headers), "/afmelden/")
	if !ok {
		t.Fatalf("no unsubscribe link in %s", headers)
	}
	token, _, _ := strings.Cut(after, ">")
	return "/afmelden/" + token, e.id(`SELECT contact_id FROM campaign_recipients WHERE campaign_id = $1`, id)
}

func (e *campaignEnv) public(method, path, ip string, form url.Values) (int, string, http.Header) {
	e.h.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, e.h.ts.URL+path, body)
	if err != nil {
		e.h.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("X-Forwarded-For", ip)
	resp, err := e.anon.Do(req)
	if err != nil {
		e.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.h.t.Fatal(err)
	}
	return resp.StatusCode, string(raw), resp.Header
}

func TestPublicUnsubscribeNeedsNoSession(t *testing.T) {
	e := newCampaignEnv(t)
	path, contact := e.unsubscribeLink(t)

	status, body, header := e.public("GET", path, "203.0.113.10", nil)
	if status != 200 || !strings.Contains(body, "Afmelden") || !strings.Contains(body, "a***@") && !strings.Contains(body, "c***@") {
		t.Fatalf("GET: %d %s", status, body)
	}
	if !strings.Contains(header.Get("Content-Security-Policy"), "default-src 'none'") || header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", header)
	}
	if e.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact) != 0 {
		t.Fatal("opening the link unsubscribed the contact")
	}

	// The one-click request of RFC 8058: no cookie, no origin, no CSRF token.
	status, body, _ = e.public("POST", path, "203.0.113.10", url.Values{"List-Unsubscribe": {"One-Click"}})
	if status != 200 || !strings.Contains(body, "Je bent afgemeld") {
		t.Fatalf("POST: %d %s", status, body)
	}
	if e.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact) != 1 {
		t.Fatal("the contact is not unsubscribed")
	}
	if status, body, _ = e.public("POST", path, "203.0.113.10", url.Values{"List-Unsubscribe": {"One-Click"}}); status != 200 {
		t.Errorf("repeating the POST: %d %s", status, body)
	}
	if status, body, _ = e.public("GET", path, "203.0.113.10", nil); status != 200 || !strings.Contains(body, "Je bent afgemeld") {
		t.Errorf("GET after unsubscribing: %d %s", status, body)
	}
	if got := e.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.unsubscribed'`); got != 1 {
		t.Errorf("audit entries = %d, want 1", got)
	}
}

func TestPublicUnsubscribeRejectsBadTokens(t *testing.T) {
	e := newCampaignEnv(t)
	path, contact := e.unsubscribeLink(t)
	tampered := path[:len(path)-2] + "AA"
	if strings.HasSuffix(path, "AA") {
		tampered = path[:len(path)-2] + "BB"
	}
	for name, p := range map[string]string{"tampered": tampered, "garbage": "/afmelden/not-a-token", "truncated": path[:len(path)-10]} {
		for _, method := range []string{"GET", "POST"} {
			status, body, _ := e.public(method, p, "203.0.113.11", nil)
			if status != 404 || !strings.Contains(body, "Link ongeldig") {
				t.Errorf("%s %s: %d %s", method, name, status, body)
			}
		}
	}
	if e.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NOT NULL`, contact) != 0 {
		t.Error("a rejected link unsubscribed the contact")
	}
}

func TestPublicUnsubscribeIsRateLimited(t *testing.T) {
	e := newCampaignEnv(t)
	limited := false
	for range 70 {
		if status, _, header := e.public("GET", "/afmelden/x", "203.0.113.12", nil); status == 429 {
			limited = header.Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Error("a client guessing tokens is never limited")
	}
	if status, _, _ := e.public("GET", "/afmelden/x", "203.0.113.13", nil); status != 404 {
		t.Errorf("another client is limited too: %d", status)
	}
}

func TestContactConsentCanBeLiftedByHand(t *testing.T) {
	e := newCampaignEnv(t)
	e.contacts(1)
	contact := e.id(`SELECT id FROM contacts LIMIT 1`)
	email := "contact.1@acme.example"
	e.exec(`UPDATE contacts SET unsubscribed_at = now() WHERE id = $1`, contact)
	e.exec(`UPDATE contact_addresses SET bounced_at = now() WHERE email = $1`, email)

	r := e.admin.do("GET", "/api/v1/contacts/"+contact, nil)
	expect(t, r, 200, "")
	c := r.body["contact"].(map[string]any)
	if c["unsubscribed_at"] == nil || c["emails"].([]any)[0].(map[string]any)["bounced_at"] == nil {
		t.Fatalf("contact does not show the unsubscription and the bounce: %v", c)
	}

	expect(t, e.agent.do("POST", "/api/v1/contacts/"+contact+"/resubscribe", nil), 403, "forbidden")
	expect(t, e.admin.do("POST", "/api/v1/contacts/"+contact+"/reactivate-address", map[string]string{"email": "nope"}), 422, "validation_failed")
	for i := 0; i < 2; i++ {
		expect(t, e.admin.do("POST", "/api/v1/contacts/"+contact+"/resubscribe", nil), 204, "")
		expect(t, e.admin.do("POST", "/api/v1/contacts/"+contact+"/reactivate-address", map[string]string{"email": email}), 204, "")
	}
	if e.count(`SELECT count(*) FROM contacts WHERE id = $1 AND unsubscribed_at IS NULL`, contact) != 1 ||
		e.count(`SELECT count(*) FROM contact_addresses WHERE email = $1 AND bounced_at IS NULL`, email) != 1 {
		t.Fatal("the marks are still set")
	}
	if got := e.count(`SELECT count(*) FROM audit_log WHERE action IN ('contact.resubscribed', 'contact.address_reactivated')`); got != 2 {
		t.Errorf("audit entries = %d, want 2 (a repeated call changes nothing)", got)
	}
	expect(t, e.admin.do("POST", "/api/v1/contacts/00000000-0000-0000-0000-000000000000/resubscribe", nil), 404, "not_found")
}

// marketer is a user with campaigns.manage and write access to mailbox A only.
func (e *campaignEnv) marketer() *client {
	e.h.t.Helper()
	_, user := e.h.loggedIn("agent")
	perms := `ARRAY['conversations.read','conversations.write','contacts.read','campaigns.manage']`
	role := e.id(`INSERT INTO custom_roles (name, permissions) VALUES ('Marketing', ` + perms + `) RETURNING id`)
	e.exec(`UPDATE users SET role = 'custom', custom_role_id = $1, permissions = `+perms+` WHERE id = $2`, role, user.ID)
	team := e.id(`INSERT INTO teams (name) VALUES ('Marketing') RETURNING id`)
	e.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team, user.ID)
	e.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, e.mailboxA, team)
	c := e.h.client()
	if r := c.login(user.Email, "correct horse agent"); r.status != 200 {
		e.h.t.Fatalf("login: %d %s", r.status, r.raw)
	}
	return c
}

func TestCampaignRecipientListsFollowContactVisibility(t *testing.T) {
	e := newCampaignEnv(t)
	marketer := e.marketer()
	seen := e.id(`INSERT INTO contacts (name) VALUES ('Seen') RETURNING id`)
	hidden := e.id(`INSERT INTO contacts (name) VALUES ('Hidden') RETURNING id`)
	e.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, 'seen@acme.example', true), ($2, 'hidden@acme.example', true)`, seen, hidden)
	e.exec(`INSERT INTO conversations (mailbox_id, contact_id, subject) VALUES ($1, $2, 's'), ($3, $4, 's')`, e.mailboxA, seen, e.mailB, hidden)
	id := e.create(nil)
	e.exec(`INSERT INTO campaign_recipients (campaign_id, contact_id, email, name, state, skip_reason) VALUES
		($1, $2, 'seen@acme.example', '', 'pending', ''), ($1, $3, 'hidden@acme.example', '', 'pending', '')`, id, seen, hidden)

	if r := e.admin.do("GET", "/api/v1/campaigns/"+id+"/recipients", nil); len(r.body["recipients"].([]any)) != 2 {
		t.Fatalf("admin's list: %s", r.raw)
	}
	r := marketer.do("GET", "/api/v1/campaigns/"+id+"/recipients", nil)
	expect(t, r, 200, "")
	rows := r.body["recipients"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["email"] != "seen@acme.example" {
		t.Errorf("marketer's list = %s, want only the contact they may see", r.raw)
	}
	r = marketer.do("GET", "/api/v1/campaigns/"+id+"/report", nil)
	if r.status != 200 || !strings.Contains(string(r.raw), "seen@acme.example") || strings.Contains(string(r.raw), "hidden@acme.example") {
		t.Errorf("marketer's report = %d %q", r.status, r.raw)
	}
}

func TestCampaignStartChecksTheSegmentForTheStarter(t *testing.T) {
	e := newCampaignEnv(t)
	marketer := e.marketer()
	id := e.create(map[string]any{"segment_id": e.private})

	r := marketer.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{})
	expect(t, r, 422, "validation_failed")
	if fieldCode(r, "segment_id") != "unknown" {
		t.Errorf("start: %s", r.raw)
	}
	expect(t, e.admin.do("POST", "/api/v1/campaigns/"+id+"/start", map[string]any{}), 200, "")
	if got := e.id(`SELECT started_by FROM campaigns WHERE id = $1`, id); got != e.adminID {
		t.Errorf("started_by = %s, want the admin who started it", got)
	}
}
