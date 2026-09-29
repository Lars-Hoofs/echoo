package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"echoo/internal/sanitize"
	"echoo/internal/storage"
)

// pngBytes is a valid 1x1 PNG, so content sniffing sees an image.
var pngBytes = mustBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

func mustBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

type renderFixture struct {
	*inboxFixture
	store storage.Store
}

func newRenderFixture(t *testing.T) *renderFixture {
	t.Helper()
	f := newInboxFixture(t)
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.h.srv.store = store
	return &renderFixture{inboxFixture: f, store: store}
}

type msgOpt struct {
	from, fromName, html, text, authResults, replyTo, direction string
}

func (f *renderFixture) message(conv, mailbox string, o msgOpt) string {
	if o.direction == "" {
		o.direction = "in"
	}
	if o.replyTo == "" {
		o.replyTo = "[]"
	}
	if o.from == "" {
		o.from = "jane@customer.test"
	}
	return f.queryID(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, from_name, body_html, body_text, auth_results, reply_to, received_at)
		VALUES ($1, $2, 'email', $3, $4, $5, $6, $7, $8, $9::jsonb, now()) RETURNING id`,
		conv, mailbox, o.direction, o.from, o.fromName, o.html, o.text, o.authResults, o.replyTo)
}

func (f *renderFixture) attachment(msg, filename, sniffed, contentID, disposition string, data []byte) string {
	f.h.t.Helper()
	key, sum, err := f.store.Put(context.Background(), data)
	if err != nil {
		f.h.t.Fatal(err)
	}
	return f.queryID(`INSERT INTO attachments (message_id, filename, sniffed_type, size_bytes, sha256, blob_key, content_id, disposition)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, msg, filename, sniffed, len(data), sum, key, contentID, disposition)
}

type plainResponse struct {
	StatusCode int
	Header     http.Header
}

// plain fetches a path without any session, like the sandboxed iframe does.
func (f *renderFixture) plain(path string) plainResponse {
	f.h.t.Helper()
	c := f.h.client()
	req, err := http.NewRequest("GET", f.h.ts.URL+path, nil)
	if err != nil {
		f.h.t.Fatal(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		f.h.t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		f.h.t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		f.h.t.Fatal(err)
	}
	return plainResponse{StatusCode: resp.StatusCode, Header: resp.Header}
}

func TestRenderMessageDocument(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("render", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{html: `<p>Hallo</p><script>alert(1)</script>` +
		`<img src="https://track.example.net/p.gif" onerror="alert(2)"><img src="cid:logo@x"><img src="cid:doc@x">` +
		`<a href="https://evil.test/">https://bank.example.com</a>`})
	att := f.attachment(msg, "logo.png", "image/png", "logo@x", "inline", pngBytes)
	f.attachment(msg, "notes.txt", "text/plain", "doc@x", "inline", []byte("not an image"))

	r := f.agent.do("GET", "/render/messages/"+msg, nil)
	expect(t, r, 200, "")
	if ct := r.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if got := r.header.Get("Content-Security-Policy"); got != sanitize.ContentSecurityPolicy {
		t.Errorf("CSP %q", got)
	}
	for _, want := range []string{"default-src 'none'", "img-src 'self' data:", "style-src 'unsafe-inline'", "script-src 'sha256-", "frame-ancestors 'self'"} {
		if !strings.Contains(r.header.Get("Content-Security-Policy"), want) {
			t.Errorf("CSP lacks %q", want)
		}
	}
	if got := r.header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options %q", got)
	}
	if r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff")
	}
	body := string(r.raw)
	if strings.Contains(body, "alert(1)") || strings.Contains(body, "alert(2)") || strings.Contains(body, "track.example.net") {
		t.Errorf("hostile content survived: %s", body)
	}
	if strings.Count(body, "<script") != 1 {
		t.Errorf("only the resize script may be present: %s", body)
	}
	if strings.Count(body, "/render/attachments/"+att+"?exp=") != 1 || strings.Count(body, "data:image/gif") != 2 {
		t.Errorf("cid handling wrong (one resolved, non-image cid and blocked remote image are placeholders): %s", body)
	}

	// The signed inline image works without a session, as the sandbox has none.
	m := findAttachmentURL(t, body)
	m = strings.ReplaceAll(m, "&amp;", "&")
	img := f.plain(m)
	if img.StatusCode != 200 || img.Header.Get("Content-Type") != "image/png" || img.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("inline image: %d %v", img.StatusCode, img.Header)
	}
	if img.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Error("an opaque-origin document must be allowed to embed the image")
	}
}

func findAttachmentURL(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "/render/attachments/")
	if i < 0 {
		t.Fatalf("no attachment URL in %s", s)
	}
	j := strings.IndexByte(s[i:], '"')
	return s[i : i+j]
}

func TestRenderAttachmentRequiresValidSignature(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("sig", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{html: "<p>x</p>"})
	att := f.attachment(msg, "logo.png", "image/png", "logo@x", "inline", pngBytes)
	svg := f.attachment(msg, "x.svg", "image/svg+xml", "svg@x", "inline", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))

	good, err := f.h.srv.signedAttachmentURL(att)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.plain(good); r.StatusCode != 200 {
		t.Fatalf("valid signature: %d", r.StatusCode)
	}
	for name, path := range map[string]string{
		"no signature":   "/render/attachments/" + att,
		"wrong sig":      strings.Replace(good, "sig=", "sig=A", 1),
		"other id":       strings.Replace(good, att, svg, 1),
		"garbage":        "/render/attachments/" + att + "?exp=1&sig=x",
		"expired":        f.expiredAttachmentURL(att),
		"not a uuid":     "/render/attachments/nope?exp=1&sig=x",
		"exp not number": "/render/attachments/" + att + "?exp=abc&sig=x",
	} {
		if r := f.plain(path); r.StatusCode != 404 {
			t.Errorf("%s: got %d, want 404", name, r.StatusCode)
		}
	}
	// A correctly signed URL for an SVG is still refused: SVG is never served inline.
	svgURL, err := f.h.srv.signedAttachmentURL(svg)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.plain(svgURL); r.StatusCode != 404 {
		t.Errorf("svg inline: got %d, want 404", r.StatusCode)
	}
}

func (f *renderFixture) expiredAttachmentURL(id string) string {
	exp := time.Now().Add(-time.Minute).Unix()
	sig, err := f.h.srv.renderSignature("attachment", id, exp)
	if err != nil {
		f.h.t.Fatal(err)
	}
	return "/render/attachments/" + id + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig
}

func TestRenderMessageScopeAndAuth(t *testing.T) {
	f := newRenderFixture(t)
	a := f.message(f.conversation("a", convOpt{mailbox: f.mailboxA}), f.mailboxA, msgOpt{html: "<p>a</p>"})
	b := f.message(f.conversation("b", convOpt{mailbox: f.mailboxB}), f.mailboxB, msgOpt{html: "<p>b</p>"})
	gone := f.message(f.conversation("gone", convOpt{mailbox: f.mailboxA}), f.mailboxA, msgOpt{html: "<p>gone</p>"})
	f.exec(`UPDATE messages SET deleted_at = now() WHERE id = $1`, gone)

	expect(t, f.agent.do("GET", "/render/messages/"+a, nil), 200, "")
	expect(t, f.readonly.do("GET", "/render/messages/"+a, nil), 200, "")
	expect(t, f.admin.do("GET", "/render/messages/"+b, nil), 200, "")
	expect(t, f.agent.do("GET", "/render/messages/"+b, nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/render/messages/"+gone, nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/render/messages/0199a000-0000-7000-8000-000000000000", nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/render/messages/nope", nil), 404, "not_found")
	expect(t, f.h.client().do("GET", "/render/messages/"+a, nil), 401, "unauthenticated")
}

func TestRenderPlainTextMessage(t *testing.T) {
	f := newRenderFixture(t)
	msg := f.message(f.conversation("plain", convOpt{mailbox: f.mailboxA}), f.mailboxA,
		msgOpt{text: "Zie <b>hier</b>: https://example.com/a?x=1&y=2."})
	r := f.agent.do("GET", "/render/messages/"+msg, nil)
	expect(t, r, 200, "")
	body := string(r.raw)
	if strings.Contains(body, "<b>hier") || !strings.Contains(body, "&lt;b&gt;hier&lt;/b&gt;") {
		t.Errorf("text not escaped: %s", body)
	}
	if !strings.Contains(body, `href="https://example.com/a?x=1&amp;y=2"`) || !strings.Contains(body, `rel="noopener noreferrer nofollow"`) {
		t.Errorf("url not linkified: %s", body)
	}
}

func TestRenderUnparseableHTMLFallsBackToText(t *testing.T) {
	f := newRenderFixture(t)
	msg := f.message(f.conversation("deep", convOpt{mailbox: f.mailboxA}), f.mailboxA,
		msgOpt{html: strings.Repeat("<div>", 5000), text: "leesbare tekst"})
	r := f.agent.do("GET", "/render/messages/"+msg, nil)
	expect(t, r, 200, "")
	if !strings.Contains(string(r.raw), "leesbare tekst") {
		t.Errorf("no text fallback: %s", r.raw)
	}
}

type detailMessage struct {
	ID               string `json:"id"`
	HasHTML          bool   `json:"has_html"`
	RenderURL        string `json:"render_url"`
	BlockedImages    int    `json:"blocked_images"`
	PhishingWarnings []struct {
		Kind   string `json:"kind"`
		Detail string `json:"detail"`
	} `json:"phishing_warnings"`
	Attachments []struct {
		ID          string `json:"id"`
		DownloadURL string `json:"download_url"`
		Dangerous   bool   `json:"dangerous"`
	} `json:"attachments"`
}

func (f *renderFixture) detail(c *client, conv string) []detailMessage {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/conversations/"+conv, nil)
	expect(f.h.t, r, 200, "")
	var out struct {
		Messages []detailMessage `json:"messages"`
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		f.h.t.Fatal(err)
	}
	return out.Messages
}

func TestConversationDetailRenderFields(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("fields", convOpt{mailbox: f.mailboxA})
	html := f.message(conv, f.mailboxA, msgOpt{
		from: "x@evil.test", fromName: "billing@bank.example", authResults: "mx.example; spf=fail; dkim=pass",
		replyTo: `[{"name":"","address":"r@other.test"}]`,
		html: `<img src="https://a.example.net/1.gif"><img src="https://a.example.net/2.gif">` +
			`<a href="https://evil.test">www.bank.example</a>`,
	})
	f.attachment(html, "setup.exe", "application/x-msdownload", "", "attachment", []byte("MZ"))
	f.attachment(html, "invoice.pdf", "application/pdf", "", "attachment", []byte("%PDF-1.4"))
	text := f.message(conv, f.mailboxA, msgOpt{text: "alleen tekst", from: "jane@customer.test"})

	byID := map[string]detailMessage{}
	for _, m := range f.detail(f.agent, conv) {
		byID[m.ID] = m
	}
	h := byID[html]
	if !h.HasHTML || h.RenderURL != "/render/messages/"+html || h.BlockedImages != 2 {
		t.Errorf("html message: %+v", h)
	}
	kinds := map[string]string{}
	for _, w := range h.PhishingWarnings {
		kinds[w.Kind] = w.Detail
	}
	for kind, detail := range map[string]string{
		"display_name_spoof": "billing@bank.example", "reply_to_mismatch": "other.test",
		"auth_failed": "spf", "link_mismatch": "www.bank.example -> evil.test",
	} {
		if kinds[kind] != detail {
			t.Errorf("warning %s = %q, want %q (all: %v)", kind, kinds[kind], detail, kinds)
		}
	}
	danger := map[string]bool{}
	for _, a := range h.Attachments {
		if a.DownloadURL != "/api/v1/attachments/"+a.ID+"/download" {
			t.Errorf("download url %q", a.DownloadURL)
		}
		danger[a.ID] = a.Dangerous
	}
	if len(danger) != 2 {
		t.Fatalf("attachments: %+v", h.Attachments)
	}
	var dangerous int
	for _, d := range danger {
		if d {
			dangerous++
		}
	}
	if dangerous != 1 {
		t.Errorf("want exactly the exe flagged, got %v", danger)
	}
	tm := byID[text]
	if tm.HasHTML || tm.RenderURL != "" || tm.BlockedImages != 0 || tm.PhishingWarnings == nil || len(tm.PhishingWarnings) != 0 {
		t.Errorf("text message: %+v", tm)
	}
}

func TestDownloadAttachment(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("dl", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{text: "x"})
	pdf := f.attachment(msg, `fac"tuur é.pdf`, "application/pdf", "", "attachment", []byte("%PDF-1.4 data"))
	exe := f.attachment(msg, "setup.exe", "application/x-msdownload", "", "attachment", []byte("MZ data"))
	html := f.attachment(msg, "page.html", "text/html", "", "attachment", []byte("<script>alert(1)</script>"))
	virus := f.attachment(msg, "virus.pdf", "application/pdf", "", "attachment", []byte("%PDF"))
	f.exec(`UPDATE attachments SET scan_status = 'infected' WHERE id = $1`, virus)
	other := f.attachment(f.message(f.conversation("b", convOpt{mailbox: f.mailboxB}), f.mailboxB, msgOpt{text: "b"}),
		"secret.pdf", "application/pdf", "", "attachment", []byte("%PDF secret"))

	r := f.readonly.do("GET", "/api/v1/attachments/"+pdf+"/download", nil)
	expect(t, r, 200, "")
	if string(r.raw) != "%PDF-1.4 data" {
		t.Errorf("body %q", r.raw)
	}
	wantCD := `attachment; filename="fac_tuur _.pdf"; filename*=UTF-8''fac%22tuur%20%C3%A9.pdf`
	if got := r.header.Get("Content-Disposition"); got != wantCD {
		t.Errorf("Content-Disposition %q, want %q", got, wantCD)
	}
	if r.header.Get("Content-Type") != "application/pdf" || r.header.Get("X-Content-Type-Options") != "nosniff" ||
		r.header.Get("Content-Security-Policy") != "sandbox" {
		t.Errorf("headers %v", r.header)
	}

	for _, id := range []string{exe, html} {
		r := f.agent.do("GET", "/api/v1/attachments/"+id+"/download", nil)
		expect(t, r, 200, "")
		if r.header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment;") {
			t.Errorf("dangerous file served as %q / %q", r.header.Get("Content-Type"), r.header.Get("Content-Disposition"))
		}
	}
	expect(t, f.agent.do("GET", "/api/v1/attachments/"+virus+"/download", nil), 403, "attachment_infected")
	expect(t, f.agent.do("GET", "/api/v1/attachments/"+other+"/download", nil), 404, "not_found")
	expect(t, f.admin.do("GET", "/api/v1/attachments/"+other+"/download", nil), 200, "")
	expect(t, f.agent.do("GET", "/api/v1/attachments/nope/download", nil), 404, "not_found")
	expect(t, f.h.client().do("GET", "/api/v1/attachments/"+pdf+"/download", nil), 401, "unauthenticated")
}

func TestContentDisposition(t *testing.T) {
	cases := map[string]string{
		"a.pdf":         `attachment; filename="a.pdf"; filename*=UTF-8''a.pdf`,
		"":              `attachment; filename="attachment"; filename*=UTF-8''`,
		"日本.pdf":        `attachment; filename="__.pdf"; filename*=UTF-8''%E6%97%A5%E6%9C%AC.pdf`,
		"a\r\nX: y.pdf": `attachment; filename="a__X: y.pdf"; filename*=UTF-8''a%0D%0AX%3A%20y.pdf`,
		`a\b%c.txt`:     `attachment; filename="a_b_c.txt"; filename*=UTF-8''a%5Cb%25c.txt`,
	}
	for in, want := range cases {
		if got := contentDisposition(in); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}

func (f *renderFixture) allow(c *client, conv, msg, scope string) response {
	return c.do("POST", "/api/v1/conversations/"+conv+"/messages/"+msg+"/allow-images?scope="+scope, nil)
}

func TestAllowImages(t *testing.T) {
	f := newRenderFixture(t)
	html := `<p>x</p><img src="https://cdn.example.net/a.png">`
	conv := f.conversation("allow", convOpt{mailbox: f.mailboxA})
	first := f.message(conv, f.mailboxA, msgOpt{html: html, from: "News@Shop.example"})
	second := f.message(conv, f.mailboxA, msgOpt{html: html, from: "other@shop.example"})
	third := f.message(conv, f.mailboxA, msgOpt{html: html, from: "someone@else.example"})
	convB := f.conversation("b", convOpt{mailbox: f.mailboxB})
	inB := f.message(convB, f.mailboxB, msgOpt{html: html, from: "other@shop.example"})

	blocked := func(id string) int {
		for _, m := range f.detail(f.admin, conv) {
			if m.ID == id {
				return m.BlockedImages
			}
		}
		t.Fatalf("message %s missing", id)
		return -1
	}
	renders := func(c *client, id, query string) bool {
		r := c.do("GET", "/render/messages/"+id+query, nil)
		expect(t, r, 200, "")
		return strings.Contains(string(r.raw), "/render/proxy?url=")
	}

	if blocked(first) != 1 || renders(f.agent, first, "") {
		t.Fatal("remote images must be blocked by default")
	}

	// once: no state, any reader.
	r := f.allow(f.readonly, conv, first, "once")
	expect(t, r, 200, "")
	if r.body["render_url"] != "/render/messages/"+first+"?images=once" {
		t.Errorf("once response %v", r.body)
	}
	if !renders(f.readonly, first, "?images=once") || renders(f.readonly, first, "") {
		t.Error("once must apply to that request only")
	}
	if n := f.h.count(`SELECT count(*) FROM sender_image_allowlist`); n != 0 {
		t.Errorf("once persisted %d rows", n)
	}

	// Persisting needs write access; readonly and out-of-scope users are refused.
	expect(t, f.allow(f.readonly, conv, first, "sender"), 403, "forbidden")
	expect(t, f.allow(f.agent, convB, inB, "sender"), 404, "not_found")
	expect(t, f.allow(f.agent, conv, inB, "sender"), 404, "not_found")
	expect(t, f.allow(f.agent, conv, first, "everyone"), 400, "invalid_request")
	expect(t, f.allow(f.agent, conv, "nope", "sender"), 404, "not_found")
	expect(t, f.allow(f.h.client(), conv, first, "sender"), 401, "unauthenticated")
	if n := f.h.count(`SELECT count(*) FROM sender_image_allowlist`); n != 0 {
		t.Fatalf("refused requests persisted %d rows", n)
	}

	r = f.allow(f.agent, conv, first, "sender")
	expect(t, r, 200, "")
	if r.body["render_url"] != "/render/messages/"+first {
		t.Errorf("sender response %v", r.body)
	}
	if !renders(f.agent, first, "") || blocked(first) != 0 {
		t.Error("sender allowlist not applied to that sender")
	}
	if renders(f.agent, second, "") || blocked(second) != 1 {
		t.Error("sender scope must not cover other addresses at the domain")
	}
	if n := f.h.count(`SELECT count(*) FROM sender_image_allowlist WHERE pattern = 'news@shop.example' AND mailbox_id = $1`, f.mailboxA); n != 1 {
		t.Errorf("pattern rows = %d, want 1 lower-cased", n)
	}
	expect(t, f.allow(f.agent, conv, first, "sender"), 200, "")
	if n := f.h.count(`SELECT count(*) FROM sender_image_allowlist`); n != 1 {
		t.Errorf("duplicate allow created %d rows", n)
	}

	expect(t, f.allow(f.agent, conv, second, "domain"), 200, "")
	if !renders(f.agent, second, "") || blocked(second) != 0 {
		t.Error("domain scope not applied")
	}
	if renders(f.agent, third, "") || blocked(third) != 1 {
		t.Error("domain scope leaked to another domain")
	}
	if n := f.h.count(`SELECT count(*) FROM sender_image_allowlist WHERE pattern = '@shop.example'`); n != 1 {
		t.Errorf("domain rows = %d", n)
	}
	// Per mailbox: the same sender in another mailbox is still blocked.
	if renders(f.admin, inB, "") {
		t.Error("allowlist entry leaked across mailboxes")
	}
	if n := f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'mailbox.sender_images_allowed'`); n != 3 {
		t.Errorf("audit entries = %d, want 3 (sender, sender again, domain)", n)
	}
}

func TestAllowImagesNeedsSenderAddress(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("noaddr", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{html: "<p>x</p>", from: "nobody"})
	expect(t, f.allow(f.agent, conv, msg, "sender"), 422, "validation_failed")
}

func (f *renderFixture) signedProxy(remote string) string {
	f.h.t.Helper()
	u, err := f.h.srv.signedProxyURL(remote)
	if err != nil {
		f.h.t.Fatal(err)
	}
	return u
}

func TestProxyRequiresSignature(t *testing.T) {
	f := newRenderFixture(t)
	remote := "https://cdn.example.net/a.png"
	good := f.signedProxy(remote)
	exp := time.Now().Add(-time.Minute).Unix()
	oldSig, err := f.h.srv.renderSignature("proxy", remote, exp)
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"no signature":  "/render/proxy?url=" + url.QueryEscape(remote),
		"bad signature": strings.Replace(good, "sig=", "sig=A", 1),
		"other url":     strings.Replace(good, url.QueryEscape(remote), url.QueryEscape("https://other.example.net/a.png"), 1),
		"expired":       "/render/proxy?url=" + url.QueryEscape(remote) + "&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + oldSig,
		"no url":        "/render/proxy?exp=1&sig=x",
	} {
		if r := f.plain(path); r.StatusCode != 404 {
			t.Errorf("%s: got %d, want 404", name, r.StatusCode)
		}
	}
}

func TestProxyRefusesUnsafeTargets(t *testing.T) {
	f := newRenderFixture(t)
	// Correctly signed, so only the target policy and the dialer stand in the way.
	for name, remote := range map[string]string{
		"loopback":      "https://127.0.0.1/a.png",
		"metadata":      "https://169.254.169.254/latest/meta-data/",
		"private":       "https://10.0.0.5/a.png",
		"ipv6 loopback": "https://[::1]/a.png",
		"localhost":     "https://localhost/a.png",
		"mapped":        "https://[::ffff:127.0.0.1]/a.png",
	} {
		r := f.plain(f.signedProxy(remote))
		if r.StatusCode != 502 {
			t.Errorf("%s: got %d, want 502", name, r.StatusCode)
		}
	}
	for name, remote := range map[string]string{
		"http":        "http://example.net/a.png",
		"ftp":         "ftp://example.net/a.png",
		"credentials": "https://user:pw@example.net/a.png",
		"odd port":    "https://example.net:22/a.png",
		"file":        "file:///etc/passwd",
	} {
		if r := f.plain(f.signedProxy(remote)); r.StatusCode != 404 {
			t.Errorf("%s: got %d, want 404", name, r.StatusCode)
		}
	}
}

// upstream is a TLS server the proxy is pointed at; the SSRF dialer is replaced by the test
// server's own transport, which is the only way to reach a loopback address.
func (f *renderFixture) upstream(h http.Handler) *httptest.Server {
	f.h.t.Helper()
	ts := httptest.NewTLSServer(h)
	f.h.t.Cleanup(ts.Close)
	f.h.srv.proxy.client.Transport = ts.Client().Transport
	u, err := url.Parse(ts.URL)
	if err != nil {
		f.h.t.Fatal(err)
	}
	f.h.srv.proxy.allowedPorts[u.Port()] = true
	return ts
}

func TestProxyFetchesSniffsAndCaches(t *testing.T) {
	f := newRenderFixture(t)
	var hits atomic.Int32
	var sawHeaders atomic.Value
	ts := f.upstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sawHeaders.Store(r.Header.Clone())
		switch r.URL.Path {
		case "/a.png":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(pngBytes)
		case "/page":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`))
		case "/big":
			_, _ = w.Write(append(append([]byte{}, pngBytes...), make([]byte, proxyMaxBytes)...))
		case "/missing":
			http.NotFound(w, r)
		case "/redirect":
			http.Redirect(w, r, "http://example.net/a.png", http.StatusFound)
		}
	}))

	r := f.plain(f.signedProxy(ts.URL + "/a.png"))
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("fetch: %d %v", r.StatusCode, r.Header)
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Error("proxied image must be served sandboxed")
	}
	h, _ := sawHeaders.Load().(http.Header)
	for _, leak := range []string{"Cookie", "Referer", "Authorization"} {
		if h.Get(leak) != "" {
			t.Errorf("upstream saw %s", leak)
		}
	}
	if strings.Contains(h.Get("User-Agent"), "Go-http-client") {
		t.Errorf("user agent %q", h.Get("User-Agent"))
	}
	f.plain(f.signedProxy(ts.URL + "/a.png"))
	if hits.Load() != 1 {
		t.Errorf("second request hit upstream again (%d hits)", hits.Load())
	}
	if n := f.h.count(`SELECT count(*) FROM image_proxy_cache`); n != 1 {
		t.Errorf("cache rows = %d", n)
	}

	for _, path := range []string{"/page", "/svg", "/big", "/missing", "/redirect"} {
		if r := f.plain(f.signedProxy(ts.URL + path)); r.StatusCode != 502 {
			t.Errorf("%s: got %d, want 502", path, r.StatusCode)
		}
	}
	if n := f.h.count(`SELECT count(*) FROM image_proxy_cache`); n != 1 {
		t.Errorf("rejected responses were cached: %d rows", n)
	}
}
