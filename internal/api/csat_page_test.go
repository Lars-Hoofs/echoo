package api

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/csat"
)

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, ok := parseUUID(s)
	if !ok {
		t.Fatalf("invalid uuid %q", s)
	}
	return id
}

// surveyFixture has a resolved conversation with a survey that was sent an hour ago.
type surveyFixture struct {
	*inboxFixture
	signer *csat.Signer
	conv   string
	token  string
}

func newSurveyFixture(t *testing.T) *surveyFixture {
	t.Helper()
	f := &surveyFixture{inboxFixture: newInboxFixture(t)}
	f.signer = csat.NewSigner(f.h.keys.Derive("csat-token"))
	f.conv = f.conversation("Factuur", convOpt{mailbox: f.mailboxA, status: "closed", assignee: f.agentID})
	f.token = f.survey(f.conv, time.Now().Add(csat.TokenTTL))
	return f
}

// survey records a sent survey for conv and returns its token.
func (f *surveyFixture) survey(conv string, expires time.Time) string {
	expires = expires.Truncate(time.Second)
	token := f.signer.Issue(mustUUID(f.h.t, conv), expires)
	f.exec(`INSERT INTO csat_requests (conversation_id, mailbox_id, token_hash, sent_at, expires_at)
		SELECT id, mailbox_id, $2, now() - interval '1 hour', $3 FROM conversations WHERE id = $1`, conv, csat.Hash(token), expires)
	return token
}

// page is an unauthenticated browser: no cookie jar, no session.
type page struct {
	status int
	header http.Header
	body   string
}

func (f *surveyFixture) request(method, token string, form url.Values, mutate func(*http.Request)) page {
	f.h.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, f.h.ts.URL+"/tevredenheid/"+token, body)
	if err != nil {
		f.h.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", testOrigin)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if mutate != nil {
		mutate(req)
	}
	hc := *f.h.ts.Client()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		f.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		f.h.t.Fatal(err)
	}
	return page{status: resp.StatusCode, header: resp.Header, body: string(raw)}
}

func (f *surveyFixture) form(token string, rating, comment string) url.Values {
	return url.Values{"csrf": {csat.NewSigner(f.h.keys.Derive("csat-token")).CSRF(token)}, "rating": {rating}, "comment": {comment}}
}

func (f *surveyFixture) stored() (rating int, comment string, n int) {
	f.h.t.Helper()
	n = f.h.count(`SELECT count(*) FROM csat_responses WHERE conversation_id = $1`, f.conv)
	if n > 0 {
		row := f.h.pool.QueryRow(f.h.t.Context(), `SELECT rating, comment FROM csat_responses WHERE conversation_id = $1`, f.conv)
		if err := row.Scan(&rating, &comment); err != nil {
			f.h.t.Fatal(err)
		}
	}
	return rating, comment, n
}

func TestSurveyPageNeedsNoSessionAndIsPlain(t *testing.T) {
	f := newSurveyFixture(t)
	p := f.request("GET", f.token+"?r=4", nil, nil)
	if p.status != 200 {
		t.Fatalf("%d %s", p.status, p.body)
	}
	if !strings.Contains(p.body, `lang="nl"`) || !strings.Contains(p.body, "Hoe tevreden bent u") || !strings.Contains(p.body, "Alpha") {
		t.Errorf("page is not the Dutch survey:\n%s", p.body)
	}
	if !strings.Contains(p.body, `id="r4" value="4" checked`) || strings.Contains(p.body, `value="3" checked`) {
		t.Errorf("rating 4 should be preselected:\n%s", p.body)
	}
	if strings.Contains(p.body, "<script") || strings.Contains(p.body, "http://") || strings.Contains(p.body, "https://") {
		t.Errorf("the page must have no scripts and make no external requests:\n%s", p.body)
	}
	if !strings.Contains(p.body, `method="post"`) || !strings.Contains(p.body, `name="csrf"`) {
		t.Error("the form has no method or CSRF field")
	}
	csp := p.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "font-src 'self'", "form-action 'self'", "frame-ancestors 'none'", "style-src 'sha256-"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "script-src") {
		t.Errorf("CSP too loose: %q", csp)
	}
	if p.header.Get("Set-Cookie") != "" {
		t.Error("the survey page must not set cookies")
	}
	if p.header.Get("Cache-Control") != "no-store" || !strings.Contains(p.header.Get("X-Robots-Tag"), "noindex") || p.header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("cache and privacy headers: %v", p.header)
	}
	if _, _, n := f.stored(); n != 0 {
		t.Error("opening the page (as a mail scanner does) must not record a rating")
	}
}

func TestSurveyLinksThatAreTamperedExpiredOrUnknownAreRefused(t *testing.T) {
	f := newSurveyFixture(t)
	flipped := []byte(f.token)
	flipped[5] ^= 1
	unissued := f.signer.Issue(mustUUID(t, f.queryID(`SELECT gen_random_uuid()`)), time.Now().Add(time.Hour))
	expired := f.survey(f.conversation("Old", convOpt{mailbox: f.mailboxA, status: "closed"}), time.Now().Add(-time.Minute))

	for name, tc := range map[string]struct {
		token  string
		status int
	}{
		"tampered":    {string(flipped), 404},
		"garbage":     {"abc", 404},
		"unknown":     {unissued, 404},
		"expired":     {expired, 410},
		"other key":   {csat.NewSigner([]byte("some other key some other key!!!")).Issue(mustUUID(t, f.conv), time.Now().Add(time.Hour)), 404},
		"valid token": {f.token, 200},
	} {
		if p := f.request("GET", tc.token, nil, nil); p.status != tc.status {
			t.Errorf("GET %s: %d, want %d", name, p.status, tc.status)
		}
		p := f.request("POST", tc.token, f.form(tc.token, "5", ""), nil)
		if want := map[bool]int{true: 303, false: tc.status}[tc.status == 200]; p.status != want {
			t.Errorf("POST %s: %d, want %d", name, p.status, want)
		}
	}
	if n := f.h.count(`SELECT count(*) FROM csat_responses WHERE conversation_id <> $1`, f.conv); n != 0 {
		t.Errorf("%d answers were stored for refused links", n)
	}
}

func TestSurveySubmitStoresOnePerConversationAndAllowsChangesWithinAWeek(t *testing.T) {
	f := newSurveyFixture(t)
	p := f.request("POST", f.token, f.form(f.token, "4", "Snel geholpen"), nil)
	if p.status != 303 || !strings.HasPrefix(p.header.Get("Location"), "/tevredenheid/"+f.token) {
		t.Fatalf("submit: %d %s", p.status, p.header.Get("Location"))
	}
	if rating, comment, n := f.stored(); n != 1 || rating != 4 || comment != "Snel geholpen" {
		t.Errorf("stored %d %q (%d rows)", rating, comment, n)
	}
	if got := f.request("GET", f.token+"?opgeslagen=1", nil, nil); !strings.Contains(got.body, "Bedankt") || !strings.Contains(got.body, `value="4" checked`) || !strings.Contains(got.body, "Snel geholpen") || !strings.Contains(got.body, "Wijzig mijn beoordeling") {
		t.Errorf("the page after saving:\n%s", got.body)
	}

	if p := f.request("POST", f.token, f.form(f.token, "2", "Toch niet"), nil); p.status != 303 {
		t.Fatalf("change: %d", p.status)
	}
	if rating, comment, n := f.stored(); n != 1 || rating != 2 || comment != "Toch niet" {
		t.Errorf("after the change %d %q (%d rows)", rating, comment, n)
	}

	f.exec(`UPDATE csat_responses SET created_at = now() - interval '8 days'`)
	if p := f.request("POST", f.token, f.form(f.token, "5", ""), nil); p.status != 409 {
		t.Errorf("after a week: %d, want 409", p.status)
	}
	if rating, _, _ := f.stored(); rating != 2 {
		t.Errorf("a locked answer changed to %d", rating)
	}
	if got := f.request("GET", f.token, nil, nil); got.status != 200 || strings.Contains(got.body, "<form") {
		t.Errorf("a locked survey must not offer the form: %d", got.status)
	}
}

func TestSurveySubmitRejectsForgeryAndBadInput(t *testing.T) {
	f := newSurveyFixture(t)
	other := f.survey(f.conversation("Other", convOpt{mailbox: f.mailboxA, status: "closed"}), time.Now().Add(time.Hour))
	good := f.form(f.token, "5", "")

	for name, tc := range map[string]struct {
		form   url.Values
		mutate func(*http.Request)
		status int
	}{
		"no csrf field":          {url.Values{"rating": {"5"}}, nil, 403},
		"csrf of another survey": {f.form(other, "5", ""), nil, 403},
		"cross-site origin":      {good, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		"no origin at all":       {good, func(r *http.Request) { r.Header.Del("Origin") }, 403},
		"cross-site fetch":       {good, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		"same-site fetch":        {good, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, 403},
		"rating 0":               {f.form(f.token, "0", ""), nil, 422},
		"rating 6":               {f.form(f.token, "6", ""), nil, 422},
		"rating text":            {f.form(f.token, "vijf", ""), nil, 422},
		"no rating":              {url.Values{"csrf": good["csrf"]}, nil, 422},
		"comment too long":       {f.form(f.token, "3", strings.Repeat("x", 1001)), nil, 422},
	} {
		if p := f.request("POST", f.token, tc.form, tc.mutate); p.status != tc.status {
			t.Errorf("%s: %d, want %d", name, p.status, tc.status)
		}
	}
	if _, _, n := f.stored(); n != 0 {
		t.Error("a rejected submit stored an answer")
	}
	// Under Referrer-Policy no-referrer a browser sends Origin: null on a form post, but says the
	// request is same-origin.
	same := f.request("POST", f.token, good, func(r *http.Request) {
		r.Header.Set("Origin", "null")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	if same.status != 303 {
		t.Errorf("same-origin with Origin null: %d", same.status)
	}
	// The message on a bad rating keeps the form usable.
	bad := f.request("POST", f.token, f.form(f.token, "9", "bewaar mij"), nil)
	if bad.status != 422 || !strings.Contains(bad.body, "Kies een cijfer van 1 tot 5") || !strings.Contains(bad.body, "bewaar mij") {
		t.Errorf("error page:\n%s", bad.body)
	}
}

func TestSurveyHTMLEscapesTheComment(t *testing.T) {
	f := newSurveyFixture(t)
	f.request("POST", f.token, f.form(f.token, "3", `</textarea><script>alert(1)</script>`), nil)
	p := f.request("GET", f.token, nil, nil)
	if strings.Contains(p.body, "<script>alert") || !strings.Contains(p.body, "&lt;script&gt;") {
		t.Errorf("comment not escaped:\n%s", p.body)
	}
}

func TestSurveyPagesAreRateLimited(t *testing.T) {
	f := newSurveyFixture(t)
	limited := false
	for i := 0; i < 40; i++ {
		p := f.request("GET", "not-a-token", nil, nil)
		if p.status == http.StatusTooManyRequests {
			limited = true
			if p.header.Get("Retry-After") == "" || !strings.Contains(p.body, "te veel verzoeken") {
				t.Errorf("rate limit page: %v %s", p.header, p.body)
			}
			break
		}
	}
	if !limited {
		t.Error("40 requests in a row from one client were not limited")
	}
	// Another client is unaffected.
	other := f.request("GET", f.token, nil, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.99") })
	if other.status != 200 {
		t.Errorf("another client: %d", other.status)
	}
}

func TestSurveyRatingShowsInTheConversationAndIsScoped(t *testing.T) {
	f := newSurveyFixture(t)
	get := func(c *client, conv string) response { return c.do("GET", "/api/v1/conversations/"+conv, nil) }

	r := get(f.agent, f.conv)
	expect(t, r, 200, "")
	if r.body["csat"] != nil {
		t.Errorf("csat before an answer: %v", r.body["csat"])
	}
	f.request("POST", f.token, f.form(f.token, "2", "Duurde lang"), nil)
	r = get(f.agent, f.conv)
	csatBody, _ := r.body["csat"].(map[string]any)
	if csatBody["rating"] != float64(2) || csatBody["comment"] != "Duurde lang" {
		t.Errorf("csat in the conversation: %v", r.body["csat"])
	}

	// A rating in a mailbox the agent cannot read stays hidden, with the whole conversation.
	hidden := f.conversation("Hidden", convOpt{mailbox: f.mailboxB, status: "closed"})
	hiddenToken := f.survey(hidden, time.Now().Add(time.Hour))
	f.request("POST", hiddenToken, f.form(hiddenToken, "1", "geheim"), nil)
	expect(t, get(f.agent, hidden), 404, "not_found")
	if r := get(f.admin, hidden); r.status != 200 || r.body["csat"] == nil {
		t.Errorf("admin should see the hidden rating: %d %v", r.status, r.body["csat"])
	}

	// The alert for the assignee is in their notifications.
	n := f.agent.do("GET", "/api/v1/notifications", nil)
	expect(t, n, 200, "")
	if !strings.Contains(string(n.raw), `"kind":"csat"`) {
		t.Errorf("no csat notification: %s", n.raw)
	}
}

func TestSurveySettingsAreAdminOnlyAndValidated(t *testing.T) {
	f := newInboxFixture(t)
	path := "/api/v1/mailboxes/" + f.mailboxA + "/csat"
	r := f.admin.do("GET", path, nil)
	expect(t, r, 200, "")
	if r.body["enabled"] != false || r.body["delay_hours"] != float64(0) {
		t.Errorf("default settings: %v", r.body)
	}
	expect(t, f.agent.do("PUT", path, map[string]any{"enabled": true, "delay_hours": 1}), 403, "forbidden")
	for _, hours := range []int{-1, 25} {
		expect(t, f.admin.do("PUT", path, map[string]any{"enabled": true, "delay_hours": hours}), 422, "validation_failed")
	}
	expect(t, f.admin.do("PUT", "/api/v1/mailboxes/0199a000-0000-7000-8000-000000000000/csat", map[string]any{"enabled": true, "delay_hours": 1}), 404, "not_found")
	expect(t, f.admin.do("PUT", path, map[string]any{"enabled": true, "delay_hours": 24}), 200, "")
	if r := f.admin.do("GET", path, nil); r.body["enabled"] != true || r.body["delay_hours"] != float64(24) {
		t.Errorf("saved settings: %v", r.body)
	}
	if n := f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'mailbox.csat_changed'`); n != 1 {
		t.Errorf("%d audit entries, want 1", n)
	}
}
