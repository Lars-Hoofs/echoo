package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"golang.org/x/oauth2"

	"echoo/internal/config"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/mailauth"
	"echoo/internal/sysmail"
	"echoo/internal/sysmail/sinktest"
)

const oauthClientID = "client-123"

// fakeIdP is an OAuth token endpoint that hands out tokens for one authorization code.
type fakeIdP struct {
	*httptest.Server
	mu           sync.Mutex
	email        string
	noRefresh    bool
	invalidGrant bool
	verifiers    []string
}

func newFakeIdP(t *testing.T, email string) *fakeIdP {
	t.Helper()
	f := &fakeIdP{email: email}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("grant_type") == "refresh_token" && f.invalidGrant || r.PostForm.Get("grant_type") == "authorization_code" && r.PostForm.Get("code") != "the-code" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		f.verifiers = append(f.verifiers, r.PostForm.Get("code_verifier"))
		seg := func(v any) string {
			b, _ := json.Marshal(v)
			return base64.RawURLEncoding.EncodeToString(b)
		}
		resp := map[string]any{
			"access_token": "access-1", "token_type": "Bearer", "expires_in": 3600,
			"id_token": seg(map[string]string{"alg": "none"}) + "." + seg(map[string]any{"aud": oauthClientID, "email": f.email, "email_verified": true}) + ".x",
		}
		if !f.noRefresh {
			resp["refresh_token"] = "refresh-1"
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIdP) provider(id, authType string) *mailauth.Provider {
	return &mailauth.Provider{
		ID: id, Name: id, AuthType: authType, ClientID: oauthClientID, ClientSecret: "secret",
		Endpoint: oauth2.Endpoint{AuthURL: f.URL + "/auth", TokenURL: f.URL + "/token", AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   []string{"openid", "email"},
		IMAP:     mailauth.ServerSettings{Host: "imap.example.net", Port: 993, TLS: "implicit"},
		SMTP:     mailauth.ServerSettings{Host: "smtp.example.net", Port: 587, TLS: "starttls"},
	}
}

// withMail wires OAuth (Google and Microsoft, both served by idp), a system mail relay into a
// sink, and a job client that only inserts jobs.
func (h *harness) withMail(idp *fakeIdP) *mailStack {
	h.t.Helper()
	rc, err := river.NewClient(riverpgxv5.New(h.pool), &river.Config{})
	if err != nil {
		h.t.Fatal(err)
	}
	sink := sinktest.Start(h.t)
	oauth := mailauth.NewManager(h.pool, h.keys, mailauth.NewProvidersFrom(idp.provider("google", "oauth_google"), idp.provider("microsoft", "oauth_microsoft")), testOrigin)
	sender := sysmail.New(h.pool, h.keys, oauth, &config.Config{SystemSMTP: &config.SMTPRelay{
		Host: "127.0.0.1", Port: sink.Port, TLS: "implicit", From: "noreply@echoo.test", AllowInternal: true,
	}}, sink.Roots)
	WithOAuth(oauth)(h.srv)
	WithSysmail(sender)(h.srv)
	WithJobs(rc)(h.srv)
	return &mailStack{h: h, sink: sink, sender: sender, oauth: oauth}
}

type mailStack struct {
	h         *harness
	sink      *sinktest.Sink
	sender    *sysmail.Sender
	oauth     *mailauth.Manager
	delivered int
}

// deliver runs the queued system mail jobs, standing in for River's workers.
func (m *mailStack) deliver() []sinktest.Message {
	m.h.t.Helper()
	rows, err := m.h.pool.Query(context.Background(), `SELECT args FROM river_job WHERE kind = 'sysmail.send' ORDER BY id`)
	if err != nil {
		m.h.t.Fatal(err)
	}
	var queued []jobs.SysmailSend
	for rows.Next() {
		var a jobs.SysmailSend
		if err := rows.Scan(&a); err != nil {
			m.h.t.Fatal(err)
		}
		queued = append(queued, a)
	}
	rows.Close()
	for _, a := range queued[m.delivered:] {
		if err := sysmail.NewWorker(m.sender).Work(context.Background(), &river.Job[jobs.SysmailSend]{Args: a}); err != nil {
			m.h.t.Fatal(err)
		}
	}
	m.delivered = len(queued)
	return m.sink.Messages()
}

// rawGet issues a GET without following redirects.
func (c *client) rawGet(path string) (status int, location string) {
	c.h.t.Helper()
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequest("GET", c.h.ts.URL+path, nil)
	if err != nil {
		c.h.t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-For", c.ip)
	resp, err := hc.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}

// startOAuth asks for the authorization URL and returns its state and PKCE challenge.
func startOAuth(t *testing.T, c *client, provider, query string) (state, challenge, redirect string) {
	t.Helper()
	r := c.do("GET", "/api/v1/mailboxes/oauth/"+provider+"/start"+query, nil)
	expect(t, r, 200, "")
	u, err := url.Parse(r.body["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("client_id") != oauthClientID {
		t.Fatalf("authorization URL %s", u)
	}
	return q.Get("state"), q.Get("code_challenge"), q.Get("redirect_uri")
}

func callback(c *client, provider string, params url.Values) (string, string) {
	status, loc := c.rawGet("/oauth/callback/" + provider + "?" + params.Encode())
	if status != http.StatusSeeOther {
		c.h.t.Fatalf("callback status %d", status)
	}
	u, err := url.Parse(loc)
	if err != nil || u.Path != "/instellingen/mailboxen" {
		c.h.t.Fatalf("redirect to %q", loc)
	}
	return u.Query().Get("oauth"), u.Query().Get("oauth_error")
}

func (h *harness) oauthMailboxes() []dbq.Mailbox {
	h.t.Helper()
	all, err := h.q.ListMailboxes(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	var out []dbq.Mailbox
	for _, mb := range all {
		if mb.AuthType != "password" {
			out = append(out, mb)
		}
	}
	return out
}

func TestOAuthProvidersListsOnlyConfiguredOnes(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("admin")
	r := c.do("GET", "/api/v1/mailboxes/oauth/providers", nil)
	expect(t, r, 200, "")
	if got := r.body["providers"].([]any); len(got) != 0 {
		t.Fatalf("without configuration: %v", got)
	}
	expect(t, c.do("GET", "/api/v1/mailboxes/oauth/google/start", nil), 404, "oauth_provider_unavailable")

	h.withMail(newFakeIdP(t, "support@example.com"))
	r = c.do("GET", "/api/v1/mailboxes/oauth/providers", nil)
	got := r.body["providers"].([]any)
	if len(got) != 2 {
		t.Fatalf("providers: %v", got)
	}
	if uri := got[0].(map[string]any)["redirect_uri"]; uri != testOrigin+"/oauth/callback/google" {
		t.Errorf("redirect_uri %v", uri)
	}
	expect(t, c.do("GET", "/api/v1/mailboxes/oauth/yahoo/start", nil), 404, "oauth_provider_unavailable")
}

func TestOAuthCallbackCreatesAMailbox(t *testing.T) {
	h := newHarness(t)
	idp := newFakeIdP(t, "Support@Example.com")
	h.withMail(idp)
	admin, adminUser := h.loggedIn("admin")

	state, challenge, redirect := startOAuth(t, admin, "google", "?name=Klantenservice")
	if redirect != testOrigin+"/oauth/callback/google" {
		t.Errorf("redirect_uri %q", redirect)
	}
	before := h.reload.calls.Load()
	oauth, oerr := callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}})
	if oauth != "connected" || oerr != "" {
		t.Fatalf("outcome %q %q", oauth, oerr)
	}

	boxes := h.oauthMailboxes()
	if len(boxes) != 1 {
		t.Fatalf("%d oauth mailboxes", len(boxes))
	}
	mb := boxes[0]
	if mb.Name != "Klantenservice" || mb.EmailAddress != "support@example.com" || mb.AuthType != "oauth_google" ||
		mb.ImapHost != "imap.example.net" || mb.ImapPort != 993 || mb.SmtpHost != "smtp.example.net" || mb.SmtpPort != 587 || mb.SmtpTls != "starttls" ||
		mb.ImapUsername != "support@example.com" || mb.SmtpUsername != "support@example.com" {
		t.Errorf("mailbox %+v", mb)
	}
	if len(mb.ImapSecretEnc) != 0 || len(mb.SmtpSecretEnc) != 0 {
		t.Error("an OAuth mailbox must hold no password")
	}
	if !mb.OauthExpiresAt.Valid || !mb.OauthConnectedAt.Valid || len(mb.OauthTokenEnc) == 0 {
		t.Errorf("token columns: %+v", mb)
	}
	if strings.Contains(string(mb.OauthTokenEnc), "refresh-1") {
		t.Error("the token must be stored encrypted")
	}
	tok, err := h.srv.oauth.AccessToken(t.Context(), mb.ID)
	if err != nil || tok != "access-1" {
		t.Errorf("stored token unusable: %q %v", tok, err)
	}
	if h.reload.calls.Load() != before+1 {
		t.Error("the running sync was not told about the new mailbox")
	}

	// PKCE: what the provider saw at the token endpoint hashes to the challenge in the URL.
	idp.mu.Lock()
	verifier := idp.verifiers[0]
	idp.mu.Unlock()
	if want := oauth2.S256ChallengeFromVerifier(verifier); want != challenge {
		t.Errorf("verifier %q does not match the challenge", verifier)
	}
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'mailbox.oauth_connected' AND actor_user_id = $1`, adminUser.ID); n != 1 {
		t.Errorf("%d oauth audit entries", n)
	}
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'mailbox.created'`); n != 1 {
		t.Errorf("%d created entries", n)
	}
	if strings.Contains(h.auditText(), "refresh-1") || strings.Contains(h.auditText(), "access-1") {
		t.Error("tokens leaked into the audit log")
	}

	got := admin.do("GET", "/api/v1/mailboxes/"+uuidStr(mb.ID), nil).body["mailbox"].(map[string]any)
	if got["auth_type"] != "oauth_google" || got["needs_reconnect"] != false || got["oauth_connected_at"] == nil || got["imap_password_set"] != false {
		t.Errorf("mailbox JSON %v", got)
	}
}

func TestOAuthCallbackRejectsForgedStaleAndForeignState(t *testing.T) {
	h := newHarness(t)
	h.withMail(newFakeIdP(t, "support@example.com"))
	admin, _ := h.loggedIn("admin")
	other, _ := h.loggedIn("admin")
	anon := h.client()
	valid := func() string { s, _, _ := startOAuth(t, admin, "google", ""); return s }
	forge := func(mutate func(*mailauth.State), at time.Time) string {
		s, err := admin.sessionState()
		if err != nil {
			t.Fatal(err)
		}
		mutate(&s)
		param, err := mailauth.SealState(h.keys, s, at)
		if err != nil {
			t.Fatal(err)
		}
		return param
	}

	tampered := []byte(valid())
	tampered[len(tampered)/2] ^= 1
	cases := []struct {
		name   string
		c      *client
		params url.Values
		want   string
	}{
		{"tampered state", admin, url.Values{"code": {"the-code"}, "state": {string(tampered)}}, "state_invalid"},
		{"no state", admin, url.Values{"code": {"the-code"}}, "state_invalid"},
		{"state of another session", other, url.Values{"code": {"the-code"}, "state": {valid()}}, "state_invalid"},
		{"expired state", admin, url.Values{"code": {"the-code"}, "state": {forge(func(*mailauth.State) {}, time.Now().Add(-time.Hour))}}, "state_expired"},
		{"state for another provider", admin, url.Values{"code": {"the-code"}, "state": {forge(func(s *mailauth.State) { s.Provider = "microsoft" }, time.Now())}}, "state_invalid"},
		{"no code", admin, url.Values{"state": {valid()}}, "state_invalid"},
		{"provider says no", admin, url.Values{"error": {"access_denied"}, "state": {valid()}}, "provider_denied"},
		{"rejected code", admin, url.Values{"code": {"stolen"}, "state": {valid()}}, "exchange_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome, code := callback(tc.c, "google", tc.params)
			if outcome != "" || code != tc.want {
				t.Fatalf("outcome %q, error %q, want %q", outcome, code, tc.want)
			}
			if n := len(h.oauthMailboxes()); n != 0 {
				t.Fatalf("%d mailboxes were created", n)
			}
		})
	}

	// A valid state is not enough without the session it was made for.
	status, loc := anon.rawGet("/oauth/callback/google?code=the-code&state=" + url.QueryEscape(valid()))
	if status != http.StatusSeeOther || loc != "/inloggen" {
		t.Errorf("anonymous: %d %q", status, loc)
	}
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'mailbox.oauth_failed'`); n != len(cases) {
		t.Errorf("%d failures audited, want %d", n, len(cases))
	}
}

// sessionState builds a state for the client's current session, for forging variants of it.
func (c *client) sessionState() (mailauth.State, error) {
	var id pgtype.UUID
	if err := c.h.pool.QueryRow(context.Background(), `
		SELECT sessions.id FROM sessions JOIN users ON users.id = sessions.user_id
		WHERE users.role = 'admin' AND sessions.csrf_token = $1`, c.csrf).Scan(&id); err != nil {
		return mailauth.State{}, err
	}
	return mailauth.State{Provider: "google", SessionID: id.String(), Verifier: oauth2.GenerateVerifier()}, nil
}

func TestOAuthCallbackNeedsAnAdminWhoIsReady(t *testing.T) {
	h := newHarness(t)
	h.withMail(newFakeIdP(t, "support@example.com"))
	agent, agentUser := h.loggedIn("agent")
	var sessionID pgtype.UUID
	if err := h.pool.QueryRow(t.Context(), `SELECT id FROM sessions WHERE user_id = $1`, agentUser.ID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	state, err := mailauth.SealState(h.keys, mailauth.State{Provider: "google", SessionID: sessionID.String(), Verifier: oauth2.GenerateVerifier()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := callback(agent, "google", url.Values{"code": {"the-code"}, "state": {state}}); code != "forbidden" {
		t.Fatalf("agent: %q", code)
	}
	if n := len(h.oauthMailboxes()); n != 0 {
		t.Fatalf("an agent created %d mailboxes", n)
	}
}

func TestOAuthReconnectKeepsTheAccountAndRefusesAnotherOne(t *testing.T) {
	h := newHarness(t)
	idp := newFakeIdP(t, "support@example.com")
	h.withMail(idp)
	admin, _ := h.loggedIn("admin")

	state, _, _ := startOAuth(t, admin, "google", "")
	callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}})
	mb := h.oauthMailboxes()[0]
	if _, err := h.pool.Exec(t.Context(), `UPDATE mailboxes SET sync_state = 'auth_failed', sync_error = 'oauth_reauth_required' WHERE id = $1`, mb.ID); err != nil {
		t.Fatal(err)
	}
	got := admin.do("GET", "/api/v1/mailboxes/"+uuidStr(mb.ID), nil).body["mailbox"].(map[string]any)
	if got["needs_reconnect"] != true {
		t.Errorf("needs_reconnect %v", got["needs_reconnect"])
	}

	// Reconnecting with the same account clears the failure and restarts the sync.
	before := h.reload.calls.Load()
	state, _, _ = startOAuth(t, admin, "google", "?mailbox_id="+uuidStr(mb.ID))
	if outcome, _ := callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}}); outcome != "connected" {
		t.Fatal("reconnect failed")
	}
	after, err := h.q.GetMailbox(t.Context(), mb.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SyncError != "" || after.SyncState == "auth_failed" || !after.OauthConnectedAt.Time.After(mb.OauthConnectedAt.Time) || h.reload.calls.Load() != before+1 {
		t.Errorf("after reconnect: %q %q, reloads %d", after.SyncState, after.SyncError, h.reload.calls.Load()-before)
	}
	if n := len(h.oauthMailboxes()); n != 1 {
		t.Errorf("%d mailboxes, reconnecting must not add one", n)
	}

	// Signing in as someone else must not repoint the mailbox.
	idp.mu.Lock()
	idp.email = "someone.else@example.com"
	idp.mu.Unlock()
	state, _, _ = startOAuth(t, admin, "google", "?mailbox_id="+uuidStr(mb.ID))
	if _, code := callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}}); code != "email_mismatch" {
		t.Fatalf("got %q", code)
	}
	if again, _ := h.q.GetMailbox(t.Context(), mb.ID); again.EmailAddress != "support@example.com" || !again.OauthConnectedAt.Time.Equal(after.OauthConnectedAt.Time) {
		t.Error("the mailbox changed after a mismatched sign-in")
	}
	expect(t, admin.do("GET", "/api/v1/mailboxes/oauth/google/start?mailbox_id=not-a-uuid", nil), 404, "not_found")
}

func TestOAuthCallbackRefusesADuplicateAddressAndAMissingRefreshToken(t *testing.T) {
	h := newHarness(t)
	idp := newFakeIdP(t, "support@example.com")
	h.withMail(idp)
	admin, _ := h.loggedIn("admin")
	expect(t, admin.do("POST", "/api/v1/mailboxes", mailboxBody(nil)), 201, "")

	state, _, _ := startOAuth(t, admin, "google", "")
	if _, code := callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}}); code != "email_taken" {
		t.Errorf("duplicate: %q", code)
	}

	idp.mu.Lock()
	idp.noRefresh, idp.email = true, "fresh@example.com"
	idp.mu.Unlock()
	state, _, _ = startOAuth(t, admin, "google", "")
	if _, code := callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}}); code != "no_refresh_token" {
		t.Errorf("no refresh token: %q", code)
	}
	if n := len(h.oauthMailboxes()); n != 0 {
		t.Errorf("%d mailboxes", n)
	}
}

func TestOAuthMailboxesCannotBeEditedByHand(t *testing.T) {
	h := newHarness(t)
	h.withMail(newFakeIdP(t, "support@example.com"))
	admin, _ := h.loggedIn("admin")
	state, _, _ := startOAuth(t, admin, "microsoft", "")
	callback(admin, "microsoft", url.Values{"code": {"the-code"}, "state": {state}})
	mb := h.oauthMailboxes()[0]
	path := "/api/v1/mailboxes/" + uuidStr(mb.ID)

	for _, body := range []map[string]any{
		{"imap_password": "x"}, {"smtp_password": "x"}, {"imap_host": "evil.example.net"}, {"smtp_host": "evil.example.net"},
		{"email_address": "other@example.com"}, {"imap_username": "other"},
	} {
		if r := admin.do("PATCH", path, body); r.status != 422 {
			t.Errorf("%v: got %d, want 422 (%s)", body, r.status, r.raw)
		}
	}
	r := admin.do("PATCH", path, map[string]any{"name": "Nieuwe naam", "send_delay_seconds": 10})
	expect(t, r, 200, "")
	if r.body["mailbox"].(map[string]any)["name"] != "Nieuwe naam" {
		t.Errorf("rename failed: %s", r.raw)
	}
	if after, _ := h.q.GetMailbox(t.Context(), mb.ID); after.ImapHost != mb.ImapHost || len(after.ImapSecretEnc) != 0 {
		t.Error("connection settings changed")
	}
}

func TestMailboxTestReportsARevokedOAuthAccount(t *testing.T) {
	h := newHarness(t)
	idp := newFakeIdP(t, "support@example.com")
	h.withMail(idp)
	admin, _ := h.loggedIn("admin")
	state, _, _ := startOAuth(t, admin, "google", "")
	callback(admin, "google", url.Values{"code": {"the-code"}, "state": {state}})
	mb := h.oauthMailboxes()[0]

	// The stored access token expires and the provider then refuses the refresh token.
	enc, err := h.srv.oauth.Seal(mb.ID, mailauth.Token{AccessToken: "old", RefreshToken: "revoked", Expiry: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(t.Context(), `UPDATE mailboxes SET oauth_token_enc = $2 WHERE id = $1`, mb.ID, enc); err != nil {
		t.Fatal(err)
	}
	idp.mu.Lock()
	idp.invalidGrant = true
	idp.mu.Unlock()

	r := admin.do("POST", "/api/v1/mailboxes/test", map[string]any{"id": uuidStr(mb.ID)})
	expect(t, r, 200, "")
	for _, side := range []string{"imap", "smtp"} {
		res := r.body[side].(map[string]any)
		if res["ok"] != false || res["code"] != "oauth_reauth_required" {
			t.Errorf("%s: %v", side, res)
		}
	}
	got := admin.do("GET", "/api/v1/mailboxes/"+uuidStr(mb.ID), nil).body["mailbox"].(map[string]any)
	if got["needs_reconnect"] != true {
		t.Errorf("the mailbox was not flagged: %v", got["needs_reconnect"])
	}
}
