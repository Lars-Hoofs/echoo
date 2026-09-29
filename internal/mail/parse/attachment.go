package parse

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxFilenameBytes = 255
	maxExtensionLen  = 32
)

// Not mime.ExtensionsByType: its answers depend on the host's mime.types file.
var extensionByType = map[string]string{
	"image/jpeg":                    ".jpg",
	"image/png":                     ".png",
	"image/gif":                     ".gif",
	"image/webp":                    ".webp",
	"image/heic":                    ".heic",
	"application/pdf":               ".pdf",
	"application/zip":               ".zip",
	"application/msword":            ".doc",
	"application/vnd.ms-excel":      ".xls",
	"application/vnd.ms-powerpoint": ".ppt",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/pkcs7-signature":                                               ".p7s",
	"application/pgp-signature":                                                 ".asc",
	"text/plain":                                                                ".txt",
	"text/html":                                                                 ".html",
	"text/csv":                                                                  ".csv",
	"text/calendar":                                                             ".ics",
	"message/rfc822":                                                            ".eml",
}

// sanitizeFilename makes an attacker-supplied name safe to store and to offer as a download
// name: no path components (either separator), drive prefix, control or format characters
// (which include bidi overrides used to fake extensions), leading dots or trailing dots and
// spaces, and at most 255 bytes.
func sanitizeFilename(name, mediaType string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.ReplaceAll(name, `\`, "/")
	name = name[strings.LastIndexByte(name, '/')+1:]
	if len(name) >= 2 && name[1] == ':' && isASCIILetter(name[0]) {
		name = name[2:]
	}
	name = strings.TrimLeft(strings.TrimSpace(name), ". ")
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return fallbackFilename(mediaType)
	}
	return truncateFilename(name)
}

func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func fallbackFilename(mediaType string) string {
	if mediaType == "message/rfc822" {
		return "Doorgestuurd bericht.eml"
	}
	return "bijlage" + extensionByType[mediaType]
}

// truncateFilename cuts on a UTF-8 boundary and keeps a short extension intact.
func truncateFilename(name string) string {
	if len(name) <= maxFilenameBytes {
		return name
	}
	ext := path.Ext(name)
	if len(ext) > maxExtensionLen {
		ext = ""
	}
	cut := maxFilenameBytes - len(ext)
	for !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut] + ext
}
