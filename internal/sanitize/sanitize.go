// Package sanitize turns untrusted inbound mail HTML into markup that is safe to serve inside
// the sandboxed render iframe, and derives phishing indicators from it and from the headers.
//
// Two layers: an html.Parse walk rewrites images and links (things an allowlist cannot express),
// then a bluemonday allowlist is the final gate, so a bug in the walk cannot let markup through.
package sanitize

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/idna"
)

// Version identifies the policy. Bump it whenever the allowlist or the rewriting changes, so a
// stored or cached rendering can be told apart from one made under an older policy.
const Version = 1

// MaxInputBytes bounds parser work per message; anything beyond it is cut off.
const MaxInputBytes = 1 << 20

// blockedPlaceholder is a transparent 1x1 GIF that stands in for images we do not load.
const blockedPlaceholder = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7"

const (
	linkRel    = "noopener noreferrer nofollow"
	linkTarget = "_blank"
)

// Options controls how images are rewritten.
type Options struct {
	// ResolveCID maps a Content-ID (without angle brackets, lower-cased) to a servable URL.
	// Unresolvable cid: images become the placeholder.
	ResolveCID func(contentID string) (string, bool)
	// ProxyURL, when set, makes remote http(s) images load through it. When nil, remote
	// images are blocked.
	ProxyURL func(remote string) string
}

// LinkMismatch is a link whose visible text names a different host than its target.
type LinkMismatch struct {
	Shown  string
	Actual string
}

type Result struct {
	HTML          string
	BlockedImages int
	Mismatches    []LinkMismatch
	Truncated     bool
}

var (
	safeImgSrc   = regexp.MustCompile(`^(/render/attachments/[0-9a-f-]{36}\?[A-Za-z0-9._~%=&+-]*|/render/proxy\?[A-Za-z0-9._~%=&+/-]*|data:image/(gif|jpeg|png|webp);base64,[A-Za-z0-9+/=]+)$`)
	safeColor    = regexp.MustCompile(`^(#[0-9a-fA-F]{3,8}|[a-zA-Z]{3,20})$`)
	safeCSSValue = regexp.MustCompile(`^[a-z0-9 #%.,()\-+_'"/]*$`)
	number       = regexp.MustCompile(`^[0-9]{1,4}$`)
	sizeValue    = regexp.MustCompile(`^[0-9]{1,4}%?$`)
	fontSize     = regexp.MustCompile(`^[+-]?[1-7]$`)
	fontFace     = regexp.MustCompile(`^[A-Za-z0-9 ,'-]{1,100}$`)
	altText      = regexp.MustCompile(`^[^<>"]{0,200}$`)
	titleText    = regexp.MustCompile(`^[^<>"]{0,200}$`)
	alignValue   = regexp.MustCompile(`^(left|right|center|justify|top|middle|bottom|baseline)$`)
	direction    = regexp.MustCompile(`^(ltr|rtl|auto)$`)
	langValue    = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// allowedStyles is deliberately small: no position, float, z-index, display, background images,
// content or anything else that could cover the app's own controls or fetch resources.
var allowedStyles = []string{
	"color", "background-color",
	"font-family", "font-size", "font-weight", "font-style", "line-height", "letter-spacing",
	"text-align", "text-decoration", "text-transform", "text-indent", "vertical-align", "white-space",
	"width", "height", "max-width", "min-width", "max-height", "min-height",
	"margin", "margin-top", "margin-right", "margin-bottom", "margin-left",
	"padding", "padding-top", "padding-right", "padding-bottom", "padding-left",
	"border", "border-top", "border-right", "border-bottom", "border-left",
	"border-color", "border-style", "border-width", "border-collapse", "border-spacing", "border-radius",
	"table-layout",
}

// safeStyleValue rejects anything that could load a resource or smuggle syntax: the character
// set has no backslash, semicolon, colon, angle bracket or brace, and functions that fetch or
// evaluate are refused by name.
func safeStyleValue(v string) bool {
	if !safeCSSValue.MatchString(v) {
		return false
	}
	for _, bad := range []string{"url(", "expression", "javascript", "image-set", "var(", "attr(", "env(", "behavior", "binding"} {
		if strings.Contains(v, bad) {
			return false
		}
	}
	return true
}

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(true)
	p.AllowURLSchemes("http", "https", "mailto", "tel")
	p.AllowDataURIImages()

	p.AllowElements(
		"a", "abbr", "address", "b", "big", "blockquote", "br", "caption", "center", "cite", "code", "col", "colgroup",
		"dd", "del", "div", "dl", "dt", "em", "font", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "i", "img", "ins", "li",
		"ol", "p", "pre", "q", "s", "small", "span", "strike", "strong", "sub", "sup", "table", "tbody", "td", "tfoot",
		"th", "thead", "tr", "tt", "u", "ul",
	)
	p.AllowAttrs("dir").Matching(direction).Globally()
	p.AllowAttrs("lang").Matching(langValue).Globally()
	p.AllowAttrs("title").Matching(titleText).Globally()

	p.AllowAttrs("href").OnElements("a")
	p.AllowAttrs("target").Matching(regexp.MustCompile("^" + linkTarget + "$")).OnElements("a")
	p.AllowAttrs("rel").Matching(regexp.MustCompile("^" + linkRel + "$")).OnElements("a")

	p.AllowAttrs("src").Matching(safeImgSrc).OnElements("img")
	p.AllowAttrs("alt").Matching(altText).OnElements("img")
	p.AllowAttrs("width", "height").Matching(sizeValue).OnElements("img", "table", "td", "th", "col", "colgroup")
	p.AllowAttrs("align", "valign").Matching(alignValue).OnElements("img", "table", "tr", "td", "th", "col", "colgroup", "tbody", "thead", "tfoot", "div", "p", "h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("colspan", "rowspan", "span", "border", "cellspacing", "cellpadding").Matching(number).OnElements("table", "td", "th", "col", "colgroup")
	p.AllowAttrs("bgcolor").Matching(safeColor).OnElements("table", "tr", "td", "th")
	p.AllowAttrs("color").Matching(safeColor).OnElements("font")
	p.AllowAttrs("size").Matching(fontSize).OnElements("font")
	p.AllowAttrs("face").Matching(fontFace).OnElements("font")

	p.AllowStyles(allowedStyles...).MatchingHandler(safeStyleValue).Globally()
	// The shorthand is everywhere in HTML mail. Only a plain colour passes; anything with an
	// image is dropped, and without it white text on a coloured header would turn invisible.
	p.AllowStyles("background").MatchingHandler(safeColor.MatchString).Globally()
	return p
}

// ErrUnparseable is returned for documents the HTML parser refuses, e.g. nesting deeper than
// 512 elements. Callers should show the plain text body instead.
var ErrUnparseable = errors.New("sanitize: html cannot be parsed")

// HTML sanitizes one message body.
func HTML(in string, opts Options) (Result, error) {
	var res Result
	if len(in) > MaxInputBytes {
		in = truncateUTF8(in, MaxInputBytes)
		res.Truncated = true
	}
	doc, err := html.Parse(strings.NewReader(in))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUnparseable, err)
	}
	w := walker{opts: opts, res: &res}
	body := findBody(doc)
	if body == nil {
		return res, nil
	}
	w.walk(body)

	var buf bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrUnparseable, err)
		}
	}
	res.HTML = policy.Sanitize(buf.String())
	res.Mismatches = dedupeMismatches(res.Mismatches)
	return res, nil
}

func truncateUTF8(s string, n int) string {
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == atom.Body {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

type walker struct {
	opts Options
	res  *Result
}

func (w *walker) walk(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			switch c.DataAtom {
			case atom.Img:
				w.rewriteImage(c)
			case atom.A:
				w.rewriteLink(c)
			}
		}
		w.walk(c)
	}
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func delAttr(n *html.Node, keys ...string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		drop := false
		for _, k := range keys {
			if a.Key == k {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}

// cleanURLValue strips the ASCII whitespace and control characters browsers ignore inside a
// scheme ("java\tscript:"), so the scheme check sees what the browser would.
func cleanURLValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func (w *walker) rewriteImage(n *html.Node) {
	delAttr(n, "srcset", "sizes", "usemap", "ismap", "longdesc", "lowsrc", "dynsrc")
	raw, _ := attr(n, "src")
	src := cleanURLValue(raw)
	lower := strings.ToLower(src)
	switch {
	case strings.HasPrefix(lower, "cid:"):
		cid := strings.ToLower(strings.Trim(src[4:], "<>"))
		if w.opts.ResolveCID != nil {
			if u, ok := w.opts.ResolveCID(cid); ok {
				setAttr(n, "src", u)
				return
			}
		}
		setAttr(n, "src", blockedPlaceholder)
	case strings.HasPrefix(lower, "data:"):
		if !safeImgSrc.MatchString(src) {
			setAttr(n, "src", blockedPlaceholder)
		}
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"), strings.HasPrefix(src, "//"):
		remote, ok := remoteImageURL(src)
		if !ok {
			setAttr(n, "src", blockedPlaceholder)
			return
		}
		if w.opts.ProxyURL == nil {
			w.res.BlockedImages++
			setAttr(n, "src", blockedPlaceholder)
			return
		}
		setAttr(n, "src", w.opts.ProxyURL(remote))
	default:
		setAttr(n, "src", blockedPlaceholder)
	}
}

// remoteImageURL normalizes a remote image reference to an https URL. Plain http is upgraded
// because the proxy only speaks TLS.
func remoteImageURL(raw string) (string, bool) {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	u.Scheme = "https"
	if u.Port() == "80" {
		u.Host = u.Hostname()
	}
	u.Fragment = ""
	return u.String(), true
}

func (w *walker) rewriteLink(n *html.Node) {
	setAttr(n, "target", linkTarget)
	setAttr(n, "rel", linkRel)
	raw, ok := attr(n, "href")
	if !ok {
		return
	}
	href := cleanURLValue(raw)
	u, err := url.Parse(href)
	if err != nil {
		delAttr(n, "href")
		return
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Hostname() == "" {
			delAttr(n, "href")
			return
		}
		setAttr(n, "href", href)
		if shown, ok := visibleHost(textOf(n)); ok && !sameSite(shown, u.Hostname()) {
			w.res.Mismatches = append(w.res.Mismatches, LinkMismatch{Shown: shown, Actual: strings.ToLower(u.Hostname())})
		}
	case "mailto", "tel":
		setAttr(n, "href", href)
	default:
		delAttr(n, "href")
	}
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return strings.TrimSpace(sb.String())
}

var visibleURL = regexp.MustCompile(`(?i)^(?:https?://)?((?:[a-z0-9-]+\.)+[a-z]{2,})(?::\d+)?(?:[/?#]\S*)?$`)

// visibleHost returns the host when the link text itself looks like a URL or domain, which is
// what makes a different target deceptive.
func visibleHost(text string) (string, bool) {
	m := visibleURL.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return strings.ToLower(m[1]), true
}

func asciiHost(h string) string {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if a, err := idna.Lookup.ToASCII(h); err == nil {
		return a
	}
	return h
}

// sameSite treats a host and its subdomains as one site, and ignores a leading "www.".
func sameSite(a, b string) bool {
	a = strings.TrimPrefix(asciiHost(a), "www.")
	b = strings.TrimPrefix(asciiHost(b), "www.")
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

func dedupeMismatches(in []LinkMismatch) []LinkMismatch {
	seen := map[LinkMismatch]bool{}
	var out []LinkMismatch
	for _, m := range in {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
