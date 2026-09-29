package mailauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const clientID = "client-123"

func newKeys(t *testing.T) *keyring.Keyring {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func idToken(claims map[string]any) string {
	seg := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return seg(map[string]string{"alg": "none"}) + "." + seg(claims) + ".sig"
}

// fakeProvider is an OAuth token endpoint. Refresh tokens rotate and each may be used once,
// like Microsoft's, so a refresh race would show up as invalid_grant.
type fakeProvider struct {
	*httptest.Server
	mu        sync.Mutex
	refreshes atomic.Int32
	valid     map[string]bool
	seq       int
	failWith  int    // HTTP status to fail refreshes with, 0 for none
	verifier  string // code_verifier of the last code exchange
	noRefresh bool
	email     string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{valid: map[string]bool{"refresh-0": true}, email: "help@example.com"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.token))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	f.seq++
	resp := map[string]any{"access_token": "access-" + string(rune('a'+f.seq)), "token_type": "Bearer", "expires_in": 3600}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		if r.PostForm.Get("code") != "the-code" {
			fail(http.StatusBadRequest, "invalid_grant")
			return
		}
		f.verifier = r.PostForm.Get("code_verifier")
		resp["id_token"] = idToken(map[string]any{"aud": clientID, "email": f.email, "email_verified": true})
		if !f.noRefresh {
			resp["refresh_token"] = "refresh-0"
		}
	case "refresh_token":
		f.refreshes.Add(1)
		if f.failWith != 0 {
			fail(f.failWith, "server_error")
			return
		}
		old := r.PostForm.Get("refresh_token")
		if !f.valid[old] {
			fail(http.StatusBadRequest, "invalid_grant")
			return
		}
		delete(f.valid, old)
		next := "refresh-" + string(rune('a'+f.seq))
		f.valid[next] = true
		resp["refresh_token"] = next
	default:
		fail(http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeProvider) provider() *Provider {
	return &Provider{
		ID: ProviderGoogle, Name: "Google", AuthType: AuthTypeGoogle, ClientID: clientID, ClientSecret: "secret",
		Endpoint: oauth2.Endpoint{AuthURL: f.URL + "/auth", TokenURL: f.URL + "/token", AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   []string{"https://mail.google.com/", "openid", "email"},
		IMAP:     ServerSettings{Host: "imap.gmail.com", Port: 993, TLS: "implicit"},
		SMTP:     ServerSettings{Host: "smtp.gmail.com", Port: 465, TLS: "implicit"},
	}
}

type env struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *dbq.Queries
	m    *Manager
	f    *fakeProvider
	id   pgtype.UUID
	now  time.Time
}

func newEnv(t *testing.T, expiresIn time.Duration) *env {
	t.Helper()
	pool := testdb.New(t)
	f := newFakeProvider(t)
	keys := newKeys(t)
	m := NewManager(pool, keys, NewProvidersFrom(f.provider()), "https://echoo.test")
	e := &env{t: t, pool: pool, q: dbq.New(pool), m: m, f: f, now: time.Now()}
	m.now = func() time.Time { return e.now }

	mb, err := e.q.InsertOAuthMailbox(t.Context(), dbq.InsertOAuthMailboxParams{
		Name: "help", EmailAddress: "help@example.com", AuthType: AuthTypeGoogle,
		ImapHost: "imap.gmail.com", ImapPort: 993, ImapTls: "implicit", SmtpHost: "smtp.gmail.com", SmtpPort: 465, SmtpTls: "implicit",
	})
	if err != nil {
		t.Fatal(err)
	}
	e.id = mb.ID
	e.store(Token{AccessToken: "access-initial", RefreshToken: "refresh-0", Expiry: e.now.Add(expiresIn)})
	return e
}

func (e *env) store(tok Token) {
	e.t.Helper()
	enc, err := e.m.Seal(e.id, tok)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.q.StoreRefreshedOAuthToken(e.t.Context(), dbq.StoreRefreshedOAuthTokenParams{ID: e.id, OauthTokenEnc: enc, OauthExpiresAt: pgtype.Timestamptz{Time: tok.Expiry, Valid: true}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) stored() Token {
	e.t.Helper()
	row, err := e.q.GetMailboxOAuth(e.t.Context(), e.id)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := e.m.open(e.id, row.OauthTokenEnc)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func TestAccessTokenReturnsAFreshTokenWithoutTalkingToTheProvider(t *testing.T) {
	e := newEnv(t, time.Hour)
	got, err := e.m.AccessToken(t.Context(), e.id)
	if err != nil || got != "access-initial" {
		t.Fatalf("got %q, %v", got, err)
	}
	if e.f.refreshes.Load() != 0 {
		t.Error("refreshed a token that was still valid")
	}
}

func TestAccessTokenRefreshesAnExpiringTokenAndKeepsTheRotatedRefreshToken(t *testing.T) {
	e := newEnv(t, time.Minute) // inside the refresh skew
	got, err := e.m.AccessToken(t.Context(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	stored := e.stored()
	if got == "access-initial" || stored.AccessToken != got {
		t.Errorf("returned %q, stored %q", got, stored.AccessToken)
	}
	if stored.RefreshToken == "refresh-0" || !strings.HasPrefix(stored.RefreshToken, "refresh-") {
		t.Errorf("rotated refresh token was not persisted: %q", stored.RefreshToken)
	}
	row, _ := e.q.GetMailboxOAuth(t.Context(), e.id)
	if !row.OauthExpiresAt.Valid || !row.OauthExpiresAt.Time.After(e.now.Add(30*time.Minute)) {
		t.Errorf("oauth_expires_at = %v", row.OauthExpiresAt)
	}
	// The next call uses the refreshed token.
	again, err := e.m.AccessToken(t.Context(), e.id)
	if err != nil || again != got || e.f.refreshes.Load() != 1 {
		t.Errorf("second call: %q, %v, %d refreshes", again, err, e.f.refreshes.Load())
	}
}

func TestConcurrentCallersRefreshOnce(t *testing.T) {
	e := newEnv(t, -time.Minute)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	tokens := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			tok, err := e.m.AccessToken(context.Background(), e.id)
			errs <- err
			tokens <- tok
		})
	}
	wg.Wait()
	close(errs)
	close(tokens)
	for err := range errs {
		if err != nil {
			t.Fatalf("a caller failed (a refresh race would end in invalid_grant): %v", err)
		}
	}
	seen := map[string]bool{}
	for tok := range tokens {
		seen[tok] = true
	}
	if len(seen) != 1 || e.f.refreshes.Load() != 1 {
		t.Errorf("%d distinct tokens, %d refresh requests; want 1 and 1", len(seen), e.f.refreshes.Load())
	}
}

func TestInvalidGrantMarksTheMailboxForReconnecting(t *testing.T) {
	e := newEnv(t, -time.Minute)
	e.store(Token{AccessToken: "old", RefreshToken: "revoked", Expiry: e.now.Add(-time.Minute)})
	_, err := e.m.AccessToken(t.Context(), e.id)
	if !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("got %v, want ErrReauthRequired", err)
	}
	mb, err := e.q.GetMailbox(t.Context(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	if mb.SyncState != "auth_failed" || mb.SyncError != ReasonReauthRequired {
		t.Errorf("sync state %q / %q", mb.SyncState, mb.SyncError)
	}
	if e.stored().RefreshToken != "revoked" {
		t.Error("a rejected refresh token must not be replaced")
	}
}

func TestTransientProviderFailureIsNotAReconnectRequest(t *testing.T) {
	e := newEnv(t, -time.Minute)
	e.f.failWith = http.StatusInternalServerError
	_, err := e.m.AccessToken(t.Context(), e.id)
	if err == nil || errors.Is(err, ErrReauthRequired) {
		t.Fatalf("got %v, want a plain error", err)
	}
	mb, err := e.q.GetMailbox(t.Context(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	if mb.SyncState == "auth_failed" {
		t.Error("a provider outage must not flag the mailbox as needing reconnection")
	}
}

func TestAccessTokenRejectsPasswordMailboxes(t *testing.T) {
	e := newEnv(t, time.Hour)
	if _, err := e.pool.Exec(t.Context(), `UPDATE mailboxes SET auth_type = 'password' WHERE id = $1`, e.id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AccessToken(t.Context(), e.id); !errors.Is(err, ErrNotOAuth) {
		t.Fatalf("got %v", err)
	}
}

func TestTokenIsBoundToItsMailboxRow(t *testing.T) {
	e := newEnv(t, time.Hour)
	other := e.newMailbox("other@example.com")
	row, _ := e.q.GetMailboxOAuth(t.Context(), e.id)
	if _, err := e.pool.Exec(t.Context(), `UPDATE mailboxes SET oauth_token_enc = $2 WHERE id = $1`, other, row.OauthTokenEnc); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.AccessToken(t.Context(), other); err == nil {
		t.Fatal("a token copied to another row must not decrypt")
	}
}

func (e *env) newMailbox(email string) pgtype.UUID {
	e.t.Helper()
	mb, err := e.q.InsertOAuthMailbox(e.t.Context(), dbq.InsertOAuthMailboxParams{
		Name: email, EmailAddress: email, AuthType: AuthTypeGoogle,
		ImapHost: "imap.gmail.com", ImapPort: 993, ImapTls: "implicit", SmtpHost: "smtp.gmail.com", SmtpPort: 465, SmtpTls: "implicit",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return mb.ID
}

func TestExchangeSendsThePKCEVerifierAndReadsTheAccount(t *testing.T) {
	e := newEnv(t, time.Hour)
	verifier := oauth2.GenerateVerifier()
	tok, email, err := e.m.Exchange(t.Context(), e.f.provider(), "the-code", verifier)
	if err != nil {
		t.Fatal(err)
	}
	if email != "help@example.com" || tok.RefreshToken != "refresh-0" || tok.AccessToken == "" {
		t.Errorf("got %+v, %q", tok, email)
	}
	if e.f.verifier != verifier {
		t.Errorf("provider saw verifier %q", e.f.verifier)
	}
	if _, _, err := e.m.Exchange(t.Context(), e.f.provider(), "wrong", verifier); err == nil {
		t.Error("a rejected code must fail")
	}
	e.f.noRefresh = true
	if _, _, err := e.m.Exchange(t.Context(), e.f.provider(), "the-code", verifier); !errors.Is(err, ErrNoRefreshToken) {
		t.Errorf("got %v, want ErrNoRefreshToken", err)
	}
}

func TestAuthURLCarriesPKCEChallengeAndState(t *testing.T) {
	e := newEnv(t, time.Hour)
	verifier := oauth2.GenerateVerifier()
	u := e.m.AuthURL(e.f.provider(), "the-state", verifier, "help@example.com")
	sum := sha256.Sum256([]byte(verifier))
	for _, want := range []string{
		"state=the-state", "code_challenge_method=S256", "code_challenge=" + base64.RawURLEncoding.EncodeToString(sum[:]),
		"redirect_uri=https%3A%2F%2Fechoo.test%2Foauth%2Fcallback%2Fgoogle", "login_hint=help%40example.com", "client_id=" + clientID,
	} {
		if !strings.Contains(u, want) {
			t.Errorf("authorization URL lacks %q: %s", want, u)
		}
	}
	if strings.Contains(u, verifier) {
		t.Error("the verifier must never appear in the URL")
	}
}

func TestStateRoundTripAndTampering(t *testing.T) {
	keys := newKeys(t)
	now := time.Now()
	in := State{Provider: ProviderGoogle, SessionID: "session-id-value", Verifier: "verifier-secret-value", MailboxID: "m", Name: "n"}
	sealed, err := SealState(keys, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, in.Verifier) || strings.Contains(sealed, in.SessionID) {
		t.Error("state must be opaque")
	}
	got, err := OpenState(keys, sealed, now.Add(time.Minute))
	if err != nil || got.Provider != in.Provider || got.SessionID != in.SessionID || got.Verifier != in.Verifier || got.MailboxID != "m" || got.Name != "n" {
		t.Fatalf("got %+v, %v", got, err)
	}

	tampered := []byte(sealed)
	tampered[len(tampered)/2] ^= 1
	if _, err := OpenState(keys, string(tampered), now); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("tampered: got %v", err)
	}
	if _, err := OpenState(newKeys(t), sealed, now); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("other key: got %v", err)
	}
	if _, err := OpenState(keys, "not base64!", now); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("garbage: got %v", err)
	}
	if _, err := OpenState(keys, sealed, now.Add(StateTTL+time.Second)); !errors.Is(err, ErrStateExpired) {
		t.Errorf("expired: got %v", err)
	}
}

func TestAccountEmail(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]any
		want   string
		fail   bool
	}{
		{"google", map[string]any{"aud": clientID, "email": "Help@Example.com", "email_verified": true}, "help@example.com", false},
		{"audience list", map[string]any{"aud": []string{"x", clientID}, "email": "a@example.com"}, "a@example.com", false},
		{"microsoft without email claim", map[string]any{"aud": clientID, "preferred_username": "user@contoso.com"}, "user@contoso.com", false},
		{"unverified", map[string]any{"aud": clientID, "email": "a@example.com", "email_verified": false}, "", true},
		{"other audience", map[string]any{"aud": "someone-else", "email": "a@example.com"}, "", true},
		{"no address", map[string]any{"aud": clientID, "preferred_username": "not-an-address"}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AccountEmail(idToken(tc.claims), clientID)
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
	for _, bad := range []string{"", "a.b", "a.!!!.c"} {
		if _, err := AccountEmail(bad, clientID); err == nil {
			t.Errorf("%q must fail", bad)
		}
	}
}

func TestXOAuth2InitialResponse(t *testing.T) {
	mech, ir, err := NewXOAuth2Client("help@example.com", "tok").Start()
	if err != nil || mech != "XOAUTH2" || string(ir) != "user=help@example.com\x01auth=Bearer tok\x01\x01" {
		t.Fatalf("got %q %q %v", mech, ir, err)
	}
	if _, _, err := NewXOAuth2Client("a@example.com", "tok\x01auth=Bearer other").Start(); err == nil {
		t.Error("control characters must be rejected")
	}
	if resp, err := NewXOAuth2Client("a", "b").Next([]byte(`{"status":"401"}`)); err != nil || resp == nil || len(resp) != 0 {
		t.Errorf("the error challenge needs an empty, non-nil answer: %v %v", resp, err)
	}
}

func TestProviders(t *testing.T) {
	ps, err := NewProviders(Credentials{GoogleClientID: "g", GoogleClientSecret: "s", MicrosoftClientID: "m", MicrosoftClientSecret: "s", MicrosoftTenant: "contoso.onmicrosoft.com"})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := ps.ByID("google")
	ms, _ := ps.ByID("microsoft")
	if g.IMAP.Host != "imap.gmail.com" || g.SMTP.Port != 465 || g.SMTP.TLS != "implicit" {
		t.Errorf("google servers: %+v %+v", g.IMAP, g.SMTP)
	}
	if ms.IMAP.Host != "outlook.office365.com" || ms.SMTP.Host != "smtp.office365.com" || ms.SMTP.Port != 587 || ms.SMTP.TLS != "starttls" {
		t.Errorf("microsoft servers: %+v %+v", ms.IMAP, ms.SMTP)
	}
	if ms.Endpoint.TokenURL != "https://login.microsoftonline.com/contoso.onmicrosoft.com/oauth2/v2.0/token" {
		t.Errorf("token URL %q", ms.Endpoint.TokenURL)
	}
	if got := ms.RedirectURL("https://support.example.com"); got != "https://support.example.com/oauth/callback/microsoft" {
		t.Errorf("redirect URL %q", got)
	}

	none, err := NewProviders(Credentials{GoogleClientID: "only-an-id"})
	if err != nil || len(none.List()) != 0 {
		t.Errorf("a provider without a secret must not be offered: %v %v", none.List(), err)
	}
	if _, err := NewProviders(Credentials{MicrosoftClientID: "m", MicrosoftClientSecret: "s", MicrosoftTenant: "a/../b"}); err == nil {
		t.Error("a tenant with path characters must be rejected")
	}
}
