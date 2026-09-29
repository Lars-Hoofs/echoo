package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/audit"
	jobargs "echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/webhooks"
)

// withJobs gives the server an insert-only River client, enough to enqueue and inspect jobs.
func (h *harness) withJobs() *river.Client[pgx.Tx] {
	h.t.Helper()
	c, err := river.NewClient(riverpgxv5.New(h.pool), &river.Config{})
	if err != nil {
		h.t.Fatal(err)
	}
	h.srv.jobs = c
	return c
}

// fieldCode returns the validation code of one field of an error response.
func fieldCode(r response, field string) string {
	e, _ := r.body["error"].(map[string]any)
	fields, _ := e["fields"].(map[string]any)
	code, _ := fields[field].(string)
	return code
}

func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it.(string)
	}
	return out
}

func TestCreateWebhookShowsSecretOnce(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("admin")
	r := c.do("POST", "/api/v1/webhooks", map[string]any{
		"url": "https://hooks.example.com/echoo?key=abc", "events": []string{"message.created", "message.created", "contact.created"},
	})
	expect(t, r, 201, "")
	secret, _ := r.body["secret"].(string)
	hook, _ := r.body["webhook"].(map[string]any)
	if !strings.HasPrefix(secret, "whsec_") || len(secret) < 40 {
		t.Fatalf("secret = %q", secret)
	}
	if hook["include_content"] != false || hook["allow_http"] != false || hook["enabled"] != true {
		t.Errorf("defaults = %v", hook)
	}
	if got := stringSlice(hook["events"]); len(got) != 2 {
		t.Errorf("events = %v, want duplicates removed", got)
	}

	id, _ := hook["id"].(string)
	row, err := h.q.ListWebhooks(context.Background())
	if err != nil || len(row) != 1 {
		t.Fatalf("rows = %v, err = %v", row, err)
	}
	if strings.Contains(string(row[0].SecretEnc), secret) {
		t.Error("secret stored in clear")
	}
	if pt, err := h.keys.Decrypt(row[0].SecretEnc, keyring.AAD("webhooks", "secret_enc", id)); err != nil || string(pt) != secret {
		t.Errorf("decrypted secret = %q, err = %v", pt, err)
	}

	list := c.do("GET", "/api/v1/webhooks", nil)
	expect(t, list, 200, "")
	if strings.Contains(string(list.raw), secret) || strings.Contains(string(list.raw), "secret") {
		t.Errorf("list exposes the secret: %s", list.raw)
	}
	audit := h.auditText()
	if !strings.Contains(audit, "webhook.created") || strings.Contains(audit, "key=abc") || strings.Contains(audit, secret) {
		t.Errorf("audit = %s", audit)
	}
}

func TestWebhookURLValidation(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	owner, _ := h.loggedIn("owner")
	events := []string{"message.created"}
	for name, tc := range map[string]struct{ url, code string }{
		"plain http":         {"http://hooks.example.com/x", "https_required"},
		"ftp":                {"ftp://hooks.example.com/x", "invalid"},
		"no host":            {"https:///x", "invalid"},
		"credentials":        {"https://user:pw@hooks.example.com/x", "invalid"},
		"garbage":            {"not a url", "invalid"},
		"loopback ip":        {"https://127.0.0.1/x", "internal_host"},
		"localhost":          {"https://localhost/x", "internal_host"},
		"private ip":         {"https://10.0.0.5/x", "internal_host"},
		"metadata address":   {"https://169.254.169.254/latest", "internal_host"},
		"ipv6 loopback":      {"https://[::1]/x", "internal_host"},
		"mapped ipv4":        {"https://[::ffff:127.0.0.1]/x", "internal_host"},
		"odd port":           {"https://hooks.example.com:22/x", "port_not_allowed"},
		"decimal ip":         {"https://2130706433/x", "invalid"},
		"too long":           {"https://hooks.example.com/" + strings.Repeat("a", 2100), "invalid"},
		"port 80 with tls":   {"https://hooks.example.com:80/x", "port_not_allowed"},
		"internal port 8443": {"https://127.0.0.1:8443/x", "internal_host"},
		"http to internal":   {"http://127.0.0.1/x", "https_required"},
	} {
		r := owner.do("POST", "/api/v1/webhooks", map[string]any{"url": tc.url, "events": events})
		if r.status != 422 || fieldCode(r, "url") != tc.code {
			t.Errorf("%s: %d %s, want url=%s", name, r.status, r.raw, tc.code)
		}
	}
	if n := h.count(`SELECT count(*) FROM webhooks`); n != 0 {
		t.Errorf("%d webhooks stored despite validation errors", n)
	}

	for name, body := range map[string]map[string]any{
		"no events":      {"url": "https://hooks.example.com/x", "events": []string{}},
		"unknown event":  {"url": "https://hooks.example.com/x", "events": []string{"message.exploded"}},
		"test event":     {"url": "https://hooks.example.com/x", "events": []string{"webhook.test"}},
		"admin http":     {"url": "http://hooks.example.com/x", "events": events, "allow_http": true},
		"admin no https": {"url": "http://hooks.example.com/x", "events": events},
	} {
		if r := admin.do("POST", "/api/v1/webhooks", body); r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	r := admin.do("POST", "/api/v1/webhooks", map[string]any{"url": "http://hooks.example.com/x", "events": events, "allow_http": true})
	if fieldCode(r, "allow_http") != "owner_only" {
		t.Errorf("admin allow_http: %s", r.raw)
	}

	// Only the owner may allow plain http, and the destination check still applies.
	r = owner.do("POST", "/api/v1/webhooks", map[string]any{"url": "http://127.0.0.1/x", "events": events, "allow_http": true})
	if r.status != 422 || fieldCode(r, "url") != "internal_host" {
		t.Errorf("allow_http must not lift the internal network check: %d %s", r.status, r.raw)
	}
	expect(t, owner.do("POST", "/api/v1/webhooks", map[string]any{"url": "http://hooks.example.com/x", "events": events, "allow_http": true}), 201, "")
}

func TestUpdateAndDeleteWebhook(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	owner, _ := h.loggedIn("owner")
	r := admin.do("POST", "/api/v1/webhooks", map[string]any{"url": "https://hooks.example.com/a", "events": []string{"message.created"}})
	expect(t, r, 201, "")
	hook, _ := r.body["webhook"].(map[string]any)
	path := "/api/v1/webhooks/" + hook["id"].(string)

	u := admin.do("PATCH", path, map[string]any{"include_content": true, "events": []string{"message.sent"}, "url": "https://hooks.example.com/b"})
	expect(t, u, 200, "")
	got, _ := u.body["webhook"].(map[string]any)
	if got["include_content"] != true || got["url"] != "https://hooks.example.com/b" || stringSlice(got["events"])[0] != "message.sent" {
		t.Errorf("updated = %v", got)
	}
	for name, body := range map[string]map[string]any{
		"internal url": {"url": "https://127.0.0.1/x"},
		"bad events":   {"events": []string{"nope"}},
		"allow http":   {"allow_http": true, "url": "http://hooks.example.com/x"},
	} {
		if r := admin.do("PATCH", path, body); r.status != 422 {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	expect(t, owner.do("PATCH", path, map[string]any{"allow_http": true, "url": "http://hooks.example.com/x"}), 200, "")
	// An admin may keep editing a webhook the owner allowed to use http.
	expect(t, admin.do("PATCH", path, map[string]any{"include_content": false}), 200, "")

	// Re-enabling clears the failure streak and the reason.
	h.exec(`UPDATE webhooks SET enabled = false, disabled_reason = 'too_many_failures', consecutive_failures = 50`)
	e := admin.do("PATCH", path, map[string]any{"enabled": true})
	expect(t, e, 200, "")
	on, _ := e.body["webhook"].(map[string]any)
	if on["enabled"] != true || on["disabled_reason"] != "" || on["consecutive_failures"] != float64(0) {
		t.Errorf("re-enabled = %v", on)
	}

	expect(t, admin.do("PATCH", "/api/v1/webhooks/0199a000-0000-7000-8000-000000000000", map[string]any{"enabled": false}), 404, "")
	expect(t, admin.do("DELETE", path, nil), 204, "")
	expect(t, admin.do("DELETE", path, nil), 404, "")
	for _, a := range []string{"webhook.created", "webhook.updated", "webhook.deleted"} {
		if !strings.Contains(h.auditText(), a) {
			t.Errorf("no audit entry %s", a)
		}
	}
}

func TestWebhookTestSendAndDeliveryLog(t *testing.T) {
	h := newHarness(t)
	h.withJobs()
	admin, _ := h.loggedIn("admin")
	r := admin.do("POST", "/api/v1/webhooks", map[string]any{"url": "https://hooks.example.com/a", "events": []string{"message.created"}})
	hook, _ := r.body["webhook"].(map[string]any)
	id := hook["id"].(string)

	send := admin.do("POST", "/api/v1/webhooks/"+id+"/test", nil)
	expect(t, send, 202, "")
	del, _ := send.body["delivery"].(map[string]any)
	if del["event"] != "webhook.test" || del["status"] != "pending" {
		t.Errorf("delivery = %v", del)
	}
	if n := h.count(`SELECT count(*) FROM river_job WHERE kind = 'webhook.deliver' AND args->>'delivery_id' = $1 AND max_attempts = $2`, del["id"], webhooks.MaxAttempts); n != 1 {
		t.Errorf("jobs for the test delivery = %d, want 1", n)
	}

	// Make it look like a delivered event, then resend it.
	h.exec(`UPDATE webhook_deliveries SET status = 'failed', status_code = 500, error = 'http_status', duration_ms = 12, attempt = 6`)
	log := admin.do("GET", "/api/v1/webhooks/"+id+"/deliveries", nil)
	expect(t, log, 200, "")
	items, _ := log.body["deliveries"].([]any)
	if len(items) != 1 {
		t.Fatalf("deliveries = %v", log.body)
	}
	first, _ := items[0].(map[string]any)
	if first["status_code"] != float64(500) || first["error"] != "http_status" || first["attempt"] != float64(6) || first["duration_ms"] != float64(12) {
		t.Errorf("delivery = %v", first)
	}

	re := admin.do("POST", "/api/v1/webhooks/"+id+"/deliveries/"+first["id"].(string)+"/resend", nil)
	expect(t, re, 202, "")
	if again, _ := re.body["delivery"].(map[string]any); again["id"] == first["id"] || again["status"] != "pending" {
		t.Errorf("resend = %v", again)
	}
	if n := h.count(`SELECT count(*) FROM river_job WHERE kind = 'webhook.deliver'`); n != 2 {
		t.Errorf("jobs = %d, want 2", n)
	}

	other := admin.do("POST", "/api/v1/webhooks", map[string]any{"url": "https://hooks.example.com/other", "events": []string{"message.created"}})
	otherID := other.body["webhook"].(map[string]any)["id"].(string)
	expect(t, admin.do("POST", "/api/v1/webhooks/"+otherID+"/deliveries/"+first["id"].(string)+"/resend", nil), 404, "")
	expect(t, admin.do("GET", "/api/v1/webhooks/"+id+"/deliveries?before=nope", nil), 400, "")
	for _, a := range []string{"webhook.tested", "webhook.delivery_resent"} {
		if !strings.Contains(h.auditText(), a) {
			t.Errorf("no audit entry %s", a)
		}
	}
}

func TestDeliveryLogPagination(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	r := admin.do("POST", "/api/v1/webhooks", map[string]any{"url": "https://hooks.example.com/a", "events": []string{"message.created"}})
	id := r.body["webhook"].(map[string]any)["id"].(string)
	h.exec(`INSERT INTO webhook_events (type, fanned_out_at) VALUES ('webhook.test', now())`)
	h.exec(`INSERT INTO webhook_deliveries (webhook_id, event_id, event)
		SELECT $1, (SELECT max(id) FROM webhook_events), 'webhook.test' FROM generate_series(1, 55)`, id)

	page1 := admin.do("GET", "/api/v1/webhooks/"+id+"/deliveries", nil)
	items, _ := page1.body["deliveries"].([]any)
	next, _ := page1.body["next_before"].(string)
	if len(items) != 50 || next == "" {
		t.Fatalf("page 1: %d items, next %q", len(items), next)
	}
	page2 := admin.do("GET", "/api/v1/webhooks/"+id+"/deliveries?before="+next, nil)
	if items2, _ := page2.body["deliveries"].([]any); len(items2) != 5 || page2.body["next_before"] != nil {
		t.Errorf("page 2: %d items, next %v", len(items2), page2.body["next_before"])
	}
}

func (h *harness) insertJob(jobs *river.Client[pgx.Tx], args river.JobArgs, state string, attempt int, errText string) int64 {
	h.t.Helper()
	res, err := jobs.Insert(context.Background(), args, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	id := res.Job.ID
	finalized := "NULL"
	if state == "discarded" {
		finalized = "now()"
	}
	h.exec(fmt.Sprintf(`UPDATE river_job SET state = $2::river_job_state, attempt = $3, finalized_at = %s,
		errors = ARRAY[jsonb_build_object('at', now(), 'attempt', 1, 'error', $4::text)] WHERE id = $1`, finalized), id, state, attempt, errText)
	return id
}

func TestJobViewerListsAndRetriesFailedJobs(t *testing.T) {
	h := newHarness(t)
	jobs := h.withJobs()
	admin, _ := h.loggedIn("admin")

	retrying := h.insertJob(jobs, jobargs.SendOutbound{MessageID: "m1"}, "retryable", 2, "dial imap://user:hunter2@mail.example.com:993: connection refused")
	failed := h.insertJob(jobs, jobargs.ParseRaw{RawMessageID: "r1"}, "discarded", 3, strings.Repeat("boom ", 200))
	if _, err := jobs.Insert(context.Background(), jobargs.WakeSnoozed{}, nil); err != nil {
		t.Fatal(err)
	}

	list := admin.do("GET", "/api/v1/jobs", nil)
	expect(t, list, 200, "")
	items, _ := list.body["jobs"].([]any)
	if len(items) != 2 {
		t.Fatalf("jobs = %v, want the retrying and the discarded one only", list.body)
	}
	byID := map[float64]map[string]any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		byID[m["id"].(float64)] = m
	}
	r := byID[float64(retrying)]
	if r["kind"] != "mail.send" || r["state"] != "retryable" || r["attempt"] != float64(2) || r["scheduled_at"] == "" {
		t.Errorf("retrying job = %v", r)
	}
	if msg, _ := r["last_error"].(string); strings.Contains(msg, "hunter2") || !strings.Contains(msg, "://***@mail.example.com") {
		t.Errorf("last_error = %q, want credentials removed", msg)
	}
	if msg, _ := byID[float64(failed)]["last_error"].(string); len([]rune(msg)) > 301 {
		t.Errorf("last_error has %d runes, want it shortened", len([]rune(msg)))
	}

	expect(t, admin.do("POST", fmt.Sprintf("/api/v1/jobs/%d/retry", failed), nil), 200, "")
	if n := h.count(`SELECT count(*) FROM river_job WHERE id = $1 AND state = 'available'`, failed); n != 1 {
		t.Error("job was not scheduled again")
	}
	expect(t, admin.do("POST", "/api/v1/jobs/999999/retry", nil), 404, "not_found")
	expect(t, admin.do("POST", "/api/v1/jobs/abc/retry", nil), 404, "not_found")
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'job.retried' AND target_id = $1`, fmt.Sprint(failed)); n != 1 {
		t.Errorf("audit entries = %d, want 1", n)
	}
}

func TestRawMessageProblemsAndRetry(t *testing.T) {
	h := newHarness(t)
	h.withJobs()
	admin, _ := h.loggedIn("admin")
	var mailbox string
	if err := h.pool.QueryRow(context.Background(), `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'help@example.com') RETURNING id::text`).Scan(&mailbox); err != nil {
		t.Fatal(err)
	}
	add := func(status, reason string) string {
		var id string
		err := h.pool.QueryRow(context.Background(), `INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key, parse_status, parse_error)
			VALUES ($1, 'webhook', sha256(uuidv7()::text::bytea), 10, 'k', $2, $3) RETURNING id::text`, mailbox, status, reason).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	failed := add("failed", "invalid MIME structure")
	skipped := add("skipped", "message too large")
	parsed := add("parsed", "")
	add("pending", "")

	list := admin.do("GET", "/api/v1/raw-messages", nil)
	expect(t, list, 200, "")
	items, _ := list.body["raw_messages"].([]any)
	if len(items) != 2 {
		t.Fatalf("raw messages = %v", list.body)
	}
	seen := map[string]string{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		seen[m["id"].(string)] = m["reason"].(string)
		if m["mailbox_name"] != "Support" {
			t.Errorf("item = %v", m)
		}
	}
	if seen[failed] != "invalid MIME structure" || seen[skipped] != "message too large" {
		t.Errorf("reasons = %v", seen)
	}

	expect(t, admin.do("POST", "/api/v1/raw-messages/"+failed+"/retry", nil), 204, "")
	var status, reason string
	if err := h.pool.QueryRow(context.Background(), `SELECT parse_status, parse_error FROM raw_messages WHERE id = $1`, failed).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || reason != "" {
		t.Errorf("after retry: %s %q", status, reason)
	}
	if n := h.count(`SELECT count(*) FROM river_job WHERE kind = 'mail.parse' AND args->>'raw_message_id' = $1`, failed); n != 1 {
		t.Errorf("parse jobs = %d, want 1", n)
	}
	expect(t, admin.do("POST", "/api/v1/raw-messages/"+failed+"/retry", nil), 404, "not_found")
	expect(t, admin.do("POST", "/api/v1/raw-messages/"+parsed+"/retry", nil), 404, "not_found")
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'raw_message.retried' AND target_id = $1`, failed); n != 1 {
		t.Errorf("audit entries = %d, want 1", n)
	}
}

func TestAuditFiltersAndPagination(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	otherUser, _ := h.user("agent", "other@example.com")
	write := func(actor string, action string, at string) {
		h.exec(`INSERT INTO audit_log (at, actor_user_id, action) VALUES ($1::timestamptz, NULLIF($2, '')::uuid, $3)`, at, actor, action)
	}
	write(otherUser.ID.String(), "auth.login_failed", "2026-03-01T10:00:00Z")
	write(otherUser.ID.String(), "authXlogin_failed", "2026-03-02T10:00:00Z")
	write(otherUser.ID.String(), "user.created", "2026-03-03T10:00:00Z")
	write("", "user.renamed", "2026-03-04T10:00:00Z")

	actions := func(query string) []string {
		t.Helper()
		r := admin.do("GET", "/api/v1/audit?"+query, nil)
		expect(t, r, 200, "")
		var out []string
		for _, it := range r.body["entries"].([]any) {
			out = append(out, it.(map[string]any)["action"].(string))
		}
		return out
	}
	contains := func(list []string, want string) bool {
		for _, a := range list {
			if a == want {
				return true
			}
		}
		return false
	}

	// Creating the test users wrote user.created entries of their own, so narrow by actor.
	if got := actions("action=user.&actor=" + otherUser.ID.String()); len(got) != 1 || got[0] != "user.created" {
		t.Errorf("prefix user. by actor: %v", got)
	}
	if got := actions("action=user.renamed"); len(got) != 1 {
		t.Errorf("exact action: %v", got)
	}
	if got := actions("action=user."); !contains(got, "user.created") || !contains(got, "user.renamed") || contains(got, "auth.login_failed") {
		t.Errorf("prefix user.: %v", got)
	}
	// An underscore in the prefix is literal, not a wildcard.
	if got := actions("action=auth.login_"); len(got) != 1 || got[0] != "auth.login_failed" {
		t.Errorf("prefix auth.login_: %v", got)
	}
	if got := actions("actor=" + otherUser.ID.String() + "&action=user."); len(got) != 1 || got[0] != "user.created" {
		t.Errorf("actor filter: %v", got)
	}
	if got := actions("from=2026-03-02&to=2026-03-03&actor=" + otherUser.ID.String()); len(got) != 2 {
		t.Errorf("date range: %v", got)
	}
	if got := actions("to=2026-03-01&actor=" + otherUser.ID.String()); len(got) != 1 || got[0] != "auth.login_failed" {
		t.Errorf("to is inclusive: %v", got)
	}

	r := admin.do("GET", "/api/v1/audit?actor="+otherUser.ID.String(), nil)
	first, _ := r.body["entries"].([]any)[0].(map[string]any)
	if first["actor_name"] != otherUser.Name || first["actor_email"] != otherUser.Email {
		t.Errorf("actor names missing: %v", first)
	}

	for _, bad := range []string{"action=User.", "action=a%25b", "actor=nope", "from=yesterday", "to=2026-13-01", "before=0"} {
		if r := admin.do("GET", "/api/v1/audit?"+bad, nil); r.status != 400 {
			t.Errorf("%s: status %d", bad, r.status)
		}
	}

	// Keyset pagination walks all entries once, newest first.
	h.exec(`INSERT INTO audit_log (action) SELECT 'bulk.entry' FROM generate_series(1, 120)`)
	seen := map[float64]bool{}
	query := "action=bulk."
	for pages := 0; pages < 10; pages++ {
		page := admin.do("GET", "/api/v1/audit?"+query, nil)
		for _, it := range page.body["entries"].([]any) {
			id := it.(map[string]any)["id"].(float64)
			if seen[id] {
				t.Fatalf("entry %v returned twice", id)
			}
			seen[id] = true
		}
		next, ok := page.body["next_before"].(float64)
		if !ok {
			break
		}
		query = fmt.Sprintf("action=bulk.&before=%d", int64(next))
	}
	if len(seen) != 120 {
		t.Errorf("paged through %d entries, want 120", len(seen))
	}
}

func TestAuditExportStreamsCSVAndIsAudited(t *testing.T) {
	h := newHarness(t)
	admin, adminUser := h.loggedIn("admin")
	h.exec(`INSERT INTO audit_log (actor_user_id, action) VALUES ($1, 'test.one')`, adminUser.ID)
	h.exec(`INSERT INTO audit_log (action) SELECT 'bulk.entry' FROM generate_series(1, 1200)`)

	r := admin.do("GET", "/api/v1/audit/export?action=bulk.", nil)
	expect(t, r, 200, "")
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") || !strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("headers = %v", r.header)
	}
	rows, err := csv.NewReader(strings.NewReader(string(r.raw))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1201 || rows[0][0] != "id" || rows[0][6] != "action" || rows[1][6] != "bulk.entry" {
		t.Fatalf("csv has %d rows, first %v", len(rows), rows[0])
	}

	f := admin.do("GET", "/api/v1/audit/export?action=test.", nil)
	rows, err = csv.NewReader(strings.NewReader(string(f.raw))).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
	if rows[1][2] != adminUser.ID.String() || rows[1][3] != "Test admin" || rows[1][4] != adminUser.Email || rows[1][6] != "test.one" {
		t.Errorf("row = %v", rows[1])
	}
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND actor_user_id = $2`, audit.AuditExported, adminUser.ID); n != 2 {
		t.Errorf("export audit entries = %d, want 2", n)
	}
	expect(t, admin.do("GET", "/api/v1/audit/export?from=nope", nil), 400, "")
}

func TestCSVSafeNeutralizesFormulas(t *testing.T) {
	for in, want := range map[string]string{"=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@cmd": "'@cmd", "\tx": "'\tx", "plain": "plain", "": ""} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
