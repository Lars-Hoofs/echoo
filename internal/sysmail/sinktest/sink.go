// Package sinktest is an in-process SMTP server for tests that need to read the mail Echoo
// sends: implicit TLS over a self-signed certificate, accepting any credentials.
package sinktest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Message is one accepted message.
type Message struct {
	From string
	To   []string
	Raw  []byte
}

// Subject returns the decoded Subject header.
func (m Message) Subject() string {
	r, err := mail.CreateReader(bytes.NewReader(m.Raw))
	if err != nil {
		return ""
	}
	subject, _ := r.Header.Subject()
	return subject
}

// Text returns the decoded text/plain part.
func (m Message) Text() string {
	r, err := mail.CreateReader(bytes.NewReader(m.Raw))
	if err != nil {
		return ""
	}
	for {
		p, err := r.NextPart()
		if err != nil {
			return ""
		}
		if h, ok := p.Header.(*mail.InlineHeader); ok {
			if ct, _, _ := h.ContentType(); ct == "text/plain" {
				b, _ := io.ReadAll(p.Body)
				return string(b)
			}
		}
	}
}

// Link returns the first URL in the text part that starts with prefix.
func (m Message) Link(prefix string) string {
	for _, f := range strings.Fields(m.Text()) {
		if strings.HasPrefix(f, prefix) {
			return f
		}
	}
	return ""
}

// Sink is a running server.
type Sink struct {
	Port int
	// Roots trusts the server's certificate.
	Roots *tls.Config

	mu   sync.Mutex
	msgs []Message
}

// Messages returns the messages accepted so far.
func (s *Sink) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Start runs a sink on a loopback port until the test ends.
func Start(t *testing.T) *Sink {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp sink"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	s := &Sink{Roots: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	srv := smtp.NewServer(smtp.BackendFunc(func(*smtp.Conn) (smtp.Session, error) { return &session{sink: s}, nil }))
	srv.Domain = "localhost"
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Port = ln.Addr().(*net.TCPAddr).Port
	ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return s
}

type session struct {
	sink *Sink
	from string
	to   []string
}

func (s *session) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *session) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, _ string) error { return nil }), nil
}
func (s *session) Reset()        { s.from, s.to = "", nil }
func (s *session) Logout() error { return nil }
func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	return nil
}
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}
func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return errors.Join(errors.New("sinktest: read data"), err)
	}
	s.sink.mu.Lock()
	s.sink.msgs = append(s.sink.msgs, Message{From: s.from, To: append([]string(nil), s.to...), Raw: raw})
	s.sink.mu.Unlock()
	return nil
}
