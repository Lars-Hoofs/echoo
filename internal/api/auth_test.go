package api

import (
	"context"
	"encoding/base32"
	"strings"
	"testing"
	"time"

	"echoo/internal/auth"
)

func TestLoginLogout(t *testing.T) {
	h := newHarness(t)
	u, pw := h.user("agent", "sanne@example.com")
	c := h.client()

	expect(t, c.login(u.Email, "wrong password!"), 401, "invalid_credentials")
	expect(t, c.login("nobody@example.com", pw), 401, "invalid_credentials")
	expect(t, c.do("GET", "/api/v1/me", nil), 401, "unauthenticated")

	r := c.login("  SANNE@example.com ", pw)
	expect(t, r, 200, "")
	if r.body["mfa_required"] != false || c.csrf == "" {
		t.Fatalf("unexpected login body %s", r.raw)
	}
	if !strings.HasPrefix(r.header.Get("Set-Cookie"), "__Host-echoo_session=") ||
		!strings.Contains(r.header.Get("Set-Cookie"), "HttpOnly") ||
		!strings.Contains(r.header.Get("Set-Cookie"), "SameSite=Lax") {
		t.Fatalf("cookie attributes: %s", r.header.Get("Set-Cookie"))
	}

	me := c.do("GET", "/api/v1/me", nil)
	expect(t, me, 200, "")
	if me.header.Get("Cache-Control") != "no-store" {
		t.Fatal("API responses must not be cached")
	}

	expect(t, c.do("POST", "/api/v1/auth/logout", nil), 204, "")
	expect(t, c.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
}

func TestDeactivatedUserCannotLogIn(t *testing.T) {
	h := newHarness(t)
	u, pw := h.user("agent", "gone@example.com")
	if _, err := h.q.SetUserDeactivated(context.Background(), dbqDeactivate(u.ID)); err != nil {
		t.Fatal(err)
	}
	expect(t, h.client().login(u.Email, pw), 401, "invalid_credentials")
}

func TestAccountLockout(t *testing.T) {
	h := newHarness(t)
	u, pw := h.user("agent", "target@example.com")
	for i := range 10 {
		c := h.client() // a new IP each time, so only the per-account limit applies
		expect(t, c.login(u.Email, "wrong password "+string(rune('a'+i))), 401, "invalid_credentials")
	}
	// The correct password is refused while the account is locked, with the same error.
	expect(t, h.client().login(u.Email, pw), 401, "invalid_credentials")

	var locked int
	if err := h.pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_log WHERE action = 'auth.account_locked'").Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if locked != 1 {
		t.Fatalf("expected one lock audit entry, got %d", locked)
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for range 10 {
		expect(t, c.login("nobody@example.com", "whatever password"), 401, "invalid_credentials")
	}
	expect(t, c.login("nobody@example.com", "whatever password"), 429, "rate_limited")
}

func TestCSRFAndOrigin(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")

	token := c.csrf
	c.csrf = ""
	expect(t, c.do("POST", "/api/v1/me/sessions/revoke-others", nil), 403, "csrf_failed")
	c.csrf = "not-the-token"
	expect(t, c.do("POST", "/api/v1/me/sessions/revoke-others", nil), 403, "csrf_failed")
	c.csrf = token
	expect(t, c.do("POST", "/api/v1/me/sessions/revoke-others", nil), 200, "")

	// A cross-site request is rejected even with a valid session and token.
	c.origin = "https://evil.example"
	expect(t, c.do("POST", "/api/v1/me/sessions/revoke-others", nil), 403, "csrf_failed")
}

func TestMFAEnrollmentAndLogin(t *testing.T) {
	h := newHarness(t)
	u, pw := h.user("agent", "mfa@example.com")
	c := h.client()
	expect(t, c.login(u.Email, pw), 200, "")

	setup := c.do("POST", "/api/v1/me/totp/setup", nil)
	expect(t, setup, 200, "")
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.body["secret"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(setup.body["uri"].(string), "otpauth://totp/Echoo:mfa@example.com?") {
		t.Fatalf("uri: %v", setup.body["uri"])
	}

	expect(t, c.do("POST", "/api/v1/me/totp/enable", map[string]string{"password": pw, "code": "000000"}), 401, "invalid_code")
	step := auth.TOTPStep(time.Now())
	valid := auth.TOTPCode(secret, step)
	expect(t, c.do("POST", "/api/v1/me/totp/enable", map[string]string{"code": valid}), 422, "validation_failed")
	expect(t, c.do("POST", "/api/v1/me/totp/enable", map[string]string{"password": "not my password", "code": valid}), 422, "validation_failed")
	enabled := c.do("POST", "/api/v1/me/totp/enable", map[string]string{"password": pw, "code": valid})
	expect(t, enabled, 200, "")
	codes := enabled.body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("expected 10 recovery codes, got %d", len(codes))
	}

	// Password alone now yields a pending session that cannot use the API.
	c2 := h.client()
	r := c2.login(u.Email, pw)
	expect(t, r, 200, "")
	if r.body["mfa_required"] != true {
		t.Fatal("expected mfa_required")
	}
	expect(t, c2.do("GET", "/api/v1/me", nil), 401, "unauthenticated")

	// The code used for enrollment cannot be replayed.
	expect(t, c2.do("POST", "/api/v1/auth/mfa", map[string]string{"code": auth.TOTPCode(secret, step)}), 401, "invalid_code")
	expect(t, c2.do("POST", "/api/v1/auth/mfa", map[string]string{"code": auth.TOTPCode(secret, step+1)}), 200, "")
	expect(t, c2.do("GET", "/api/v1/me", nil), 200, "")

	// Recovery codes work once.
	rc := codes[0].(string)
	c3 := h.client()
	expect(t, c3.login(u.Email, pw), 200, "")
	expect(t, c3.do("POST", "/api/v1/auth/mfa", map[string]string{"code": strings.ToUpper(rc)}), 200, "")
	me := c3.do("GET", "/api/v1/me", nil)
	if me.body["recovery_codes_remaining"] != float64(9) {
		t.Fatalf("remaining codes: %v", me.body["recovery_codes_remaining"])
	}
	c4 := h.client()
	expect(t, c4.login(u.Email, pw), 200, "")
	expect(t, c4.do("POST", "/api/v1/auth/mfa", map[string]string{"code": rc}), 401, "invalid_code")

	// Disabling requires the password.
	expect(t, c3.do("POST", "/api/v1/me/totp/disable", map[string]string{"password": "nope nope nope"}), 422, "validation_failed")
	expect(t, c3.do("POST", "/api/v1/me/totp/disable", map[string]string{"password": pw}), 204, "")
}

func TestTemporaryPasswordMustBeChanged(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	created := admin.do("POST", "/api/v1/users", map[string]string{"email": "new@example.com", "name": "Nieuw", "role": "agent"})
	expect(t, created, 201, "")
	temp := created.body["temporary_password"].(string)

	c := h.client()
	expect(t, c.login("new@example.com", temp), 200, "")
	me := c.do("GET", "/api/v1/me", nil)
	if me.body["must_change_password"] != true {
		t.Fatal("expected must_change_password")
	}
	expect(t, c.do("GET", "/api/v1/me/sessions", nil), 403, "password_change_required")
	expect(t, c.do("POST", "/api/v1/me/password", map[string]string{"current_password": temp, "new_password": "short"}), 422, "validation_failed")
	expect(t, c.do("POST", "/api/v1/me/password", map[string]string{"current_password": temp, "new_password": "a much better passphrase"}), 200, "")
	expect(t, c.do("GET", "/api/v1/me/sessions", nil), 200, "")
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	h := newHarness(t)
	u, pw := h.user("agent", "rotate@example.com")
	a, b := h.client(), h.client()
	expect(t, a.login(u.Email, pw), 200, "")
	expect(t, b.login(u.Email, pw), 200, "")

	expect(t, a.do("POST", "/api/v1/me/password", map[string]string{"current_password": pw, "new_password": "another good passphrase"}), 200, "")
	expect(t, a.do("GET", "/api/v1/me", nil), 200, "")
	expect(t, b.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
}

func TestRequireMFASetting(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	agent, _ := h.loggedIn("agent")
	expect(t, admin.do("PUT", "/api/v1/settings/security", map[string]bool{"require_mfa": true}), 200, "")

	expect(t, agent.do("GET", "/api/v1/me/sessions", nil), 403, "mfa_enrollment_required")
	me := agent.do("GET", "/api/v1/me", nil)
	if me.body["mfa_enrollment_required"] != true {
		t.Fatal("expected mfa_enrollment_required")
	}
	expect(t, agent.do("POST", "/api/v1/me/totp/setup", nil), 200, "")
}

func TestDeactivationEndsSessions(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	agent, u := h.loggedIn("agent")
	expect(t, admin.do("PATCH", "/api/v1/users/"+u.ID.String(), map[string]bool{"deactivated": true}), 200, "")
	expect(t, agent.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
}

func TestRequestValidation(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	expect(t, admin.do("POST", "/api/v1/users", map[string]any{"email": "x@example.com", "name": "X", "role": "agent", "extra": 1}), 400, "invalid_request")
	r := admin.do("POST", "/api/v1/users", map[string]string{"email": "Not An Email", "name": "", "role": "god"})
	expect(t, r, 422, "validation_failed")
	fields := r.body["error"].(map[string]any)["fields"].(map[string]any)
	if len(fields) != 3 {
		t.Fatalf("fields: %v", fields)
	}
	expect(t, admin.do("POST", "/api/v1/teams", map[string]string{"name": "Support"}), 201, "")
	expect(t, admin.do("POST", "/api/v1/teams", map[string]string{"name": "support"}), 422, "validation_failed")
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	h := newHarness(t)
	h.loggedIn("agent")
	ctx := context.Background()
	if _, err := h.pool.Exec(ctx, "UPDATE audit_log SET action = 'x'"); err == nil {
		t.Fatal("update of audit_log succeeded")
	}
	if _, err := h.pool.Exec(ctx, "DELETE FROM audit_log"); err == nil {
		t.Fatal("delete from audit_log succeeded")
	}
}

func TestSPAAndHeaders(t *testing.T) {
	h := newHarness(t)
	c := h.client()

	idx := c.do("GET", "/inbox/some/deep/link", nil)
	expect(t, idx, 200, "")
	csp := idx.header.Get("Content-Security-Policy")
	body := string(idx.raw)
	if strings.Contains(body, "__CSP_NONCE__") || !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("nonce not applied: %s / %s", csp, body)
	}
	start := strings.Index(body, `content="`) + len(`content="`)
	nonce := body[start : start+strings.Index(body[start:], `"`)]
	if !strings.Contains(csp, "'nonce-"+nonce+"'") {
		t.Fatalf("CSP nonce does not match page: %s vs %s", csp, nonce)
	}
	if again := c.do("GET", "/", nil); strings.Contains(string(again.raw), nonce) {
		t.Fatal("nonce reused across requests")
	}

	asset := c.do("GET", "/assets/app-1.js", nil)
	if asset.header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset caching: %q", asset.header.Get("Cache-Control"))
	}
	if dot := c.do("GET", "/.gitkeep", nil); !strings.Contains(string(dot.raw), `id="root"`) {
		t.Fatal("dotfiles must not be served")
	}
	for _, hdr := range []string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Strict-Transport-Security"} {
		if asset.header.Get(hdr) == "" {
			t.Errorf("missing %s", hdr)
		}
	}
	expect(t, c.do("GET", "/api/v1/nope", nil), 404, "not_found")

	sw := c.do("GET", "/sw.js", nil)
	if sw.header.Get("Content-Security-Policy") != "default-src 'self'; object-src 'none'; base-uri 'none'" || sw.header.Get("Cache-Control") != "no-cache" {
		t.Errorf("service worker headers = %v", sw.header)
	}
	if m := c.do("GET", "/manifest.webmanifest", nil); m.header.Get("Content-Type") != "application/manifest+json" {
		t.Errorf("manifest type = %q", m.header.Get("Content-Type"))
	}
}
