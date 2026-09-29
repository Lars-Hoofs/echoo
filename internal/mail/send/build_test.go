package send

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomail "github.com/emersion/go-message/mail"

	"echoo/internal/mail"
)

var update = flag.Bool("update", false, "rewrite golden files")

func counterBoundary() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("boundary-%d", n)
	}
}

func baseMessage() Outgoing {
	return Outgoing{
		From:      mail.Address{Name: "Support Team", Address: "help@example.com"},
		To:        []mail.Address{{Name: "Jan de Vries", Address: "jan@example.org"}},
		Subject:   "Your order",
		MessageID: "echoo.abc.123@example.com",
		Date:      time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC),
		Text:      "Hello,\nyour order has shipped.\n",
	}
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (run with -update to accept):\n%s", path, got)
	}
}

func TestBuildGolden(t *testing.T) {
	plain := baseMessage()

	full := baseMessage()
	full.Cc = []mail.Address{{Address: "cc@example.org"}}
	full.Bcc = []mail.Address{{Address: "hidden@example.org"}}
	full.Subject = "Réponse à votre commande €5"
	full.From.Name = "Zoë Support"
	full.InReplyTo = "parent@example.org"
	full.References = []string{"root@example.org", "parent@example.org"}
	full.HTML = `<p>Hallo <img src="cid:logo"></p>`
	full.AutoReplied = true
	full.Attachments = []mail.Attachment{
		{Filename: "logo.png", DeclaredType: "image/png", ContentID: "logo", Inline: true, Data: []byte("PNGDATA")},
		{Filename: "rapport café.pdf", DeclaredType: "application/pdf", Data: []byte("%PDF-1.4 body")},
	}

	htmlOnlyAttachment := baseMessage()
	htmlOnlyAttachment.HTML = "<p>hi</p>"
	htmlOnlyAttachment.Attachments = []mail.Attachment{{Filename: "a.txt", DeclaredType: "text/plain", Data: []byte("note")}}

	for name, msg := range map[string]Outgoing{"plain": plain, "full": full, "html_attachment": htmlOnlyAttachment} {
		t.Run(name, func(t *testing.T) {
			got, err := build(msg, counterBoundary())
			if err != nil {
				t.Fatal(err)
			}
			assertGolden(t, name, got)
		})
	}
}

func TestBuildUsesCRLFOnly(t *testing.T) {
	msg := baseMessage()
	msg.HTML = "<p>one</p>\n<p>two</p>"
	got, err := Build(msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
		t.Errorf("bare LF in output:\n%q", got)
	}
}

func TestBuildNeverExposesBcc(t *testing.T) {
	msg := baseMessage()
	msg.Bcc = []mail.Address{{Address: "hidden@example.org"}}
	got, err := Build(msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("hidden@example.org")) || bytes.Contains(bytes.ToLower(got), []byte("bcc:")) {
		t.Errorf("Bcc leaked into message:\n%s", got)
	}
	if rcpt := msg.Recipients(); len(rcpt) != 2 || rcpt[1] != "hidden@example.org" {
		t.Errorf("Recipients() = %v, want To then Bcc", rcpt)
	}
}

func TestBuildRoundTrip(t *testing.T) {
	msg := baseMessage()
	msg.Subject = "Réponse: ünïcode"
	msg.HTML = "<p>x</p>"
	msg.Attachments = []mail.Attachment{{Filename: "naïve résumé.pdf", DeclaredType: "application/pdf", Data: []byte("data")}}
	raw, err := Build(msg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if subject, _ := r.Header.Subject(); subject != msg.Subject {
		t.Errorf("subject = %q, want %q", subject, msg.Subject)
	}
	var filename string
	var attachmentBody []byte
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ah, ok := p.Header.(*gomail.AttachmentHeader); ok {
			filename, _ = ah.Filename()
			attachmentBody, _ = io.ReadAll(p.Body)
		}
	}
	if filename != "naïve résumé.pdf" || string(attachmentBody) != "data" {
		t.Errorf("attachment = %q %q", filename, attachmentBody)
	}
	if !strings.Contains(string(raw), "filename*=utf-8''") {
		t.Errorf("filename is not RFC 2231 encoded:\n%s", raw)
	}
}

func TestBuildCapsReferences(t *testing.T) {
	msg := baseMessage()
	for i := 0; i < 30; i++ {
		msg.References = append(msg.References, fmt.Sprintf("m%d@example.org", i))
	}
	raw, err := Build(msg)
	if err != nil {
		t.Fatal(err)
	}
	r, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := r.Header.MsgIDList("References")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 20 || refs[0] != "m0@example.org" || refs[1] != "m11@example.org" || refs[19] != "m29@example.org" {
		t.Errorf("references = %v", refs)
	}
}

func TestBuildRejectsInvalidValues(t *testing.T) {
	tests := map[string]func(*Outgoing){
		"subject CRLF":         func(m *Outgoing) { m.Subject = "hi\r\nBcc: evil@example.net" },
		"subject LF":           func(m *Outgoing) { m.Subject = "hi\nX: y" },
		"subject NUL":          func(m *Outgoing) { m.Subject = "hi\x00" },
		"from name CR":         func(m *Outgoing) { m.From.Name = "A\rB" },
		"to name LF":           func(m *Outgoing) { m.To[0].Name = "A\nB" },
		"to address CRLF":      func(m *Outgoing) { m.To[0].Address = "a@example.org\r\nBcc: x@example.org" },
		"address not an addr":  func(m *Outgoing) { m.To[0].Address = "not an address" },
		"address with name":    func(m *Outgoing) { m.To[0].Address = "Eve <eve@example.org>" },
		"address non-ASCII":    func(m *Outgoing) { m.To[0].Address = "jörg@example.org" },
		"bcc invalid":          func(m *Outgoing) { m.Bcc = []mail.Address{{Address: "nope"}} },
		"cc invalid":           func(m *Outgoing) { m.Cc = []mail.Address{{Address: "nope"}} },
		"no recipients":        func(m *Outgoing) { m.To = nil },
		"message id LF":        func(m *Outgoing) { m.MessageID = "a@b.c\nX: y" },
		"message id malformed": func(m *Outgoing) { m.MessageID = "no-at-sign" },
		"in-reply-to CR":       func(m *Outgoing) { m.InReplyTo = "a@b.c\r" },
		"references LF":        func(m *Outgoing) { m.References = []string{"a@b.c", "x@y.z\n"} },
		"missing date":         func(m *Outgoing) { m.Date = time.Time{} },
		"filename NUL":         func(m *Outgoing) { m.Attachments = []mail.Attachment{{Filename: "a\x00b", Data: []byte("x")}} },
		"content type CRLF": func(m *Outgoing) {
			m.Attachments = []mail.Attachment{{DeclaredType: "text/plain\r\nX: y", Data: []byte("x")}}
		},
		"content type invalid": func(m *Outgoing) { m.Attachments = []mail.Attachment{{DeclaredType: "///", Data: []byte("x")}} },
		"content id LF": func(m *Outgoing) {
			m.Attachments = []mail.Attachment{{ContentID: "a\nb", Inline: true, Data: []byte("x")}}
		},
		"inline without content id": func(m *Outgoing) { m.Attachments = []mail.Attachment{{Inline: true, Data: []byte("x")}} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			msg := baseMessage()
			mutate(&msg)
			out, err := Build(msg)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want *ValidationError (output %q)", err, out)
			}
			if out != nil {
				t.Errorf("output must be nil on error")
			}
		})
	}
}
