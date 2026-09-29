// Package sniff determines attachment types from content instead of trusting the sender's
// Content-Type or file extension, and flags types that are dangerous to open.
package sniff

import (
	"path/filepath"
	"strings"

	"github.com/gabriel-vasile/mimetype"
)

// Type returns the detected media type without parameters, e.g. "application/pdf".
func Type(data []byte) string {
	t := mimetype.Detect(data).String()
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	return t
}

var dangerousTypes = map[string]bool{
	"application/x-msdownload":                      true,
	"application/vnd.microsoft.portable-executable": true,
	"application/x-executable":                      true,
	"application/x-elf":                             true,
	"application/x-mach-binary":                     true,
	"application/x-sh":                              true,
	"application/x-bat":                             true,
	"text/x-shellscript":                            true,
	"application/javascript":                        true,
	"text/javascript":                               true,
	"text/html":                                     true,
	"image/svg+xml":                                 true,
	"application/x-ms-shortcut":                     true,
	"application/java-archive":                      true,
	"application/vnd.ms-cab-compressed":             true,
	"application/x-msi":                             true,
	"application/hta":                               true,
}

var dangerousExtensions = map[string]bool{
	".exe": true, ".com": true, ".scr": true, ".pif": true, ".bat": true, ".cmd": true, ".ps1": true,
	".vbs": true, ".vbe": true, ".js": true, ".jse": true, ".wsf": true, ".wsh": true, ".hta": true,
	".msi": true, ".msp": true, ".lnk": true, ".jar": true, ".html": true, ".htm": true, ".svg": true,
	".iso": true, ".img": true, ".docm": true, ".xlsm": true, ".pptm": true, ".dotm": true, ".xlam": true,
	".one": true, ".cpl": true, ".reg": true, ".sh": true, ".app": true, ".dmg": true,
}

// Dangerous reports whether opening the file could run code or scripts. Either signal is
// enough: a renamed executable and an "invoice.pdf.exe" are both caught.
func Dangerous(filename, sniffed string) bool {
	return dangerousTypes[sniffed] || dangerousExtensions[strings.ToLower(filepath.Ext(filename))]
}
