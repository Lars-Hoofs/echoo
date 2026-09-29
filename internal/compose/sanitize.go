// Package compose holds the domain logic of writing mail: sanitizing editor HTML, deriving
// plain text, the branded email layout, template variables and notifications.
package compose

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

var (
	linkHref   = regexp.MustCompile(`(?i)^(https?:|mailto:)`)
	uuidValue  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	mentionLbl = regexp.MustCompile(`^[^<>"&]{1,200}$`)
)

// SanitizeOptions widens the strict editor policy for one call.
type SanitizeOptions struct {
	// ContentIDs are the Content-IDs of the caller's own uploads; only `cid:` images that
	// reference one of them survive.
	ContentIDs []string
	// Mentions keeps @mention spans, which only appear in notes.
	Mentions bool
}

// Sanitize reduces HTML from the editor to the small set of elements the editor can produce.
// Everything else, including all styles and scripts, is dropped. The result is what gets
// stored and sent, so a crafted request cannot smuggle markup into outgoing mail or into the
// thread.
func Sanitize(src string, o SanitizeOptions) string {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "strong", "em", "u", "s", "ul", "ol", "li", "blockquote", "code", "pre", "h1", "h2", "h3")
	p.AllowAttrs("href").Matching(linkHref).OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.RequireParseableURLs(true)
	p.RequireNoFollowOnLinks(true)
	p.RequireNoReferrerOnLinks(true)
	if len(o.ContentIDs) > 0 {
		quoted := make([]string, len(o.ContentIDs))
		allowed := make(map[string]bool, len(o.ContentIDs))
		for i, id := range o.ContentIDs {
			quoted[i] = regexp.QuoteMeta(id)
			allowed[id] = true
		}
		p.AllowAttrs("src").Matching(regexp.MustCompile(`^cid:(` + strings.Join(quoted, "|") + `)$`)).OnElements("img")
		p.AllowAttrs("alt").Matching(mentionLbl).OnElements("img")
		p.AllowURLSchemeWithCustomPolicy("cid", func(u *url.URL) bool { return allowed[u.Opaque] })
	}
	if o.Mentions {
		p.AllowElements("span")
		p.AllowAttrs("data-type").Matching(regexp.MustCompile(`^mention$`)).OnElements("span")
		p.AllowAttrs("data-id").Matching(uuidValue).OnElements("span")
		p.AllowAttrs("data-label").Matching(mentionLbl).OnElements("span")
	}
	return p.Sanitize(src)
}

// CIDs returns the Content-IDs referenced by cid: images in sanitized HTML.
func CIDs(sanitized string) []string {
	var out []string
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
				if a.Key == "src" && strings.HasPrefix(a.Val, "cid:") {
					out = append(out, strings.TrimPrefix(a.Val, "cid:"))
				}
			}
		}
	}
}

// TextFromHTML derives the plain text alternative of sanitized editor HTML.
func TextFromHTML(src string) string {
	var b strings.Builder
	var hrefs []string
	var linkStart []int
	listStack := []int{} // 0 for bullets, n for the next number of an ordered list
	z := html.NewTokenizer(strings.NewReader(src))
	breaks := func(n int) {
		s := b.String()
		trimmed := strings.TrimRight(s, "\n")
		have := len(s) - len(trimmed)
		if trimmed == "" {
			return
		}
		for ; have < n; have++ {
			b.WriteByte('\n')
		}
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(b.String())
		case html.TextToken:
			b.WriteString(string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			switch tok.Data {
			case "br":
				b.WriteByte('\n')
			case "p", "blockquote", "pre", "h1", "h2", "h3":
				breaks(2)
			case "ul", "ol":
				if len(listStack) == 0 {
					breaks(2)
				} else {
					breaks(1)
				}
				next := 0
				if tok.Data == "ol" {
					next = 1
				}
				listStack = append(listStack, next)
			case "li":
				breaks(1)
				if n := len(listStack); n > 0 && listStack[n-1] > 0 {
					b.WriteString(strconv.Itoa(listStack[n-1]) + ". ")
					listStack[n-1]++
				} else {
					b.WriteString("- ")
				}
			case "a":
				href := ""
				for _, a := range tok.Attr {
					if a.Key == "href" {
						href = a.Val
					}
				}
				hrefs = append(hrefs, href)
				linkStart = append(linkStart, b.Len())
			}
		case html.EndTagToken:
			tok := z.Token()
			switch tok.Data {
			case "p", "blockquote", "pre", "h1", "h2", "h3":
				breaks(2)
			case "ul", "ol":
				if len(listStack) > 0 {
					listStack = listStack[:len(listStack)-1]
				}
				if len(listStack) == 0 {
					breaks(2)
				} else {
					breaks(1)
				}
			case "a":
				if len(hrefs) == 0 {
					continue
				}
				href, start := hrefs[len(hrefs)-1], linkStart[len(linkStart)-1]
				hrefs, linkStart = hrefs[:len(hrefs)-1], linkStart[:len(linkStart)-1]
				label := b.String()[start:]
				if href != "" && strings.TrimPrefix(href, "mailto:") != strings.TrimSpace(label) && href != strings.TrimSpace(label) {
					b.WriteString(" (" + href + ")")
				}
			}
		}
	}
}

// IsEmpty reports whether sanitized HTML carries no text and no image.
func IsEmpty(sanitized string) bool {
	return strings.TrimSpace(TextFromHTML(sanitized)) == "" && len(CIDs(sanitized)) == 0
}
