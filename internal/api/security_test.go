package api

import (
	"context"
	"encoding/base32"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
)

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) exec(query string, args ...any) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), query, args...); err != nil {
		h.t.Fatal(err)
	}
}

func (c *client) sessionToken() string {
	c.h.t.Helper()
	u, err := url.Parse(c.h.ts.URL)
	if err != nil {
		c.h.t.Fatal(err)
	}
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == "__Host-echoo_session" {
			return ck.Value
		}
	}
	c.h.t.Fatal("no session cookie")
	return ""
}

// withToken returns a fresh client that presents the given session token.
func (h *harness) withToken(token string) *client {
	c := h.client()
	u, err := url.Parse(h.ts.URL)
	if err != nil {
		h.t.Fatal(err)
	}
	c.http.Jar.SetCookies(u, []*http.Cookie{{Name: "__Host-echoo_session", Value: token, Path: "/", Secure: true}})
	return c
}

// mfaUser creates an agent with 2FA enabled and returns it with its password and TOTP secret.
func (h *harness) mfaUser(email string) (dbq.User, string, []byte) {
	h.t.Helper()
	u, pw := h.user("agent", email)
	c := h.client()
	expect(h.t, c.login(email, pw), 200, "")
	setup := c.do("POST", "/api/v1/me/totp/setup", nil)
	expect(h.t, setup, 200, "")
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.body["secret"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	expect(h.t, c.do("POST", "/api/v1/me/totp/enable", map[string]string{"password": pw, "code": auth.TOTPCode(secret, auth.TOTPStep(time.Now()))}), 200, "")
	return u, pw, secret
}

func TestSessionTokenRotatesAfterMFA(t *testing.T) {
	h := newHarness(t)
	u, pw, secret := h.mfaUser("rotate-mfa@example.com")

	c := h.client()
	expect(t, c.login(u.Email, pw), 200, "")
	pending := c.sessionToken()
	expect(t, c.do("POST", "/api/v1/auth/mfa", map[string]string{"code": auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1)}), 200, "")

	full := c.sessionToken()
	if full == pending {
		t.Fatal("the session token must change when the second factor completes")
	}
	expect(t, c.do("GET", "/api/v1/me", nil), 200, "")

	old := h.withToken(pending)
	expect(t, old.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
	expect(t, old.do("POST", "/api/v1/auth/mfa", map[string]string{"code": auth.TOTPCode(secret, auth.TOTPStep(time.Now())-1)}), 401, "unauthenticated")
}

func TestMFAWithoutSecretIsAnInvalidCode(t *testing.T) {
	h := newHarness(t)
	u, pw, _ := h.mfaUser("nosecret@example.com")
	c := h.client()
	expect(t, c.login(u.Email, pw), 200, "")
	h.exec("UPDATE users SET totp_secret_enc = NULL WHERE id = $1", u.ID)
	expect(t, c.do("POST", "/api/v1/auth/mfa", map[string]string{"code": "123456"}), 401, "invalid_code")
}

func TestLockoutStartsFreshAfterItExpires(t *testing.T) {
	h := newHarness(t)
	u, pw := h.user("agent", "expiry@example.com")
	fail := func() { expect(t, h.client().login(u.Email, "wrong password!"), 401, "invalid_credentials") }
	locks := func() int { return h.count("SELECT count(*) FROM audit_log WHERE action = 'auth.account_locked'") }

	for range 10 {
		fail()
	}
	if locks() != 1 {
		t.Fatalf("want one lock entry, got %d", locks())
	}
	h.exec("UPDATE users SET locked_until = now() - interval '1 minute' WHERE id = $1", u.ID)

	fail()
	cur, err := h.q.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.FailedLoginCount != 1 || cur.LockedUntil.Valid {
		t.Fatalf("one failure after an expired lock must not lock again: count=%d locked_until=%v", cur.FailedLoginCount, cur.LockedUntil)
	}
	expect(t, h.client().login(u.Email, pw), 200, "")

	for range 10 {
		fail()
	}
	if locks() != 2 {
		t.Fatalf("the second transition into locked must be audited, got %d entries", locks())
	}
	expect(t, h.client().login(u.Email, pw), 401, "invalid_credentials")
}

func TestExpiredLockIsRestartedByTheNextFailureInsteadOfExtended(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("agent", "noextend@example.com")
	h.exec("UPDATE users SET failed_login_count = 10, locked_until = now() + interval '10 minutes' WHERE id = $1", u.ID)
	before, err := h.q.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A failure that lands while the lock is active (a race) must not push the lock further out.
	if _, err := h.q.RecordLoginFailure(context.Background(), dbq.RecordLoginFailureParams{ID: u.ID, LockThreshold: 10, LockSeconds: 900}); err != nil {
		t.Fatal(err)
	}
	after, err := h.q.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.LockedUntil.Time.Equal(before.LockedUntil.Time) {
		t.Fatalf("active lock was extended: %v -> %v", before.LockedUntil.Time, after.LockedUntil.Time)
	}
}

func TestIPv6ClientsShareOneBucketPerSlash64(t *testing.T) {
	h := newHarness(t)
	a, b, other := h.client(), h.client(), h.client()
	a.ip, b.ip, other.ip = "2001:db8:1:2::1", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:3::1"
	for range 5 {
		expect(t, a.login("nobody@example.com", "whatever password"), 401, "invalid_credentials")
		expect(t, b.login("nobody@example.com", "whatever password"), 401, "invalid_credentials")
	}
	expect(t, a.login("nobody@example.com", "whatever password"), 429, "rate_limited")
	expect(t, b.login("nobody@example.com", "whatever password"), 429, "rate_limited")
	expect(t, other.login("nobody@example.com", "whatever password"), 401, "invalid_credentials")
}

func TestConcurrentMFAAttemptsOnLockedAccount(t *testing.T) {
	h := newHarness(t)
	u, pw, secret := h.mfaUser("locked-mfa@example.com")
	const attempts = 5
	clients := make([]*client, attempts)
	for i := range clients {
		clients[i] = h.client()
		expect(t, clients[i].login(u.Email, pw), 200, "")
	}
	fullSessions := func() int {
		return h.count("SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL AND NOT mfa_pending", u.ID)
	}
	before := fullSessions()
	h.exec("UPDATE users SET failed_login_count = 10, locked_until = now() + interval '15 minutes' WHERE id = $1", u.ID)

	statuses := make([]int, attempts)
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code := auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1)
			statuses[i] = c.do("POST", "/api/v1/auth/mfa", map[string]string{"code": code}).status
		}()
	}
	wg.Wait()
	for i, st := range statuses {
		if st != 401 {
			t.Errorf("attempt %d: want 401 while locked, got %d", i, st)
		}
	}
	if after := fullSessions(); after != before {
		t.Fatalf("full sessions were issued for a locked account: %d -> %d", before, after)
	}
}

func TestTemporaryPasswordExpires(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	created := admin.do("POST", "/api/v1/users", map[string]string{"email": "late@example.com", "name": "Late", "role": "agent"})
	expect(t, created, 201, "")
	temp := created.body["temporary_password"].(string)

	if n := h.count("SELECT count(*) FROM users WHERE email = 'late@example.com' AND temp_password_expires_at BETWEEN now() + interval '71 hours' AND now() + interval '73 hours'"); n != 1 {
		t.Fatal("a new temporary password must expire after 72 hours")
	}
	h.exec("UPDATE users SET temp_password_expires_at = now() - interval '1 second' WHERE email = 'late@example.com'")
	expect(t, h.client().login("late@example.com", temp), 401, "invalid_credentials")

	// A reset issues a fresh temporary password with a fresh window.
	target, err := h.q.GetUserByEmail(context.Background(), "late@example.com")
	if err != nil {
		t.Fatal(err)
	}
	reset := admin.do("POST", "/api/v1/users/"+target.ID.String()+"/reset-password", nil)
	expect(t, reset, 200, "")
	expect(t, h.client().login("late@example.com", reset.body["temporary_password"].(string)), 200, "")

	// Choosing a real password clears the expiry.
	c := h.client()
	expect(t, c.login("late@example.com", reset.body["temporary_password"].(string)), 200, "")
	expect(t, c.do("POST", "/api/v1/me/password", map[string]string{"current_password": reset.body["temporary_password"].(string), "new_password": "a much better passphrase"}), 200, "")
	if n := h.count("SELECT count(*) FROM users WHERE email = 'late@example.com' AND temp_password_expires_at IS NULL"); n != 1 {
		t.Fatal("expiry must be cleared once the password is changed")
	}
}

func TestAdminResetsRevokeTargetSessions(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	u, pw, _ := h.mfaUser("victim@example.com")

	signIn := func() *client {
		c := h.client()
		expect(t, c.login(u.Email, pw), 200, "")
		return c
	}

	// While 2FA is on, password sign-in only yields pending sessions; resetting 2FA kills them.
	pending := signIn()
	expect(t, admin.do("POST", "/api/v1/users/"+u.ID.String()+"/reset-mfa", nil), 204, "")
	expect(t, pending.do("GET", "/api/v1/me", nil), 401, "unauthenticated")

	// Without 2FA the same sign-in gives full sessions, which a password reset kills.
	live := signIn()
	expect(t, live.do("GET", "/api/v1/me", nil), 200, "")
	other := signIn()
	expect(t, admin.do("POST", "/api/v1/users/"+u.ID.String()+"/reset-password", nil), 200, "")
	expect(t, live.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
	expect(t, other.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
	expect(t, admin.do("GET", "/api/v1/me", nil), 200, "")
	if n := h.count("SELECT count(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL", u.ID); n != 0 {
		t.Fatalf("%d sessions of the target survived", n)
	}
}

func TestResetMFARevokesFullSessions(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	u, pw, secret := h.mfaUser("victim2@example.com")

	c := h.client()
	expect(t, c.login(u.Email, pw), 200, "")
	expect(t, c.do("POST", "/api/v1/auth/mfa", map[string]string{"code": auth.TOTPCode(secret, auth.TOTPStep(time.Now())+1)}), 200, "")
	expect(t, c.do("GET", "/api/v1/me", nil), 200, "")

	expect(t, admin.do("POST", "/api/v1/users/"+u.ID.String()+"/reset-mfa", nil), 204, "")
	expect(t, c.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
}

func TestTeamMemberChangesAuditWhoWasAddedAndRemoved(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	a, _ := h.user("agent", "a@example.com")
	b, _ := h.user("agent", "b@example.com")
	c, _ := h.user("agent", "c@example.com")
	team := admin.do("POST", "/api/v1/teams", map[string]string{"name": "Support"})
	expect(t, team, 201, "")
	path := "/api/v1/teams/" + team.body["team"].(map[string]any)["id"].(string) + "/members"

	expect(t, admin.do("PUT", path, map[string][]string{"user_ids": {a.ID.String(), b.ID.String()}}), 204, "")
	expect(t, admin.do("PUT", path, map[string][]string{"user_ids": {b.ID.String(), c.ID.String()}}), 204, "")

	entries := admin.do("GET", "/api/v1/audit", nil).body["entries"].([]any)
	var changes []map[string]any
	for _, e := range entries {
		if e.(map[string]any)["action"] == "team.members_changed" {
			changes = append(changes, e.(map[string]any)["metadata"].(map[string]any))
		}
	}
	if len(changes) != 2 {
		t.Fatalf("want 2 member change entries, got %d", len(changes))
	}
	// Newest first.
	latest, first := changes[0], changes[1]
	if got := stringsOf(latest["added"]); len(got) != 1 || got[0] != c.ID.String() {
		t.Errorf("added: %v", latest["added"])
	}
	if got := stringsOf(latest["removed"]); len(got) != 1 || got[0] != a.ID.String() {
		t.Errorf("removed: %v", latest["removed"])
	}
	if got := stringsOf(first["added"]); len(got) != 2 {
		t.Errorf("first change added: %v", first["added"])
	}
	if len(stringsOf(first["removed"])) != 0 {
		t.Errorf("first change removed: %v", first["removed"])
	}
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, e := range list {
		out[i], _ = e.(string)
	}
	return out
}

func TestRoleChangeAuthorizesTheLockedTarget(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	target, _ := h.user("agent", "promoted@example.com")

	// Another transaction promotes the target to admin but has not committed yet.
	ctx := context.Background()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "UPDATE users SET role = 'admin' WHERE id = $1", target.ID); err != nil {
		t.Fatal(err)
	}

	done := make(chan response, 1)
	go func() {
		done <- admin.do("PATCH", "/api/v1/users/"+target.ID.String(), map[string]string{"role": "readonly"})
	}()
	time.Sleep(300 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	expect(t, <-done, 403, "forbidden")

	cur, err := h.q.GetUser(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Role != "admin" {
		t.Fatalf("an admin was demoted by an admin: role=%s", cur.Role)
	}
}
