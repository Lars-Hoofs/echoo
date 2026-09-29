package compose

import (
	"strings"
	"testing"

	"echoo/internal/mail"
)

func TestSanitizeStripsEverythingOutsideTheEditorSet(t *testing.T) {
	tests := map[string]struct {
		in   string
		opts SanitizeOptions
		want string
	}{
		"script removed":     {in: `<p>a</p><script>alert(1)</script>`, want: `<p>a</p>`},
		"event handler":      {in: `<p onclick="x()">a</p>`, want: `<p>a</p>`},
		"style attribute":    {in: `<p style="color:red">a</p>`, want: `<p>a</p>`},
		"javascript url":     {in: `<a href="javascript:alert(1)">a</a>`, want: `a`},
		"data url":           {in: `<a href="data:text/html,x">a</a>`, want: `a`},
		"iframe":             {in: `<iframe src="https://x"></iframe><p>a</p>`, want: `<p>a</p>`},
		"table dropped":      {in: `<table><tr><td>a</td></tr></table>`, want: `a`},
		"remote image":       {in: `<img src="https://evil.test/p.png">`, want: ``},
		"cid without upload": {in: `<img src="cid:x@echoo.upload">`, want: ``},
		"span without opt":   {in: `<span data-type="mention" data-id="0199a000-0000-7000-8000-000000000000">@x</span>`, want: `@x`},
		"formatting kept":    {in: `<h1>t</h1><p><strong>a</strong> <em>b</em> <u>c</u> <s>d</s> <code>e</code></p><blockquote>q</blockquote><ul><li>x</li></ul><ol><li>y</li></ol><pre>p</pre>`, want: `<h1>t</h1><p><strong>a</strong> <em>b</em> <u>c</u> <s>d</s> <code>e</code></p><blockquote>q</blockquote><ul><li>x</li></ul><ol><li>y</li></ol><pre>p</pre>`},
		"h4 dropped":         {in: `<h4>a</h4>`, want: `a`},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Sanitize(tt.in, tt.opts); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeLinks(t *testing.T) {
	got := Sanitize(`<a href="https://example.com/x?a=1" target="_blank" onclick="x">l</a><a href="mailto:a@b.nl">m</a>`, SanitizeOptions{})
	for _, want := range []string{`href="https://example.com/x?a=1"`, `rel="nofollow noreferrer"`, `href="mailto:a@b.nl"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "target") || strings.Contains(got, "onclick") {
		t.Errorf("unexpected attribute in %q", got)
	}
}

func TestSanitizeAllowsOnlyOwnContentIDs(t *testing.T) {
	opts := SanitizeOptions{ContentIDs: []string{"aa@echoo.upload"}}
	got := Sanitize(`<img src="cid:aa@echoo.upload" alt="foto"><img src="cid:bb@echoo.upload"><img src="cid:aa@echoo.upload.evil">`, opts)
	if got != `<img src="cid:aa@echoo.upload" alt="foto">` {
		t.Fatalf("got %q", got)
	}
	if cids := CIDs(got); len(cids) != 1 || cids[0] != "aa@echoo.upload" {
		t.Fatalf("CIDs = %v", cids)
	}
	if IsEmpty(got) {
		t.Fatal("a message with only an image is not empty")
	}
}

func TestSanitizeMentions(t *testing.T) {
	in := `<p><span data-type="mention" data-id="0199a000-0000-7000-8000-000000000000" data-label="Sanne" class="x" onclick="y">@Sanne</span></p>`
	got := Sanitize(in, SanitizeOptions{Mentions: true})
	want := `<p><span data-type="mention" data-id="0199a000-0000-7000-8000-000000000000" data-label="Sanne">@Sanne</span></p>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := Sanitize(`<span data-type="mention" data-id="not-a-uuid">@x</span>`, SanitizeOptions{Mentions: true}); strings.Contains(got, "data-id") {
		t.Fatalf("invalid id survived: %q", got)
	}
}

func TestTextFromHTML(t *testing.T) {
	in := `<p>Hallo <strong>Jan</strong>,</p><p>Zie <a href="https://example.com">de site</a> of <a href="mailto:a@b.nl">a@b.nl</a>.</p><ul><li>een</li><li>twee</li></ul><ol><li>x</li><li>y</li></ol><p>a<br>b</p><blockquote>citaat &amp; meer</blockquote>`
	want := "Hallo Jan,\n\nZie de site (https://example.com) of a@b.nl.\n\n- een\n- twee\n\n1. x\n2. y\n\na\nb\n\ncitaat & meer"
	if got := TextFromHTML(in); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestIsEmpty(t *testing.T) {
	for in, want := range map[string]bool{"": true, "<p></p>": true, "<p> <br></p>": true, "<p>x</p>": false} {
		if got := IsEmpty(in); got != want {
			t.Errorf("IsEmpty(%q) = %v", in, got)
		}
	}
}

func TestLayoutEscapesAndKeepsPlainText(t *testing.T) {
	h, txt := Layout{
		BrandName: `Acme <script>`, BodyHTML: `<p>Hallo</p>`, SignatureHTML: `<p>Groet, Sanne</p>`, FooterText: `Acme & Co`,
	}.Render()
	if strings.Contains(h, "<script>") || !strings.Contains(h, "Acme &lt;script&gt;") || !strings.Contains(h, "Acme &amp; Co") {
		t.Errorf("brand or footer not escaped: %s", h)
	}
	if !strings.Contains(h, "<table") || strings.Contains(h, "<style") {
		t.Errorf("layout must be tables with inline styles only: %s", h)
	}
	want := "Hallo\n\n-- \nGroet, Sanne\n\nAcme & Co"
	if txt != want {
		t.Errorf("text = %q, want %q", txt, want)
	}
	_, txt = Layout{BodyHTML: `<p>x</p>`, BodyText: "eigen tekst"}.Render()
	if txt != "eigen tekst" {
		t.Errorf("explicit text ignored: %q", txt)
	}
}

func TestVariablesRender(t *testing.T) {
	v := Variables{ContactName: `Jan <b>de</b> Vries`, ContactEmail: "jan@example.com", AgentName: "Sanne Bakker", ConversationNumber: "42", MailboxName: "Support"}
	got, unresolved := v.Render(`Hoi {{contact.first_name}} ({{ contact.email }}), ik ben {{agent.first_name}} #{{conversation.number}} van {{mailbox.name}}. {{contact.name}} {{unknown.thing}} {{contact.first_name}}`)
	want := `Hoi Jan (jan@example.com), ik ben Sanne #42 van Support. Jan &lt;b&gt;de&lt;/b&gt; Vries {{unknown.thing}} Jan`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(unresolved) != 1 || unresolved[0] != "unknown.thing" {
		t.Fatalf("unresolved = %v", unresolved)
	}
}

func TestVariablesEmptyValuesStayVisible(t *testing.T) {
	got, unresolved := Variables{ContactEmail: "x@example.com"}.Render(`{{contact.first_name}}|{{contact.name}}`)
	if got != `{{contact.first_name}}|x@example.com` || len(unresolved) != 1 || unresolved[0] != "contact.first_name" {
		t.Fatalf("got %q %v", got, unresolved)
	}
}

func TestVariablesRenderTextDoesNotEscape(t *testing.T) {
	got, _ := Variables{ContactName: "O'Brien & Zn"}.RenderText("Re: {{contact.name}}")
	if got != "Re: O'Brien & Zn" {
		t.Fatalf("got %q", got)
	}
}

func addr(a string) mail.Address { return mail.Address{Address: a} }

func TestReplyRecipients(t *testing.T) {
	own := map[string]bool{"support@acme.nl": true, "sales@acme.nl": true}
	everyone := map[string]bool{"jan@x.nl": true, "noreply-desk@x.nl": true, "piet@x.nl": true, "kim@x.nl": true}
	t.Run("reply-to wins and cc drops own addresses", func(t *testing.T) {
		in := Message{From: addr("jan@x.nl"), ReplyTo: []mail.Address{addr("noreply-desk@x.nl")}, To: []mail.Address{addr("support@acme.nl"), addr("piet@x.nl")}, Cc: []mail.Address{addr("sales@acme.nl"), addr("kim@x.nl")}}
		to, cc, _ := ReplyRecipients(in, &in, &in, everyone, own)
		if len(to) != 1 || to[0].Address != "noreply-desk@x.nl" {
			t.Errorf("to = %v", to)
		}
		if len(cc) != 2 || cc[0].Address != "piet@x.nl" || cc[1].Address != "kim@x.nl" {
			t.Errorf("cc = %v", cc)
		}
	})
	t.Run("from when no reply-to", func(t *testing.T) {
		in := Message{From: addr("jan@x.nl"), To: []mail.Address{addr("support@acme.nl")}}
		to, cc, _ := ReplyRecipients(in, &in, &in, everyone, own)
		if len(to) != 1 || to[0].Address != "jan@x.nl" || len(cc) != 0 {
			t.Errorf("to = %v cc = %v", to, cc)
		}
	})
	t.Run("customer is not repeated in cc after our own reply", func(t *testing.T) {
		inbound := Message{From: addr("jan@x.nl"), To: []mail.Address{addr("support@acme.nl")}}
		last := Message{From: addr("support@acme.nl"), To: []mail.Address{addr("jan@x.nl")}, Cc: []mail.Address{addr("kim@x.nl")}}
		to, cc, _ := ReplyRecipients(last, &inbound, &inbound, everyone, own)
		if len(to) != 1 || to[0].Address != "jan@x.nl" || len(cc) != 1 || cc[0].Address != "kim@x.nl" {
			t.Errorf("to = %v cc = %v", to, cc)
		}
	})
	t.Run("a stranger in the thread never becomes the recipient", func(t *testing.T) {
		known := map[string]bool{"jan@x.nl": true}
		jan := Message{From: addr("jan@x.nl"), To: []mail.Address{addr("support@acme.nl")}}
		stranger := Message{From: addr("evil@bad.example"), To: []mail.Address{addr("support@acme.nl"), addr("victim@bad.example")}, Cc: []mail.Address{addr("jan@x.nl")}}
		to, cc, suggested := ReplyRecipients(stranger, &stranger, &jan, known, own)
		if len(to) != 1 || to[0].Address != "jan@x.nl" {
			t.Errorf("to = %v", to)
		}
		if len(cc) != 0 {
			t.Errorf("cc = %v", cc)
		}
		if len(suggested) != 2 || suggested[0].Address != "evil@bad.example" || suggested[1].Address != "victim@bad.example" {
			t.Errorf("suggested = %v", suggested)
		}
	})
	t.Run("a stranger writing after our reply keeps the customer we wrote to", func(t *testing.T) {
		known := map[string]bool{"jan@x.nl": true}
		last := Message{From: addr("support@acme.nl"), To: []mail.Address{addr("jan@x.nl")}}
		stranger := Message{From: addr("evil@bad.example"), To: []mail.Address{addr("support@acme.nl")}}
		to, _, suggested := ReplyRecipients(last, &stranger, nil, known, own)
		if len(to) != 1 || to[0].Address != "jan@x.nl" || len(suggested) != 1 || suggested[0].Address != "evil@bad.example" {
			t.Errorf("to = %v suggested = %v", to, suggested)
		}
	})
	t.Run("a stranger naming the customer in Reply-To is still a stranger", func(t *testing.T) {
		known := map[string]bool{"jan@x.nl": true}
		jan := Message{From: addr("jan@x.nl"), To: []mail.Address{addr("support@acme.nl")}}
		forged := Message{From: addr("a@evil.example"), ReplyTo: []mail.Address{addr("jan@x.nl")}, To: []mail.Address{addr("support@acme.nl")}, Cc: []mail.Address{addr("a@evil.example")}}
		to, cc, suggested := ReplyRecipients(forged, &forged, &jan, known, own)
		if len(to) != 1 || to[0].Address != "jan@x.nl" || len(cc) != 0 {
			t.Errorf("to = %v cc = %v", to, cc)
		}
		if len(suggested) != 1 || suggested[0].Address != "a@evil.example" {
			t.Errorf("suggested = %v", suggested)
		}
	})
	t.Run("no inbound mail replies to whoever we wrote", func(t *testing.T) {
		last := Message{From: addr("support@acme.nl"), To: []mail.Address{addr("jan@x.nl")}}
		to, _, _ := ReplyRecipients(last, nil, nil, everyone, own)
		if len(to) != 1 || to[0].Address != "jan@x.nl" {
			t.Errorf("to = %v", to)
		}
	})
}

func TestNormalizeAddresses(t *testing.T) {
	got, err := NormalizeAddresses([]mail.Address{{Name: " Jan ", Address: " Jan@X.nl "}, {Address: "jan@x.nl"}})
	if err != nil || len(got) != 1 || got[0].Address != "jan@x.nl" || got[0].Name != "Jan" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"", "not an address", "Jan <jan@x.nl>", "a@b.nl\r\nBcc: x@y.nl", "a@b.nl, c@d.nl"} {
		if _, err := NormalizeAddresses([]mail.Address{{Address: bad}}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := NormalizeAddresses([]mail.Address{{Name: "a\r\nBcc: x", Address: "a@b.nl"}}); err == nil {
		t.Error("line break in name accepted")
	}
}

func TestSubjects(t *testing.T) {
	for in, want := range map[string]string{"Factuur": "Re: Factuur", "RE: Factuur": "RE: Factuur", "Aw: x": "Aw: x", "": "Re:"} {
		if got := ReplySubject(in); got != want {
			t.Errorf("ReplySubject(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"Factuur": "Fwd: Factuur", "Fwd: Factuur": "Fwd: Factuur", "FW: x": "FW: x"} {
		if got := ForwardSubject(in); got != want {
			t.Errorf("ForwardSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeFilename(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd":       "passwd",
		`C:\Users\x\factuur.pdf`: "factuur.pdf",
		"fac\r\ntuur.pdf":        "factuur.pdf",
		"invoice\u202Efdp.exe":   "invoicefdp.exe",
		"":                       "bijlage",
		"...":                    "bijlage",
		strings.Repeat("é", 300): strings.Repeat("é", 127),
	} {
		if got := SafeFilename(in); got != want {
			t.Errorf("SafeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
