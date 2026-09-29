package send

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
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/mail"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const (
	smtpUser = "smtpuser"
	smtpPass = "smtppass"
	imapUser = "imapuser"
	imapPass = "imappass"
)

func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "echoo test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

type behavior int

const (
	accept behavior = iota
	temp4xx
	perm5xx
	dropAfterData
)

type tlsMode int

const (
	serverImplicit tlsMode = iota
	serverStartTLS
	serverPlaintext // no TLS at all, and it would accept AUTH in the clear
)

type delivery struct {
	from string
	rcpt []string
	data []byte
}

type fakeSMTP struct {
	mu        sync.Mutex
	script    []behavior
	calls     int
	mails     int
	dataCalls int
	accepted  []delivery
	port      int
	conns     atomic.Int32
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

func newFakeSMTP(t *testing.T, cert tls.Certificate, mode tlsMode, script ...behavior) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{script: script}
	srv := smtp.NewServer(smtp.BackendFunc(func(c *smtp.Conn) (smtp.Session, error) {
		return &smtpSession{f: f, conn: c}, nil
	}))
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = mode == serverPlaintext
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	if mode == serverStartTLS {
		srv.TLSConfig = tlsCfg
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.port = ln.Addr().(*net.TCPAddr).Port
	ln = countingListener{Listener: ln, n: &f.conns}
	if mode == serverImplicit {
		ln = tls.NewListener(ln, tlsCfg)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func (f *fakeSMTP) next() behavior {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.script) == 0 {
		return accept
	}
	b := f.script[min(f.calls, len(f.script)-1)]
	f.calls++
	return b
}

func (f *fakeSMTP) deliveries() []delivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]delivery(nil), f.accepted...)
}

func (f *fakeSMTP) counts() (mails, dataCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mails, f.dataCalls
}

type smtpSession struct {
	f      *fakeSMTP
	conn   *smtp.Conn
	authed bool
	from   string
	rcpt   []string
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		if username != smtpUser || password != smtpPass {
			return errors.New("bad credentials")
		}
		s.authed = true
		return nil
	}), nil
}

func (s *smtpSession) Reset()        { s.from, s.rcpt = "", nil }
func (s *smtpSession) Logout() error { return nil }

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	}
	s.f.mu.Lock()
	s.f.mails++
	s.f.mu.Unlock()
	s.from = from
	return nil
}

func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.rcpt = append(s.rcpt, to)
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.f.mu.Lock()
	s.f.dataCalls++
	s.f.mu.Unlock()
	switch s.f.next() {
	case temp4xx:
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "try again later"}
	case perm5xx:
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "rejected as spam"}
	case dropAfterData:
		_ = s.conn.Conn().Close()
		return errors.New("connection dropped")
	}
	s.f.mu.Lock()
	s.f.accepted = append(s.f.accepted, delivery{from: s.from, rcpt: append([]string(nil), s.rcpt...), data: data})
	s.f.mu.Unlock()
	return nil
}

type fakeIMAP struct {
	port  int
	conns atomic.Int32
}

func newFakeIMAP(t *testing.T, cert tls.Certificate) *fakeIMAP {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(imapUser, imapPass)
	if err := user.Create("Sent", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIMAP{port: ln.Addr().(*net.TCPAddr).Port}
	ln = countingListener{Listener: ln, n: &f.conns}
	go func() { _ = srv.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

type memStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func newMemStore() *memStore { return &memStore{blobs: map[string][]byte{}} }

func (s *memStore) Put(_ context.Context, data []byte) (string, []byte, error) {
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[key] = append([]byte(nil), data...)
	return key, sum[:], nil
}

func (s *memStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.blobs[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// fakeClock stands still until advanced, so backoff deadlines can be asserted exactly.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type env struct {
	t             *testing.T
	pool          *pgxpool.Pool
	q             *dbq.Queries
	rc            *river.Client[pgx.Tx]
	smtp          *fakeSMTP
	imap          *fakeIMAP
	store         *memStore
	keys          *keyring.Keyring
	clock         *fakeClock
	roots         *x509.CertPool
	worker        *Worker
	mailbox       pgtype.UUID
	conv          pgtype.UUID
	smtpTLS       string
	blockInternal bool
}

type envOpts struct {
	smtpMode tlsMode
	script   []behavior
	// noTrust leaves the test CA out of the worker's TLS config.
	noTrust bool
	// blockInternal leaves allow_internal_host off, as for a mailbox created without the owner's opt-in.
	blockInternal bool
}

func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	cert, roots := selfSignedCert(t)
	e := &env{
		t:             t,
		pool:          testdb.New(t),
		smtp:          newFakeSMTP(t, cert, o.smtpMode, o.script...),
		imap:          newFakeIMAP(t, cert),
		store:         newMemStore(),
		roots:         roots,
		blockInternal: o.blockInternal,
		// Ahead of the real clock so rows scheduled "now" by Enqueue are already due.
		clock: &fakeClock{now: time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)},
	}
	e.q = dbq.New(e.pool)
	var err error
	if e.keys, err = keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))); err != nil {
		t.Fatal(err)
	}
	if e.rc, err = river.NewClient(riverpgxv5.New(e.pool), &river.Config{}); err != nil {
		t.Fatal(err)
	}
	e.smtpTLS = "implicit"
	if o.smtpMode != serverImplicit {
		e.smtpTLS = "starttls"
	}
	e.insertMailbox()
	tlsCfg := &tls.Config{RootCAs: roots}
	if o.noTrust {
		tlsCfg = nil
	}
	e.worker = NewWorker(Deps{Pool: e.pool, Storage: e.store, Keyring: e.keys, Clock: e.clock.Now, TLS: tlsCfg})
	return e
}

func (e *env) encrypt(column, plain string) []byte {
	e.t.Helper()
	enc, err := e.keys.Encrypt([]byte(plain), keyring.AAD("mailboxes", column, e.mailbox.String()))
	if err != nil {
		e.t.Fatal(err)
	}
	return enc
}

func (e *env) insertMailbox() {
	e.t.Helper()
	ctx := context.Background()
	if err := e.pool.QueryRow(ctx, `SELECT uuidv7()`).Scan(&e.mailbox); err != nil {
		e.t.Fatal(err)
	}
	_, err := e.pool.Exec(ctx, `
		INSERT INTO mailboxes (id, name, email_address, display_name, imap_host, imap_port, imap_username, imap_secret_enc,
		                       smtp_host, smtp_port, smtp_tls, smtp_username, smtp_secret_enc, sent_folder, send_delay_seconds, allow_internal_host)
		VALUES ($1, 'Support', 'help@example.com', 'Echoo Support', '127.0.0.1', $2, $3, $4,
		        '127.0.0.1', $5, $6, $7, $8, 'Sent', 0, $9)`,
		e.mailbox, e.imap.port, imapUser, e.encrypt("imap_secret_enc", imapPass),
		e.smtp.port, e.smtpTLS, smtpUser, e.encrypt("smtp_secret_enc", smtpPass), !e.blockInternal)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 'Order') RETURNING id`, e.mailbox).Scan(&e.conv); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) params(key pgtype.UUID) EnqueueParams {
	return EnqueueParams{
		ConversationID: e.conv,
		MailboxID:      e.mailbox,
		IdempotencyKey: key,
		To:             []mail.Address{{Name: "Jan", Address: "jan@example.org"}},
		Bcc:            []mail.Address{{Address: "audit@example.org"}},
		Subject:        "Your order",
		Text:           "It shipped.",
	}
}

func newKey(t *testing.T) pgtype.UUID {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}
}

func (e *env) enqueue(p EnqueueParams) Enqueued {
	e.t.Helper()
	var res Enqueued
	err := pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		var err error
		res, err = Enqueue(context.Background(), tx, e.rc, p)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) work(id pgtype.UUID) error { return e.workAttempt(id, 1) }

func (e *env) workAttempt(id pgtype.UUID, attempt int) error {
	job := &river.Job[jobs.SendOutbound]{
		JobRow: &rivertype.JobRow{ID: 1, Attempt: attempt},
		Args:   jobs.SendOutbound{MessageID: id.String()},
	}
	return e.worker.Work(context.Background(), job)
}

func (e *env) outbound(id pgtype.UUID) dbq.Outbound {
	e.t.Helper()
	ob, err := e.q.SendGetOutbound(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ob
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) imapConfig() IMAPConfig {
	return IMAPConfig{
		Host: "127.0.0.1", Port: e.imap.port, TLS: TLSImplicit,
		Username: imapUser, Password: imapPass, Folder: "Sent", AllowInternal: true, TLSConfig: &tls.Config{RootCAs: e.roots},
	}
}

func isSnooze(err error) bool {
	var s *river.JobSnoozeError
	return errors.As(err, &s)
}
