package imapsync

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/keyring"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const (
	testUser     = "helpdesk"
	testPassword = "s3cret-pw"
	waitTimeout  = 30 * time.Second
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type quietLogger struct{}

func (quietLogger) Printf(string, ...any) {}

type literal struct{ *bytes.Reader }

func (l literal) Size() int64 { return int64(l.Len()) }

// testServer is an in-process IMAP server over a self-signed certificate.
type testServer struct {
	host     string
	port     int
	user     *imapmemserver.User
	clientTL *tls.Config
	conns    *atomic.Int32
	idle     *idleGauge
}

// idleGauge tracks how many sessions sit in IDLE right now and how many IDLE commands the server
// has seen. A message delivered while the client is between a sync pass and its next IDLE is only
// noticed at the next IDLE renewal, so tests that expect prompt delivery wait for the gauge first.
type idleGauge struct {
	active  atomic.Int32
	entries atomic.Int32
}

type idleTracking struct {
	imapserver.Session
	g *idleGauge
}

func (s idleTracking) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	s.g.entries.Add(1)
	s.g.active.Add(1)
	defer s.g.active.Add(-1)
	return s.Session.Idle(w, stop)
}

// countingListener counts accepted TCP connections, to prove a guarded dial never arrives.
type countingListener struct {
	net.Listener
	n *atomic.Int32
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

type sessionWrapper func(imapserver.Session) imapserver.Session

func startServer(t *testing.T, mode TLSMode, wrap sessionWrapper) *testServer {
	t.Helper()
	cert, roots := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPassword)
	if err := user.Create(inboxName, nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)

	gauge := new(idleGauge)
	opts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			// The counter sits innermost: a wrapper that adds interfaces (SASL) must stay outermost.
			var s imapserver.Session = idleTracking{Session: mem.NewSession(), g: gauge}
			if wrap != nil {
				s = wrap(s)
			}
			return s, nil, nil
		},
		InsecureAuth: true,
		Logger:       quietLogger{},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conns := new(atomic.Int32)
	ln = countingListener{Listener: ln, n: conns}
	switch mode {
	case TLSImplicit:
		ln = tls.NewListener(ln, serverTLS)
	case TLSStartTLS:
		opts.TLSConfig = serverTLS
	}
	srv := imapserver.New(opts)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	addr := ln.Addr().(*net.TCPAddr)
	return &testServer{
		host:     "127.0.0.1",
		port:     addr.Port,
		user:     user,
		conns:    conns,
		idle:     gauge,
		clientTL: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}
}

func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "imap test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// waitIdling blocks until a client is inside an IDLE command.
func (s *testServer) waitIdling(t *testing.T) {
	t.Helper()
	waitFor(t, "a client in IDLE", func() bool { return s.idle.active.Load() > 0 })
}

// waitIdleEntries blocks until the server has seen n IDLE commands in total.
func (s *testServer) waitIdleEntries(t *testing.T, n int32) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d IDLE commands", n), func() bool { return s.idle.entries.Load() >= n })
}

func (s *testServer) deliver(t *testing.T, n int) {
	t.Helper()
	base := time.Now().UnixNano()
	for i := range n {
		body := fmt.Sprintf("From: customer@example.org\r\nTo: help@example.com\r\nSubject: question %d\r\nMessage-ID: <%d.%d@example.org>\r\n\r\nhello %d\r\n", i, base, i, i)
		if _, err := s.user.Append(inboxName, literal{bytes.NewReader([]byte(body))}, &imap.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func (s *testServer) deliverRaw(t *testing.T, body []byte) {
	t.Helper()
	if _, err := s.user.Append(inboxName, literal{bytes.NewReader(body)}, &imap.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
}

// flags returns the flags of every message in INBOX, in UID order.
func (s *testServer) flags(t *testing.T) [][]imap.Flag {
	t.Helper()
	c, _, err := openInbox(context.Background(), s.connConfig(TLSImplicit), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer logout(c, time.Second)
	var set imap.UIDSet
	set.AddRange(1, 0)
	bufs, err := c.Fetch(set, &imap.FetchOptions{Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]imap.Flag, len(bufs))
	for i, b := range bufs {
		out[i] = b.Flags
	}
	return out
}

func (s *testServer) connConfig(mode TLSMode) ConnConfig {
	return ConnConfig{Host: s.host, Port: s.port, TLS: mode, Username: testUser, Password: testPassword, Timeout: 15 * time.Second, AllowInternal: true, TLSConfig: s.clientTL}
}

// memStore is an in-memory storage.Store.
type memStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
	puts  atomic.Int64
}

func newMemStore() *memStore { return &memStore{blobs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, data []byte) (string, []byte, error) {
	m.puts.Add(1)
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = bytes.Clone(data)
	return key, sum[:], nil
}

func (m *memStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memStore) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.blobs)
}

type harness struct {
	t         *testing.T
	pool      *pgxpool.Pool
	store     *memStore
	kr        *keyring.Keyring
	srv       *testServer
	mailboxID pgtype.UUID
	deps      Deps
}

type mailboxOpts struct {
	password string
	markSeen bool
	mode     TLSMode
	// blockInternal leaves allow_internal_host off, as for a mailbox created without the owner's opt-in.
	blockInternal bool
}

func newKeyring(t *testing.T) *keyring.Keyring {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func newRiver(t *testing.T, pool *pgxpool.Pool) *river.Client[pgx.Tx] {
	t.Helper()
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func newHarness(t *testing.T, srv *testServer, mo mailboxOpts, tune func(*Config)) *harness {
	t.Helper()
	pool := testdb.New(t)
	h := &harness{t: t, pool: pool, store: newMemStore(), kr: newKeyring(t), srv: srv}
	cfg := Config{
		ConnectTimeout:    15 * time.Second,
		LogoutTimeout:     2 * time.Second,
		PollInterval:      time.Hour,
		AuthRetryInterval: time.Hour,
		LockRetryInterval: 20 * time.Millisecond,
		Backoff:           func(int) time.Duration { return 20 * time.Millisecond },
		TLSConfig:         srv.clientTL,
	}
	if tune != nil {
		tune(&cfg)
	}
	h.deps = Deps{
		Pool:    pool,
		Store:   h.store,
		River:   newRiver(t, pool),
		Keyring: h.kr,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:  cfg,
	}
	h.mailboxID = h.insertMailbox(mo)
	return h
}

func (h *harness) insertMailbox(mo mailboxOpts) pgtype.UUID {
	h.t.Helper()
	ctx := context.Background()
	var id pgtype.UUID
	if err := h.pool.QueryRow(ctx, "SELECT uuidv7()").Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	if mo.password == "" {
		mo.password = testPassword
	}
	if mo.mode == "" {
		mo.mode = TLSImplicit
	}
	enc := h.encryptFor(id, mo.password)
	_, err := h.pool.Exec(ctx, `
		INSERT INTO mailboxes (id, name, email_address, transport, imap_host, imap_port, imap_tls, imap_username, imap_secret_enc, mark_seen, allow_internal_host)
		VALUES ($1, 'test', $2, 'imap', $3, $4, $5, $6, $7, $8, $9)`,
		id, "mb-"+id.String()+"@example.com", h.srv.host, h.srv.port, string(mo.mode), testUser, enc, mo.markSeen, !mo.blockInternal)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) encryptFor(id pgtype.UUID, password string) []byte {
	h.t.Helper()
	enc, err := h.kr.Encrypt([]byte(password), keyring.AAD("mailboxes", "imap_secret_enc", id.String()))
	if err != nil {
		h.t.Fatal(err)
	}
	return enc
}

func (h *harness) encrypt(password string) []byte { return h.encryptFor(h.mailboxID, password) }

func (h *harness) count(query string, args ...any) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) rawCount() int { return h.count("SELECT count(*) FROM raw_messages") }
func (h *harness) jobCount() int {
	return h.count("SELECT count(*) FROM river_job WHERE kind = 'mail.parse'")
}

func (h *harness) syncState() (state, errText string) {
	h.t.Helper()
	err := h.pool.QueryRow(context.Background(), "SELECT sync_state, sync_error FROM mailboxes WHERE id = $1", h.mailboxID).Scan(&state, &errText)
	if err != nil {
		h.t.Fatal(err)
	}
	return state, errText
}

func (h *harness) folder() (uidvalidity, lastUID int64) {
	h.t.Helper()
	err := h.pool.QueryRow(context.Background(), "SELECT uidvalidity, last_uid FROM mailbox_folders WHERE mailbox_id = $1", h.mailboxID).Scan(&uidvalidity, &lastUID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.t.Fatal(err)
	}
	return uidvalidity, lastUID
}

func (h *harness) waitState(want string) {
	h.t.Helper()
	waitFor(h.t, "sync state "+want, func() bool { s, _ := h.syncState(); return s == want })
}

func (h *harness) waitRaw(n int) {
	h.t.Helper()
	waitFor(h.t, fmt.Sprintf("%d raw messages", n), func() bool { return h.rawCount() == n })
}

// run starts a supervisor and returns a function that stops it and waits for it to finish.
func (h *harness) run(sup *Supervisor) (stop func()) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sup.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(waitTimeout):
				h.t.Error("supervisor did not stop")
			}
		})
	}
	h.t.Cleanup(stop)
	return stop
}

func (h *harness) supervisor() *Supervisor { return NewSupervisor(h.deps, h.mailboxID) }

// lockFree reports whether the mailbox's advisory lock can be taken, releasing it again.
func (h *harness) lockFree() bool {
	h.t.Helper()
	l, ok, err := acquireLock(context.Background(), h.pool, lockKey(h.mailboxID))
	if err != nil {
		h.t.Fatal(err)
	}
	if ok {
		if err := l.release(time.Second); err != nil {
			h.t.Fatal(err)
		}
	}
	return ok
}
