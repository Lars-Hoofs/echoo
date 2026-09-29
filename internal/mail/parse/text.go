package parse

import (
	"bytes"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-message/charset"
	"golang.org/x/net/html"
)

// decodeText normalizes a text body that go-message already transfer-decoded and converted
// to UTF-8. Bytes that are still not UTF-8 (undeclared 8-bit text, unknown charset,
// mislabeled utf-8) are read as windows-1252, the most common culprit.
func decodeText(data []byte, ct contentType) string {
	var s string
	if utf8.Valid(data) {
		s = string(data)
	} else {
		s = latin1Fallback(data)
	}
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\x00", "")
	if ct.mediaType != "text/html" {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
		if strings.EqualFold(ct.params["format"], "flowed") {
			s = unflow(s, strings.EqualFold(ct.params["delsp"], "yes"))
		}
	}
	return s
}

func latin1Fallback(data []byte) string {
	r, err := charset.Reader("windows-1252", bytes.NewReader(data))
	if err == nil {
		if out, err := io.ReadAll(r); err == nil {
			return string(out)
		}
	}
	return strings.ToValidUTF8(string(data), "\ufffd")
}

// unflow undoes RFC 3676 soft line breaks: a line ending in a space continues on the next
// line of the same quote depth. Quote prefixes are kept as "> " so the text stays quotable.
func unflow(s string, delSP bool) string {
	var out []string
	var cur strings.Builder
	depth, open := 0, false
	flush := func() {
		prefix := strings.Repeat(">", depth)
		if depth > 0 && cur.Len() > 0 {
			prefix += " "
		}
		out = append(out, prefix+cur.String())
		cur.Reset()
		open = false
	}
	for _, line := range strings.Split(s, "\n") {
		d := 0
		for d < len(line) && line[d] == '>' {
			d++
		}
		text := strings.TrimPrefix(line[d:], " ")
		if open && d != depth {
			flush()
		}
		depth, open = d, true
		soft := strings.HasSuffix(text, " ") && text != "-- "
		if soft && delSP {
			text = text[:len(text)-1]
		}
		cur.WriteString(text)
		if !soft {
			flush()
		}
	}
	if open {
		flush()
	}
	return strings.Join(out, "\n")
}

var (
	skippedElements = map[string]bool{"script": true, "style": true, "title": true, "template": true}
	blockElements   = map[string]int{
		"div": 1, "li": 1, "tr": 1, "dd": 1, "dt": 1, "pre": 1, "section": 1, "article": 1,
		"header": 1, "footer": 1, "nav": 1, "aside": 1, "address": 1, "form": 1, "fieldset": 1,
		"center": 1,
		"p":      2, "h1": 2, "h2": 2, "h3": 2, "h4": 2, "h5": 2, "h6": 2, "blockquote": 2,
		"ul": 2, "ol": 2, "dl": 2, "table": 2, "hr": 2,
	}
)

// htmlToText extracts the visible text of an HTML body: script, style and title content is
// dropped, block elements become line breaks, whitespace is collapsed.
func htmlToText(src string) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var b strings.Builder
	skipping := ""
	for {
		switch z.Next() {
		case html.ErrorToken:
			return tidyLines(b.String())
		case html.TextToken:
			if skipping == "" {
				appendCollapsed(&b, string(z.Text()))
			}
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			tt := z.Token()
			name := tt.Data
			switch {
			case skipping != "":
				if tt.Type == html.EndTagToken && name == skipping {
					skipping = ""
				}
			case skippedElements[name]:
				if tt.Type == html.StartTagToken {
					skipping = name
				}
			case name == "br":
				b.WriteByte('\n')
			case name == "td" || name == "th":
				appendCollapsed(&b, " ")
			default:
				if n := blockElements[name]; n > 0 {
					ensureBreaks(&b, n)
				}
			}
		}
	}
}

func appendCollapsed(b *strings.Builder, text string) {
	text = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u200e', '\u200f', '\u2060', '\ufeff', '\u00ad', '\u034f':
			return -1
		}
		return r
	}, text)
	words := strings.Fields(text)
	if len(text) > 0 && unicode.IsSpace([]rune(text)[0]) {
		appendSpace(b)
	}
	for i, w := range words {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(w)
	}
	if len(words) > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text); unicode.IsSpace(r) {
			appendSpace(b)
		}
	}
}

func appendSpace(b *strings.Builder) {
	s := b.String()
	if s == "" || strings.HasSuffix(s, "\n") || strings.HasSuffix(s, " ") {
		return
	}
	b.WriteByte(' ')
}

// ensureBreaks makes the output end with n line breaks (1 = new line, 2 = blank line).
func ensureBreaks(b *strings.Builder, n int) {
	s := strings.TrimRight(b.String(), " ")
	if s == "" {
		return
	}
	have := len(s) - len(strings.TrimRight(s, "\n"))
	for ; have < n; have++ {
		b.WriteByte('\n')
	}
}

func tidyLines(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
