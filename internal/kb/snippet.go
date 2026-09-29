package kb

import "strings"

// Markers that ts_headline puts around matches; Text removes them from article text first.
const (
	HitStart = "\x02"
	HitEnd   = "\x03"
)

// HeadlineOptions is the ts_headline option string that produces the markers above.
const HeadlineOptions = "StartSel=" + HitStart + ",StopSel=" + HitEnd + ",MaxWords=32,MinWords=14,MaxFragments=1"

// SnippetPart is a run of snippet text; Hit marks the words that matched the query. The
// template escapes every part, so no HTML from the article text reaches the page.
type SnippetPart struct {
	Text string
	Hit  bool
}

// ParseSnippet splits a ts_headline result on the hit markers.
func ParseSnippet(s string) []SnippetPart {
	var parts []SnippetPart
	for s != "" {
		before, rest, found := strings.Cut(s, HitStart)
		if before != "" {
			parts = append(parts, SnippetPart{Text: before})
		}
		if !found {
			break
		}
		hit, after, _ := strings.Cut(rest, HitEnd)
		if hit != "" {
			parts = append(parts, SnippetPart{Text: hit, Hit: true})
		}
		s = after
	}
	return parts
}
