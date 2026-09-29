package compose

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NewContentID returns a fresh Content-ID (without angle brackets) for an uploaded file.
func NewContentID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate content id: %w", err)
	}
	return hex.EncodeToString(b[:]) + "@echoo.upload", nil
}

const maxFilenameBytes = 255

// SafeFilename makes a client-supplied file name safe to store and to put in a header: no path
// components, control or format characters (which include bidi overrides that fake extensions)
// and no leading or trailing dots.
func SafeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.ReplaceAll(name, `\`, "/")
	name = name[strings.LastIndexByte(name, '/')+1:]
	name = strings.Trim(strings.TrimSpace(name), ". ")
	if name == "" {
		return "bijlage"
	}
	for len(name) > maxFilenameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}
