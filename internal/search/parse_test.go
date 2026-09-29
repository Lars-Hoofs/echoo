package search

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func date(s string) *time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &d
}

func number(n int64) *int64 { return &n }

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Query
	}{
		{"empty", "", Query{}},
		{"only spaces", "   ", Query{}},
		{"free text", "factuur maart", Query{Text: "factuur maart"}},
		{"phrase stays quoted", `"exacte zin" los`, Query{Text: `"exacte zin" los`}},
		{"unterminated quote", `"exacte zin`, Query{Text: `"exacte zin"`}},
		{"empty quotes are dropped", `"" a`, Query{Text: "a"}},
		{"status dutch", "status:wachtend", Query{Statuses: []string{"waiting"}}},
		{"status english upper key", "STATUS:Closed", Query{Statuses: []string{"closed"}}},
		{"status snoozed", "status:uitgesteld", Query{Snoozed: true}},
		{"two statuses", "status:open status:waiting", Query{Statuses: []string{"open", "waiting"}}},
		{"duplicate status", "status:open status:OPEN", Query{Statuses: []string{"open"}}},
		{"invalid status is text", "status:bogus", Query{Text: "status:bogus"}},
		{"from dutch", "van:jan@voorbeeld.nl", Query{From: []string{"jan@voorbeeld.nl"}}},
		{"from domain english", "from:@bedrijf.nl", Query{From: []string{"@bedrijf.nl"}}},
		{"to", "aan:info@x.nl to:@y.nl", Query{To: []string{"info@x.nl", "@y.nl"}}},
		{"mailbox quoted value", `mailbox:"Support NL"`, Query{Mailboxes: []string{"Support NL"}}},
		{"label", "label:factuur", Query{Labels: []string{"factuur"}}},
		{"assignee me", "toegewezen:me", Query{AssigneeMe: true}},
		{"assignee mij", "assignee:mij", Query{AssigneeMe: true}},
		{"assignee none", "toegewezen:niemand", Query{AssigneeNone: true}},
		{"assignee name", "toegewezen:Jan", Query{Assignees: []string{"Jan"}}},
		{"team", `team:"Klant service"`, Query{Teams: []string{"Klant service"}}},
		{"priority", "prioriteit:hoog priority:urgent", Query{Priorities: []string{"high", "urgent"}}},
		{"invalid priority is text", "prioriteit:enorm", Query{Text: "prioriteit:enorm"}},
		{"has attachment dutch", "heeft:bijlage", Query{HasAttachment: true}},
		{"has attachment english", "has:attachment", Query{HasAttachment: true}},
		{"has other is text", "heeft:hond", Query{Text: "heeft:hond"}},
		{"dates", "na:2026-01-05 voor:2026-02-01", Query{After: date("2026-01-05"), Before: date("2026-02-01")}},
		{"english dates", "after:2026-01-05 before:2026-02-01", Query{After: date("2026-01-05"), Before: date("2026-02-01")}},
		{"invalid date is text", "voor:gisteren", Query{Text: "voor:gisteren"}},
		{"impossible date is text", "na:2026-02-30", Query{Text: "na:2026-02-30"}},
		{"number", "#1234", Query{Number: number(1234)}},
		{"number with text", "#42 factuur", Query{Number: number(42), Text: "factuur"}},
		{"bad number is text", "#abc #0 #-3 #+3", Query{Text: "#abc #0 #-3 #+3"}},
		{"quoted number is text", `"#42"`, Query{Text: `"#42"`}},
		{"unknown key is text", "http://example.com kleur:blauw", Query{Text: "http://example.com kleur:blauw"}},
		{"empty value is text", "status: open", Query{Text: "status: open"}},
		{"mixed", `status:open van:@bedrijf.nl label:factuur "exacte zin"`,
			Query{Text: `"exacte zin"`, Statuses: []string{"open"}, From: []string{"@bedrijf.nl"}, Labels: []string{"factuur"}}},
		{"filters between text", "hallo label:x wereld", Query{Text: "hallo wereld", Labels: []string{"x"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Parse(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Parse(%q)\n got %+v\nwant %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseTruncatesLongInput(t *testing.T) {
	got := Parse(strings.Repeat("a", 500))
	if len(got.Text) != MaxQueryRunes {
		t.Fatalf("text length %d, want %d", len(got.Text), MaxQueryRunes)
	}
}

func TestQueryPredicates(t *testing.T) {
	if !Parse("").Empty() {
		t.Error("empty query is not Empty")
	}
	if Parse("#5").Empty() {
		t.Error("number query is Empty")
	}
	if !Parse("label:x").HasFilters() || Parse("x").HasFilters() {
		t.Error("HasFilters wrong")
	}
	if !Parse("x").HasText() || Parse("label:x").HasText() {
		t.Error("HasText wrong")
	}
}

func TestPatterns(t *testing.T) {
	tests := []struct{ in, want string }{
		{"@Bedrijf.nl", "%@bedrijf.nl"},
		{"Jan@X.nl", "jan@x.nl"},
		{"jan", "%jan%"},
		{"100%_x", "%100\\%\\_x%"},
	}
	for _, tc := range tests {
		if got := AddressPattern(tc.in); got != tc.want {
			t.Errorf("AddressPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := LikePattern(`a\b`, true, true); got != `%a\\b%` {
		t.Errorf("LikePattern escaped backslash: %q", got)
	}
}
