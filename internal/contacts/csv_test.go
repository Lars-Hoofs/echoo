package contacts

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSafeCellNeutralisesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"":                   "",
		"Anna":               "Anna",
		"=HYPERLINK(\"x\")":  "'=HYPERLINK(\"x\")",
		"+31612345678":       "'+31612345678",
		"-2+3":               "'-2+3",
		"@SUM(A1)":           "'@SUM(A1)",
		"\t=1":               "'\t=1",
		"\r=1":               "'\r=1",
		"a=b":                "a=b",
		"'already quoted":    "'already quoted",
		"=cmd|' /C calc'!A0": "'=cmd|' /C calc'!A0",
		"Zoë =Ünicode":       "Zoë =Ünicode",
	} {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSVWriterEscapesAndProtects(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCSVWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write("Naam", "Notitie"); err != nil {
		t.Fatal(err)
	}
	if err := w.Write("=1+1", "regel een\nregel \"twee\"; drie"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\xEF\xBB\xBF") {
		t.Error("no byte order mark")
	}
	if !strings.Contains(out, "'=1+1;") {
		t.Errorf("formula not neutralised: %q", out)
	}
	table, err := ParseCSV(buf.Bytes(), ";")
	if err != nil {
		t.Fatal(err)
	}
	if got := table.Rows[0][1]; got != "regel een\nregel \"twee\"; drie" {
		t.Errorf("round trip lost content: %q", got)
	}
}

func TestCleanCellUndoesSafeCell(t *testing.T) {
	for _, in := range []string{"=1+1", "+31612345678", "-5", "@handle", "Anna"} {
		if got := cleanCell(SafeCell(in)); got != in {
			t.Errorf("cleanCell(SafeCell(%q)) = %q", in, got)
		}
	}
	if got := cleanCell("'quoted"); got != "'quoted" {
		t.Errorf("a plain apostrophe must stay: %q", got)
	}
}

func TestDetectDelimiter(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"comma":                  {"a,b,c\n1,2,3", ","},
		"semicolon":              {"a;b;c\n1;2;3", ";"},
		"semicolon with commas":  {"naam;omschrijving\nAnna;een, twee, drie", ";"},
		"quoted semicolons":      {"\"a;b\",c\n1,2", ","},
		"single column":          {"email\nx@y.nl", ","},
		"only first line counts": {"a,b\n1;2;3;4;5", ","},
	} {
		if got := DetectDelimiter([]byte(tc.in)); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestParseCSV(t *testing.T) {
	t.Run("detects semicolons, strips BOM, skips blank lines", func(t *testing.T) {
		table, err := ParseCSV([]byte("\xEF\xBB\xBFE-mail;Naam\n\na@x.nl;Anna\n;\nb@x.nl;\"Bram; de \"\"Bouwer\"\"\"\n"), "")
		if err != nil {
			t.Fatal(err)
		}
		if table.Delimiter != ";" || !slices.Equal(table.Header, []string{"E-mail", "Naam"}) || len(table.Rows) != 2 {
			t.Fatalf("table = %+v", table)
		}
		if table.Rows[1][1] != `Bram; de "Bouwer"` {
			t.Errorf("quoted cell = %q", table.Rows[1][1])
		}
	})
	t.Run("explicit delimiter wins over detection", func(t *testing.T) {
		table, err := ParseCSV([]byte("a;b,c\n1;2,3\n"), ",")
		if err != nil || len(table.Header) != 2 || table.Header[0] != "a;b" {
			t.Fatalf("%+v %v", table, err)
		}
	})
	t.Run("crlf and embedded newlines", func(t *testing.T) {
		table, err := ParseCSV([]byte("a,b\r\n1,\"x\r\ny\"\r\n"), "")
		if err != nil || len(table.Rows) != 1 {
			t.Fatalf("%+v %v", table, err)
		}
	})
	t.Run("rows of different width", func(t *testing.T) {
		table, err := ParseCSV([]byte("a,b,c\n1\n1,2,3,4\n"), "")
		if err != nil || len(table.Rows) != 2 {
			t.Fatalf("%+v %v", table, err)
		}
	})
	for name, tc := range map[string]struct {
		in   []byte
		want error
	}{
		"not utf-8":   {[]byte("a,b\n\xff\xfe,1\n"), ErrNotUTF8},
		"header only": {[]byte("a,b\n"), ErrEmptyCSV},
		"empty":       {[]byte(""), ErrEmptyCSV},
		"bad quotes":  {[]byte("a,b\n\"unterminated,1\n"), ErrBadCSV},
	} {
		if _, err := ParseCSV(tc.in, ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := ParseCSV([]byte("a\n1\n"), "|"); err == nil {
		t.Error("unsupported delimiter accepted")
	}
}

func TestParseCSVRowLimit(t *testing.T) {
	build := func(rows int) []byte {
		var b strings.Builder
		b.WriteString("email\n")
		for i := range rows {
			fmt.Fprintf(&b, "u%d@example.com\n", i)
		}
		return []byte(b.String())
	}
	table, err := ParseCSV(build(MaxImportRows), "")
	if err != nil || len(table.Rows) != MaxImportRows {
		t.Fatalf("limit itself must be accepted: %d rows, %v", len(table.Rows), err)
	}
	if _, err := ParseCSV(build(MaxImportRows+1), ""); !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("err = %v, want ErrTooManyRows", err)
	}
	if _, err := PrepareImport(make([]byte, MaxImportBytes+1), ImportOptions{}, nil); err == nil {
		t.Fatal("oversized file accepted")
	}
}
