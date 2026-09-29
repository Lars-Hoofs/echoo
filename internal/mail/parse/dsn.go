package parse

import (
	"bufio"
	"strings"

	"github.com/emersion/go-message/textproto"

	"echoo/internal/mail"
)

func isDeliveryStatusReport(ct contentType) bool {
	return ct.mediaType == "multipart/report" &&
		strings.EqualFold(strings.TrimSpace(ct.params["report-type"]), "delivery-status")
}

// readDeliveryStatus parses a message/delivery-status body (RFC 3464): a per-message field
// group followed by one group per recipient, separated by blank lines.
func (w *walker) readDeliveryStatus(data []byte) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, group := range strings.Split(text, "\n\n") {
		if strings.TrimSpace(group) == "" {
			continue
		}
		// A malformed line leaves the fields read so far, which is the best we can do.
		h, _ := textproto.ReadHeader(bufio.NewReader(strings.NewReader(group + "\n\n")))
		if w.envelopeID == "" {
			w.envelopeID = strings.TrimSpace(h.Get("Original-Envelope-Id"))
		}
		rcpt := h.Get("Final-Recipient")
		if rcpt == "" {
			rcpt = h.Get("Original-Recipient")
		}
		if rcpt == "" {
			continue
		}
		// The value is "<address-type>; <address>", normally "rfc822; user@example.com".
		if _, addr, ok := strings.Cut(rcpt, ";"); ok {
			rcpt = addr
		}
		rcpt = strings.ToLower(strings.Trim(strings.TrimSpace(rcpt), "<>"))
		w.dsn.Recipients = append(w.dsn.Recipients, mail.DSNRecipient{
			Address:        cleanHeader(rcpt),
			Action:         strings.ToLower(cleanHeader(strings.TrimSpace(h.Get("Action")))),
			Status:         cleanHeader(strings.TrimSpace(h.Get("Status"))),
			DiagnosticCode: cleanHeader(strings.TrimSpace(h.Get("Diagnostic-Code"))),
		})
	}
}

// setOriginalID takes the Message-ID from the headers of the returned original message.
func (w *walker) setOriginalID(returned []byte) {
	if w.dsn.OriginalMessageID != "" {
		return
	}
	h, _ := textproto.ReadHeader(bufio.NewReader(strings.NewReader(string(returned))))
	if ids := extractIDs(h.Get("Message-Id")); len(ids) > 0 {
		w.dsn.OriginalMessageID = ids[0]
	}
}
