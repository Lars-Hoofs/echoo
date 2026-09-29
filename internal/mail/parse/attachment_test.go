package parse

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeFilename(t *testing.T) {
	long := strings.Repeat("a", 300)
	tests := []struct {
		name, in, mediaType, want string
	}{
		{"plain", "report.pdf", "application/pdf", "report.pdf"},
		{"unicode kept", "Überweisung €.pdf", "application/pdf", "Überweisung €.pdf"},
		{"unix traversal", "../../etc/passwd", "text/plain", "passwd"},
		{"windows path", `C:\Users\x\evil.exe`, "application/octet-stream", "evil.exe"},
		{"windows traversal", `..\..\windows\system32\cmd.exe`, "application/octet-stream", "cmd.exe"},
		{"drive without separator", "C:evil.exe", "application/octet-stream", "evil.exe"},
		{"mixed separators", `a/b\c/d.txt`, "text/plain", "d.txt"},
		{"absolute", "/etc/shadow", "text/plain", "shadow"},
		{"leading dots", "...hidden", "text/plain", "hidden"},
		{"only dots", "..", "application/pdf", "bijlage.pdf"},
		{"trailing dot and space", "file.txt. .", "text/plain", "file.txt"},
		{"control characters", "a\x00b\r\nc\x7f.txt", "text/plain", "abc.txt"},
		{"bidi override", "photo\u202egpj.exe", "image/jpeg", "photogpj.exe"},
		{"zero width", "in\u200bvoice.pdf", "application/pdf", "invoice.pdf"},
		{"empty", "", "image/png", "bijlage.png"},
		{"whitespace", "  \t ", "image/jpeg", "bijlage.jpg"},
		{"unknown type has no extension", "", "application/x-thing", "bijlage"},
		{"rfc822 fallback", "", "message/rfc822", "Doorgestuurd bericht.eml"},
		{"trailing slash", "dir/", "application/pdf", "bijlage.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eq(t, "filename", sanitizeFilename(tt.in, tt.mediaType), tt.want)
		})
	}

	t.Run("long name keeps extension", func(t *testing.T) {
		got := sanitizeFilename(long+".pdf", "application/pdf")
		eq(t, "length", len(got), 255)
		if !strings.HasSuffix(got, ".pdf") {
			t.Errorf("extension lost: %q", got[len(got)-10:])
		}
	})
	t.Run("long name without extension", func(t *testing.T) {
		eq(t, "length", len(sanitizeFilename(long, "text/plain")), 255)
	})
	t.Run("cut on UTF-8 boundary", func(t *testing.T) {
		for _, pad := range []int{0, 1, 2, 3} {
			in := strings.Repeat("a", pad) + strings.Repeat("€", 200) + ".txt"
			got := sanitizeFilename(in, "text/plain")
			if len(got) > 255 || !utf8.ValidString(got) || !strings.HasSuffix(got, ".txt") {
				t.Errorf("pad %d: %d bytes, valid=%v", pad, len(got), utf8.ValidString(got))
			}
		}
	})
	t.Run("absurd extension is not preserved", func(t *testing.T) {
		got := sanitizeFilename("a."+strings.Repeat("b", 400), "text/plain")
		eq(t, "length", len(got), 255)
	})
}
