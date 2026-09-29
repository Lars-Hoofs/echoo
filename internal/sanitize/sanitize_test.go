package sanitize

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func run(in string) Result {
	res, err := HTML(in, Options{})
	if err != nil {
		panic(err)
	}
	return res
}

// forbidden lists substrings that must never survive sanitization, in any case.
var forbidden = []string{
	"<script", "<iframe", "<object", "<embed", "<form", "<input", "<button", "<base", "<meta", "<link", "<style",
	"<svg", "<math", "<video", "<audio", "<frame", "<applet", "<textarea", "<select",
	"javascript:", "vbscript:", "onerror", "onload", "onclick", "onmouseover", "onfocus", "onanimation",
	"expression(", "srcset", "background=", "formaction", "xlink:href", "@import", "url(",
}

func TestHostileMarkup(t *testing.T) {
	vectors := map[string]string{
		"script tag":         `<p>a</p><script>alert(1)</script>`,
		"script case":        `<ScRiPt>alert(1)</sCrIpT>`,
		"img onerror":        `<img src=x onerror=alert(1)>`,
		"img onerror quoted": `<img src="x" onerror="alert(1)">`,
		"svg onload":         `<svg onload=alert(1)>`,
		"svg script":         `<svg><script>alert(1)</script></svg>`,
		"svg animate":        `<svg><animate onbegin=alert(1) attributeName=x dur=1s>`,
		"math":               `<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
		"iframe":             `<iframe src="javascript:alert(1)"></iframe>`,
		"iframe srcdoc":      `<iframe srcdoc="<script>alert(1)</script>"></iframe>`,
		"object":             `<object data="javascript:alert(1)"></object>`,
		"embed":              `<embed src="javascript:alert(1)">`,
		"form":               `<form action="https://evil.test"><input name=pw><button>go</button></form>`,
		"form formaction":    `<button formaction="javascript:alert(1)">x</button>`,
		"base href":          `<base href="https://evil.test/"><a href="/x">x</a>`,
		"meta refresh":       `<meta http-equiv="refresh" content="0;url=https://evil.test">`,
		"link stylesheet":    `<link rel="stylesheet" href="https://evil.test/x.css">`,
		"style import":       `<style>@import url(https://evil.test/x.css);</style>x`,
		"style block":        `<style>body{background:url(https://evil.test/t.gif)}</style>x`,
		"js href":            `<a href="javascript:alert(1)">x</a>`,
		"js href entity":     `<a href="jav&#x61;script:alert(1)">x</a>`,
		"js href tab":        `<a href="java&#9;script:alert(1)">x</a>`,
		"js href newline":    `<a href="java&#10;script:alert(1)">x</a>`,
		"js href space":      `<a href=" javascript:alert(1)">x</a>`,
		"vbscript":           `<a href="vbscript:msgbox(1)">x</a>`,
		"data html href":     `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		"data html img":      `<img src="data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">`,
		"data svg img":       `<img src="data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+">`,
		"css expression":     `<p style="width:expression(alert(1))">x</p>`,
		"css url":            `<p style="background:url(https://evil.test/t.gif)">x</p>`,
		"css url escaped":    `<p style="background:u\72l(https://evil.test/t.gif)">x</p>`,
		"css behavior":       `<p style="behavior:url(x.htc)">x</p>`,
		"css moz binding":    `<p style="-moz-binding:url(https://evil.test/x.xml#y)">x</p>`,
		"css import":         `<p style="@import 'https://evil.test/x.css'">x</p>`,
		"css comment":        `<p style="color:red;/*x*/background:url(https://evil.test/t.gif)">x</p>`,
		"css position":       `<p style="position:fixed;top:0;left:0;width:100%;height:100%;background-color:white">Log in</p>`,
		"background attr":    `<table background="https://evil.test/t.gif"><tr><td background="https://evil.test/u.gif">x</td></tr></table>`,
		"body background":    `<body background="https://evil.test/t.gif" onload=alert(1)>x</body>`,
		"srcset":             `<img src="cid:a" srcset="https://evil.test/a.gif 1x, https://evil.test/b.gif 2x">`,
		"picture source":     `<picture><source srcset="https://evil.test/a.gif"><img src="cid:a"></picture>`,
		"video poster":       `<video poster="https://evil.test/p.gif" src="https://evil.test/v.mp4"></video>`,
		"input image":        `<input type="image" src="https://evil.test/t.gif">`,
		"noscript trick":     `<noscript><p title="</noscript><img src=x onerror=alert(1)>"></noscript>`,
		"mutation svg style": `<svg></p><style><a id="</style><img src=1 onerror=alert(1)>">`,
		"nested form":        `<form><math><mtext></form><form><mglyph><style></math><img src onerror=alert(1)>`,
		"backtick attr":      "<img src=`x` onerror=alert(1)>",
		"null bytes":         "<scr\x00ipt>alert(1)</scr\x00ipt>",
		"comment script":     `<!--[if gte mso 9]><script>alert(1)</script><![endif]-->`,
		"xml pi":             `<?xml version="1.0"?><p onclick=alert(1)>x</p>`,
		"target frame":       `<a href="https://x.test" target="_top">x</a>`,
		"rel override":       `<a href="https://x.test" rel="opener">x</a>`,
		"protocol relative":  `<a href="//evil.test/x">x</a>`,
		"relative app link":  `<a href="/api/v1/auth/logout">x</a>`,
		"event on body":      `<body onload=alert(1)><div onmouseover=alert(1)>x</div></body>`,
		"details ontoggle":   `<details open ontoggle=alert(1)>x</details>`,
		"marquee":            `<marquee onstart=alert(1)>x</marquee>`,
		"formaction on div":  `<div formaction="javascript:alert(1)">x</div>`,
		"font face url":      `<font face="x;background:url(https://evil.test/t.gif)">x</font>`,
	}
	for name, in := range vectors {
		t.Run(name, func(t *testing.T) {
			got := strings.ToLower(run(in).HTML)
			for _, bad := range forbidden {
				if strings.Contains(got, bad) {
					t.Errorf("output contains %q\n in:  %s\n out: %s", bad, in, got)
				}
			}
			if strings.Contains(got, "evil.test") && !strings.Contains(got, `href="http`) {
				t.Errorf("remote host survived outside an http(s) link\n in:  %s\n out: %s", in, got)
			}
		})
	}
}

func TestAllowedFormattingSurvives(t *testing.T) {
	in := `<div style="color: #333; font-family: 'Helvetica Neue', Arial; padding:4px"><p><b>Hallo</b> <i>wereld</i></p>` +
		`<table width="100%" cellpadding="4" bgcolor="#eeeeee"><tr><td align="center" colspan="2">cel</td></tr></table>` +
		`<ul><li>een</li></ul><blockquote>quote</blockquote><h2>Kop</h2></div>`
	got := run(in).HTML
	for _, want := range []string{`<b>Hallo</b>`, `<i>wereld</i>`, `<table`, `colspan="2"`, `bgcolor="#eeeeee"`, `<blockquote>`, `<h2>Kop</h2>`, `color: #333`, `padding: 4px`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestBackgroundShorthandKeepsPlainColoursOnly(t *testing.T) {
	got := run(`<table><tr><td style="background:#0b5cad;color:#fff">a</td><td style="background:#fff url(https://evil.test/t.gif)">b</td></tr></table>`).HTML
	if !strings.Contains(got, "background: #0b5cad") {
		t.Errorf("plain background colour lost: %s", got)
	}
	if strings.Contains(got, "evil.test") || strings.Contains(got, "url(") {
		t.Errorf("background image survived: %s", got)
	}
}

func TestLinksGetSafeAttributes(t *testing.T) {
	got := run(`<a href="https://example.com/a" target="_top" rel="opener" onclick="x()">x</a><a href="mailto:a@b.test">m</a><a name="anchor">n</a>`).HTML
	link := regexp.MustCompile(`<a [^>]*>`)
	tags := link.FindAllString(got, -1)
	if len(tags) != 3 {
		t.Fatalf("want 3 anchors, got %v", tags)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, `target="_blank"`) || !strings.Contains(tag, `rel="noopener noreferrer nofollow"`) {
			t.Errorf("anchor without safe target/rel: %s", tag)
		}
	}
	if strings.Contains(got, "_top") || strings.Contains(got, `"opener"`) {
		t.Errorf("sender-controlled target/rel survived: %s", got)
	}
}

func TestDangerousLinkTargetsAreRemoved(t *testing.T) {
	for _, href := range []string{"javascript:alert(1)", "//evil.test/x", "/relative", "ftp://x.test/f", "data:text/html,x", "file:///etc/passwd"} {
		got := run(`<a href="` + href + `">x</a>`).HTML
		if strings.Contains(got, "href=") {
			t.Errorf("href %q survived: %s", href, got)
		}
	}
}

func TestLinkMismatch(t *testing.T) {
	cases := []struct {
		name, in string
		want     int
	}{
		{"different host", `<a href="https://evil.test/login">https://bank.example.com/login</a>`, 1},
		{"bare domain text", `<a href="https://evil.test">bank.example.com</a>`, 1},
		{"lookalike suffix", `<a href="https://bank.example.com.evil.test/">bank.example.com</a>`, 1},
		{"same host", `<a href="https://bank.example.com/a">https://bank.example.com/b</a>`, 0},
		{"www ignored", `<a href="https://www.example.com/">example.com</a>`, 0},
		{"subdomain", `<a href="https://mail.example.com/">example.com</a>`, 0},
		{"plain words", `<a href="https://evil.test">Klik hier</a>`, 0},
		{"nested markup", `<a href="https://evil.test"><b>www.bank.example.com</b></a>`, 1},
		{"mailto", `<a href="mailto:a@b.test">bank.example.com</a>`, 0},
		{"duplicates collapse", strings.Repeat(`<a href="https://evil.test">bank.example.com</a>`, 3), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(c.in).Mismatches; len(got) != c.want {
				t.Fatalf("want %d mismatches, got %v", c.want, got)
			}
		})
	}
}

func TestRemoteImagesBlockedByDefault(t *testing.T) {
	in := `<img src="https://track.example.net/p.gif?u=1" width="1" height="1"><img src="http://cdn.example.net/logo.png"><img src="//cdn.example.net/x.png">`
	res := run(in)
	if res.BlockedImages != 3 {
		t.Fatalf("blocked = %d, want 3", res.BlockedImages)
	}
	if strings.Contains(res.HTML, "example.net") {
		t.Fatalf("remote URL survived: %s", res.HTML)
	}
	if !strings.Contains(res.HTML, "data:image/gif;base64,") {
		t.Fatalf("no placeholder: %s", res.HTML)
	}
}

func TestRemoteImagesThroughProxy(t *testing.T) {
	var seen []string
	res, err := HTML(`<img src="http://cdn.example.net/a.png?x=1&y=2#frag" alt="logo">`, Options{ProxyURL: func(u string) string {
		seen = append(seen, u)
		return "/render/proxy?url=abc"
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "https://cdn.example.net/a.png?x=1&y=2" {
		t.Fatalf("proxy saw %v", seen)
	}
	if res.BlockedImages != 0 || !strings.Contains(res.HTML, `src="/render/proxy?url=abc"`) {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestProxyOnlyFedHTTPURLs(t *testing.T) {
	var seen []string
	opts := Options{ProxyURL: func(u string) string { seen = append(seen, u); return "/render/proxy?url=x" }}
	if _, err := HTML(`<img src="https://user:pw@cdn.example.net/a.png"><img src="ftp://x.test/a.png"><img src="file:///etc/passwd"><img src="jav&#x61;script:alert(1)">`, opts); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("proxy was asked to fetch %v", seen)
	}
}

func TestCIDImages(t *testing.T) {
	opts := Options{ResolveCID: func(cid string) (string, bool) {
		if cid == "logo@x" {
			return "/render/attachments/0199a000-0000-7000-8000-000000000000?exp=1&sig=2", true
		}
		return "", false
	}}
	res, err := HTML(`<img src="cid:logo@x"><img src="cid:LOGO@X"><img src="cid:missing">`, opts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.HTML, "/render/attachments/0199a000-0000-7000-8000-000000000000") != 2 || strings.Count(res.HTML, "data:image/gif") != 1 {
		t.Fatalf("cid resolution is case-insensitive, unknown cids get the placeholder: %s", res.HTML)
	}
	if res.BlockedImages != 0 {
		t.Fatalf("cid images are not remote: %d", res.BlockedImages)
	}
}

func TestDataImages(t *testing.T) {
	png := "data:image/png;base64,iVBORw0KGgo="
	res := run(`<img src="` + png + `"><img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=">`)
	if strings.Count(res.HTML, png) != 1 {
		t.Fatalf("png data URI should survive exactly once: %s", res.HTML)
	}
	if strings.Contains(res.HTML, "svg") {
		t.Fatalf("svg data URI survived: %s", res.HTML)
	}
}

func TestOversizedInputIsTruncated(t *testing.T) {
	res := run(strings.Repeat("<p>é</p>", MaxInputBytes))
	if !res.Truncated {
		t.Fatal("expected Truncated")
	}
}

func TestDeeplyNestedInputIsRefused(t *testing.T) {
	_, err := HTML(strings.Repeat("<div>", 100000), Options{})
	if !errors.Is(err, ErrUnparseable) {
		t.Fatalf("err = %v, want ErrUnparseable", err)
	}
}

func TestTextEscapesAndLinkifies(t *testing.T) {
	got := Text("Zie <b>dit</b> & https://example.com/a?x=1&y=2, of (https://example.org/x).\n<script>alert(1)</script> javascript:alert(1)")
	if strings.Contains(got, "<b>") || strings.Contains(got, "<script>") {
		t.Fatalf("not escaped: %s", got)
	}
	for _, want := range []string{
		`<a href="https://example.com/a?x=1&amp;y=2" target="_blank" rel="noopener noreferrer nofollow">https://example.com/a?x=1&amp;y=2</a>,`,
		`(<a href="https://example.org/x" target="_blank" rel="noopener noreferrer nofollow">https://example.org/x</a>).`,
		"&lt;b&gt;dit&lt;/b&gt; &amp;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, `href="javascript`) {
		t.Fatal("javascript link")
	}
}

func TestTextQuoteInURLCannotBreakAttribute(t *testing.T) {
	got := Text(`https://example.com/"onmouseover="alert(1)`)
	if strings.Contains(got, `"onmouseover`) {
		t.Fatalf("attribute injection: %s", got)
	}
}

func TestDocumentCSPHashMatchesScript(t *testing.T) {
	doc := string(Document("<p>x</p>", false, false))
	if !strings.Contains(doc, "<script>"+resizeScript+"</script>") {
		t.Fatal("resize script not embedded verbatim")
	}
	for _, want := range []string{"default-src 'none'", "img-src 'self' data:", "style-src 'unsafe-inline'", "frame-ancestors 'self'", "script-src 'sha256-"} {
		if !strings.Contains(ContentSecurityPolicy, want) {
			t.Errorf("CSP lacks %q: %s", want, ContentSecurityPolicy)
		}
	}
}

// The allowlist is the last gate: even if a rewrite hook returned a remote URL, it would not
// reach the output.
func TestAllowlistBackstopsRewriteMistakes(t *testing.T) {
	res, err := HTML(`<img src="https://cdn.example.net/a.png">`, Options{ProxyURL: func(u string) string { return u }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.HTML, "example.net") {
		t.Fatalf("remote URL reached the output: %s", res.HTML)
	}
}
