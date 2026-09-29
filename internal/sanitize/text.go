package sanitize

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

var plainURL = regexp.MustCompile(`https?://[^\s<>"']+`)

// Text renders plain text as an HTML fragment: everything escaped, http(s) URLs linkified with
// the same attributes as sanitized mail links.
func Text(in string) string {
	if len(in) > MaxInputBytes {
		in = truncateUTF8(in, MaxInputBytes)
	}
	var sb strings.Builder
	last := 0
	for _, loc := range plainURL.FindAllStringIndex(in, -1) {
		raw := trimURLTail(in[loc[0]:loc[1]])
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			continue
		}
		sb.WriteString(html.EscapeString(in[last:loc[0]]))
		esc := html.EscapeString(raw)
		sb.WriteString(`<a href="` + esc + `" target="` + linkTarget + `" rel="` + linkRel + `">` + esc + `</a>`)
		last = loc[0] + len(raw)
	}
	sb.WriteString(html.EscapeString(in[last:]))
	return sb.String()
}

// trimURLTail drops sentence punctuation and an unbalanced closing bracket that belong to the
// surrounding prose rather than the URL.
func trimURLTail(s string) string {
	for len(s) > 0 {
		c := s[len(s)-1]
		switch {
		case strings.IndexByte(".,;:!?", c) >= 0:
		case c == ')' && strings.Count(s, "(") < strings.Count(s, ")"):
		case c == ']' && strings.Count(s, "[") < strings.Count(s, "]"):
		default:
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}
