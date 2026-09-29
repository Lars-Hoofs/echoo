package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/mail/imapsync"
)

type fakeReloader struct {
	calls atomic.Int32
	err   error
}

func (f *fakeReloader) Reload(context.Context) error {
	f.calls.Add(1)
	return f.err
}

const (
	testIMAPPassword = "imap-secret-1"
	testSMTPPassword = "smtp-secret-1"
)

func mailboxBody(over map[string]any) map[string]any {
	b := map[string]any{
		"name": "Support", "email_address": "support@example.com", "display_name": "Support team",
		"imap_host": "imap.example.invalid", "imap_port": 993, "imap_tls": "implicit",
		"imap_username": "support@example.com", "imap_password": testIMAPPassword,
		"smtp_host": "smtp.example.invalid", "smtp_port": 465, "smtp_tls": "implicit",
		"smtp_username": "support@example.com", "smtp_password": testSMTPPassword,
		"sent_folder": "Sent", "send_delay_seconds": 5,
	}
	for k, v := range over {
		if v == nil {
			delete(b, k)
		} else {
			b[k] = v
		}
	}
	return b
}

func mailboxOf(t *testing.T, r response) map[string]any {
	t.Helper()
	mb, ok := r.body["mailbox"].(map[string]any)
	if !ok {
		t.Fatalf("no mailbox in %s", r.raw)
	}
	return mb
}

func createMailbox(t *testing.T, c *client, over map[string]any) map[string]any {
	t.Helper()
	r := c.do("POST", "/api/v1/mailboxes", mailboxBody(over))
	if r.status != 201 {
		t.Fatalf("create mailbox: %d %s", r.status, r.raw)
	}
	return mailboxOf(t, r)
}

func auditActions(t *testing.T, h *harness) []string {
	t.Helper()
	rows, err := h.q.ListAudit(t.Context(), dbq.ListAuditParams{PageSize: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range rows {
		if strings.HasPrefix(e.AuditLog.Action, "mailbox.") {
			out = append(out, e.AuditLog.Action)
		}
	}
	return out
}

func TestMailboxExposesASafeSyncReason(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	created := createMailbox(t, admin, nil)
	id := created["id"].(string)
	if created["sync_reason"] != nil {
		t.Fatalf("sync_reason of a healthy mailbox = %v, want null", created["sync_reason"])
	}
	uid, _ := parseUUID(id)

	const secret = "s3cret-server-text"
	stored := imapsync.ErrConnect.Error() + ": dial tcp 10.1.2.3:993: " + secret
	for _, tc := range []struct{ state, stored, want string }{
		{"backoff", stored, "connect_failed"},
		{"auth_failed", imapsync.ErrAuth.Error(), "auth_failed"},
		{"backoff", "something the classifier has never seen " + secret, "unknown"},
	} {
		if _, err := h.pool.Exec(t.Context(), `UPDATE mailboxes SET sync_state = $2, sync_error = $3 WHERE id = $1`, uid, tc.state, tc.stored); err != nil {
			t.Fatal(err)
		}
		res := admin.do("GET", "/api/v1/mailboxes/"+id, nil)
		if got := res.body["mailbox"].(map[string]any)["sync_reason"]; got != tc.want {
			t.Errorf("sync_reason for %q = %v, want %s", tc.stored, got, tc.want)
		}
		if bytes.Contains(res.raw, []byte(secret)) {
			t.Errorf("response leaks the stored error text: %s", res.raw)
		}
	}
}

func TestMailboxLifecycle(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	admin, _ := h.loggedIn("admin")

	created := createMailbox(t, admin, nil)
	id := created["id"].(string)
	if created["imap_password_set"] != true || created["smtp_password_set"] != true || created["allow_internal_host"] != false {
		t.Fatalf("unexpected create response: %v", created)
	}
	if created["sync_state"] != "disabled" {
		t.Fatalf("new mailbox should wait for the sync to pick it up: %v", created["sync_state"])
	}
	if h.reload.calls.Load() != 1 {
		t.Fatalf("reload calls after create: %d", h.reload.calls.Load())
	}

	// Secrets are encrypted at rest, bound to their column and row, and never returned.
	uid, _ := parseUUID(id)
	row, err := h.q.GetMailbox(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.ImapSecretEnc, []byte(testIMAPPassword)) || bytes.Contains(row.SmtpSecretEnc, []byte(testSMTPPassword)) {
		t.Fatal("secret stored in plaintext")
	}
	if pt, err := h.keys.Decrypt(row.ImapSecretEnc, keyring.AAD("mailboxes", "imap_secret_enc", id)); err != nil || string(pt) != testIMAPPassword {
		t.Fatalf("imap secret: %q %v", pt, err)
	}
	if pt, err := h.keys.Decrypt(row.SmtpSecretEnc, keyring.AAD("mailboxes", "smtp_secret_enc", id)); err != nil || string(pt) != testSMTPPassword {
		t.Fatalf("smtp secret: %q %v", pt, err)
	}
	if _, err := h.keys.Decrypt(row.ImapSecretEnc, keyring.AAD("mailboxes", "smtp_secret_enc", id)); err == nil {
		t.Fatal("ciphertext must be bound to its column")
	}

	noSecrets := func(r response) {
		t.Helper()
		if strings.Contains(string(r.raw), testIMAPPassword) || strings.Contains(string(r.raw), testSMTPPassword) || strings.Contains(string(r.raw), "secret_enc") {
			t.Fatalf("response leaks a secret: %s", r.raw)
		}
	}
	list := owner.do("GET", "/api/v1/mailboxes", nil)
	noSecrets(list)
	if got := list.body["mailboxes"].([]any); len(got) != 1 {
		t.Fatalf("list: %s", list.raw)
	}
	get := owner.do("GET", "/api/v1/mailboxes/"+id, nil)
	noSecrets(get)
	if mailboxOf(t, get)["imap_host"] != "imap.example.invalid" {
		t.Fatalf("get: %s", get.raw)
	}

	// A partial update keeps everything else, including the stored passwords.
	patch := admin.do("PATCH", "/api/v1/mailboxes/"+id, map[string]any{"name": "Sales", "send_delay_seconds": 0})
	noSecrets(patch)
	mb := mailboxOf(t, patch)
	if mb["name"] != "Sales" || mb["send_delay_seconds"] != float64(0) || mb["imap_password_set"] != true {
		t.Fatalf("patch: %v", mb)
	}
	if h.reload.calls.Load() != 2 {
		t.Fatalf("reload calls after patch: %d", h.reload.calls.Load())
	}
	row2, _ := h.q.GetMailbox(t.Context(), uid)
	if !bytes.Equal(row.ImapSecretEnc, row2.ImapSecretEnc) {
		t.Fatal("secret changed without a new password")
	}

	// A new password replaces only that secret.
	noSecrets(admin.do("PATCH", "/api/v1/mailboxes/"+id, map[string]any{"smtp_password": "new-smtp-secret"}))
	row3, _ := h.q.GetMailbox(t.Context(), uid)
	if !bytes.Equal(row.ImapSecretEnc, row3.ImapSecretEnc) || bytes.Equal(row.SmtpSecretEnc, row3.SmtpSecretEnc) {
		t.Fatal("only the smtp secret should change")
	}
	if pt, err := h.keys.Decrypt(row3.SmtpSecretEnc, keyring.AAD("mailboxes", "smtp_secret_enc", id)); err != nil || string(pt) != "new-smtp-secret" {
		t.Fatalf("smtp secret after change: %q %v", pt, err)
	}

	// An update that changes nothing does not write, audit or reload.
	before := h.reload.calls.Load()
	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+id, map[string]any{"name": "Sales"}), 200, "")
	if h.reload.calls.Load() != before {
		t.Fatal("no-op update reloaded")
	}

	dis := admin.do("POST", "/api/v1/mailboxes/"+id+"/disable", nil)
	if mailboxOf(t, dis)["disabled_at"] == nil {
		t.Fatalf("disable: %s", dis.raw)
	}
	expect(t, admin.do("POST", "/api/v1/mailboxes/"+id+"/disable", nil), 200, "")
	en := admin.do("POST", "/api/v1/mailboxes/"+id+"/enable", nil)
	if mailboxOf(t, en)["disabled_at"] != nil {
		t.Fatalf("enable: %s", en.raw)
	}
	if h.reload.calls.Load() != before+2 {
		t.Fatalf("reload calls: %d, want %d (repeated disable must not reload)", h.reload.calls.Load(), before+2)
	}

	expect(t, admin.do("GET", "/api/v1/mailboxes/0199a000-0000-7000-8000-000000000000", nil), 404, "not_found")
	expect(t, admin.do("GET", "/api/v1/mailboxes/nope", nil), 404, "not_found")
	expect(t, admin.do("POST", "/api/v1/mailboxes/0199a000-0000-7000-8000-000000000000/disable", nil), 404, "not_found")

	got := strings.Join(auditActions(t, h), ",")
	for _, want := range []string{"mailbox.created", "mailbox.updated", "mailbox.secret_changed", "mailbox.disabled", "mailbox.enabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("audit log lacks %s: %s", want, got)
		}
	}
	entries, err := h.q.ListAudit(t.Context(), dbq.ListAuditParams{PageSize: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(string(e.AuditLog.Metadata), "secret-") {
			t.Fatalf("audit metadata contains a secret: %s", e.AuditLog.Metadata)
		}
	}
}

func TestMailboxReloadFailureIsNotReturned(t *testing.T) {
	h := newHarness(t)
	h.reload.err = errors.New("boom")
	admin, _ := h.loggedIn("admin")
	createMailbox(t, admin, nil)
	if h.reload.calls.Load() != 1 {
		t.Fatal("reloader not called")
	}
}

func TestMailboxValidation(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")

	for _, tc := range []struct {
		name  string
		over  map[string]any
		field string
	}{
		{"empty name", map[string]any{"name": " "}, "name"},
		{"long name", map[string]any{"name": strings.Repeat("a", 101)}, "name"},
		{"control character in name", map[string]any{"name": "a\nb"}, "name"},
		{"bad email", map[string]any{"email_address": "not-an-address"}, "email_address"},
		{"email with display name", map[string]any{"email_address": "A <a@example.com>"}, "email_address"},
		{"missing email", map[string]any{"email_address": nil}, "email_address"},
		{"url as imap host", map[string]any{"imap_host": "imaps://imap.example.com"}, "imap_host"},
		{"path in smtp host", map[string]any{"smtp_host": "smtp.example.com/x"}, "smtp_host"},
		{"port in host", map[string]any{"imap_host": "imap.example.com:993"}, "imap_host"},
		{"host with space", map[string]any{"imap_host": "imap example.com"}, "imap_host"},
		{"numeric host spelling", map[string]any{"imap_host": "2130706433"}, "imap_host"},
		{"empty label", map[string]any{"imap_host": "imap..example.com"}, "imap_host"},
		{"missing smtp host", map[string]any{"smtp_host": nil}, "smtp_host"},
		{"imap port zero", map[string]any{"imap_port": 0}, "imap_port"},
		{"smtp port too high", map[string]any{"smtp_port": 65536}, "smtp_port"},
		{"imap tls plain", map[string]any{"imap_tls": "none"}, "imap_tls"},
		{"smtp tls plain", map[string]any{"smtp_tls": ""}, "smtp_tls"},
		{"missing imap username", map[string]any{"imap_username": ""}, "imap_username"},
		{"send delay negative", map[string]any{"send_delay_seconds": -1}, "send_delay_seconds"},
		{"send delay too long", map[string]any{"send_delay_seconds": 31}, "send_delay_seconds"},
		{"sent folder with newline", map[string]any{"sent_folder": "Sent\r\nX"}, "sent_folder"},
		{"missing imap password", map[string]any{"imap_password": nil}, "imap_password"},
		{"empty imap password", map[string]any{"imap_password": ""}, "imap_password"},
		{"huge imap password", map[string]any{"imap_password": strings.Repeat("x", 1025)}, "imap_password"},
		{"missing smtp password", map[string]any{"smtp_password": nil}, "smtp_password"},
	} {
		r := admin.do("POST", "/api/v1/mailboxes", mailboxBody(tc.over))
		expect(t, r, 422, "validation_failed")
		fields, _ := r.body["error"].(map[string]any)["fields"].(map[string]any)
		if _, ok := fields[tc.field]; !ok {
			t.Errorf("%s: fields %v lack %s", tc.name, fields, tc.field)
		}
	}
	expect(t, admin.do("POST", "/api/v1/mailboxes", mailboxBody(map[string]any{"unknown": 1})), 400, "invalid_request")

	// SMTP without authentication needs neither username nor password.
	createMailbox(t, admin, map[string]any{"smtp_username": "", "smtp_password": nil})

	dup := admin.do("POST", "/api/v1/mailboxes", mailboxBody(map[string]any{"email_address": "SUPPORT@example.com", "name": "Other"}))
	expect(t, dup, 422, "validation_failed")

	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+createMailbox(t, admin, map[string]any{"email_address": "b@example.com"})["id"].(string), map[string]any{"imap_port": 70000}), 422, "validation_failed")
}

func TestMailboxRejectsInternalHosts(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	owner, _ := h.loggedIn("owner")

	for _, host := range []string{"127.0.0.1", "10.1.2.3", "192.168.0.10", "172.16.5.5", "169.254.169.254", "100.64.0.1", "0.0.0.0", "224.0.0.1", "[::1]", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1", "localhost"} {
		for _, field := range []string{"imap_host", "smtp_host"} {
			r := admin.do("POST", "/api/v1/mailboxes", mailboxBody(map[string]any{field: host}))
			expect(t, r, 422, "validation_failed")
			fields, _ := r.body["error"].(map[string]any)["fields"].(map[string]any)
			if fields[field] != "internal_host" {
				t.Errorf("%s=%s: fields %v", field, host, fields)
			}
		}
		r := admin.do("POST", "/api/v1/mailboxes/test", mailboxBody(map[string]any{"imap_host": host}))
		expect(t, r, 422, "validation_failed")
	}
	if n := len(admin.do("GET", "/api/v1/mailboxes", nil).body["mailboxes"].([]any)); n != 0 {
		t.Fatalf("%d mailboxes stored", n)
	}

	// Updating an existing mailbox is checked the same way.
	id := createMailbox(t, admin, nil)["id"].(string)
	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+id, map[string]any{"smtp_host": "10.0.0.5"}), 422, "validation_failed")

	// Only the owner can allow internal hosts, and the choice is audited.
	expect(t, admin.do("POST", "/api/v1/mailboxes", mailboxBody(map[string]any{"imap_host": "127.0.0.1", "allow_internal_host": true})), 403, "forbidden")
	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+id, map[string]any{"allow_internal_host": true}), 403, "forbidden")
	expect(t, admin.do("POST", "/api/v1/mailboxes/test", mailboxBody(map[string]any{"imap_host": "127.0.0.1", "allow_internal_host": true})), 403, "forbidden")

	internal := createMailbox(t, owner, map[string]any{"email_address": "internal@example.com", "imap_host": "127.0.0.1", "allow_internal_host": true})
	if internal["allow_internal_host"] != true {
		t.Fatalf("owner create: %v", internal)
	}
	iid := internal["id"].(string)
	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+iid, map[string]any{"name": "Renamed"}), 200, "")
	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+iid, map[string]any{"allow_internal_host": false}), 403, "forbidden")
	expect(t, owner.do("PATCH", "/api/v1/mailboxes/"+iid, map[string]any{"allow_internal_host": false}), 422, "validation_failed")
	if got := strings.Count(strings.Join(auditActions(t, h), ","), "mailbox.internal_host_changed"); got != 1 {
		t.Fatalf("internal host audit entries: %d", got)
	}
}

func TestMailboxAccess(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	id := createMailbox(t, admin, nil)["id"].(string)
	team := func(name string) string {
		r := admin.do("POST", "/api/v1/teams", map[string]string{"name": name})
		expect(t, r, 201, "")
		return r.body["team"].(map[string]any)["id"].(string)
	}
	a, b := team("Support"), team("Sales")

	r := admin.do("GET", "/api/v1/mailboxes/"+id+"/access", nil)
	expect(t, r, 200, "")
	if got := r.body["access"].([]any); len(got) != 0 {
		t.Fatalf("initial access: %s", r.raw)
	}
	expect(t, admin.do("PUT", "/api/v1/mailboxes/"+id+"/access", map[string]any{"access": []map[string]string{{"team_id": a, "level": "write"}, {"team_id": b, "level": "read"}}}), 204, "")
	r = admin.do("GET", "/api/v1/mailboxes/"+id+"/access", nil)
	levels := map[string]string{}
	for _, e := range r.body["access"].([]any) {
		levels[e.(map[string]any)["team_id"].(string)] = e.(map[string]any)["level"].(string)
	}
	if len(levels) != 2 || levels[a] != "write" || levels[b] != "read" {
		t.Fatalf("access: %s", r.raw)
	}
	expect(t, admin.do("PUT", "/api/v1/mailboxes/"+id+"/access", map[string]any{"access": []map[string]string{{"team_id": b, "level": "read"}}}), 204, "")
	if got := admin.do("GET", "/api/v1/mailboxes/"+id+"/access", nil).body["access"].([]any); len(got) != 1 {
		t.Fatalf("access after replace: %v", got)
	}

	for _, body := range map[string]any{
		"bad level":    map[string]any{"access": []map[string]string{{"team_id": a, "level": "admin"}}},
		"bad team id":  map[string]any{"access": []map[string]string{{"team_id": "x", "level": "read"}}},
		"duplicate":    map[string]any{"access": []map[string]string{{"team_id": a, "level": "read"}, {"team_id": a, "level": "write"}}},
		"unknown team": map[string]any{"access": []map[string]string{{"team_id": "0199a000-0000-7000-8000-000000000000", "level": "read"}}},
	} {
		expect(t, admin.do("PUT", "/api/v1/mailboxes/"+id+"/access", body), 422, "validation_failed")
	}
	expect(t, admin.do("PUT", "/api/v1/mailboxes/0199a000-0000-7000-8000-000000000000/access", map[string]any{"access": []any{}}), 404, "not_found")
	expect(t, admin.do("GET", "/api/v1/mailboxes/0199a000-0000-7000-8000-000000000000/access", nil), 404, "not_found")

	if got := strings.Count(strings.Join(auditActions(t, h), ","), "mailbox.access_changed"); got != 2 {
		t.Fatalf("access audit entries: %d", got)
	}
}

func selfSignedServerCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "echoo test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true,
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

const (
	mailUser = "helpdesk"
	mailPass = "server-side-secret"
)

type quietLogger struct{}

func (quietLogger) Printf(string, ...any) {}

func startIMAP(t *testing.T, cert tls.Certificate, withInbox bool) int {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(mailUser, mailPass)
	if withInbox {
		if err := user.Create("INBOX", nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Logger:       quietLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

type smtpBackend struct{}

func (smtpBackend) NewSession(*smtp.Conn) (smtp.Session, error) { return &smtpSession{}, nil }

type smtpSession struct{}

func (*smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (*smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		if username != mailUser || password != mailPass {
			return errors.New("bad credentials")
		}
		return nil
	}), nil
}
func (*smtpSession) Reset()                               {}
func (*smtpSession) Logout() error                        { return nil }
func (*smtpSession) Mail(string, *smtp.MailOptions) error { return nil }
func (*smtpSession) Rcpt(string, *smtp.RcptOptions) error { return nil }
func (*smtpSession) Data(io.Reader) error                 { return nil }

func startSMTP(t *testing.T, cert tls.Certificate) int {
	t.Helper()
	srv := smtp.NewServer(smtpBackend{})
	srv.Domain = "localhost"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestMailboxConnectionTest(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	cert, roots := selfSignedServerCert(t)
	h.srv.testTLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	imapPort, smtpPort := startIMAP(t, cert, true), startSMTP(t, cert)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	local := func(over map[string]any) map[string]any {
		b := map[string]any{
			"imap_host": "127.0.0.1", "imap_port": imapPort, "imap_username": mailUser, "imap_password": mailPass,
			"smtp_host": "127.0.0.1", "smtp_port": smtpPort, "smtp_username": mailUser, "smtp_password": mailPass,
			"allow_internal_host": true,
		}
		maps.Copy(b, over)
		return mailboxBody(b)
	}
	run := func(body map[string]any) (imap, smtp map[string]any) {
		t.Helper()
		r := owner.do("POST", "/api/v1/mailboxes/test", body)
		expect(t, r, 200, "")
		if strings.Contains(string(r.raw), mailPass) {
			t.Fatalf("test response leaks the password: %s", r.raw)
		}
		return r.body["imap"].(map[string]any), r.body["smtp"].(map[string]any)
	}
	code := func(m map[string]any) string { c, _ := m["code"].(string); return c }

	i, s := run(local(nil))
	if i["ok"] != true || s["ok"] != true {
		t.Fatalf("working settings: imap %v smtp %v", i, s)
	}
	i, s = run(local(map[string]any{"imap_password": "wrong", "smtp_password": "wrong"}))
	if code(i) != "imap_auth_failed" || code(s) != "smtp_auth_failed" || i["ok"] != false {
		t.Fatalf("wrong passwords: imap %v smtp %v", i, s)
	}
	i, s = run(local(map[string]any{"imap_port": closedPort, "smtp_port": closedPort}))
	if code(i) != "imap_connect_failed" || code(s) != "smtp_connect_failed" {
		t.Fatalf("closed ports: imap %v smtp %v", i, s)
	}
	i, s = run(local(map[string]any{"imap_tls": "starttls", "smtp_tls": "starttls"}))
	if code(i) != "imap_tls_failed" || code(s) != "smtp_tls_failed" {
		t.Fatalf("starttls against implicit TLS: imap %v smtp %v", i, s)
	}
	i, _ = run(local(map[string]any{"imap_port": startIMAP(t, cert, false)}))
	if code(i) != "imap_no_inbox" {
		t.Fatalf("no inbox: %v", i)
	}
	// A name that does not resolve is a connection failure, not a validation error.
	i, s = run(local(map[string]any{"imap_host": "imap.example.invalid", "smtp_host": "smtp.example.invalid", "allow_internal_host": false}))
	if code(i) != "imap_connect_failed" || code(s) != "smtp_connect_failed" {
		t.Fatalf("unresolvable hosts: imap %v smtp %v", i, s)
	}

	// The certificate must be trusted: without the injected roots the test fails on TLS.
	h.srv.testTLS = nil
	i, s = run(local(nil))
	if code(i) != "imap_tls_failed" || code(s) != "smtp_tls_failed" {
		t.Fatalf("untrusted certificate: imap %v smtp %v", i, s)
	}
	h.srv.testTLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}

	// Testing a saved mailbox uses its stored passwords unless new ones are given.
	saved := createMailbox(t, owner, local(nil))
	only := map[string]any{"id": saved["id"]}
	i, s = run(only)
	if i["ok"] != true || s["ok"] != true {
		t.Fatalf("stored secrets: imap %v smtp %v", i, s)
	}
	i, _ = run(map[string]any{"id": saved["id"], "imap_password": "wrong"})
	if code(i) != "imap_auth_failed" {
		t.Fatalf("override password: %v", i)
	}
	if n := h.count(`SELECT count(*) FROM audit_log WHERE action = 'mailbox.stored_secret_tested' AND target_id = $1`, saved["id"]); n != 2 {
		t.Fatalf("tests that used stored secrets audited %d times, want 2", n)
	}
	expect(t, owner.do("POST", "/api/v1/mailboxes/test", map[string]any{"id": "0199a000-0000-7000-8000-000000000000"}), 422, "validation_failed")
	expect(t, owner.do("POST", "/api/v1/mailboxes/test", map[string]any{"id": "x"}), 422, "validation_failed")
	expect(t, owner.do("POST", "/api/v1/mailboxes/test", mailboxBody(map[string]any{"imap_password": nil})), 422, "validation_failed")

	if b, _ := json.Marshal(saved); strings.Contains(string(b), mailPass) {
		t.Fatal("saved mailbox leaks the password")
	}
}

// A stored password is only sent to the server it was saved for. Changing host, port, TLS mode
// or username demands the password again, on the connection test and on save alike.
func TestStoredSecretsDoNotFollowChangedServers(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.loggedIn("admin")
	id := createMailbox(t, admin, nil)["id"].(string)
	other := map[string]any{"imap_host": "Attacker.Example.COM", "imap_port": 143, "imap_tls": "starttls", "imap_username": "someone-else"}
	smtpOther := map[string]any{"smtp_host": "attacker.example.com", "smtp_port": 587, "smtp_tls": "starttls", "smtp_username": "someone-else"}

	for field, change := range map[string]map[string]any{
		"imap_host": {"imap_host": "attacker.example.com"}, "imap_port": {"imap_port": 143},
		"imap_tls": {"imap_tls": "starttls"}, "imap_username": {"imap_username": "someone-else"},
	} {
		body := map[string]any{"id": id}
		maps.Copy(body, change)
		r := admin.do("POST", "/api/v1/mailboxes/test", body)
		if r.status != 422 || fieldCode(r, "imap_password") != "required" {
			t.Errorf("test with changed %s: %d %s, want imap_password=required", field, r.status, r.raw)
		}
		r = admin.do("PATCH", "/api/v1/mailboxes/"+id, change)
		if r.status != 422 || fieldCode(r, "imap_password") != "required" {
			t.Errorf("update with changed %s: %d %s, want imap_password=required", field, r.status, r.raw)
		}
	}
	for field, change := range map[string]map[string]any{
		"smtp_host": {"smtp_host": "attacker.example.com"}, "smtp_port": {"smtp_port": 587},
		"smtp_tls": {"smtp_tls": "starttls"}, "smtp_username": {"smtp_username": "someone-else"},
	} {
		body := map[string]any{"id": id}
		maps.Copy(body, change)
		r := admin.do("POST", "/api/v1/mailboxes/test", body)
		if r.status != 422 || fieldCode(r, "smtp_password") != "required" {
			t.Errorf("test with changed %s: %d %s, want smtp_password=required", field, r.status, r.raw)
		}
		r = admin.do("PATCH", "/api/v1/mailboxes/"+id, change)
		if r.status != 422 || fieldCode(r, "smtp_password") != "required" {
			t.Errorf("update with changed %s: %d %s, want smtp_password=required", field, r.status, r.raw)
		}
	}

	// Supplying the password is what lets the server change.
	full := map[string]any{"imap_password": "new-imap-password", "smtp_password": "new-smtp-password"}
	maps.Copy(full, other)
	maps.Copy(full, smtpOther)
	r := admin.do("PATCH", "/api/v1/mailboxes/"+id, full)
	expect(t, r, 200, "")
	if mailboxOf(t, r)["imap_host"] != "attacker.example.com" {
		t.Errorf("host is stored lower-cased: %v", mailboxOf(t, r)["imap_host"])
	}
	// Unrelated changes keep working without a password.
	expect(t, admin.do("PATCH", "/api/v1/mailboxes/"+id, map[string]any{"name": "Renamed"}), 200, "")
}
