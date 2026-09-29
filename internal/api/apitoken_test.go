package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"echoo/internal/auth"
)

// bearer sends a request with only an Authorization header: no cookie jar, no Origin, no CSRF.
func (h *harness) bearer(token, method, path string, body any) response {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rdr)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	out := response{status: resp.StatusCode, header: resp.Header, raw: raw}
	if len(raw) > 0 && resp.Header.Get("Content-Type") == "application/json; charset=utf-8" {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			h.t.Fatal(err)
		}
	}
	return out
}

func (h *harness) newToken(c *client, scope string) (token, id string) {
	h.t.Helper()
	r := c.do("POST", "/api/v1/me/tokens", map[string]any{"name": "ci", "scope": scope})
	if r.status != 201 {
		h.t.Fatalf("create token: %d %s", r.status, r.raw)
	}
	token, _ = r.body["token"].(string)
	meta, _ := r.body["api_token"].(map[string]any)
	id, _ = meta["id"].(string)
	return token, id
}

func TestCreateTokenShowsSecretOnceAndStoresOnlyItsHash(t *testing.T) {
	h := newHarness(t)
	c, user := h.loggedIn("agent")
	r := c.do("POST", "/api/v1/me/tokens", map[string]any{"name": "Zapier", "scope": "write"})
	expect(t, r, 201, "")
	token, _ := r.body["token"].(string)
	if !strings.HasPrefix(token, "ech_") || len(token) < 40 {
		t.Fatalf("token = %q", token)
	}
	meta, _ := r.body["api_token"].(map[string]any)
	if meta["prefix"] != token[:8] || meta["scope"] != "write" || meta["name"] != "Zapier" {
		t.Errorf("api_token = %v", meta)
	}

	rows, err := h.q.ListUserAPITokens(context.Background(), user.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, err = %v", rows, err)
	}
	if bytes.Equal(rows[0].TokenHash, []byte(token)) || !bytes.Equal(rows[0].TokenHash, auth.HashToken(token)) {
		t.Error("token hash is not the SHA-256 of the token")
	}

	list := c.do("GET", "/api/v1/me/tokens", nil)
	expect(t, list, 200, "")
	if strings.Contains(string(list.raw), token) {
		t.Error("the list shows the token")
	}
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'api_token.created' AND actor_user_id = $1`, user.ID); n != 1 {
		t.Errorf("created audit entries = %d", n)
	}
	if strings.Contains(h.auditText(), token) {
		t.Error("the audit log holds the token")
	}
}

func TestCreateTokenValidation(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	far := time.Now().AddDate(10, 0, 0).UTC().Format(time.RFC3339)
	for name, body := range map[string]map[string]any{
		"no name":      {"name": " ", "scope": "read"},
		"bad scope":    {"name": "x", "scope": "admin"},
		"expired":      {"name": "x", "scope": "read", "expires_at": past},
		"too far away": {"name": "x", "scope": "read", "expires_at": far},
	} {
		if r := c.do("POST", "/api/v1/me/tokens", body); r.status != 422 {
			t.Errorf("%s: status %d %s", name, r.status, r.raw)
		}
	}
	soon := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	expect(t, c.do("POST", "/api/v1/me/tokens", map[string]any{"name": "x", "scope": "read", "expires_at": soon}), 201, "")
}

func TestBearerTokenActsAsItsUserWithoutOriginOrCSRF(t *testing.T) {
	h := newHarness(t)
	c, user := h.loggedIn("agent")
	token, _ := h.newToken(c, "write")

	me := h.bearer(token, "GET", "/api/v1/me", nil)
	expect(t, me, 200, "")
	if u, _ := me.body["user"].(map[string]any); u["email"] != user.Email {
		t.Errorf("me = %v", me.body)
	}
	// A state-changing request works with no Origin and no CSRF token.
	expect(t, h.bearer(token, "PATCH", "/api/v1/me", map[string]any{"name": "Via token"}), 200, "")
	if u, _ := h.q.GetUser(context.Background(), user.ID); u.Name != "Via token" {
		t.Errorf("name = %q", u.Name)
	}
	// It has the rights of the user and no more.
	expect(t, h.bearer(token, "GET", "/api/v1/users", nil), 403, "forbidden")
	expect(t, h.bearer(token, "POST", "/api/v1/teams", map[string]any{"name": "x"}), 403, "forbidden")
}

func TestAdminTokenReachesAdminRoutes(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("admin")
	token, _ := h.newToken(c, "write")
	expect(t, h.bearer(token, "GET", "/api/v1/users", nil), 200, "")
	expect(t, h.bearer(token, "POST", "/api/v1/teams", map[string]any{"name": "Support"}), 201, "")
}

func TestCookieRequestsStillNeedCSRFAndOrigin(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")

	c.csrf = ""
	expect(t, c.do("PATCH", "/api/v1/me", map[string]any{"name": "x"}), 403, "csrf_failed")

	c2, _ := h.loggedIn("agent")
	c2.origin = "https://evil.example"
	expect(t, c2.do("PATCH", "/api/v1/me", map[string]any{"name": "x"}), 403, "csrf_failed")

	// The same request with the token in place still works.
	c3, _ := h.loggedIn("agent")
	expect(t, c3.do("PATCH", "/api/v1/me", map[string]any{"name": "x"}), 200, "")
}

func TestBearerHeaderMakesTheCookieIrrelevant(t *testing.T) {
	h := newHarness(t)
	cookieUser, _ := h.loggedIn("admin")
	other, otherUser := h.loggedIn("agent")
	token, _ := h.newToken(other, "read")

	// Send the admin's cookie together with the agent's token: only the token counts.
	req, err := http.NewRequest("GET", h.ts.URL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := http.NewRequest("GET", h.ts.URL, nil)
	for _, ck := range cookieUser.http.Jar.Cookies(u.URL) {
		req.AddCookie(ck)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		User struct{ Email string } `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.User.Email != otherUser.Email {
		t.Errorf("acted as %q (err %v), want %q", body.User.Email, err, otherUser.Email)
	}

	// A bad token is rejected even though the cookie is valid.
	req.Header.Set("Authorization", "Bearer ech_"+strings.Repeat("x", 43))
	resp2, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != 401 || resp2.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("bad token with valid cookie: %d %q", resp2.StatusCode, resp2.Header.Get("WWW-Authenticate"))
	}
}

func TestBearerRejectsMalformedRevokedExpiredAndDeactivated(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	c, user := h.loggedIn("agent")

	for name, header := range map[string]string{"basic scheme": "Basic abc", "empty token": "Bearer ", "not an echoo token": "Bearer abcdef"} {
		req, _ := http.NewRequest("GET", h.ts.URL+"/api/v1/me", nil)
		req.Header.Set("Authorization", header)
		resp, err := h.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
	}

	revoked, revokedID := h.newToken(c, "write")
	expect(t, h.bearer(revoked, "GET", "/api/v1/me", nil), 200, "")
	expect(t, c.do("DELETE", "/api/v1/me/tokens/"+revokedID, nil), 204, "")
	expect(t, h.bearer(revoked, "GET", "/api/v1/me", nil), 401, "unauthenticated")

	expired, expiredID := h.newToken(c, "write")
	h.exec(`UPDATE api_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, expiredID)
	expect(t, h.bearer(expired, "GET", "/api/v1/me", nil), 401, "unauthenticated")

	live, _ := h.newToken(c, "write")
	expect(t, h.bearer(live, "GET", "/api/v1/me", nil), 200, "")
	expect(t, admin.do("PATCH", "/api/v1/users/"+user.ID.String(), map[string]any{"deactivated": true}), 200, "")
	expect(t, h.bearer(live, "GET", "/api/v1/me", nil), 401, "unauthenticated")
	expect(t, admin.do("PATCH", "/api/v1/users/"+user.ID.String(), map[string]any{"deactivated": false}), 200, "")
	expect(t, h.bearer(live, "GET", "/api/v1/me", nil), 200, "")
}

func TestReadScopeBlocksWrites(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")
	token, _ := h.newToken(c, "read")
	expect(t, h.bearer(token, "GET", "/api/v1/me", nil), 200, "")
	expect(t, h.bearer(token, "GET", "/api/v1/me/tokens", nil), 200, "")
	for _, m := range []string{"POST", "PATCH", "PUT", "DELETE"} {
		expect(t, h.bearer(token, m, "/api/v1/me", map[string]any{"name": "x"}), 403, "token_read_only")
	}
}

func TestTokensCannotManageCredentialsOrTokens(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("owner")
	token, id := h.newToken(c, "write")
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/me/tokens"},
		{"DELETE", "/api/v1/me/tokens/" + id},
		{"DELETE", "/api/v1/tokens/" + id},
		{"POST", "/api/v1/me/password"},
		{"POST", "/api/v1/me/totp/setup"},
		{"POST", "/api/v1/me/totp/disable"},
		{"POST", "/api/v1/me/recovery-codes"},
		{"GET", "/api/v1/me/sessions"},
		{"POST", "/api/v1/me/sessions/revoke-others"},
		{"POST", "/api/v1/auth/logout"},
	} {
		body := any(nil)
		if tc.method == "POST" {
			body = map[string]any{"name": "x", "scope": "read"}
		}
		if r := h.bearer(token, tc.method, tc.path, body); r.status != 403 || r.errCode() != "session_required" {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, r.status, r.raw)
		}
	}
	expect(t, h.bearer(token, "GET", "/api/v1/me", nil), 200, "")
}

func TestTokenRateLimit(t *testing.T) {
	h := newHarness(t)
	h.srv.tokenLimiter = auth.NewLimiter(3, time.Minute)
	c, _ := h.loggedIn("agent")
	token, _ := h.newToken(c, "read")
	other, _ := h.newToken(c, "read")
	for i := range 3 {
		if r := h.bearer(token, "GET", "/api/v1/me", nil); r.status != 200 {
			t.Fatalf("request %d: %d", i+1, r.status)
		}
	}
	r := h.bearer(token, "GET", "/api/v1/me", nil)
	expect(t, r, 429, "rate_limited")
	if r.header.Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	// The limit is per token.
	expect(t, h.bearer(other, "GET", "/api/v1/me", nil), 200, "")
}

func TestBadTokenAttemptsAreLimitedPerClient(t *testing.T) {
	h := newHarness(t)
	h.srv.badTokenLimiter = auth.NewLimiter(2, time.Minute)
	bad := "ech_" + strings.Repeat("y", 43)
	expect(t, h.bearer(bad, "GET", "/api/v1/me", nil), 401, "")
	expect(t, h.bearer(bad, "GET", "/api/v1/me", nil), 401, "")
	expect(t, h.bearer(bad, "GET", "/api/v1/me", nil), 429, "rate_limited")
}

func TestLastUsedIsUpdatedAtMostOncePerMinute(t *testing.T) {
	h := newHarness(t)
	c, user := h.loggedIn("agent")
	token, id := h.newToken(c, "read")
	lastUsed := func() time.Time {
		rows, err := h.q.ListUserAPITokens(context.Background(), user.ID)
		if err != nil || len(rows) != 1 || rows[0].ID.String() != id {
			t.Fatalf("rows = %v, err = %v", rows, err)
		}
		return rows[0].LastUsedAt.Time
	}
	if !lastUsed().IsZero() {
		t.Fatal("last_used_at set before use")
	}
	expect(t, h.bearer(token, "GET", "/api/v1/me", nil), 200, "")
	first := lastUsed()
	if first.IsZero() {
		t.Fatal("last_used_at not set by first use")
	}
	expect(t, h.bearer(token, "GET", "/api/v1/me", nil), 200, "")
	if !lastUsed().Equal(first) {
		t.Error("last_used_at changed within a minute")
	}
	h.exec(`UPDATE api_tokens SET last_used_at = now() - interval '2 minutes' WHERE id = $1`, id)
	stale := lastUsed()
	expect(t, h.bearer(token, "GET", "/api/v1/me", nil), 200, "")
	if !lastUsed().After(stale) {
		t.Error("last_used_at not refreshed after a minute")
	}
}

func TestTokenListsAndRevocationRights(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	alice, _ := h.loggedIn("agent")
	bob, _ := h.loggedIn("agent")
	aliceToken, aliceID := h.newToken(alice, "read")
	h.newToken(bob, "write")

	mine := alice.do("GET", "/api/v1/me/tokens", nil)
	if toks, _ := mine.body["tokens"].([]any); len(toks) != 1 {
		t.Errorf("alice sees %d tokens, want her own only", len(toks))
	}
	all := admin.do("GET", "/api/v1/tokens", nil)
	expect(t, all, 200, "")
	toks, _ := all.body["tokens"].([]any)
	if len(toks) != 2 {
		t.Fatalf("admin sees %d tokens, want 2", len(toks))
	}
	if first, _ := toks[0].(map[string]any); first["user_email"] == "" || first["user_name"] == "" {
		t.Errorf("admin list lacks the owner: %v", first)
	}

	// Somebody else's token looks like it does not exist.
	expect(t, bob.do("DELETE", "/api/v1/me/tokens/"+aliceID, nil), 404, "not_found")
	expect(t, h.bearer(aliceToken, "GET", "/api/v1/me", nil), 200, "")
	expect(t, admin.do("DELETE", "/api/v1/tokens/"+aliceID, nil), 204, "")
	expect(t, h.bearer(aliceToken, "GET", "/api/v1/me", nil), 401, "")
	expect(t, admin.do("DELETE", "/api/v1/tokens/"+aliceID, nil), 404, "")
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'api_token.revoked'`); n != 1 {
		t.Errorf("revoked audit entries = %d, want 1", n)
	}
}

func (h *harness) auditText() string {
	h.t.Helper()
	var s string
	err := h.pool.QueryRow(context.Background(), `SELECT coalesce(string_agg(action || ' ' || target_id || ' ' || metadata::text, ' '), '') FROM audit_log`).Scan(&s)
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}
