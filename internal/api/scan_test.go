package api

import (
	"context"
	"strings"
	"testing"

	"echoo/internal/scan"
)

type verdictScanner struct{ verdicts map[string]scan.Result }

func (v verdictScanner) Scan(_ context.Context, data []byte) scan.Result {
	return v.verdicts[string(data)]
}

func TestUploadsAreScanned(t *testing.T) {
	f := newComposerFixture(t)
	f.h.srv.scanner = verdictScanner{map[string]scan.Result{
		"clean":  {Status: scan.StatusClean},
		"virus":  {Status: scan.StatusInfected, Detail: "Eicar-Test-Signature"},
		"broken": {Status: scan.StatusError, Detail: "connect to clamd: refused"},
	}}

	expect(t, f.upload(f.agent, "ok.txt", []byte("clean")), 201, "")
	expect(t, f.upload(f.agent, "virus.txt", []byte("virus")), 422, "attachment_infected")
	if got := f.scalar(`SELECT count(*) FROM uploads WHERE filename = 'virus.txt'`); got != "0" {
		t.Errorf("infected upload was stored: %s", got)
	}
	// clamd being down must not block sending mail; the documented fallback is to allow.
	expect(t, f.upload(f.agent, "unknown.txt", []byte("broken")), 201, "")
}

func TestInfectedInlineImageIsNotServed(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("virus", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{html: "<p>x</p>"})
	att := f.attachment(msg, "logo.png", "image/png", "logo@x", "inline", pngBytes)
	url, err := f.h.srv.signedAttachmentURL(att)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.plain(url); r.StatusCode != 200 {
		t.Fatalf("clean inline image: %d", r.StatusCode)
	}
	f.exec(`UPDATE attachments SET scan_status = 'infected' WHERE id = $1`, att)
	if r := f.plain(url); r.StatusCode != 404 {
		t.Fatalf("infected inline image: got %d, want 404", r.StatusCode)
	}
}

func TestConversationExposesScanStatus(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("status", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{text: "x"})
	att := f.attachment(msg, "a.pdf", "application/pdf", "", "attachment", []byte("%PDF"))
	f.exec(`UPDATE attachments SET scan_status = 'error' WHERE id = $1`, att)
	r := f.agent.do("GET", "/api/v1/conversations/"+conv, nil)
	expect(t, r, 200, "")
	if !strings.Contains(string(r.raw), `"scan_status":"error"`) {
		t.Fatalf("scan_status missing: %s", r.raw)
	}
}
