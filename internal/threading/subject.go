// Package threading decides which conversation an incoming message belongs to.
package threading

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// prefixRE matches one reply/forward prefix including counted forms ("Re[2]:", "Re(3):",
// "Re^2:") and the full-width colon. The colon is mandatory so words such as "Resultaten"
// or "Retour" are never touched. French typography puts a space before the colon.
var prefixRE = regexp.MustCompile(`(?i)^(re|fwd?|aw|wg|antw|antwoord|doorst|doorgestuurd|sv|vs|tr|réf|r|rif|enc)\s*(?:\[\d+\]|\(\d+\)|\^\d+)?\s*[:：]\s*`)

// forwardPrefixes are the prefixes (lower-cased) that mark a forward rather than a reply.
var forwardPrefixes = map[string]bool{
	"fw": true, "fwd": true, "wg": true, "doorst": true, "doorgestuurd": true,
	"vs": true, "tr": true, "enc": true,
}

// leadingTagRE matches a bracketed tag at the start: a mailing list name or a ticket tag.
var leadingTagRE = regexp.MustCompile(`^\[[^\]]*\]\s*`)

// ticketTagRE matches "[#1234]" and "[Echoo #1234]" anywhere in the subject, because helpdesks
// append them as well as prepend them.
var ticketTagRE = regexp.MustCompile(`\[[^\]]*#\d+\]`)

var genericSubjects = map[string]bool{
	"": true, "vraag": true, "question": true, "factuur": true, "invoice": true,
	"contact": true, "info": true, "hallo": true, "hello": true, "hi": true, "test": true,
	"no subject": true, "(geen onderwerp)": true, "(no subject)": true,
}

const minSubjectRunes = 4

// NormalizeSubject reduces a subject to the form stored in conversations.subject_normalized.
func NormalizeSubject(s string) string {
	n, _ := normalizeSubject(s)
	return n
}

// IsGenericSubject reports whether a normalized subject is too common to thread on.
func IsGenericSubject(normalized string) bool {
	return genericSubjects[normalized] || utf8.RuneCountInString(normalized) < minSubjectRunes
}

// normalizeSubject also returns the first reply/forward prefix found (lower-cased), which
// tells forwards from replies.
func normalizeSubject(s string) (normalized, firstPrefix string) {
	s = ticketTagRE.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	for {
		if loc := leadingTagRE.FindStringIndex(s); loc != nil {
			s = s[loc[1]:]
			continue
		}
		if m := prefixRE.FindStringSubmatchIndex(s); m != nil {
			if firstPrefix == "" {
				firstPrefix = strings.ToLower(s[m[2]:m[3]])
			}
			s = s[m[1]:]
			continue
		}
		break
	}
	return strings.ToLower(strings.Join(strings.Fields(s), " ")), firstPrefix
}
