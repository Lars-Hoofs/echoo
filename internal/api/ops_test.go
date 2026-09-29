package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"echoo/internal/keyring"
)

func TestRetentionSettingsRoundTripPreviewAndAudit(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	ctx := context.Background()

	r := admin.do("GET", "/api/v1/settings/retention", nil)
	expect(t, r, 200, "")
	if r.body["audit_months"] != float64(12) || r.body["last_run"] != nil {
		t.Fatalf("defaults: %s", r.raw)
	}
	global := r.body["global"].(map[string]any)
	if global["closed_conversation_months"] != nil || global["spam_days"] != nil {
		t.Fatalf("nothing is deleted by default: %s", r.raw)
	}

	var box string
	if err := h.pool.QueryRow(ctx, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id::text`).Scan(&box); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `INSERT INTO conversations (mailbox_id, subject, status, last_message_at, resolved_at)
		SELECT $1, 'x', 'closed', now() - interval '400 days', now() - interval '400 days' FROM generate_series(1, 3)`, box); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"global":       map[string]any{"closed_conversation_months": 6, "attachment_months": nil, "spam_days": 30},
		"audit_months": 12,
		"mailboxes":    []map[string]any{},
	}

	r = admin.do("POST", "/api/v1/settings/retention/preview", body)
	expect(t, r, 200, "")
	closed := r.body["closed_conversations"].(map[string]any)
	if closed["conversations"] != float64(3) {
		t.Fatalf("preview: %s", r.raw)
	}
	if n := count(t, h, `SELECT count(*) FROM conversations`); n != 3 {
		t.Fatalf("a preview must not delete: %d conversations left", n)
	}
	if n := count(t, h, `SELECT count(*) FROM settings WHERE key = 'retention'`); n != 0 {
		t.Fatal("a preview must not save")
	}

	body["mailboxes"] = []map[string]any{{"mailbox_id": box, "closed_conversation_months": nil, "attachment_months": 3, "spam_days": nil}}
	r = admin.do("PUT", "/api/v1/settings/retention", body)
	expect(t, r, 200, "")
	mailboxes := r.body["mailboxes"].([]any)
	if len(mailboxes) != 1 || mailboxes[0].(map[string]any)["attachment_months"] != float64(3) {
		t.Fatalf("saved: %s", r.raw)
	}
	var meta string
	if err := h.pool.QueryRow(ctx, `SELECT metadata::text FROM audit_log WHERE action = 'settings.retention_changed'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, `"audit_months": 12`) || !strings.Contains(meta, box) {
		t.Errorf("audit metadata = %s", meta)
	}
}

func TestRetentionValidation(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	bad := map[string]any{
		"global":       map[string]any{"closed_conversation_months": 0, "attachment_months": nil, "spam_days": 99999},
		"audit_months": 1,
		"mailboxes":    []map[string]any{{"mailbox_id": "0199a000-0000-7000-8000-000000000000", "closed_conversation_months": nil, "attachment_months": nil, "spam_days": nil}},
	}
	for _, path := range []string{"PUT /api/v1/settings/retention", "POST /api/v1/settings/retention/preview"} {
		method, url, _ := strings.Cut(path, " ")
		r := admin.do(method, url, bad)
		expect(t, r, 422, "validation_failed")
		fields := r.body["error"].(map[string]any)["fields"].(map[string]any)
		for _, f := range []string{"global.closed_conversation_months", "global.spam_days", "audit_months", "mailboxes[0].mailbox_id"} {
			if fields[f] == nil {
				t.Errorf("%s: no error for %s: %v", path, f, fields)
			}
		}
	}
}

func count(t *testing.T, h *harness, sql string) int {
	t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestKeyStatusIsOwnerOnlyAndCountsPerKey(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	adm, _ := h.loggedIn("admin")
	expect(t, adm.do("GET", "/api/v1/admin/keys", nil), 403, "forbidden")

	ctx := context.Background()
	var box string
	if err := h.pool.QueryRow(ctx, `INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id::text`).Scan(&box); err != nil {
		t.Fatal(err)
	}
	sealed, err := h.keys.Encrypt([]byte("pw"), keyring.AAD("mailboxes", "imap_secret_enc", box))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE mailboxes SET imap_secret_enc = $1 WHERE id = $2`, sealed, box); err != nil {
		t.Fatal(err)
	}
	// A value under a key nobody configured any more.
	other, err := keyring.Parse("k9:" + "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=")
	if err != nil {
		t.Fatal(err)
	}
	stray, err := other.Encrypt([]byte("x"), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE mailboxes SET smtp_secret_enc = $1 WHERE id = $2`, stray, box); err != nil {
		t.Fatal(err)
	}

	r := owner.do("GET", "/api/v1/admin/keys", nil)
	expect(t, r, 200, "")
	keys := r.body["keys"].([]any)
	first := keys[0].(map[string]any)
	if len(keys) != 1 || first["id"] != "k1" || first["active"] != true || first["values"] != float64(1) {
		t.Fatalf("keys: %s", r.raw)
	}
	if fmt.Sprint(first["columns"]) != "map[mailboxes.imap_secret_enc:1]" {
		t.Errorf("columns: %v", first["columns"])
	}
	unknown := r.body["unknown"].([]any)
	if len(unknown) != 1 || unknown[0].(map[string]any)["id"] != "k9" {
		t.Errorf("unknown: %s", r.raw)
	}
}
