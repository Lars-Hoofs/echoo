package parse

import (
	"io"
	"mime"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"

	"echoo/internal/mail"
)

func fillHeaders(p *mail.Parsed, h message.Header) {
	for _, v := range h.Values("Message-Id") {
		if ids := extractIDs(v); len(ids) > 0 {
			p.MessageID = ids[0]
			break
		}
	}
	p.InReplyTo = extractIDs(strings.Join(h.Values("In-Reply-To"), " "))
	p.References = extractIDs(strings.Join(h.Values("References"), " "))

	if from := parseAddresses(h.Values("From")); len(from) > 0 {
		p.From = from[0]
	}
	if sender := parseAddresses(h.Values("Sender")); len(sender) > 0 {
		p.Sender = &sender[0]
	}
	p.ReplyTo = parseAddresses(h.Values("Reply-To"))
	p.To = parseAddresses(h.Values("To"))
	p.Cc = parseAddresses(h.Values("Cc"))
	p.Bcc = parseAddresses(h.Values("Bcc"))

	p.Subject = strings.TrimSpace(decodeHeaderText(h.Get("Subject")))
	p.Date = parseDate(h.Get("Date"))

	p.AutoSubmitted = isAutoSubmitted(h)
	p.Bulk = isBulk(h)
	p.ListID = listID(h.Get("List-Id"))
	p.ThreadTopic = strings.TrimSpace(decodeHeaderText(h.Get("Thread-Topic")))
	p.ThreadIndex = strings.Join(strings.Fields(cleanHeader(h.Get("Thread-Index"))), "")
	for _, v := range h.Values("Authentication-Results") {
		p.AuthResults = append(p.AuthResults, cleanHeader(strings.TrimSpace(v)))
	}
}

// extractIDs finds message IDs in a Message-ID, In-Reply-To or References value. It accepts
// folded values, IDs without angle brackets, commas and garbage between IDs; anything that
// mail.NormalizeMessageID rejects is skipped. Duplicates are dropped, first occurrence wins.
func extractIDs(v string) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(s string) {
		if id := mail.NormalizeMessageID(s); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	rest := v
	for {
		rest = strings.TrimLeft(rest, " \t\r\n,;")
		if rest == "" {
			return ids
		}
		switch rest[0] {
		case '(':
			end := strings.IndexByte(rest, ')')
			if end < 0 {
				return ids
			}
			rest = rest[end+1:]
		case '<':
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				end = tokenEnd(rest[1:]) + 1
				add(rest[1:end])
				rest = rest[end:]
				continue
			}
			add(rest[1:end])
			rest = rest[end+1:]
		default:
			end := tokenEnd(rest)
			add(rest[:end])
			rest = rest[end:]
		}
	}
}

func tokenEnd(s string) int {
	if i := strings.IndexAny(s, " \t\r\n,;<"); i >= 0 {
		return i
	}
	return len(s)
}

var wordDecoder = &mime.WordDecoder{CharsetReader: lenientCharset}

// lenientCharset converts unknown charsets as windows-1252 instead of failing, so one odd
// label cannot make a subject or address undecodable.
func lenientCharset(label string, r io.Reader) (io.Reader, error) {
	if cr, err := charset.Reader(label, r); err == nil {
		return cr, nil
	}
	return charset.Reader("windows-1252", r)
}

func decodeHeaderText(s string) string {
	if !utf8.ValidString(s) {
		s = latin1Fallback([]byte(s))
	}
	if dec, err := wordDecoder.DecodeHeader(s); err == nil {
		s = dec
	}
	return cleanHeader(s)
}

// cleanHeader replaces control characters (which include NUL, unstorable in Postgres text)
// with spaces.
func cleanHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

var addressParser = &netmail.AddressParser{WordDecoder: wordDecoder}

// parseAddresses keeps every address that parses. A list with one bad entry is re-parsed
// entry by entry instead of being rejected as a whole.
func parseAddresses(values []string) []mail.Address {
	joined := strings.TrimSpace(strings.Join(values, ", "))
	if joined == "" {
		return nil
	}
	list, err := addressParser.ParseList(joined)
	if err != nil {
		list = nil
		for _, part := range splitAddressList(joined) {
			if a, err := addressParser.Parse(part); err == nil {
				list = append(list, a)
			}
		}
	}
	out := make([]mail.Address, 0, len(list))
	for _, a := range list {
		if a.Address == "" {
			continue
		}
		out = append(out, mail.Address{
			Name:    strings.TrimSpace(cleanHeader(a.Name)),
			Address: strings.ToLower(cleanHeader(a.Address)),
		})
	}
	return out
}

// splitAddressList splits on commas and semicolons (Outlook's separator) outside quoted
// strings, angle brackets and comments.
func splitAddressList(s string) []string {
	var parts []string
	var quoted, escaped bool
	angle, comment := 0, 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && (quoted || comment > 0):
			escaped = true
		case quoted:
			quoted = c != '"'
		case c == '"' && comment == 0:
			quoted = true
		case c == '(':
			comment++
		case c == ')' && comment > 0:
			comment--
		case comment > 0:
		case c == '<':
			angle++
		case c == '>' && angle > 0:
			angle--
		case (c == ',' || c == ';') && angle == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var (
	commentRE = regexp.MustCompile(`\([^()]*\)`)
	spacesRE  = regexp.MustCompile(`\s+`)
)

// dateLayouts covers what net/mail.ParseDate rejects but clients send anyway: missing
// weekday, two-digit years, named zones, missing seconds, ISO 8601, ctime.
var dateLayouts = []string{
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"Mon, 2 Jan 2006 15:04 -0700",
	"Mon, 2 Jan 2006 15:04 MST",
	"Mon, 2 Jan 06 15:04:05 -0700",
	"Mon, 2 Jan 06 15:04:05 MST",
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04 -0700",
	"2 Jan 06 15:04:05 -0700",
	"Mon Jan 2 15:04:05 2006",
	"Mon Jan 2 15:04:05 -0700 2006",
	"Mon Jan 2 15:04:05 MST 2006",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05",
	"Mon, 2 Jan 2006 15:04:05",
	"Mon, 2 Jan 2006",
}

// parseDate returns the zero time when v cannot be understood. The result is in UTC.
func parseDate(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := netmail.ParseDate(v); err == nil {
		return t.UTC()
	}
	v = spacesRE.ReplaceAllString(strings.TrimSpace(commentRE.ReplaceAllString(v, " ")), " ")
	v = strings.NewReplacer("GMT+", "+", "UTC+", "+", "GMT-", "-", "UTC-", "-").Replace(v)
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func isAutoSubmitted(h message.Header) bool {
	if h.Has("Auto-Submitted") {
		v, _, _ := strings.Cut(h.Get("Auto-Submitted"), ";")
		if !strings.EqualFold(strings.TrimSpace(v), "no") {
			return true
		}
	}
	if h.Has("X-Autoreply") || h.Has("X-Autorespond") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "auto_reply", "auto-reply":
		return true
	}
	return false
}

func isBulk(h message.Header) bool {
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "bulk", "list", "junk":
		return true
	}
	return h.Has("List-Unsubscribe")
}

// listID returns the ID inside the angle brackets; some lists send the bare ID.
func listID(v string) string {
	v = strings.TrimSpace(v)
	if start := strings.LastIndexByte(v, '<'); start >= 0 {
		if end := strings.IndexByte(v[start:], '>'); end > 0 {
			v = v[start+1 : start+end]
		}
	}
	return strings.TrimSpace(cleanHeader(v))
}

type contentType struct {
	mediaType string
	params    map[string]string
}

// parseContentType never fails: an absent header means text/plain (RFC 2045), an unusable
// one means application/octet-stream.
func parseContentType(h message.Header) contentType {
	v := h.Get("Content-Type")
	if strings.TrimSpace(v) == "" {
		return contentType{mediaType: "text/plain"}
	}
	mt, params, err := mime.ParseMediaType(v)
	if err != nil && mt == "" {
		mt = leadingToken(v)
		if !mediaTypeRE.MatchString(mt) {
			mt = "application/octet-stream"
		}
	}
	return contentType{mediaType: mt, params: params}
}

func parseDisposition(h message.Header) (string, map[string]string) {
	v := h.Get("Content-Disposition")
	disp, params, err := mime.ParseMediaType(v)
	if err != nil && disp == "" {
		disp = leadingToken(v)
	}
	return disp, params
}

var mediaTypeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

func leadingToken(v string) string {
	v, _, _ = strings.Cut(v, ";")
	return strings.ToLower(strings.TrimSpace(v))
}
