package sanitize

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// Warning kinds. The frontend maps them to Dutch copy; Detail carries the value to show.
const (
	KindDisplayNameSpoof = "display_name_spoof"
	KindReplyToMismatch  = "reply_to_mismatch"
	KindPunycodeDomain   = "punycode_domain"
	KindMixedScript      = "mixed_script_domain"
	KindAuthFailed       = "auth_failed"
	KindLinkMismatch     = "link_mismatch"
)

type Warning struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Sender is what the header analysis needs about one inbound message and its mailbox.
type Sender struct {
	FromName    string
	FromAddr    string
	ReplyTo     []string // addresses
	AuthResults string   // first Authentication-Results header only

	MailboxAddr string
	MailboxName string
}

var (
	emailInName = regexp.MustCompile(`(?i)[a-z0-9._%+'-]+@([a-z0-9-]+(?:\.[a-z0-9-]+)+)`)
	authResult  = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc)\s*=\s*([a-z]+)`)
	comment     = regexp.MustCompile(`\([^()]*\)`)
)

// Analyze returns the header-based phishing indicators. It never fails: missing or malformed
// headers simply produce no warning.
func Analyze(s Sender) []Warning {
	var out []Warning
	fromDomain := domainOf(s.FromAddr)

	if d, ok := spoofedName(s, fromDomain); ok {
		out = append(out, Warning{KindDisplayNameSpoof, d})
	}
	for _, rt := range s.ReplyTo {
		if d := domainOf(rt); d != "" && fromDomain != "" && !sameSite(d, fromDomain) {
			out = append(out, Warning{KindReplyToMismatch, d})
			break
		}
	}
	if fromDomain != "" {
		if uni, ok := punycodeForm(fromDomain); ok {
			out = append(out, Warning{KindPunycodeDomain, uni})
		}
		if mixedScript(fromDomain) {
			out = append(out, Warning{KindMixedScript, fromDomain})
		}
	}
	if failed := failedAuth(s.AuthResults); len(failed) > 0 {
		out = append(out, Warning{KindAuthFailed, strings.Join(failed, ", ")})
	}
	return out
}

// LinkWarnings converts sanitizer link mismatches into warnings, at most three.
func LinkWarnings(m []LinkMismatch) []Warning {
	var out []Warning
	for _, l := range m {
		if len(out) == 3 {
			break
		}
		out = append(out, Warning{KindLinkMismatch, l.Shown + " -> " + l.Actual})
	}
	return out
}

func domainOf(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}

// spoofedName catches names that claim another identity: an address at a different domain
// ("billing@bank.com" <x@evil.test>), or the mailbox's own address, domain or name when the
// mail did not come from that domain.
func spoofedName(s Sender, fromDomain string) (string, bool) {
	if fromDomain == "" || strings.TrimSpace(s.FromName) == "" {
		return "", false
	}
	name := strings.ToLower(s.FromName)
	for _, m := range emailInName.FindAllStringSubmatch(name, -1) {
		if !sameSite(m[1], fromDomain) {
			return m[0], true
		}
	}
	if own := domainOf(s.MailboxAddr); own != "" && !sameSite(own, fromDomain) {
		if containsDomain(name, own) {
			return own, true
		}
		if mn := strings.ToLower(strings.TrimSpace(s.MailboxName)); mn != "" && name == mn {
			return own, true
		}
	}
	return "", false
}

func containsDomain(text, domain string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], domain)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(domain)
		before := start == 0 || !isDomainChar(text[start-1])
		after := end == len(text) || !isDomainChar(text[end])
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isDomainChar(c byte) bool {
	return c == '-' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
}

func punycodeForm(domain string) (string, bool) {
	if !strings.Contains(domain, "xn--") {
		return "", false
	}
	uni, err := idna.Lookup.ToUnicode(domain)
	if err != nil || uni == domain {
		return domain, true
	}
	return uni, true
}

// mixedScript reports a label mixing letters of several scripts, the classic homograph trick
// ("paypal" with a Cyrillic "а"). Digits and hyphens belong to every script.
func mixedScript(domain string) bool {
	uni, err := idna.Lookup.ToUnicode(domain)
	if err != nil {
		uni = domain
	}
	for _, label := range strings.Split(uni, ".") {
		scripts := map[string]bool{}
		for _, r := range label {
			if !unicode.IsLetter(r) {
				continue
			}
			scripts[scriptOf(r)] = true
		}
		if len(scripts) > 1 {
			return true
		}
	}
	return false
}

var scriptTables = []struct {
	name  string
	table *unicode.RangeTable
}{
	{"latin", unicode.Latin}, {"cyrillic", unicode.Cyrillic}, {"greek", unicode.Greek}, {"cjk", unicode.Han},
	{"arabic", unicode.Arabic}, {"hebrew", unicode.Hebrew}, {"armenian", unicode.Armenian},
	{"cjk", unicode.Hiragana}, {"cjk", unicode.Katakana}, {"hangul", unicode.Hangul}, {"thai", unicode.Thai},
}

func scriptOf(r rune) string {
	for _, s := range scriptTables {
		if unicode.Is(s.table, r) {
			return s.name
		}
	}
	return "other"
}

// failedAuth lists the mechanisms the receiving server reported as failed. Parenthesized
// comments are dropped first: they are free text and may quote anything.
func failedAuth(header string) []string {
	for comment.MatchString(header) {
		header = comment.ReplaceAllString(header, "")
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range authResult.FindAllStringSubmatch(header, -1) {
		mech := strings.ToLower(m[1])
		if strings.EqualFold(m[2], "fail") && !seen[mech] {
			seen[mech] = true
			out = append(out, mech)
		}
	}
	return out
}
