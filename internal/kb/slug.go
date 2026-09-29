package kb

import (
	"regexp"
	"strings"
)

// MaxSlugLen matches the length check in the database.
const MaxSlugLen = 80

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidSlug reports whether s is a lowercase, dash-separated slug of at most MaxSlugLen.
func ValidSlug(s string) bool { return len(s) <= MaxSlugLen && slugPattern.MatchString(s) }

var fold = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "æ", "ae", "ç", "c",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ì", "i", "í", "i", "î", "i", "ï", "i",
	"ñ", "n", "ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "œ", "oe",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ý", "y", "ÿ", "y", "ß", "ss",
)

// Slugify turns a title into a slug: lowercase, accents folded, everything that is not a
// letter or digit collapsed into single dashes. It returns "" when nothing is left.
func Slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range fold.Replace(strings.ToLower(title)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > MaxSlugLen {
		s = strings.TrimRight(s[:MaxSlugLen], "-")
	}
	return s
}
