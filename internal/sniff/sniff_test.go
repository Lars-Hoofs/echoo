package sniff

import "testing"

func TestType(t *testing.T) {
	cases := map[string]string{
		"%PDF-1.7\n%âãÏÓ\n":                          "application/pdf",
		"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR":        "image/png",
		"MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00": "application/vnd.microsoft.portable-executable",
		"<!DOCTYPE html><html><body>x</body></html>": "text/html",
		"gewoon wat tekst":                           "text/plain",
	}
	for in, want := range cases {
		if got := Type([]byte(in)); got != want {
			t.Errorf("%q: got %q want %q", in[:4], got, want)
		}
	}
}

func TestDangerous(t *testing.T) {
	cases := []struct {
		name, sniffed string
		want          bool
	}{
		{"factuur.pdf", "application/pdf", false},
		{"factuur.pdf", "application/vnd.microsoft.portable-executable", true}, // renamed executable
		{"factuur.pdf.exe", "application/pdf", true},
		{"logo.SVG", "text/plain", true},
		{"offerte.docm", "application/zip", true},
		{"foto.jpg", "image/jpeg", false},
	}
	for _, c := range cases {
		if got := Dangerous(c.name, c.sniffed); got != c.want {
			t.Errorf("%s (%s): got %v", c.name, c.sniffed, got)
		}
	}
}
