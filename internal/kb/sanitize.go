// Package kb holds the domain logic of the knowledge base: sanitizing article HTML, slugs,
// the signed feedback token and the templates of the public site.
package kb

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

const uuidPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

var (
	// Relative links are limited to the help center itself; "//host" and "/other" never match.
	linkHref = regexp.MustCompile(`(?i)^(https?://|mailto:|/hulp/)`)
	imageSrc = regexp.MustCompile(`^/hulp/i/(` + uuidPattern + `)$`)
	altText  = regexp.MustCompile(`^[^<>"&]{0,200}$`)
	spanNum  = regexp.MustCompile(`^[1-9][0-9]?$`)

	policy = newPolicy()
)

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "em", "u", "s", "ul", "ol", "li", "blockquote", "code", "pre",
		"h1", "h2", "h3", "table", "thead", "tbody", "tr", "th", "td")
	p.AllowAttrs("colspan", "rowspan").Matching(spanNum).OnElements("th", "td")
	p.AllowAttrs("href").Matching(linkHref).OnElements("a")
	p.AllowAttrs("src").Matching(imageSrc).OnElements("img")
	p.AllowAttrs("alt").Matching(altText).OnElements("img")
	p.AllowURLSchemes("http", "https", "mailto")
	p.AllowRelativeURLs(true)
	p.RequireParseableURLs(true)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	return p
}

// Sanitize reduces article HTML to what the editor and the public site can show: text
// structure, links, tables, code and images uploaded through the knowledge base itself.
// Styles, classes, scripts and every other image source are dropped. It runs on write and
// again on render, so a row changed behind the API's back is still safe to serve.
func Sanitize(src string) string { return policy.Sanitize(src) }

// ImageIDs returns the knowledge base images referenced by sanitized HTML, without duplicates.
func ImageIDs(sanitized string) []string {
	var out []string
	seen := map[string]bool{}
	z := html.NewTokenizer(strings.NewReader(sanitized))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.Data != "img" {
				continue
			}
			for _, a := range tok.Attr {
				if a.Key != "src" {
					continue
				}
				if m := imageSrc.FindStringSubmatch(a.Val); m != nil && !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
			}
		}
	}
}

// Text is the plain text of sanitized HTML with single spaces between words. It feeds the
// search index and the excerpt. Control characters are removed because the search snippets
// use two of them as markers.
func Text(sanitized string) string {
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(sanitized))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(strings.Map(dropControl, b.String())), " ")
		case html.TextToken:
			b.Write(z.Text())
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			// Inline tags sit inside a sentence: a space there would detach punctuation
			// from the word before it ("vergeten .").
			if name, _ := z.TagName(); !inlineTags[string(name)] {
				b.WriteByte(' ')
			}
		}
	}
}

var inlineTags = map[string]bool{"a": true, "b": true, "strong": true, "i": true, "em": true, "u": true, "s": true, "code": true, "span": true, "sub": true, "sup": true, "mark": true, "small": true}

func dropControl(r rune) rune {
	if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
		return -1
	}
	return r
}

// Excerpt shortens text to at most max characters at a word boundary.
func Excerpt(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	cut := string(runes[:max])
	if i := strings.LastIndexByte(cut, ' '); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " .,;:") + "…"
}
