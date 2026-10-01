package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/auth"
	"echoo/internal/config"
	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const testOrigin = "https://echoo.test"

type harness struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	pool   *pgxpool.Pool
	keys   *keyring.Keyring
	reload *fakeReloader
	q      *dbq.Queries
	auth   *auth.Service
	hasher *auth.Hasher
	ipSeq  atomic.Int32
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := testdb.New(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	hasher := auth.NewHasher(auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}, 4)
	svc := auth.NewService(pool, hasher, keys)
	base, _ := url.Parse(testOrigin)
	cfg := &config.Config{
		BaseURL: base,
		// Tests vary X-Forwarded-For to exercise per-account limits without hitting per-IP limits.
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}
	web := fstest.MapFS{
		"index.html":           {Data: []byte(`<meta name="csp-nonce" content="__CSP_NONCE__"><div id="root"></div>`)},
		"assets/app-1.js":      {Data: []byte(`console.log(1)`)},
		".gitkeep":             {Data: nil},
		"fonts/plex.woff2":     {Data: []byte("font")},
		"sw.js":                {Data: []byte("self.addEventListener('push', () => {})")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Echoo"}`)},
	}
	reload := &fakeReloader{}
	srv := New(cfg, pool, svc, web, WithKeyring(keys), WithMailboxReloader(reload))
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, srv: srv, ts: ts, pool: pool, keys: keys, reload: reload, q: dbq.New(pool), auth: svc, hasher: hasher}
}

// user creates an account with a known password that does not need changing.
func (h *harness) user(role, email string) (dbq.User, string) {
	h.t.Helper()
	ctx := context.Background()
	u, _, err := h.auth.CreateUser(ctx, dbq.User{}.ID, email, "Test "+role, role, auth.Client{})
	if err != nil {
		h.t.Fatal(err)
	}
	pw := "correct horse " + role
	enc, err := h.hasher.Hash(ctx, pw)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.q.SetUserPassword(ctx, dbq.SetUserPasswordParams{ID: u.ID, PasswordHash: enc, PasswordMustChange: false}); err != nil {
		h.t.Fatal(err)
	}
	return u, pw
}

type client struct {
	h      *harness
	http   *http.Client
	csrf   string
	ip     string
	origin string
}

func (h *harness) client() *client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	// ts.Client() returns a shared *http.Client; copy it so each client has its own jar.
	hc := *h.ts.Client()
	hc.Jar = jar
	return &client{h: h, http: &hc, ip: fmt.Sprintf("198.51.100.%d", h.ipSeq.Add(1)), origin: testOrigin}
}

type response struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (r response) errCode() string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

func (c *client) do(method, path string, body any) response {
	c.h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.h.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.h.ts.URL+path, rdr)
	if err != nil {
		c.h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", c.origin)
	req.Header.Set("X-Forwarded-For", c.ip)
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.h.t.Fatal(err)
	}
	out := response{status: resp.StatusCode, header: resp.Header, raw: raw}
	if len(raw) > 0 && resp.Header.Get("Content-Type") == "application/json; charset=utf-8" {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			c.h.t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	if tok, ok := out.body["csrf_token"].(string); ok {
		c.csrf = tok
	}
	return out
}

func (c *client) login(email, password string) response {
	c.h.t.Helper()
	return c.do("POST", "/api/v1/auth/login", map[string]string{"email": email, "password": password})
}

// loggedIn returns a client with a full session for a new user of the given role.
func (h *harness) loggedIn(role string) (*client, dbq.User) {
	h.t.Helper()
	u, pw := h.user(role, fmt.Sprintf("%s-%d@example.com", role, h.ipSeq.Add(1)))
	c := h.client()
	if r := c.login(u.Email, pw); r.status != 200 {
		h.t.Fatalf("login %s: %d %s", role, r.status, r.raw)
	}
	return c, u
}

func expect(t *testing.T, r response, status int, code string) {
	t.Helper()
	if r.status != status || (code != "" && r.errCode() != code) {
		t.Fatalf("want %d %s, got %d %s", status, code, r.status, r.raw)
	}
}
