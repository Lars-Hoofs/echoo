package contacts

import (
	"encoding/json"
	"testing"

	"echoo/internal/db/dbq"
)

func def(typ string, options ...string) dbq.CustomAttributeDef {
	return dbq.CustomAttributeDef{Key: "k", Type: typ, Options: options}
}

func TestNormalize(t *testing.T) {
	valid := []struct {
		name    string
		def     dbq.CustomAttributeDef
		in      any
		lenient bool
		want    any
	}{
		{"text trims", def(TypeText), "  hallo ", false, "hallo"},
		{"number float", def(TypeNumber), 12.5, false, 12.5},
		{"number json.Number", def(TypeNumber), json.Number("7"), false, 7.0},
		{"number from csv with comma", def(TypeNumber), "12,5", true, 12.5},
		{"date", def(TypeDate), "2026-02-28", false, "2026-02-28"},
		{"boolean", def(TypeBoolean), true, false, true},
		{"boolean from csv", def(TypeBoolean), "Ja", true, true},
		{"boolean false from csv", def(TypeBoolean), "nee", true, false},
		{"list", def(TypeList, "gold", "silver"), "gold", false, "gold"},
		{"list case-insensitive from csv", def(TypeList, "Gold"), "gold", true, "Gold"},
		{"link", def(TypeLink), "https://example.com/a?b=1", false, "https://example.com/a?b=1"},
		{"empty string removes", def(TypeText), "   ", false, nil},
		{"null removes", def(TypeNumber), nil, false, nil},
	}
	for _, c := range valid {
		got, err := Normalize(c.def, c.in, c.lenient)
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	invalid := []struct {
		name    string
		def     dbq.CustomAttributeDef
		in      any
		lenient bool
	}{
		{"text is number", def(TypeText), 5.0, false},
		{"text with control character", def(TypeText), "a\x00b", false},
		{"text too long", def(TypeText), string(make([]byte, 0)) + repeat("x", 1001), false},
		{"number as string via api", def(TypeNumber), "12", false},
		{"number not numeric", def(TypeNumber), "abc", true},
		{"number too large", def(TypeNumber), 1e15, false},
		{"date wrong format", def(TypeDate), "28-02-2026", false},
		{"date impossible", def(TypeDate), "2026-02-30", false},
		{"boolean as string via api", def(TypeBoolean), "true", false},
		{"boolean unknown word", def(TypeBoolean), "misschien", true},
		{"list unknown option", def(TypeList, "gold"), "bronze", false},
		{"list wrong case via api", def(TypeList, "Gold"), "gold", false},
		{"link javascript", def(TypeLink), "javascript:alert(1)", false},
		{"link without host", def(TypeLink), "https://", false},
		{"link relative", def(TypeLink), "/etc/passwd", false},
		{"link with control", def(TypeLink), "https://example.com/a\nb", false},
	}
	for _, c := range invalid {
		if got, err := Normalize(c.def, c.in, c.lenient); err == nil {
			t.Errorf("%s: accepted as %v", c.name, got)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func TestApplyPatch(t *testing.T) {
	defs := []dbq.CustomAttributeDef{
		{Key: "tier", Type: TypeList, Options: []string{"gold", "silver"}},
		{Key: "seats", Type: TypeNumber},
	}
	current := Attributes{"tier": "gold", "seats": 3.0}

	got, problems := ApplyPatch(defs, current, map[string]any{"seats": json.Number("10"), "tier": nil}, false)
	if len(problems) > 0 || got["seats"] != 10.0 || got["tier"] != nil {
		t.Fatalf("patch = %v %v", got, problems)
	}
	if current["tier"] != "gold" {
		t.Fatal("ApplyPatch changed its input")
	}
	_, problems = ApplyPatch(defs, current, map[string]any{"unknown": "x", "tier": "bronze"}, false)
	if problems["unknown"] != "unknown_attribute" || problems["tier"] != "invalid" {
		t.Fatalf("problems = %v", problems)
	}
}

func TestCleanOptions(t *testing.T) {
	got, err := CleanOptions(TypeList, []string{" a ", "b", "a"})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for name, in := range map[string][]string{"empty": {}, "blank option": {"a", " "}, "too long": {repeat("x", 61)}} {
		if _, err := CleanOptions(TypeList, in); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := CleanOptions(TypeText, []string{"a"}); err == nil {
		t.Error("options on a text attribute accepted")
	}
}

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{"Anna@Example.COM": "anna@example.com", " a@b.nl ": "a@b.nl"} {
		if got, err := NormalizeEmail(in); err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "no-at", "a@b", "Name <a@b.nl>", "a@b.nl, c@d.nl", "a b@c.nl", "a@b.nl\nBcc: x@y.nl"} {
		if got, err := NormalizeEmail(in); err == nil {
			t.Errorf("NormalizeEmail(%q) accepted as %q", in, got)
		}
	}
}
