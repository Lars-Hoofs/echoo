// Package search parses the search field's query language. Filters are written as key:value
// (Dutch or English keys), everything else is free text for the full-text search.
package search

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MaxQueryRunes bounds the work a single request can ask for.
const MaxQueryRunes = 200

// Query is a parsed search string. Values of one key are alternatives, different keys must all match.
type Query struct {
	// Text is the free text, with quoted phrases kept quoted, ready for websearch_to_tsquery.
	Text          string
	Statuses      []string
	Snoozed       bool
	From, To      []string
	Mailboxes     []string
	Labels        []string
	Teams         []string
	Assignees     []string
	AssigneeMe    bool
	AssigneeNone  bool
	Priorities    []string
	HasAttachment bool
	// After is inclusive, Before exclusive, both at 00:00 UTC.
	After, Before *time.Time
	Number        *int64
}

var (
	keys = map[string]string{
		"status": "status", "van": "from", "from": "from", "aan": "to", "to": "to",
		"mailbox": "mailbox", "label": "label", "toegewezen": "assignee", "assignee": "assignee",
		"team": "team", "prioriteit": "priority", "priority": "priority", "heeft": "has", "has": "has",
		"voor": "before", "before": "before", "na": "after", "after": "after",
	}
	statusValues = map[string]string{
		"open": "open", "waiting": "waiting", "wachtend": "waiting", "closed": "closed", "gesloten": "closed",
		"spam": "spam", "snoozed": "snoozed", "uitgesteld": "snoozed",
	}
	priorityValues = map[string]string{
		"none": "none", "geen": "none", "low": "low", "laag": "low", "normal": "normal", "normaal": "normal",
		"high": "high", "hoog": "high", "urgent": "urgent",
	}
	attachmentValues = []string{"bijlage", "bijlagen", "attachment", "attachments"}
	meValues         = []string{"me", "mij", "ik"}
	noneValues       = []string{"none", "niemand", "geen"}
)

type token struct {
	key, value string
	quoted     bool
	raw        string
}

// Parse never fails: a token that is not a valid filter is kept as free text.
func Parse(input string) Query {
	runes := []rune(input)
	if len(runes) > MaxQueryRunes {
		runes = runes[:MaxQueryRunes]
	}
	var q Query
	var text []string
	for _, t := range tokenize(runes) {
		if !q.apply(t) {
			text = append(text, t.raw)
		}
	}
	q.Text = strings.Join(text, " ")
	return q
}

func tokenize(r []rune) []token {
	var out []token
	i := 0
	for i < len(r) {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		if r[i] == '"' {
			phrase, next := readQuoted(r, i)
			if strings.TrimSpace(phrase) != "" {
				out = append(out, token{value: phrase, quoted: true, raw: `"` + phrase + `"`})
			}
			i = next
			continue
		}
		start := i
		for i < len(r) && !unicode.IsSpace(r[i]) && (r[i] != '"' || r[i-1] != ':') {
			i++
		}
		word := string(r[start:i])
		key, value, ok := strings.Cut(word, ":")
		if ok && value == "" && i < len(r) && r[i] == '"' {
			// key:"value with spaces"
			quoted, next := readQuoted(r, i)
			out = append(out, token{key: strings.ToLower(key), value: quoted, quoted: true, raw: key + `:"` + quoted + `"`})
			i = next
			continue
		}
		if ok {
			out = append(out, token{key: strings.ToLower(key), value: value, raw: word})
		} else {
			out = append(out, token{value: word, raw: word})
		}
	}
	return out
}

// readQuoted reads a quoted string starting at the opening quote and returns the text and the
// index after the closing quote. An unterminated quote runs to the end.
func readQuoted(r []rune, open int) (string, int) {
	end := slices.Index(r[open+1:], '"')
	if end < 0 {
		return string(r[open+1:]), len(r)
	}
	return string(r[open+1 : open+1+end]), open + 1 + end + 1
}

// apply records t as a filter and reports whether it was one.
func (q *Query) apply(t token) bool {
	if t.key == "" {
		if !t.quoted && strings.HasPrefix(t.value, "#") {
			n, err := strconv.ParseInt(t.value[1:], 10, 64)
			if err == nil && n > 0 && !strings.ContainsAny(t.value[1:], "+-") {
				q.Number = &n
				return true
			}
		}
		return false
	}
	name, ok := keys[t.key]
	value := strings.TrimSpace(t.value)
	if !ok || value == "" {
		return false
	}
	lower := strings.ToLower(value)
	switch name {
	case "status":
		s, ok := statusValues[lower]
		if !ok {
			return false
		}
		if s == "snoozed" {
			q.Snoozed = true
		} else {
			q.Statuses = appendUnique(q.Statuses, s)
		}
	case "priority":
		p, ok := priorityValues[lower]
		if !ok {
			return false
		}
		q.Priorities = appendUnique(q.Priorities, p)
	case "has":
		if !slices.Contains(attachmentValues, lower) {
			return false
		}
		q.HasAttachment = true
	case "before", "after":
		d, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return false
		}
		if name == "before" {
			q.Before = &d
		} else {
			q.After = &d
		}
	case "assignee":
		switch {
		case slices.Contains(meValues, lower):
			q.AssigneeMe = true
		case slices.Contains(noneValues, lower):
			q.AssigneeNone = true
		default:
			q.Assignees = appendUnique(q.Assignees, value)
		}
	case "from":
		q.From = appendUnique(q.From, value)
	case "to":
		q.To = appendUnique(q.To, value)
	case "mailbox":
		q.Mailboxes = appendUnique(q.Mailboxes, value)
	case "label":
		q.Labels = appendUnique(q.Labels, value)
	case "team":
		q.Teams = appendUnique(q.Teams, value)
	}
	return true
}

func appendUnique(list []string, v string) []string {
	if slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, v) }) {
		return list
	}
	return append(list, v)
}

// HasText reports whether there is free text to run through the full-text search.
func (q Query) HasText() bool { return strings.TrimSpace(q.Text) != "" }

// Empty reports whether the query has neither text nor filters.
func (q Query) Empty() bool {
	return !q.HasText() && q.Number == nil && !q.HasFilters()
}

// HasFilters reports whether any filter other than the number is set.
func (q Query) HasFilters() bool {
	return len(q.Statuses) > 0 || q.Snoozed || len(q.From) > 0 || len(q.To) > 0 || len(q.Mailboxes) > 0 ||
		len(q.Labels) > 0 || len(q.Teams) > 0 || len(q.Assignees) > 0 || q.AssigneeMe || q.AssigneeNone ||
		len(q.Priorities) > 0 || q.HasAttachment || q.After != nil || q.Before != nil
}

// LikePattern turns user input into an ILIKE pattern that matches it literally, with
// wildcards added only where asked for.
func LikePattern(s string, prefix, suffix bool) string {
	var b strings.Builder
	if prefix {
		b.WriteByte('%')
	}
	for _, r := range s {
		if r == '\\' || r == '%' || r == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	if suffix {
		b.WriteByte('%')
	}
	return b.String()
}

// AddressPattern matches a from/to value against an address: "@domain" matches the domain,
// a full address matches exactly, anything else is a substring of the address.
func AddressPattern(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "@"):
		return LikePattern(v, true, false)
	case strings.Contains(v, "@"):
		return LikePattern(v, false, false)
	default:
		return LikePattern(v, true, true)
	}
}
