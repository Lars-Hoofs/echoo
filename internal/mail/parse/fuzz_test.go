package parse

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzParse(f *testing.F) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.eml"))
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range files {
		raw, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
		f.Add([]byte(strings.ReplaceAll(string(raw), "\n", "\r\n")))
	}
	f.Add(nested(30))
	f.Add(flat(600))

	// Small limits keep every iteration cheap and exercise the limit paths.
	lim := Limits{MaxParts: 50, MaxDepth: 8, MaxHeaderBytes: 8 << 10, MaxMessageBytes: 1 << 20}
	f.Fuzz(func(t *testing.T, raw []byte) {
		p, err := Parse(raw, lim)
		if err != nil {
			if !errors.Is(err, ErrLimits) && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			return
		}
		if p.MessageID == "" {
			t.Fatal("empty MessageID")
		}
		for what, s := range map[string]string{"Subject": p.Subject, "Text": p.Text, "From": p.From.Name} {
			if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
				t.Fatalf("%s is not storable text: %q", what, s)
			}
		}
		for _, a := range p.Attachments {
			if a.Filename == "" || len(a.Filename) > 255 || strings.ContainsAny(a.Filename, "/\\\x00") {
				t.Fatalf("unsafe attachment filename %q", a.Filename)
			}
		}
	})
}
