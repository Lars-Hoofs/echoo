package mailflow

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"strings"
	"sync"
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

	"echoo/internal/automation"
	"echoo/internal/keyring"
	"echoo/internal/mail/imapsync"
	"echoo/internal/mail/ingest"
	"echoo/internal/mail/send"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const (
	mailboxAddr  = "help@example.com"
	imapUsername = "helpdesk"
	imapPassword = "imap-s3cret-pw"
	smtpUsername = "submit"
	smtpPassword = "smtp-s3cret-pw"
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

func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mailflow test"},
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
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}

// smtpSink is an in-process SMTP server that records every accepted message.
type smtpSink struct {
	mu       sync.Mutex
	accepted [][]byte
	rcpts    [][]string
}

func (s *smtpSink) messages() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.accepted...)
}

type smtpSession struct {
	sink   *smtpSink
	authed bool
	rcpt   []string
}

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		if username != smtpUsername || password != smtpPassword {
			return errors.New("bad credentials")
		}
		s.authed = true
		return nil
	}), nil
}

func (s *smtpSession) Reset()        { s.rcpt = nil }
func (s *smtpSession) Logout() error { return nil }

func (s *smtpSession) Mail(string, *smtp.MailOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	}
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
	s.sink.mu.Lock()
	defer s.sink.mu.Unlock()
	s.sink.accepted = append(s.sink.accepted, data)
	s.sink.rcpts = append(s.sink.rcpts, append([]string(nil), s.rcpt...))
	return nil
}

// env is a complete deployment in a test: one mailbox whose IMAP and SMTP servers are
// in-process, with River running the production ingest and send workers.
type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	store  *storage.FS
	imap   *imapmemserver.User
	smtp   *smtpSink
	river  *river.Client[pgx.Tx]
	deps   imapsync.Deps
	mailID pgtype.UUID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	cert, roots := selfSignedCert(t)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	clientTLS := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}

	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: testdb.New(t), store: store, smtp: &smtpSink{}}

	mem := imapmemserver.New()
	e.imap = imapmemserver.NewUser(imapUsername, imapPassword)
	for _, folder := range []string{"INBOX", "Sent"} {
		if err := e.imap.Create(folder, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(e.imap)
	imapPort := serve(t, imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Logger:       quietLogger{},
	}), serverTLS)

	smtpSrv := smtp.NewServer(smtp.BackendFunc(func(*smtp.Conn) (smtp.Session, error) {
		return &smtpSession{sink: e.smtp}, nil
	}))
	smtpSrv.Domain = "localhost"
	smtpPort := serve(t, smtpSrv, serverTLS)

	keys := newKeyring(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	workers := river.NewWorkers()
	river.AddWorker(workers, ingest.NewWorker(ingest.Deps{Pool: e.pool, Store: store, Logger: logger}))
	river.AddWorker(workers, send.NewWorker(send.Deps{Pool: e.pool, Storage: store, Keyring: keys, TLS: clientTLS}))
	automation.Register(workers, automation.NewEngine(automation.Deps{Pool: e.pool, Logger: logger}))
	e.river, err = river.NewClient(riverpgxv5.New(e.pool), &river.Config{
		Queues:            map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}},
		Workers:           workers,
		Logger:            logger,
		FetchCooldown:     10 * time.Millisecond,
		FetchPollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.river.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := e.river.Stop(ctx); err != nil {
			t.Errorf("stop river: %v", err)
		}
	})

	e.deps = imapsync.Deps{
		Pool: e.pool, Store: store, River: e.river, Keyring: keys, Logger: logger,
		Config: imapsync.Config{
			ConnectTimeout: 15 * time.Second,
			LogoutTimeout:  2 * time.Second,
			PollInterval:   100 * time.Millisecond,
			// A message delivered between a sync pass and the next IDLE is only announced at the
			// next IDLE renewal; renewing often bounds that gap without the tests timing it.
			IdleRestart:       250 * time.Millisecond,
			AuthRetryInterval: time.Hour,
			LockRetryInterval: 20 * time.Millisecond,
			Backoff:           func(int) time.Duration { return 20 * time.Millisecond },
			TLSConfig:         clientTLS,
		},
	}
	e.mailID = e.insertMailbox(keys, imapPort, smtpPort)
	return e
}

func serve(t *testing.T, srv interface{ Serve(net.Listener) error }, cfg *tls.Config) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(tls.NewListener(ln, cfg)) }()
	t.Cleanup(func() {
		if c, ok := srv.(io.Closer); ok {
			_ = c.Close()
		}
	})
	return ln.Addr().(*net.TCPAddr).Port
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

func (e *env) insertMailbox(keys *keyring.Keyring, imapPort, smtpPort int) pgtype.UUID {
	e.t.Helper()
	ctx := context.Background()
	var id pgtype.UUID
	if err := e.pool.QueryRow(ctx, "SELECT uuidv7()").Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	enc := func(column, plain string) []byte {
		b, err := keys.Encrypt([]byte(plain), keyring.AAD("mailboxes", column, id.String()))
		if err != nil {
			e.t.Fatal(err)
		}
		return b
	}
	_, err := e.pool.Exec(ctx, `
		INSERT INTO mailboxes (id, name, email_address, display_name, transport,
		                       imap_host, imap_port, imap_tls, imap_username, imap_secret_enc,
		                       smtp_host, smtp_port, smtp_tls, smtp_username, smtp_secret_enc,
		                       sent_folder, send_delay_seconds, allow_internal_host)
		VALUES ($1, 'Support', $2, 'Echoo Support', 'imap',
		        '127.0.0.1', $3, 'implicit', $4, $5,
		        '127.0.0.1', $6, 'implicit', $7, $8,
		        'Sent', 0, true)`,
		id, mailboxAddr, imapPort, imapUsername, enc("imap_secret_enc", imapPassword),
		smtpPort, smtpUsername, enc("smtp_secret_enc", smtpPassword))
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// startSync runs a Manager against the mailbox and shuts it down when the test ends.
func (e *env) startSync() *imapsync.Manager {
	e.t.Helper()
	m := imapsync.NewManager(e.deps)
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	if err := m.Reload(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) deliver(raw []byte) {
	e.t.Helper()
	if _, err := e.imap.Append("INBOX", literal{bytes.NewReader(raw)}, &imap.AppendOptions{}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) waitCount(what string, want int, query string, args ...any) {
	e.t.Helper()
	waitFor(e.t, fmt.Sprintf("%d %s", want, what), func() bool { return e.count(query, args...) == want })
}

// waitIngested waits until n raw messages were fetched and every one of them was processed.
func (e *env) waitIngested(n int) {
	e.t.Helper()
	e.waitCount("parsed raw messages", n, `SELECT count(*) FROM raw_messages WHERE parse_status = 'parsed'`)
	if got := e.count(`SELECT count(*) FROM raw_messages`); got != n {
		e.t.Fatalf("raw messages = %d, want %d", got, n)
	}
}

func (e *env) sentFolderCount() int {
	e.t.Helper()
	data, err := e.imap.Status("Sent", &imap.StatusOptions{NumMessages: true})
	if err != nil {
		e.t.Fatal(err)
	}
	return int(*data.NumMessages)
}

func (e *env) conversationOf(messageIDHeader string) pgtype.UUID {
	e.t.Helper()
	var id pgtype.UUID
	err := e.pool.QueryRow(context.Background(),
		`SELECT conversation_id FROM messages WHERE message_id_header = $1`, messageIDHeader).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// latestMessage returns the Message-ID and References of the newest message of a conversation.
func (e *env) latestMessage(conv pgtype.UUID) (id string, refs []string) {
	e.t.Helper()
	err := e.pool.QueryRow(context.Background(), `
		SELECT message_id_header, references_hdr FROM messages
		WHERE conversation_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, conv).Scan(&id, &refs)
	if err != nil {
		e.t.Fatal(err)
	}
	return id, refs
}

// requireNoSecrets fails when a credential or an error shows up where operators can read it.
func (e *env) requireNoSecrets() {
	e.t.Helper()
	var syncState, syncErr string
	err := e.pool.QueryRow(context.Background(), `SELECT sync_state, sync_error FROM mailboxes WHERE id = $1`, e.mailID).Scan(&syncState, &syncErr)
	if err != nil {
		e.t.Fatal(err)
	}
	if syncErr != "" {
		e.t.Errorf("sync_error = %q (state %s), want empty", syncErr, syncState)
	}
	rows, err := e.pool.Query(context.Background(), `SELECT error, smtp_response FROM outbound`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var errText, resp string
		if err := rows.Scan(&errText, &resp); err != nil {
			e.t.Fatal(err)
		}
		if errText != "" {
			e.t.Errorf("outbound.error = %q, want empty", errText)
		}
		for _, secret := range []string{imapPassword, smtpPassword} {
			if strings.Contains(errText+resp+syncErr, secret) {
				e.t.Errorf("credential leaked into status text: %q %q", errText, resp)
			}
		}
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) sentConfig() send.IMAPConfig {
	e.t.Helper()
	var host string
	var port int
	err := e.pool.QueryRow(context.Background(), `SELECT imap_host, imap_port FROM mailboxes WHERE id = $1`, e.mailID).Scan(&host, &port)
	if err != nil {
		e.t.Fatal(err)
	}
	return send.IMAPConfig{
		Host: host, Port: port, TLS: send.TLSImplicit, Username: imapUsername, Password: imapPassword,
		Folder: "Sent", AllowInternal: true, TLSConfig: e.deps.Config.TLSConfig,
	}
}
