package kb

import (
	"strings"
	"testing"
	"time"
)

const imgID = "0199a000-0000-7000-8000-000000000001"

func TestSanitizeDropsScriptVectors(t *testing.T) {
	vectors := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<a href="JaVaScRiPt:alert(1)">x</a>`,
		`<a href="&#x6A;avascript:alert(1)">x</a>`,
		`<a href="data:text/html,<script>alert(1)</script>">x</a>`,
		`<a href="//evil.example/x">x</a>`,
		`<a href="/api/v1/me">x</a>`,
		`<p onclick="alert(1)" style="color:red" class="x">x</p>`,
		`<svg onload=alert(1)><circle/></svg>`,
		`<iframe src="https://evil.example"></iframe>`,
		`<object data="x"></object><embed src="x">`,
		`<form action="https://evil.example"><input name=x></form>`,
		`<math><mi xlink:href="javascript:alert(1)">x</mi></math>`,
		`<img src="https://evil.example/track.gif">`,
		`<img src="/hulp/i/../../etc/passwd">`,
		`<img src="/hulp/i/` + imgID + `?x=1">`,
		`<table><tr><td colspan="1000" onclick="x">x</td></tr></table>`,
		`<style>body{display:none}</style>`,
		`<meta http-equiv="refresh" content="0;url=https://evil.example">`,
		`<base href="https://evil.example/">`,
		`<a href="https://ok.example" target="_blank" onmouseover="x">x</a>`,
	}
	bad := []string{"<script", "onerror", "onclick", "onload", "onmouseover", "javascript:", "data:", "style", "class=", "<iframe", "<svg", "<form", "<style", "<meta", "<base", "evil.example/track", "//evil", "target=", `href="/api`, "etc/passwd", "?x=1", "colspan=\"1000\""}
	for _, v := range vectors {
		out := Sanitize(v)
		for _, b := range bad {
			if strings.Contains(strings.ToLower(out), strings.ToLower(b)) {
				t.Errorf("Sanitize(%q) = %q still contains %q", v, out, b)
			}
		}
	}
}

func TestSanitizeKeepsArticleMarkup(t *testing.T) {
	in := `<h2>Titel</h2><p>Tekst met <strong>vet</strong>, <a href="https://example.com/a?b=1">link</a> en <a href="/hulp/a/ander">intern</a>.</p>` +
		`<pre><code>go run .</code></pre><blockquote>citaat</blockquote><ul><li>een</li></ul>` +
		`<table><thead><tr><th colspan="2">K</th></tr></thead><tbody><tr><td>a</td><td>b</td></tr></tbody></table>` +
		`<img src="/hulp/i/` + imgID + `" alt="Schermafbeelding">`
	out := Sanitize(in)
	for _, want := range []string{"<h2>Titel</h2>", "<strong>vet</strong>", `href="https://example.com/a?b=1"`, `href="/hulp/a/ander"`, "<pre><code>go run .</code></pre>",
		"<blockquote>citaat</blockquote>", `<th colspan="2">`, "<td>a</td>", `src="/hulp/i/` + imgID + `"`, `alt="Schermafbeelding"`, `rel="nofollow noreferrer"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}
}

func TestImageIDs(t *testing.T) {
	out := Sanitize(`<img src="/hulp/i/` + imgID + `"><p><img src="/hulp/i/` + imgID + `"></p><img src="https://x.example/a.png">`)
	got := ImageIDs(out)
	if len(got) != 1 || got[0] != imgID {
		t.Fatalf("ImageIDs = %v", got)
	}
}

func TestTextAndExcerpt(t *testing.T) {
	text := Text(`<h2>Kop</h2><p>een<br>twee</p><table><tr><td>a</td><td>b</td></tr></table><p>x` + "\x02" + `y</p>`)
	if text != "Kop een twee a b xy" {
		t.Fatalf("Text = %q", text)
	}
	if got := Text(`<p>kies <strong>Wachtwoord vergeten</strong>. Klaar, zie <a href="/x">hier</a>.</p>`); got != "kies Wachtwoord vergeten. Klaar, zie hier." {
		t.Fatalf("inline Text = %q", got)
	}
	long := strings.Repeat("woord ", 100)
	ex := Excerpt(strings.TrimSpace(long), 50)
	if len([]rune(ex)) > 51 || !strings.HasSuffix(ex, "…") || strings.Contains(ex, "woor…") {
		t.Fatalf("Excerpt = %q", ex)
	}
	if Excerpt("kort", 50) != "kort" {
		t.Fatal("short text must stay")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Hoe betaal ik een factuur?": "hoe-betaal-ik-een-factuur",
		"  Café: één ëxtra  ":        "cafe-een-extra",
		"Straße & Œuvre":             "strasse-oeuvre",
		"---":                        "",
		"":                           "",
		"a_b/c":                      "a-b-c",
		strings.Repeat("ab ", 60):    strings.TrimRight(strings.Repeat("ab-", 27)[:80], "-"),
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
		if got := Slugify(in); got != "" && !ValidSlug(got) {
			t.Errorf("Slugify(%q) = %q is not a valid slug", in, got)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "a--b", "A", "a b", "a/b", strings.Repeat("a", 81)} {
		if ValidSlug(bad) {
			t.Errorf("ValidSlug(%q) = true", bad)
		}
	}
}

func TestFeedbackToken(t *testing.T) {
	s := NewFeedbackSigner([]byte("key"))
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tok := s.Token("a1", now)
	if tok != s.Token("a1", now.Add(3*time.Hour)) {
		t.Error("the token must be stable within a day so cached pages stay identical")
	}
	if !s.Valid("a1", tok, now.Add(30*time.Hour)) {
		t.Error("a token stays valid into the next day")
	}
	if s.Valid("a1", tok, now.Add(72*time.Hour)) {
		t.Error("an old token must expire")
	}
	if s.Valid("a2", tok, now) {
		t.Error("a token is bound to its article")
	}
	if NewFeedbackSigner([]byte("other")).Valid("a1", tok, now) {
		t.Error("a token is bound to the key")
	}
	if s.Valid("a1", "", now) || s.Valid("a1", "garbage", now) {
		t.Error("garbage must not validate")
	}
}

func TestParseSnippet(t *testing.T) {
	parts := ParseSnippet("voor " + HitStart + "<b>factuur</b>" + HitEnd + " na")
	if len(parts) != 3 || parts[0].Hit || !parts[1].Hit || parts[1].Text != "<b>factuur</b>" || parts[2].Text != " na" {
		t.Fatalf("parts = %#v", parts)
	}
	if got := ParseSnippet("zonder"); len(got) != 1 || got[0].Hit {
		t.Fatalf("plain = %#v", got)
	}
	if got := ParseSnippet(HitStart + "open"); len(got) != 1 || !got[0].Hit {
		t.Fatalf("unterminated = %#v", got)
	}
}

func TestSiteRendersAndEscapes(t *testing.T) {
	site, err := NewSite()
	if err != nil {
		t.Fatal(err)
	}
	_, css := site.CSS()
	if !strings.HasPrefix(css, "/hulp/static/kb.") || !strings.HasSuffix(css, ".css") {
		t.Fatalf("css path = %q", css)
	}
	page := Page{Title: `T"><script>x</script>`, CSS: css, Portal: Portal{Name: "<i>Naam</i>"}}
	out, err := site.Render("search", SearchData{Page: page, Query: `"><script>alert(1)</script>`, Results: []Result{
		{Slug: "s", Title: "<b>t</b>", Snippet: []SnippetPart{{Text: "<script>", Hit: true}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "<script") || strings.Contains(string(out), "<b>t") || strings.Contains(string(out), "<i>Naam") {
		t.Errorf("unescaped output: %s", out)
	}
	for _, name := range pageNames {
		if _, ok := site.pages[name]; !ok {
			t.Errorf("page %s missing", name)
		}
	}
}

func TestFeedbackTokenSurvivesKeyRotation(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tok := NewFeedbackSigner([]byte("old")).Token("a1", now)
	if !NewFeedbackSigner([]byte("new"), []byte("old")).Valid("a1", tok, now) {
		t.Error("a token from before the rotation must stay valid")
	}
	if NewFeedbackSigner([]byte("new")).Valid("a1", tok, now) {
		t.Error("a token of a removed key must fail")
	}
}
