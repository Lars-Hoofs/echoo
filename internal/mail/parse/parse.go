// Package parse turns a raw RFC 5322 message into a mail.Parsed. It is lenient about the
// mistakes real mail clients make and strict about resource limits, because its input is
// attacker-controlled (SECURITY.md, section A).
package parse

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers message.CharsetReader

	"echoo/internal/mail"
)

var (
	// ErrTooLarge is returned for messages over Limits.MaxMessageBytes. The caller should
	// keep the raw bytes and mark the message skipped rather than failed.
	ErrTooLarge = errors.New("parse: message too large")
	// ErrLimits is returned when the header block, MIME depth or part count exceeds the
	// configured limits. Like ErrTooLarge it means skipped, not failed.
	ErrLimits = errors.New("parse: structural limits exceeded")
)

// Limits bounds the work done for one message. Zero fields fall back to DefaultLimits.
type Limits struct {
	MaxParts        int
	MaxDepth        int
	MaxHeaderBytes  int
	MaxMessageBytes int
}

var DefaultLimits = Limits{
	MaxParts:        500,
	MaxDepth:        20,
	MaxHeaderBytes:  256 << 10,
	MaxMessageBytes: 50 << 20,
}

func (l Limits) withDefaults() Limits {
	if l.MaxParts <= 0 {
		l.MaxParts = DefaultLimits.MaxParts
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultLimits.MaxDepth
	}
	if l.MaxHeaderBytes <= 0 {
		l.MaxHeaderBytes = DefaultLimits.MaxHeaderBytes
	}
	if l.MaxMessageBytes <= 0 {
		l.MaxMessageBytes = DefaultLimits.MaxMessageBytes
	}
	return l
}

// Parse never returns a Parsed with an empty MessageID. Malformed content is repaired on a
// best-effort basis; the only errors are ErrTooLarge, ErrLimits and an unreadable header.
func Parse(raw []byte, lim Limits) (*mail.Parsed, error) {
	lim = lim.withDefaults()
	if len(raw) > lim.MaxMessageBytes {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(raw), lim.MaxMessageBytes)
	}
	src, err := cleanHeaderBlock(raw, lim.MaxHeaderBytes)
	if err != nil {
		return nil, err
	}
	// go-message returns a usable entity together with an unknown charset/encoding error;
	// those are handled per part by decodeText, so only a nil entity is fatal here.
	root, err := message.ReadWithOptions(src, &message.ReadOptions{MaxHeaderBytes: -1})
	if root == nil {
		return nil, fmt.Errorf("parse: read header: %w", err)
	}

	p := &mail.Parsed{}
	fillHeaders(p, root.Header)
	if p.MessageID == "" {
		p.MessageID = fmt.Sprintf("synthetic-%x@echoo.invalid", sha256.Sum256(raw))
	}

	w := &walker{lim: lim}
	if ct := parseContentType(root.Header); isDeliveryStatusReport(ct) {
		w.dsn = &mail.DSN{}
	}
	b, err := w.walk(root, 1)
	if err != nil {
		return nil, err
	}

	p.HTML = b.html
	p.Text = b.text
	if strings.TrimSpace(p.Text) == "" && strings.TrimSpace(p.HTML) != "" {
		p.Text = htmlToText(p.HTML)
	}
	markReferencedInline(w.attachments, p.HTML)
	p.Attachments = w.attachments
	if w.dsn != nil {
		if w.dsn.OriginalMessageID == "" {
			w.dsn.OriginalMessageID = mail.NormalizeMessageID(w.envelopeID)
		}
		p.DSN = w.dsn
	}
	return p, nil
}

type walker struct {
	lim         Limits
	parts       int
	attachments []mail.Attachment
	dsn         *mail.DSN
	envelopeID  string
}

// body is the text and HTML found below one MIME node.
type body struct {
	text, html       string
	hasText, hasHTML bool
}

func (w *walker) walk(e *message.Entity, depth int) (body, error) {
	if depth > w.lim.MaxDepth {
		return body{}, fmt.Errorf("%w: MIME depth exceeds %d", ErrLimits, w.lim.MaxDepth)
	}
	w.parts++
	if w.parts > w.lim.MaxParts {
		return body{}, fmt.Errorf("%w: more than %d MIME parts", ErrLimits, w.lim.MaxParts)
	}
	ct := parseContentType(e.Header)
	if mr := e.MultipartReader(); mr != nil {
		return w.walkMultipart(mr, ct, depth)
	}
	return w.leaf(e, ct)
}

func (w *walker) walkMultipart(mr message.MultipartReader, ct contentType, depth int) (body, error) {
	var out body
	for {
		// A non-nil child with an error is an unknown charset or encoding, which decodeText
		// repairs. A nil child is io.EOF or a malformed tail; either way the parts already
		// read are kept instead of failing the whole message.
		child, _ := mr.NextPart()
		if child == nil {
			return out, nil
		}
		b, err := w.walk(child, depth+1)
		if err != nil {
			return body{}, err
		}
		if ct.mediaType == "multipart/alternative" {
			// Alternatives are renditions of the same content: the last of each kind wins.
			if b.hasText {
				out.text, out.hasText = b.text, true
			}
			if b.hasHTML {
				out.html, out.hasHTML = b.html, true
			}
			continue
		}
		if b.hasText {
			out.text = joinBodies(out.text, b.text, out.hasText, "\n\n")
			out.hasText = true
		}
		if b.hasHTML {
			out.html = joinBodies(out.html, b.html, out.hasHTML, "\n")
			out.hasHTML = true
		}
	}
}

func joinBodies(acc, next string, hasAcc bool, sep string) string {
	if !hasAcc {
		return next
	}
	return acc + sep + next
}

func (w *walker) leaf(e *message.Entity, ct contentType) (body, error) {
	disp, dparams := parseDisposition(e.Header)
	rawName := dparams["filename"]
	if rawName == "" {
		rawName = ct.params["name"]
	}

	// A truncated or corrupt transfer encoding yields the bytes decoded so far; the raw
	// message is stored untouched, so nothing is lost for a later retry.
	data, _ := io.ReadAll(e.Body)

	isText := ct.mediaType == "text/plain" || ct.mediaType == "text/html"
	if isText && disp != "attachment" && rawName == "" {
		s := decodeText(data, ct)
		if ct.mediaType == "text/html" {
			return body{html: s, hasHTML: true}, nil
		}
		return body{text: s, hasText: true}, nil
	}

	if w.dsn != nil {
		switch ct.mediaType {
		case "message/delivery-status":
			w.readDeliveryStatus(data)
			return body{}, nil
		case "text/rfc822-headers":
			w.setOriginalID(data)
			return body{}, nil
		case "message/rfc822":
			w.setOriginalID(data)
		}
	}

	w.attachments = append(w.attachments, mail.Attachment{
		Filename:     sanitizeFilename(decodeHeaderText(rawName), ct.mediaType),
		DeclaredType: ct.mediaType,
		ContentID:    cleanContentID(e.Header.Get("Content-Id")),
		Inline:       disp == "inline",
		Data:         data,
	})
	return body{}, nil
}

func markReferencedInline(atts []mail.Attachment, html string) {
	if html == "" {
		return
	}
	lower := strings.ToLower(html)
	for i := range atts {
		a := &atts[i]
		if a.ContentID != "" && !a.Inline && strings.Contains(lower, "cid:"+strings.ToLower(a.ContentID)) {
			a.Inline = true
		}
	}
}

func cleanContentID(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	return cleanHeader(strings.TrimSpace(v))
}

// cleanHeaderBlock returns a reader over raw whose header block has been rewritten into a
// form go-message accepts: lines that cannot be header fields (an mbox "From " line, stray
// text) are dropped, because go-message would otherwise misread the rest of the header as
// body, and line endings are normalized. The body is not copied. The header size limit is
// enforced here because the reader itself only reports an unexported error.
func cleanHeaderBlock(raw []byte, maxHeader int) (io.Reader, error) {
	var hdr []byte
	keptPrev := false
	pos := 0
	for pos < len(raw) {
		start := pos
		pos = len(raw)
		if nl := bytes.IndexByte(raw[start:], '\n'); nl >= 0 {
			pos = start + nl + 1
		}
		if pos > maxHeader {
			return nil, fmt.Errorf("%w: header block exceeds %d bytes", ErrLimits, maxHeader)
		}
		line := bytes.TrimRight(raw[start:pos], "\r\n")
		if len(line) == 0 {
			break
		}
		keptPrev = isFieldLine(line) || (keptPrev && (line[0] == ' ' || line[0] == '\t'))
		if keptPrev {
			hdr = append(append(hdr, line...), '\r', '\n')
		}
	}
	hdr = append(hdr, '\r', '\n')
	return io.MultiReader(bytes.NewReader(hdr), bytes.NewReader(raw[pos:])), nil
}

func isFieldLine(line []byte) bool {
	i := bytes.IndexByte(line, ':')
	if i <= 0 {
		return false
	}
	key := bytes.TrimRight(line[:i], " \t")
	if len(key) == 0 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
