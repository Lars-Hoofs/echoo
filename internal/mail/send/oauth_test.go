package send

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/mailauth"
)

const oauthGoodToken = "ya29.good-access-token"

// oauthSMTP is an SMTP server that offers only XOAUTH2 and answers a bad token like Gmail: a
// JSON challenge, then 535 after the client's empty response.
type oauthSMTP struct {
	port      int
	delivered atomic.Int32
}

type oauthSession struct {
	srv    *oauthSMTP
	authed bool
}

func (s *oauthSession) AuthMechanisms() []string { return []string{"XOAUTH2"} }

func (s *oauthSession) Auth(mech string) (sasl.Server, error) {
	if mech != "XOAUTH2" {
		return nil, errors.New("unsupported mechanism")
	}
	return &oauthSASL{s: s}, nil
}

type oauthSASL struct {
	s        *oauthSession
	rejected bool
}

func (x *oauthSASL) Next(resp []byte) ([]byte, bool, error) {
	if x.rejected {
		return nil, false, &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "authentication failed"}
	}
	if string(resp) == "user="+smtpUser+"\x01auth=Bearer "+oauthGoodToken+"\x01\x01" {
		x.s.authed = true
		return nil, true, nil
	}
	x.rejected = true
	return []byte(`{"status":"401"}`), false, nil
}

func (s *oauthSession) Reset()        {}
func (s *oauthSession) Logout() error { return nil }
func (s *oauthSession) Mail(string, *smtp.MailOptions) error {
	if !s.authed {
		return &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	}
	return nil
}
func (s *oauthSession) Rcpt(string, *smtp.RcptOptions) error { return nil }
func (s *oauthSession) Data(r io.Reader) error {
	if _, err := io.Copy(io.Discard, r); err != nil {
		return err
	}
	s.srv.delivered.Add(1)
	return nil
}

func newOAuthSMTP(t *testing.T) (*oauthSMTP, *tls.Config) {
	t.Helper()
	cert, roots := selfSignedCert(t)
	f := &oauthSMTP{}
	srv := smtp.NewServer(smtp.BackendFunc(func(*smtp.Conn) (smtp.Session, error) { return &oauthSession{srv: f}, nil }))
	srv.Domain = "localhost"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.port = ln.Addr().(*net.TCPAddr).Port
	ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f, &tls.Config{RootCAs: roots}
}

func oauthConfig(srv *oauthSMTP, trust *tls.Config, token string) SMTPConfig {
	return SMTPConfig{Host: "127.0.0.1", Port: srv.port, TLS: TLSImplicit, Username: smtpUser, AccessToken: token, AllowInternal: true, TLSConfig: trust}
}

func TestDeliverAuthenticatesWithXOAuth2(t *testing.T) {
	srv, trust := newOAuthSMTP(t)
	res := Deliver(t.Context(), oauthConfig(srv, trust, oauthGoodToken), "help@example.com", []string{"a@example.org"}, []byte("Subject: x\r\n\r\nhi\r\n"))
	if res.Outcome != Delivered {
		t.Fatalf("outcome %v: %v", res.Outcome, res.Err)
	}
	if srv.delivered.Load() != 1 {
		t.Errorf("delivered %d", srv.delivered.Load())
	}
}

func TestDeliverWithARejectedTokenFailsAtAuthentication(t *testing.T) {
	srv, trust := newOAuthSMTP(t)
	res := Deliver(t.Context(), oauthConfig(srv, trust, "expired"), "help@example.com", []string{"a@example.org"}, []byte("Subject: x\r\n\r\nhi\r\n"))
	if res.Outcome != PermanentFailure || !strings.Contains(res.Response, "535") {
		t.Fatalf("outcome %v, response %q, err %v", res.Outcome, res.Response, res.Err)
	}
	if srv.delivered.Load() != 0 {
		t.Error("a message was delivered without authentication")
	}
}

func TestProbeWithXOAuth2(t *testing.T) {
	srv, trust := newOAuthSMTP(t)
	if err := Probe(t.Context(), oauthConfig(srv, trust, oauthGoodToken), ""); err != nil {
		t.Fatalf("good token: %v", err)
	}
	if err := Probe(t.Context(), oauthConfig(srv, trust, "expired"), ""); !errors.Is(err, ErrProbeAuth) {
		t.Fatalf("bad token: got %v, want ErrProbeAuth", err)
	}
}

func TestPasswordAuthIsNotOfferedXOAuth2Only(t *testing.T) {
	srv, trust := newOAuthSMTP(t)
	cfg := oauthConfig(srv, trust, "")
	cfg.Password = smtpPass
	if err := Probe(t.Context(), cfg, ""); !errors.Is(err, ErrProbeAuth) {
		t.Fatalf("got %v, want ErrProbeAuth: the server offers neither PLAIN nor LOGIN", err)
	}
}

type fakeTokens struct {
	token string
	err   error
}

func (f fakeTokens) AccessToken(context.Context, pgtype.UUID) (string, error) { return f.token, f.err }

func TestMailboxSMTPPicksTheCredentialByAuthType(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys, err := keyring.Parse("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	enc, err := keys.Encrypt([]byte("smtp-secret"), keyring.AAD("mailboxes", "smtp_secret_enc", id.String()))
	if err != nil {
		t.Fatal(err)
	}
	base := dbq.Mailbox{ID: id, SmtpHost: "smtp.example.com", SmtpPort: 465, SmtpTls: "implicit", SmtpUsername: "help@example.com"}

	pw := base
	pw.AuthType, pw.SmtpSecretEnc = "password", enc
	cfg, err := MailboxSMTP(t.Context(), keys, nil, nil, pw)
	if err != nil || cfg.Password != "smtp-secret" || cfg.AccessToken != "" {
		t.Errorf("password mailbox: %+v, %v", cfg, err)
	}

	oa := base
	oa.AuthType = "oauth_google"
	cfg, err = MailboxSMTP(t.Context(), keys, fakeTokens{token: "tok"}, nil, oa)
	if err != nil || cfg.AccessToken != "tok" || cfg.Password != "" {
		t.Errorf("oauth mailbox: %+v, %v", cfg, err)
	}

	_, err = MailboxSMTP(t.Context(), keys, fakeTokens{err: mailauth.ErrReauthRequired}, nil, oa)
	if !IsUnsendable(err) {
		t.Errorf("a revoked account must be unsendable, got %v", err)
	}
	transient := errors.New("network down")
	if _, err = MailboxSMTP(t.Context(), keys, fakeTokens{err: transient}, nil, oa); !errors.Is(err, transient) || IsUnsendable(err) {
		t.Errorf("a refresh failure must stay retryable, got %v", err)
	}
	if _, err = MailboxSMTP(t.Context(), keys, nil, nil, oa); !IsUnsendable(err) {
		t.Errorf("no token source must be unsendable, got %v", err)
	}
}
