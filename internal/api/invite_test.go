package api

import (
	"bytes"
	"encoding/base32"
	"fmt"
	"strings"
	"testing"
	"time"

	"echoo/internal/auth"
	"echoo/internal/policy"
)

const testPassword = "een lange zin als wachtwoord"

func (h *harness) team(name string) string {
	h.t.Helper()
	var id string
	if err := h.pool.QueryRow(h.t.Context(), `INSERT INTO teams (name) VALUES ($1) RETURNING id::text`, name).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func invitation(email string, extra map[string]any) map[string]any {
	b := map[string]any{"email": email, "name": "Sanne de Vries", "role": "agent"}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

// tokenFrom extracts the token of a link with the given path prefix from the newest mail.
func tokenFrom(t *testing.T, m *mailStack, prefix string) string {
	t.Helper()
	msgs := m.deliver()
	if len(msgs) == 0 {
		t.Fatal("no mail was sent")
	}
	link := msgs[len(msgs)-1].Link(testOrigin + prefix)
	if link == "" {
		t.Fatalf("no %s link in %q", prefix, msgs[len(msgs)-1].Text())
	}
	return strings.TrimPrefix(link, testOrigin+prefix)
}

func TestInvitationFlow(t *testing.T) {
	h := newHarness(t)
	m := h.withMail(newFakeIdP(t, "x@example.com"))
	admin, adminUser := h.loggedIn("admin")
	team := h.team("Support")

	r := admin.do("POST", "/api/v1/invitations", invitation("Sanne@Example.com", map[string]any{"team_ids": []string{team}}))
	expect(t, r, 201, "")
	user := r.body["user"].(map[string]any)
	if user["invited_at"] == nil || user["deactivated"] != true || user["email"] != "sanne@example.com" {
		t.Fatalf("invited user %v", user)
	}

	// Nobody can sign in as the invited account, and it stays out of assignee lists.
	expect(t, h.client().login("sanne@example.com", "anything at all really"), 401, "invalid_credentials")
	assignees := admin.do("GET", "/api/v1/assignees", nil)
	if strings.Contains(string(assignees.raw), "sanne@example.com") {
		t.Error("an invited user shows up as an assignee")
	}

	msgs := m.deliver()
	if len(msgs) != 1 || len(msgs[0].To) != 1 || msgs[0].To[0] != "sanne@example.com" || msgs[0].Subject() != "Uitnodiging voor Echoo" {
		t.Fatalf("mail: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Text(), adminUser.Name) || !strings.Contains(msgs[0].Text(), "72 uur") {
		t.Errorf("text: %q", msgs[0].Text())
	}
	token := tokenFrom(t, m, "/uitnodiging/")
	if len(token) != 43 {
		t.Errorf("token %q is not 32 random bytes", token)
	}
	var stored, ttl string
	if err := h.pool.QueryRow(t.Context(), `SELECT encode(token_hash, 'hex'), (expires_at - created_at)::text FROM account_tokens WHERE purpose = 'invitation'`).Scan(&stored, &ttl); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, token) || ttl != "3 days" {
		t.Errorf("stored hash %q, ttl %q", stored, ttl)
	}

	anon := h.client()
	peek := anon.do("GET", "/api/v1/invitations/"+token, nil)
	expect(t, peek, 200, "")
	if peek.body["email"] != "sanne@example.com" || peek.body["name"] != "Sanne de Vries" {
		t.Errorf("peek %v", peek.body)
	}

	path := "/api/v1/invitations/" + token + "/accept"
	expect(t, anon.do("POST", path, map[string]any{"name": "Sanne", "password": "kort"}), 422, "validation_failed")
	expect(t, anon.do("POST", path, map[string]any{"name": "", "password": testPassword}), 422, "validation_failed")
	accepted := anon.do("POST", path, map[string]any{"name": "Sanne V.", "password": testPassword})
	expect(t, accepted, 200, "")
	me := anon.do("GET", "/api/v1/me", nil)
	expect(t, me, 200, "")
	got := me.body["user"].(map[string]any)
	if got["name"] != "Sanne V." || got["invited_at"] != nil || got["deactivated"] != false || me.body["must_change_password"] != false {
		t.Errorf("after accepting: %v", got)
	}
	if n := h.count(`SELECT count(*) FROM team_members WHERE team_id = $1 AND user_id = (SELECT id FROM users WHERE email = 'sanne@example.com')`, team); n != 1 {
		t.Error("the invited team membership was lost")
	}
	expect(t, h.client().login("sanne@example.com", testPassword), 200, "")

	// The link works once.
	expect(t, h.client().do("POST", path, map[string]any{"name": "Again", "password": testPassword}), 404, "link_invalid")
	expect(t, h.client().do("GET", "/api/v1/invitations/"+token, nil), 404, "link_invalid")

	for _, action := range []string{"user.invited", "user.invitation_accepted"} {
		if h.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND target_type = 'user'`, action) != 1 {
			t.Errorf("%s is not audited exactly once", action)
		}
	}
	if strings.Contains(h.auditText(), token) || strings.Contains(h.auditText(), testPassword) {
		t.Error("the audit log holds a secret")
	}
}

func TestInvitationLinksExpireAndAreReplacedByResends(t *testing.T) {
	h := newHarness(t)
	m := h.withMail(newFakeIdP(t, "x@example.com"))
	admin, _ := h.loggedIn("admin")
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("sanne@example.com", nil)), 201, "")
	first := tokenFrom(t, m, "/uitnodiging/")
	id := h.userID("sanne@example.com")

	expect(t, admin.do("POST", "/api/v1/users/"+id+"/invitation/resend", nil), 204, "")
	second := tokenFrom(t, m, "/uitnodiging/")
	if first == second {
		t.Fatal("resend reused the link")
	}
	expect(t, h.client().do("GET", "/api/v1/invitations/"+first, nil), 404, "link_invalid")
	expect(t, h.client().do("GET", "/api/v1/invitations/"+second, nil), 200, "")

	if _, err := h.pool.Exec(t.Context(), `UPDATE account_tokens SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	expect(t, h.client().do("POST", "/api/v1/invitations/"+second+"/accept", map[string]any{"name": "S", "password": testPassword}), 404, "link_invalid")

	// A fresh resend revives the invitation; revoking removes the user and voids the link.
	expect(t, admin.do("POST", "/api/v1/users/"+id+"/invitation/resend", nil), 204, "")
	third := tokenFrom(t, m, "/uitnodiging/")
	expect(t, admin.do("DELETE", "/api/v1/users/"+id+"/invitation", nil), 204, "")
	expect(t, h.client().do("GET", "/api/v1/invitations/"+third, nil), 404, "link_invalid")
	if h.count(`SELECT count(*) FROM users WHERE email = 'sanne@example.com'`) != 0 {
		t.Error("the revoked user still exists")
	}
	expect(t, admin.do("DELETE", "/api/v1/users/"+id+"/invitation", nil), 404, "not_found")
	expect(t, h.client().do("GET", "/api/v1/invitations/unknown-token", nil), 404, "link_invalid")
	for _, action := range []string{"user.invitation_resent", "user.invitation_revoked"} {
		if h.count(`SELECT count(*) FROM audit_log WHERE action = $1`, action) == 0 {
			t.Errorf("%s is not audited", action)
		}
	}
}

func (h *harness) userID(email string) string {
	h.t.Helper()
	var id string
	if err := h.pool.QueryRow(h.t.Context(), `SELECT id::text FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func TestInvitationRules(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	owner, _ := h.loggedIn("owner")

	// Without system mail there is nothing to send: the admin is told, and the temporary
	// password flow keeps working.
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("a@example.com", nil)), 409, "system_mail_unavailable")
	st := admin.do("GET", "/api/v1/settings/system-mail", nil)
	if st.body["available"] != false {
		t.Errorf("system mail: %v", st.body)
	}
	expect(t, admin.do("POST", "/api/v1/users", map[string]string{"email": "temp@example.com", "name": "Tijdelijk", "role": "agent"}), 201, "")

	h.withMail(newFakeIdP(t, "x@example.com"))
	if st := admin.do("GET", "/api/v1/settings/system-mail", nil); st.body["available"] != true || st.body["source"] != "smtp" {
		t.Errorf("system mail: %v", st.body)
	}
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("boss@example.com", map[string]any{"role": "admin"})), 403, "forbidden")
	expect(t, owner.do("POST", "/api/v1/invitations", invitation("boss@example.com", map[string]any{"role": "admin"})), 201, "")
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("boss@example.com", nil)), 422, "validation_failed")
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("not-an-address", nil)), 422, "validation_failed")
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("b@example.com", map[string]any{"role": "owner"})), 403, "forbidden")
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("c@example.com", map[string]any{"team_ids": []string{"0199a000-0000-7000-8000-000000000000"}})), 422, "validation_failed")
	if h.count(`SELECT count(*) FROM users WHERE email IN ('b@example.com', 'c@example.com')`) != 0 {
		t.Error("a rejected invitation left a user behind")
	}

	// An invited user cannot be reactivated around the invitation.
	id := h.userID("boss@example.com")
	expect(t, owner.do("PATCH", "/api/v1/users/"+id, map[string]any{"deactivated": false}), 422, "validation_failed")
	// Resending needs a pending invitation.
	agent, _ := h.user("agent", "plain@example.com")
	expect(t, admin.do("POST", "/api/v1/users/"+agent.ID.String()+"/invitation/resend", nil), 404, "link_invalid")
}

func TestPasswordResetFlow(t *testing.T) {
	h := newHarness(t)
	m := h.withMail(newFakeIdP(t, "x@example.com"))
	u, pw := h.user("agent", "reset@example.com")

	known := h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "Reset@Example.com"})
	unknown := h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "nobody@example.com"})
	garbage := h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "not an address"})
	expect(t, known, 202, "")
	if unknown.status != 202 || garbage.status != 202 || !bytes.Equal(known.raw, unknown.raw) || !bytes.Equal(known.raw, garbage.raw) {
		t.Fatalf("answers differ: %d %s / %d %s / %d %s", known.status, known.raw, unknown.status, unknown.raw, garbage.status, garbage.raw)
	}
	msgs := m.deliver()
	if len(msgs) != 1 || msgs[0].To[0] != "reset@example.com" || msgs[0].Subject() != "Wachtwoord herstellen voor Echoo" || !strings.Contains(msgs[0].Text(), "30 minuten") {
		t.Fatalf("mail: %+v", msgs)
	}
	token := tokenFrom(t, m, "/wachtwoord-herstellen/")
	var ttl string
	if err := h.pool.QueryRow(t.Context(), `SELECT (expires_at - created_at)::text FROM account_tokens WHERE purpose = 'password_reset'`).Scan(&ttl); err != nil || ttl != "00:30:00" {
		t.Errorf("ttl %q, %v", ttl, err)
	}

	// The account is signed in and locked out; the reset ends every session and the lock.
	session := h.client()
	expect(t, session.login(u.Email, pw), 200, "")
	if _, err := h.pool.Exec(t.Context(), `UPDATE users SET failed_login_count = 10, locked_until = now() + interval '15 minutes' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/auth/password-reset/" + token
	anon := h.client()
	peek := anon.do("GET", path, nil)
	expect(t, peek, 200, "")
	if peek.body["email"] != "reset@example.com" {
		t.Errorf("peek %v", peek.body)
	}
	expect(t, anon.do("POST", path, map[string]any{"password": "kort"}), 422, "validation_failed")
	expect(t, anon.do("POST", path, map[string]any{"password": testPassword}), 204, "")

	expect(t, session.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
	expect(t, h.client().login(u.Email, pw), 401, "invalid_credentials")
	expect(t, h.client().login(u.Email, testPassword), 200, "")
	expect(t, anon.do("POST", path, map[string]any{"password": testPassword + " again"}), 404, "link_invalid")
	expect(t, anon.do("GET", path, nil), 404, "link_invalid")
	for _, action := range []string{"auth.password_reset_requested", "auth.password_reset_completed"} {
		if h.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND target_type = 'user'`, action) != 1 {
			t.Errorf("%s is not audited exactly once", action)
		}
	}
	if strings.Contains(h.auditText(), token) || strings.Contains(h.auditText(), testPassword) {
		t.Error("the audit log holds a secret")
	}
}

func TestPasswordResetKnownAndUnknownAddressesDoTheSameWork(t *testing.T) {
	h := newHarness(t)
	m := h.withMail(newFakeIdP(t, "x@example.com"))
	h.user("agent", "known@example.com")

	measure := func(email string) (jobs, audits int) {
		jobsBefore := h.count(`SELECT count(*) FROM river_job WHERE kind = 'sysmail.send'`)
		auditsBefore := h.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.password_reset_requested'`)
		expect(t, h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": email}), 202, "")
		return h.count(`SELECT count(*) FROM river_job WHERE kind = 'sysmail.send'`) - jobsBefore,
			h.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.password_reset_requested'`) - auditsBefore
	}
	knownJobs, knownAudits := measure("known@example.com")
	unknownJobs, unknownAudits := measure("nobody@example.com")
	if knownJobs != 1 || unknownJobs != 1 || knownAudits != 1 || unknownAudits != 1 {
		t.Errorf("known: %d jobs, %d audits; unknown: %d jobs, %d audits; want 1 of each on both paths",
			knownJobs, knownAudits, unknownJobs, unknownAudits)
	}
	if n := len(m.deliver()); n != 1 {
		t.Errorf("%d mails delivered, want 1: the unknown address must not get one", n)
	}
	if h.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.password_reset_requested' AND target_type = ''`) != 1 {
		t.Error("the unknown request is not audited without a target")
	}
}

func TestPasswordResetStillNeedsTheSecondFactor(t *testing.T) {
	h := newHarness(t)
	m := h.withMail(newFakeIdP(t, "x@example.com"))
	u, pw := h.user("owner", "owner-mfa@example.com")
	c := h.client()
	expect(t, c.login(u.Email, pw), 200, "")
	setup := c.do("POST", "/api/v1/me/totp/setup", nil)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.body["secret"].(string))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, c.do("POST", "/api/v1/me/totp/enable", map[string]string{"password": pw, "code": auth.TOTPCode(secret, auth.TOTPStep(time.Now()))}), 200, "")

	expect(t, h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": u.Email}), 202, "")
	token := tokenFrom(t, m, "/wachtwoord-herstellen/")
	expect(t, h.client().do("POST", "/api/v1/auth/password-reset/"+token, map[string]any{"password": testPassword}), 204, "")

	login := h.client().login(u.Email, testPassword)
	expect(t, login, 200, "")
	if login.body["mfa_required"] != true {
		t.Fatalf("a reset must not bypass 2FA: %v", login.body)
	}
}

func TestPasswordResetIsRateLimitedAndSkipsInactiveAccounts(t *testing.T) {
	h := newHarness(t)
	m := h.withMail(newFakeIdP(t, "x@example.com"))
	h.user("agent", "busy@example.com")
	off, _ := h.user("agent", "off@example.com")
	if _, err := h.pool.Exec(t.Context(), `UPDATE users SET deactivated_at = now() WHERE id = $1`, off.ID); err != nil {
		t.Fatal(err)
	}

	// The same account, asked for repeatedly from different addresses: three mails, same answer.
	for range 6 {
		expect(t, h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "busy@example.com"}), 202, "")
	}
	if n := len(m.deliver()); n != 3 {
		t.Errorf("%d mails for one account, want 3", n)
	}
	expect(t, h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "off@example.com"}), 202, "")
	if n := len(m.deliver()); n != 3 {
		t.Errorf("a deactivated account got mail (%d total)", n)
	}

	// One client asking for many different accounts is stopped by the per-IP limit.
	flood := h.client()
	var last response
	for i := range 12 {
		last = flood.do("POST", "/api/v1/auth/password-reset", map[string]string{"email": fmt.Sprintf("nobody%d@example.com", i)})
	}
	expect(t, last, 429, "rate_limited")

	// An invited user has no password to reset either.
	h2 := newHarness(t)
	m2 := h2.withMail(newFakeIdP(t, "x@example.com"))
	admin, _ := h2.loggedIn("admin")
	expect(t, admin.do("POST", "/api/v1/invitations", invitation("pending@example.com", nil)), 201, "")
	before := len(m2.deliver())
	expect(t, h2.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "pending@example.com"}), 202, "")
	if len(m2.deliver()) != before {
		t.Error("an invited user received a password reset")
	}
}

func TestPasswordResetWithoutSystemMailSaysSo(t *testing.T) {
	h := newHarness(t)
	h.user("agent", "reset@example.com")
	expect(t, h.client().do("POST", "/api/v1/auth/password-reset", map[string]string{"email": "reset@example.com"}), 409, "system_mail_unavailable")
}

func TestNotificationSettings(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")
	other, _ := h.loggedIn("agent")

	r := c.do("GET", "/api/v1/me/notification-settings", nil)
	expect(t, r, 200, "")
	email := r.body["email"].(map[string]any)
	if email["mentions"] != false || email["assignments"] != false || r.body["system_mail_available"] != false {
		t.Fatalf("defaults %v", r.body)
	}
	put := c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"mentions": true, "assignments": false})
	expect(t, put, 200, "")
	if put.body["email"].(map[string]any)["mentions"] != true {
		t.Errorf("put %v", put.body)
	}
	if again := c.do("GET", "/api/v1/me/notification-settings", nil).body["email"].(map[string]any); again["mentions"] != true || again["assignments"] != false {
		t.Errorf("not persisted: %v", again)
	}
	if o := other.do("GET", "/api/v1/me/notification-settings", nil).body["email"].(map[string]any); o["mentions"] != false {
		t.Error("settings leaked to another user")
	}
	expect(t, c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"mentions": true}), 422, "validation_failed")
}

func TestInvitationTeamsNeedTeamManagement(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	h.withMail(newFakeIdP(t, "x@example.com"))
	team := h.team("Support")
	perms := append(policy.RolePermissions(policy.RoleAgent), "users.manage")
	inviter, _ := h.userWithRole(createRole(t, owner, "Inviter", perms...))
	expect(t, inviter.do("POST", "/api/v1/invitations", invitation("a@example.com", map[string]any{"team_ids": []string{team}})), 403, "forbidden")
	if h.count(`SELECT count(*) FROM users WHERE email = 'a@example.com'`) != 0 {
		t.Error("a refused invitation left a user behind")
	}
	expect(t, inviter.do("POST", "/api/v1/invitations", invitation("b@example.com", nil)), 201, "")
	withTeams := createRole(t, owner, "Inviter with teams", append(perms, "teams.manage")...)
	manager, _ := h.userWithRole(withTeams)
	expect(t, manager.do("POST", "/api/v1/invitations", invitation("c@example.com", map[string]any{"team_ids": []string{team}})), 201, "")
}
