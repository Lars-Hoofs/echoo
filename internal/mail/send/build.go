// Package send builds outbound messages, delivers them over SMTP and tracks the outcome in
// the outbound table (ADR 0005).
package send

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	"echoo/internal/mail"
)

// maxReferences caps the References header: the first ID (the thread root) plus the newest
// ones, so long threads do not grow the header without bound.
const maxReferences = 20

// Outgoing is one message to build. Bcc recipients only appear in the SMTP envelope.
type Outgoing struct {
	From        mail.Address
	To, Cc, Bcc []mail.Address
	Subject     string
	MessageID   string // without angle brackets
	InReplyTo   string // without angle brackets, "" if none
	References  []string
	Date        time.Time
	Text, HTML  string
	Attachments []mail.Attachment
	AutoReplied bool
	// Headers are extra fields, limited to the names in extraHeaderNames.
	Headers []Header
}

// Header is one extra message header field.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// extraHeaderNames are the only extra fields a caller may add: the ones bulk mail needs. Any
// other name is refused so a caller cannot forge routing or authentication fields.
var extraHeaderNames = map[string]bool{"List-Unsubscribe": true, "List-Unsubscribe-Post": true, "Precedence": true}

// ValidationError reports a value that must not be put into a message.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return "invalid " + e.Field + ": " + e.Reason }

// Recipients returns the envelope recipients: To, Cc and Bcc, without duplicates.
func (m Outgoing) Recipients() []string {
	var out []string
	seen := map[string]bool{}
	for _, list := range [][]mail.Address{m.To, m.Cc, m.Bcc} {
		for _, a := range list {
			key := strings.ToLower(a.Address)
			if !seen[key] {
				seen[key] = true
				out = append(out, a.Address)
			}
		}
	}
	return out
}

// Build renders msg as RFC 5322 bytes with a random MIME boundary.
func Build(msg Outgoing) ([]byte, error) {
	return build(msg, randomBoundary)
}

func randomBoundary() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("send: crypto/rand failed: " + err.Error())
	}
	return "echoo-" + hex.EncodeToString(b[:])
}

func (m Outgoing) validate() error {
	if err := checkAddress("From", m.From); err != nil {
		return err
	}
	for _, f := range []struct {
		field string
		list  []mail.Address
	}{{"To", m.To}, {"Cc", m.Cc}, {"Bcc", m.Bcc}} {
		for _, a := range f.list {
			if err := checkAddress(f.field, a); err != nil {
				return err
			}
		}
	}
	if len(m.Recipients()) == 0 {
		return &ValidationError{"recipients", "at least one recipient is required"}
	}
	if err := checkValue("Subject", m.Subject); err != nil {
		return err
	}
	if m.Date.IsZero() {
		return &ValidationError{"Date", "must be set"}
	}
	if err := checkMessageID("Message-ID", m.MessageID); err != nil {
		return err
	}
	if m.InReplyTo != "" {
		if err := checkMessageID("In-Reply-To", m.InReplyTo); err != nil {
			return err
		}
	}
	for _, r := range m.References {
		if err := checkMessageID("References", r); err != nil {
			return err
		}
	}
	for _, h := range m.Headers {
		if !extraHeaderNames[h.Name] {
			return &ValidationError{"header", "field " + h.Name + " is not allowed"}
		}
		if err := checkValue(h.Name, h.Value); err != nil {
			return err
		}
	}
	for _, a := range m.Attachments {
		if err := checkValue("attachment filename", a.Filename); err != nil {
			return err
		}
		if err := checkValue("attachment Content-ID", a.ContentID); err != nil {
			return err
		}
		if err := checkValue("attachment Content-Type", a.DeclaredType); err != nil {
			return err
		}
		if a.DeclaredType != "" {
			if _, _, err := mime.ParseMediaType(a.DeclaredType); err != nil {
				return &ValidationError{"attachment Content-Type", err.Error()}
			}
		}
		if a.Inline && a.ContentID == "" {
			return &ValidationError{"attachment Content-ID", "required for inline attachments"}
		}
	}
	return nil
}

func checkValue(field, v string) error {
	if strings.ContainsAny(v, "\r\n\x00") {
		return &ValidationError{field, "contains CR, LF or NUL"}
	}
	return nil
}

func checkMessageID(field, id string) error {
	if err := checkValue(field, id); err != nil {
		return err
	}
	if mail.NormalizeMessageID(id) == "" {
		return &ValidationError{field, "not a valid Message-ID"}
	}
	return nil
}

func checkAddress(field string, a mail.Address) error {
	if err := checkValue(field, a.Name); err != nil {
		return err
	}
	if err := checkValue(field, a.Address); err != nil {
		return err
	}
	parsed, err := netmail.ParseAddress(a.Address)
	if err != nil || parsed.Name != "" || parsed.Address != a.Address {
		return &ValidationError{field, "not a plain email address"}
	}
	for _, r := range a.Address {
		if r > 0x7e {
			return &ValidationError{field, "non-ASCII addresses are not supported"}
		}
	}
	return nil
}

func capReferences(refs []string) []string {
	if len(refs) <= maxReferences {
		return refs
	}
	out := make([]string, 0, maxReferences)
	out = append(out, refs[0])
	return append(out, refs[len(refs)-(maxReferences-1):]...)
}

func toGoMail(list []mail.Address) []*gomail.Address {
	out := make([]*gomail.Address, len(list))
	for i, a := range list {
		out[i] = &gomail.Address{Name: a.Name, Address: a.Address}
	}
	return out
}

// node is one MIME entity: either a leaf with a body or a multipart with children.
type node struct {
	header   message.Header
	body     []byte
	children []node
}

// orderedHeader keeps the given field order in the output: message.Header writes the most
// recently set field first.
func orderedHeader(kv ...string) message.Header {
	var h message.Header
	for i := len(kv) - 2; i >= 0; i -= 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func multipart(subtype string, boundary func() string, children ...node) node {
	ct := mime.FormatMediaType("multipart/"+subtype, map[string]string{"boundary": boundary()})
	return node{header: orderedHeader("Content-Type", ct), children: children}
}

func textPart(subtype, body string) node {
	h := orderedHeader(
		"Content-Type", "text/"+subtype+"; charset=utf-8",
		"Content-Transfer-Encoding", "quoted-printable",
	)
	return node{header: h, body: []byte(body)}
}

func attachmentPart(a mail.Attachment, inline bool) node {
	ct := a.DeclaredType
	if ct == "" {
		ct = "application/octet-stream"
	}
	disposition := "attachment"
	kv := []string{"Content-Type", ct}
	if inline {
		disposition = "inline"
		kv = append(kv, "Content-ID", "<"+a.ContentID+">")
	}
	var params map[string]string
	if a.Filename != "" {
		params = map[string]string{"filename": a.Filename}
	}
	kv = append(kv,
		"Content-Disposition", mime.FormatMediaType(disposition, params),
		"Content-Transfer-Encoding", "base64",
	)
	return node{header: orderedHeader(kv...), body: a.Data}
}

func (m Outgoing) structure(boundary func() string) node {
	body := textPart("plain", m.Text)
	if m.HTML != "" {
		body = multipart("alternative", boundary, body, textPart("html", m.HTML))
	}
	var inline, regular []node
	for _, a := range m.Attachments {
		if a.Inline && m.HTML != "" {
			inline = append(inline, attachmentPart(a, true))
		} else {
			regular = append(regular, attachmentPart(a, false))
		}
	}
	if len(inline) > 0 {
		body = multipart("related", boundary, append([]node{body}, inline...)...)
	}
	if len(regular) > 0 {
		body = multipart("mixed", boundary, append([]node{body}, regular...)...)
	}
	return body
}

func (m Outgoing) topHeader(content message.Header) message.Header {
	var h gomail.Header
	h.SetAddressList("From", toGoMail([]mail.Address{m.From}))
	h.SetAddressList("To", toGoMail(m.To))
	h.SetSubject(m.Subject)
	h.SetDate(m.Date)
	h.SetMessageID(m.MessageID)
	keys := []string{"From", "To"}
	if len(m.Cc) > 0 {
		h.SetAddressList("Cc", toGoMail(m.Cc))
		keys = append(keys, "Cc")
	}
	keys = append(keys, "Subject", "Date", "Message-ID")
	if m.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{m.InReplyTo})
		keys = append(keys, "In-Reply-To")
	}
	if refs := capReferences(m.References); len(refs) > 0 {
		h.SetMsgIDList("References", refs)
		keys = append(keys, "References")
	}
	if m.AutoReplied {
		h.Set("Auto-Submitted", "auto-replied")
		keys = append(keys, "Auto-Submitted")
	}
	for _, x := range m.Headers {
		h.Set(x.Name, x.Value)
		keys = append(keys, x.Name)
	}
	h.Set("MIME-Version", "1.0")
	keys = append(keys, "MIME-Version")

	var kv []string
	for _, k := range keys {
		kv = append(kv, k, h.Get(k))
	}
	for f := content.Fields(); f.Next(); {
		kv = append(kv, f.Key(), f.Value())
	}
	return orderedHeader(kv...)
}

func writeNode(w *message.Writer, n node) error {
	if len(n.children) == 0 {
		if _, err := w.Write(n.body); err != nil {
			return err
		}
		return w.Close()
	}
	for _, c := range n.children {
		cw, err := w.CreatePart(c.header)
		if err != nil {
			return err
		}
		if err := writeNode(cw, c); err != nil {
			return err
		}
	}
	return w.Close()
}

func build(msg Outgoing, boundary func() string) ([]byte, error) {
	if err := msg.validate(); err != nil {
		return nil, err
	}
	root := msg.structure(boundary)
	var buf bytes.Buffer
	w, err := message.CreateWriter(&buf, msg.topHeader(root.header))
	if err != nil {
		return nil, fmt.Errorf("write message header: %w", err)
	}
	if err := writeNode(w, root); err != nil {
		return nil, errors.Join(errors.New("write message body"), err)
	}
	return buf.Bytes(), nil
}
