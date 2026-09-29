package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
)

// oidcProvider is an OpenID Connect provider: discovery, key set and a token endpoint that signs
// ID tokens with a test key. It checks what a real provider checks (client credentials, the
// redirect URI and the PKCE verifier), so the flow is tested end to end.
type oidcProvider struct {
	t        *testing.T
	srv      *httptest.Server
	key      *rsa.PrivateKey
	clientID string
	secret   string

	mu     sync.Mutex
	grants map[string]idpGrant
}

type idpGrant struct {
	claims    map[string]any
	challenge string
}

func newOIDCProvider(t *testing.T) *oidcProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &oidcProvider{t: t, key: key, clientID: "echoo-client", secret: "s3cret-value", grants: map[string]idpGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer": p.issuer(), "authorization_endpoint": p.issuer() + "/authorize", "token_endpoint": p.issuer() + "/token",
			"jwks_uri": p.issuer() + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": b64(p.key.N.Bytes()), "e": b64(big.NewInt(int64(p.key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *oidcProvider) issuer() string { return p.srv.URL }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *oidcProvider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.clientID || secret != p.secret {
		writeTestJSONStatus(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	grant, ok := p.grants[r.PostForm.Get("code")]
	delete(p.grants, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || b64(sum[:]) != grant.challenge {
		writeTestJSONStatus(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
		return
	}
	writeTestJSON(w, map[string]any{"access_token": "at-" + r.PostForm.Get("code"), "token_type": "Bearer", "expires_in": 300, "id_token": p.sign(grant.claims)})
}

func writeTestJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *oidcProvider) sign(claims map[string]any) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

// ssoFixture is a workspace with SSO switched on against a fake provider.
type ssoFixture struct {
	h     *harness
	idp   *oidcProvider
	owner *client
}

func newSSOFixture(t *testing.T) *ssoFixture {
	t.Helper()
	h := newHarness(t)
	f := &ssoFixture{h: h, idp: newOIDCProvider(t)}
	f.owner, _ = h.loggedIn("owner")
	f.configure(nil)
	return f
}

func (f *ssoFixture) settings(tweak func(map[string]any)) map[string]any {
	s := map[string]any{
		"enabled": true, "issuer_url": f.idp.issuer(), "client_id": f.idp.clientID, "client_secret": f.idp.secret,
		"allowed_domains": []string{"example.com"}, "button_label": "Bedrijfsaccount", "required": false, "auto_provision": false,
		"default_role": "agent", "trust_idp_mfa": false, "trust_missing_email_verified": false, "allow_internal_issuer": true,
	}
	if tweak != nil {
		tweak(s)
	}
	return s
}

func (f *ssoFixture) configure(tweak func(map[string]any)) response {
	f.h.t.Helper()
	r := f.owner.do("PUT", "/api/v1/settings/sso", f.settings(tweak))
	if tweak == nil {
		expect(f.h.t, r, 200, "")
	}
	return r
}

// browser is a client that does not follow redirects, so a test can read each one.
func (f *ssoFixture) browser() *client {
	c := f.h.client()
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func (f *ssoFixture) baseClaims(email string) map[string]any {
	return map[string]any{
		"iss": f.idp.issuer(), "aud": f.idp.clientID, "sub": "sub-" + email, "email": email, "email_verified": true,
		"name": "Sam Voorbeeld", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
	}
}

type ssoResult struct {
	location string
	status   int
	cookies  http.Header
}

// signIn runs the whole flow in browser b: start, the provider's answer with claims (changed by
// tweak), and the callback. It returns where the callback sent the browser.
func (f *ssoFixture) signIn(b *client, email string, tweak func(claims map[string]any)) ssoResult {
	f.h.t.Helper()
	start := b.do("GET", "/auth/sso/start", nil)
	if start.status != http.StatusFound {
		f.h.t.Fatalf("start: %d %s (location %s)", start.status, start.raw, start.header.Get("Location"))
	}
	loc, err := url.Parse(start.header.Get("Location"))
	if err != nil {
		f.h.t.Fatal(err)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" ||
		q.Get("response_type") != "code" || q.Get("client_id") != f.idp.clientID || q.Get("redirect_uri") != testOrigin+"/auth/sso/callback" {
		f.h.t.Fatalf("authorization request: %v", q)
	}
	claims := f.baseClaims(email)
	claims["nonce"] = q.Get("nonce")
	if tweak != nil {
		tweak(claims)
	}
	code := fmt.Sprintf("code-%d", f.h.ipSeq.Add(1))
	f.idp.mu.Lock()
	f.idp.grants[code] = idpGrant{claims: claims, challenge: q.Get("code_challenge")}
	f.idp.mu.Unlock()
	return f.callback(b, url.Values{"code": {code}, "state": {q.Get("state")}})
}

func (f *ssoFixture) callback(b *client, v url.Values) ssoResult {
	f.h.t.Helper()
	r := b.do("GET", "/auth/sso/callback?"+v.Encode(), nil)
	return ssoResult{location: r.header.Get("Location"), status: r.status, cookies: r.header}
}

func (r ssoResult) errorCode(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(r.location)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("sso_error")
}

func (f *ssoFixture) expectSignedIn(b *client, res ssoResult, email string) {
	f.h.t.Helper()
	if res.status != http.StatusSeeOther || res.location != "/" {
		f.h.t.Fatalf("callback: %d -> %q", res.status, res.location)
	}
	me := b.do("GET", "/api/v1/me", nil)
	expect(f.h.t, me, 200, "")
	if got := me.body["user"].(map[string]any)["email"]; got != email {
		f.h.t.Fatalf("signed in as %v", got)
	}
}

func (f *ssoFixture) expectRefused(b *client, res ssoResult, reason string) {
	f.h.t.Helper()
	if res.status != http.StatusSeeOther || res.errorCode(f.h.t) != reason {
		f.h.t.Fatalf("callback: %d -> %q, want sso_error=%s", res.status, res.location, reason)
	}
	expect(f.h.t, b.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
}

// failureReasons returns the reason of every audited failed login, oldest first.
func (f *ssoFixture) failureReasons() []string {
	f.h.t.Helper()
	rows, err := f.h.pool.Query(context.Background(), `SELECT metadata->>'reason' FROM audit_log WHERE action = 'auth.login_failed' ORDER BY id`)
	if err != nil {
		f.h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			f.h.t.Fatal(err)
		}
		out = append(out, reason)
	}
	return out
}

func (f *ssoFixture) count(sql string, args ...any) int {
	f.h.t.Helper()
	var n int
	if err := f.h.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.h.t.Fatal(err)
	}
	return n
}

func TestSSOSignsInAnExistingUser(t *testing.T) {
	f := newSSOFixture(t)
	u, _ := f.h.user("agent", "sam@example.com")
	b := f.browser()
	res := f.signIn(b, "Sam@Example.com", nil)
	f.expectSignedIn(b, res, u.Email)

	if n := f.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND metadata->>'method' = 'sso'`); n != 1 {
		t.Fatalf("audited sso logins: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, u.ID); n != 1 {
		t.Fatalf("sessions: %d", n)
	}
	// The flow cookie is single use: replaying the callback fails.
	replay := f.callback(b, url.Values{"code": {"whatever"}, "state": {"whatever"}})
	if replay.errorCode(t) != "invalid_state" {
		t.Fatalf("replay: %q", replay.location)
	}
}

func TestSSOSessionCookieMatchesPasswordLogin(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "sam@example.com")
	b := f.browser()
	res := f.signIn(b, "sam@example.com", nil)
	var session string
	for _, c := range res.cookies.Values("Set-Cookie") {
		if strings.HasPrefix(c, "__Host-echoo_session=") {
			session = c
		}
	}
	for _, want := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(session, want) {
			t.Errorf("session cookie %q lacks %s", session, want)
		}
	}
}

func TestSSOFlowCookieAttributes(t *testing.T) {
	f := newSSOFixture(t)
	start := f.browser().do("GET", "/auth/sso/start", nil)
	var flow string
	for _, c := range start.header.Values("Set-Cookie") {
		if strings.HasPrefix(c, "__Host-echoo_sso=") {
			flow = c
		}
	}
	for _, want := range []string{"HttpOnly", "Secure", "SameSite=Lax", "Path=/", "Max-Age=600"} {
		if !strings.Contains(flow, want) {
			t.Errorf("flow cookie %q lacks %s", flow, want)
		}
	}
	loc, _ := url.Parse(start.header.Get("Location"))
	if strings.Contains(flow, loc.Query().Get("state")) || strings.Contains(flow, loc.Query().Get("nonce")) {
		t.Error("the flow cookie must be encrypted")
	}
}

func TestSSORejectsInvalidIDTokens(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "sam@example.com")
	cases := map[string]struct {
		tweak  func(map[string]any)
		reason string
	}{
		"wrong nonce":    {func(c map[string]any) { c["nonce"] = "another" }, "nonce_mismatch"},
		"missing nonce":  {func(c map[string]any) { delete(c, "nonce") }, "nonce_mismatch"},
		"wrong audience": {func(c map[string]any) { c["aud"] = "someone-else" }, "invalid_id_token"},
		"wrong issuer":   {func(c map[string]any) { c["iss"] = "https://evil.example" }, "invalid_id_token"},
		"expired":        {func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, "invalid_id_token"},
		"unverified":     {func(c map[string]any) { c["email_verified"] = false }, "email_unverified"},
		"unverified str": {func(c map[string]any) { c["email_verified"] = "false" }, "email_unverified"},
		"no verified":    {func(c map[string]any) { delete(c, "email_verified") }, "email_unverified"},
		"no email":       {func(c map[string]any) { delete(c, "email") }, "email_missing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := f.browser()
			f.expectRefused(b, f.signIn(b, "sam@example.com", c.tweak), c.reason)
		})
	}
	if n := f.count(`SELECT count(*) FROM sessions`); n != 1 { // the owner's
		t.Fatalf("sessions after refused sign-ins: %d", n)
	}
	got := f.failureReasons()
	if len(got) != len(cases) {
		t.Fatalf("audited failures: %v", got)
	}
	for _, reason := range got {
		if strings.ContainsAny(reason, " .") || strings.Contains(reason, "eyJ") {
			t.Errorf("audit reason %q must be a plain code", reason)
		}
	}
}

func TestSSOAcceptsMissingEmailVerifiedOnlyWhenTrusted(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "sam@example.com")
	noClaim := func(c map[string]any) { delete(c, "email_verified") }
	b := f.browser()
	f.expectRefused(b, f.signIn(b, "sam@example.com", noClaim), "email_unverified")

	expect(t, f.configure(func(s map[string]any) { s["trust_missing_email_verified"] = true }), 200, "")
	b = f.browser()
	f.expectSignedIn(b, f.signIn(b, "sam@example.com", noClaim), "sam@example.com")
	// A claim that says false is refused whatever the setting.
	b = f.browser()
	f.expectRefused(b, f.signIn(b, "sam@example.com", func(c map[string]any) { c["email_verified"] = false }), "email_unverified")
}

func TestSSOStateAndCookieAreChecked(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "sam@example.com")

	// Another browser, without the flow cookie, cannot finish a flow that was started elsewhere.
	starter := f.browser()
	start := starter.do("GET", "/auth/sso/start", nil)
	loc, _ := url.Parse(start.header.Get("Location"))
	other := f.browser()
	res := f.callback(other, url.Values{"code": {"x"}, "state": {loc.Query().Get("state")}})
	if res.errorCode(t) != "invalid_state" {
		t.Fatalf("callback without cookie: %q", res.location)
	}

	// The right browser with another state fails as well, and the flow is used up.
	res = f.callback(starter, url.Values{"code": {"x"}, "state": {"forged"}})
	if res.errorCode(t) != "invalid_state" {
		t.Fatalf("forged state: %q", res.location)
	}
	res = f.callback(starter, url.Values{"code": {"x"}, "state": {loc.Query().Get("state")}})
	if res.errorCode(t) != "invalid_state" {
		t.Fatalf("flow reused after a failed attempt: %q", res.location)
	}

	// A provider error is reported as such and audited without its text.
	b := f.browser()
	start = b.do("GET", "/auth/sso/start", nil)
	loc, _ = url.Parse(start.header.Get("Location"))
	res = f.callback(b, url.Values{"error": {"access_denied"}, "error_description": {"secret detail"}, "state": {loc.Query().Get("state")}})
	if res.errorCode(t) != "idp_error" {
		t.Fatalf("provider error: %q", res.location)
	}
	for _, reason := range f.failureReasons() {
		if strings.Contains(reason, "secret") {
			t.Fatalf("provider text in the audit log: %q", reason)
		}
	}
}

func TestSSODomainRestriction(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "outsider@other.example")
	b := f.browser()
	f.expectRefused(b, f.signIn(b, "outsider@other.example", nil), "domain_not_allowed")
	if n := f.count(`SELECT count(*) FROM users WHERE email = 'outsider@other.example'`); n != 1 {
		t.Fatal("the existing user must be left alone")
	}
}

func TestSSOProvisioning(t *testing.T) {
	f := newSSOFixture(t)

	b := f.browser()
	f.expectRefused(b, f.signIn(b, "new@example.com", nil), "no_account")
	if f.count(`SELECT count(*) FROM users WHERE email = 'new@example.com'`) != 0 {
		t.Fatal("no user may be created while provisioning is off")
	}

	expect(t, f.configure(func(s map[string]any) { s["auto_provision"] = true }), 200, "")
	b = f.browser()
	f.expectSignedIn(b, f.signIn(b, "new@example.com", nil), "new@example.com")
	var role, name string
	var mustChange bool
	if err := f.h.pool.QueryRow(context.Background(), `SELECT role, name, password_must_change FROM users WHERE email = 'new@example.com'`).Scan(&role, &name, &mustChange); err != nil {
		t.Fatal(err)
	}
	if role != "agent" || name != "Sam Voorbeeld" || mustChange {
		t.Fatalf("provisioned user: %s %q %v", role, name, mustChange)
	}
	if f.count(`SELECT count(*) FROM audit_log WHERE action = 'user.created' AND metadata->>'sso' = 'true'`) != 1 {
		t.Fatal("provisioning must be audited")
	}
	// The random password cannot be guessed into a password login.
	expect(t, f.h.client().login("new@example.com", "anything at all"), 401, "invalid_credentials")

	// The domain list still applies to new users.
	b = f.browser()
	f.expectRefused(b, f.signIn(b, "new@other.example", nil), "domain_not_allowed")

	// A custom role can be the default; a privileged one cannot.
	roleID := createRole(t, f.owner, "SSO default", "conversations.read", "reports.view")
	expect(t, f.configure(func(s map[string]any) {
		s["auto_provision"] = true
		delete(s, "default_role")
		s["default_custom_role_id"] = roleID
	}), 200, "")
	b = f.browser()
	f.expectSignedIn(b, f.signIn(b, "viewer@example.com", nil), "viewer@example.com")
	if f.count(`SELECT count(*) FROM users WHERE email = 'viewer@example.com' AND role = 'custom' AND custom_role_id = $1 AND 'reports.view' = ANY(permissions)`, roleID) != 1 {
		t.Fatal("provisioned custom role user")
	}
	privileged := createRole(t, f.owner, "Too strong", "users.manage")
	expect(t, f.configure(func(s map[string]any) {
		delete(s, "default_role")
		s["default_custom_role_id"] = privileged
	}), 422, "validation_failed")
	expect(t, f.owner.do("DELETE", "/api/v1/roles/"+roleID, nil), 409, "role_in_use")
}

func TestSSOProvisioningRefusesARoleThatBecamePrivileged(t *testing.T) {
	f := newSSOFixture(t)
	roleID := createRole(t, f.owner, "SSO default", "conversations.read")
	expect(t, f.configure(func(s map[string]any) {
		s["auto_provision"] = true
		delete(s, "default_role")
		s["default_custom_role_id"] = roleID
	}), 200, "")
	if _, err := f.h.pool.Exec(context.Background(), `UPDATE custom_roles SET permissions = permissions || ARRAY['users.manage'] WHERE id = $1`, roleID); err != nil {
		t.Fatal(err)
	}
	b := f.browser()
	f.expectRefused(b, f.signIn(b, "late@example.com", nil), "role_privileged")
	if f.count(`SELECT count(*) FROM users WHERE email = 'late@example.com'`) != 0 {
		t.Error("a user was provisioned into a privileged role")
	}
	if f.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.login_failed' AND metadata->>'reason' = 'role_privileged'`) != 1 {
		t.Error("the refusal is not audited")
	}
}

// The caller reads trust_idp_mfa before calling SSOLogin; the login re-reads it inside its
// transaction, so trust withdrawn in between does not mark the session as MFA-backed.
func TestSSOLoginRechecksTrustInsideTheTransaction(t *testing.T) {
	f := newSSOFixture(t)
	u, _ := f.h.user("agent", "sam@example.com")
	if _, err := f.h.pool.Exec(context.Background(), `UPDATE users SET totp_enabled_at = now() WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.configure(func(s map[string]any) { s["trust_idp_mfa"] = false }), 200, "")
	sess, err := f.h.auth.SSOLogin(context.Background(), auth.SSOIdentity{Email: "sam@example.com", IdPMFA: true}, nil, auth.SSOProvision{}, auth.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if sess.IdpMfa {
		t.Error("the session was marked idp_mfa although the trust is off")
	}
	if !sess.MfaPending {
		t.Error("the user's own 2FA must still be asked for")
	}
	if f.count(`SELECT count(*) FROM sessions WHERE idp_mfa`) != 0 {
		t.Error("an idp_mfa session exists")
	}
}

func TestSSOProvisioningNeedsDomains(t *testing.T) {
	f := newSSOFixture(t)
	r := f.configure(func(s map[string]any) { s["auto_provision"] = true; s["allowed_domains"] = []string{} })
	expect(t, r, 422, "validation_failed")
}

func TestSSOTurnsAwayDeactivatedAndInvitedUsers(t *testing.T) {
	f := newSSOFixture(t)
	u, _ := f.h.user("agent", "gone@example.com")
	if _, err := f.h.q.SetUserDeactivated(context.Background(), dbq.SetUserDeactivatedParams{ID: u.ID, Deactivated: true}); err != nil {
		t.Fatal(err)
	}
	b := f.browser()
	f.expectRefused(b, f.signIn(b, "gone@example.com", nil), "deactivated")

	f.h.user("agent", "invited@example.com")
	if _, err := f.h.pool.Exec(context.Background(), `UPDATE users SET invited_at = now(), deactivated_at = now() WHERE email = 'invited@example.com'`); err != nil {
		t.Fatal(err)
	}
	b = f.browser()
	f.expectRefused(b, f.signIn(b, "invited@example.com", nil), "deactivated")
	// Deactivated accounts stay out even when provisioning is on.
	expect(t, f.configure(func(s map[string]any) { s["auto_provision"] = true }), 200, "")
	b = f.browser()
	f.expectRefused(b, f.signIn(b, "gone@example.com", nil), "deactivated")
}

func TestSSORequiredBlocksPasswordLoginExceptForTheOwner(t *testing.T) {
	f := newSSOFixture(t)
	agent, agentPassword := f.h.user("agent", "sam@example.com")
	owner := mustUserByEmail(t, f.h, "owner")
	ownerPassword := "correct horse owner"

	before := f.h.client().login(agent.Email, agentPassword)
	expect(t, before, 200, "")

	expect(t, f.configure(func(s map[string]any) { s["required"] = true }), 200, "")
	expect(t, f.h.client().login(agent.Email, agentPassword), 401, "invalid_credentials")
	expect(t, f.h.client().login("unknown@example.com", "whatever"), 401, "invalid_credentials")
	expect(t, f.h.client().login(owner.Email, ownerPassword), 200, "")
	if reasons := f.failureReasons(); len(reasons) == 0 || reasons[0] != "sso_required" {
		t.Fatalf("audited reasons: %v", reasons)
	}

	// SSO itself still works, and the public info tells the login page.
	b := f.browser()
	f.expectSignedIn(b, f.signIn(b, agent.Email, nil), agent.Email)
	info := f.h.client().do("GET", "/api/v1/auth/sso", nil)
	expect(t, info, 200, "")
	if info.body["enabled"] != true || info.body["required"] != true || info.body["label"] != "Bedrijfsaccount" {
		t.Fatalf("info: %v", info.body)
	}

	// An invitation cannot be turned into a password login either.
	if _, err := f.h.auth.AcceptInvitation(context.Background(), "token", "Name", "a long enough password 12", auth.Client{}); !errors.Is(err, auth.ErrSSORequired) {
		t.Fatalf("accept invitation: %v", err)
	}
}

func TestSSOInfoWhenNotConfigured(t *testing.T) {
	h := newHarness(t)
	info := h.client().do("GET", "/api/v1/auth/sso", nil)
	expect(t, info, 200, "")
	if info.body["enabled"] != false || info.body["required"] != false {
		t.Fatalf("info: %v", info.body)
	}
	// Starting a flow that is not configured goes back to the login page.
	res := h.client()
	res.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r := res.do("GET", "/auth/sso/start", nil)
	if r.status != http.StatusSeeOther || !strings.Contains(r.header.Get("Location"), "sso_error=not_configured") {
		t.Fatalf("start: %d %q", r.status, r.header.Get("Location"))
	}
}

func TestSSOIdPMFA(t *testing.T) {
	amr := func(values ...string) func(map[string]any) {
		return func(c map[string]any) { c["amr"] = values }
	}
	cases := []struct {
		name    string
		trust   bool
		claims  func(map[string]any)
		pending bool
	}{
		{"trusted mfa", true, amr("pwd", "mfa"), false},
		{"trusted otp", true, amr("pwd", "otp"), false},
		{"trusted hardware key", true, amr("hwk"), false},
		{"trusted but password only", true, amr("pwd"), true},
		{"trusted without amr", true, nil, true},
		{"mfa but not trusted", false, amr("pwd", "mfa"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSSOFixture(t)
			u, _ := f.h.user("agent", "sam@example.com")
			if _, err := f.h.pool.Exec(context.Background(), `UPDATE users SET totp_enabled_at = now(), totp_secret_enc = '\x00' WHERE id = $1`, u.ID); err != nil {
				t.Fatal(err)
			}
			expect(t, f.configure(func(s map[string]any) { s["trust_idp_mfa"] = c.trust }), 200, "")
			b := f.browser()
			res := f.signIn(b, "sam@example.com", c.claims)
			if c.pending {
				if res.status != http.StatusSeeOther || res.location != "/inloggen?mfa=1" {
					t.Fatalf("callback: %d -> %q", res.status, res.location)
				}
				expect(t, b.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
				if f.count(`SELECT count(*) FROM audit_log WHERE action = 'auth.login' AND metadata->>'method' = 'sso'`) != 0 {
					t.Fatal("a login that still waits for the code is not a login yet")
				}
				return
			}
			f.expectSignedIn(b, res, u.Email)
		})
	}
}

func TestSSOTurningOffTrustRevokesIdPMFASessions(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "sam@example.com")
	f.h.user("agent", "kim@example.com")
	expect(t, f.configure(func(s map[string]any) { s["trust_idp_mfa"] = true }), 200, "")
	trusted := f.browser()
	f.expectSignedIn(trusted, f.signIn(trusted, "sam@example.com", func(c map[string]any) { c["amr"] = []string{"mfa"} }), "sam@example.com")
	plain := f.browser()
	f.expectSignedIn(plain, f.signIn(plain, "kim@example.com", nil), "kim@example.com")

	// Saving with the trust still on keeps everybody signed in.
	expect(t, f.configure(func(s map[string]any) { s["button_label"] = "Ander label"; s["trust_idp_mfa"] = true }), 200, "")
	expect(t, trusted.do("GET", "/api/v1/me", nil), 200, "")

	expect(t, f.configure(func(s map[string]any) { s["trust_idp_mfa"] = false }), 200, "")
	expect(t, trusted.do("GET", "/api/v1/me", nil), 401, "unauthenticated")
	expect(t, plain.do("GET", "/api/v1/me", nil), 200, "")
	if f.count(`SELECT count(*) FROM audit_log WHERE action = 'settings.sso_changed' AND (metadata->>'idp_mfa_sessions_revoked')::int = 1`) != 1 {
		t.Error("the revoked session count is not audited")
	}
}

func TestSSOMFAEnrollmentIsWaivedForIdPMFA(t *testing.T) {
	f := newSSOFixture(t)
	f.h.user("agent", "sam@example.com")
	expect(t, f.configure(func(s map[string]any) { s["trust_idp_mfa"] = true }), 200, "")
	expect(t, f.owner.do("PUT", "/api/v1/settings/security", map[string]any{"require_mfa": true}), 200, "")

	b := f.browser()
	f.expectSignedIn(b, f.signIn(b, "sam@example.com", func(c map[string]any) { c["amr"] = []string{"mfa"} }), "sam@example.com")
	if me := b.do("GET", "/api/v1/me", nil); me.body["mfa_enrollment_required"] != false {
		t.Fatalf("with provider MFA: %v", me.body)
	}
	b = f.browser()
	f.expectSignedIn(b, f.signIn(b, "sam@example.com", nil), "sam@example.com")
	if me := b.do("GET", "/api/v1/me", nil); me.body["mfa_enrollment_required"] != true {
		t.Fatalf("without provider MFA Echoo's own rule applies: %v", me.body)
	}
}

func TestSSOSettingsAreOwnerOnlyAndSecretIsWriteOnly(t *testing.T) {
	f := newSSOFixture(t)
	adm, _ := f.h.loggedIn("admin")
	expect(t, adm.do("GET", "/api/v1/settings/sso", nil), 403, "forbidden")
	expect(t, adm.do("PUT", "/api/v1/settings/sso", f.settings(nil)), 403, "forbidden")

	get := f.owner.do("GET", "/api/v1/settings/sso", nil)
	expect(t, get, 200, "")
	if strings.Contains(string(get.raw), f.idp.secret) || get.body["client_secret_set"] != true || get.body["redirect_uri"] != testOrigin+"/auth/sso/callback" {
		t.Fatalf("settings response: %s", get.raw)
	}
	var stored []byte
	if err := f.h.pool.QueryRow(context.Background(), `SELECT client_secret_enc FROM sso_settings`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 || strings.Contains(string(stored), f.idp.secret) {
		t.Fatal("the client secret must be stored encrypted")
	}

	// Leaving the secret out keeps it, and sign-in keeps working with it.
	expect(t, f.configure(func(s map[string]any) { delete(s, "client_secret"); s["button_label"] = "Nieuw label" }), 200, "")
	f.h.user("agent", "sam@example.com")
	b := f.browser()
	f.expectSignedIn(b, f.signIn(b, "sam@example.com", nil), "sam@example.com")

	// A wrong secret makes the token exchange fail; the reason is audited.
	expect(t, f.configure(func(s map[string]any) { s["client_secret"] = "wrong" }), 200, "")
	b = f.browser()
	f.expectRefused(b, f.signIn(b, "sam@example.com", nil), "exchange_failed")

	if f.count(`SELECT count(*) FROM audit_log WHERE action = 'settings.sso_changed'`) < 3 {
		t.Fatal("changes must be audited")
	}
	if f.count(`SELECT count(*) FROM audit_log WHERE metadata::text LIKE '%' || $1 || '%'`, f.idp.secret) != 0 {
		t.Fatal("the secret must never reach the audit log")
	}
}

func TestSSOSettingsValidation(t *testing.T) {
	f := newSSOFixture(t)
	cases := map[string]func(map[string]any){
		"plain http issuer":       func(s map[string]any) { s["allow_internal_issuer"] = false; s["issuer_url"] = "http://idp.example.com" },
		"issuer with query":       func(s map[string]any) { s["issuer_url"] = f.idp.issuer() + "?x=1" },
		"enabled without issuer":  func(s map[string]any) { s["issuer_url"] = "" },
		"enabled without client":  func(s map[string]any) { s["client_id"] = "" },
		"required without enable": func(s map[string]any) { s["enabled"] = false; s["required"] = true },
		"bad domain":              func(s map[string]any) { s["allowed_domains"] = []string{"not a domain"} },
		"empty label":             func(s map[string]any) { s["button_label"] = " " },
		"admin as default role":   func(s map[string]any) { s["default_role"] = "admin" },
		"unknown default role": func(s map[string]any) {
			s["default_role"] = "custom"
			s["default_custom_role_id"] = "0199a000-0000-7000-8000-000000000000"
		},
		"unreachable issuer": func(s map[string]any) { s["issuer_url"] = f.idp.issuer() + "/nothing-here" },
	}
	for name, tweak := range cases {
		t.Run(name, func(t *testing.T) {
			expect(t, f.configure(tweak), 422, "validation_failed")
		})
	}
	// Domains are normalised.
	r := f.configure(func(s map[string]any) {
		s["allowed_domains"] = []string{" @Example.COM ", "example.com", "b.example.org"}
	})
	expect(t, r, 200, "")
	if got := fmt.Sprint(r.body["allowed_domains"]); got != "[example.com b.example.org]" {
		t.Fatalf("domains: %s", got)
	}
}

func TestSSODiscoveryDoesNotReachInternalAddressesWithoutTheFlag(t *testing.T) {
	f := newSSOFixture(t)
	// The fake provider listens on 127.0.0.1. Over HTTPS the address is refused by the dialer;
	// the plain HTTP variant is refused by validation. Only the owner-only flag allows either.
	tls := strings.Replace(f.idp.issuer(), "http://", "https://", 1)
	r := f.configure(func(s map[string]any) { s["allow_internal_issuer"] = false; s["issuer_url"] = tls })
	expect(t, r, 422, "validation_failed")
	if r.body["error"].(map[string]any)["fields"].(map[string]any)["issuer_url"] != "unreachable" {
		t.Fatalf("fields: %s", r.raw)
	}
	// With the flag the same provider works (the baseline of every other test).
	expect(t, f.configure(nil), 200, "")
}
