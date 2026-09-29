// Package mail holds the types shared by parsing, threading, ingestion and sending.
package mail

import "time"

type Address struct {
	Name    string `json:"name"`
	Address string `json:"address"` // lower-cased
}

// Parsed is the result of parsing one raw RFC 5322 message. It never contains the raw bytes;
// those stay in blob storage.
type Parsed struct {
	// MessageID is the normalized Message-ID without angle brackets, lower-cased domain part
	// preserved as sent. When the header is missing, the parser synthesizes
	// "synthetic-<sha256 of raw>@echoo.invalid" so every message has an identity.
	MessageID  string
	InReplyTo  []string // normalized IDs, in header order
	References []string // normalized IDs, oldest first as in the header

	From    Address
	Sender  *Address
	ReplyTo []Address
	To      []Address
	Cc      []Address
	Bcc     []Address

	Subject string    // decoded (RFC 2047), trimmed
	Date    time.Time // zero if missing or unparsable

	Text string // plain text body; derived from HTML when only HTML exists
	HTML string // raw HTML body as received (never sanitized here), "" if none

	Attachments []Attachment

	// Header facts used by threading, rules and auto-reply suppression.
	AutoSubmitted bool   // Auto-Submitted != no, X-Autoreply, X-Autorespond, Precedence auto_reply
	Bulk          bool   // Precedence: bulk/list/junk or List-Unsubscribe present
	ListID        string // List-Id value without angle brackets, "" if none
	ThreadTopic   string // Outlook Thread-Topic, "" if none
	ThreadIndex   string // Outlook Thread-Index (base64), "" if none

	// AuthResults holds the raw Authentication-Results header values, top-most first.
	AuthResults []string

	// DSN is set when the message is a delivery status notification (multipart/report).
	DSN *DSN
}

type Attachment struct {
	Filename     string // sanitized: no path components, no control characters
	DeclaredType string // Content-Type as sent, lower-cased, without parameters
	ContentID    string // without angle brackets, "" if none
	Inline       bool   // Content-Disposition inline or referenced via cid:
	Data         []byte
}

// DSN is a parsed delivery status notification (RFC 3464).
type DSN struct {
	OriginalMessageID string // from the returned headers or Original-Envelope-Id, normalized
	Recipients        []DSNRecipient
}

type DSNRecipient struct {
	Address        string
	Action         string // failed, delayed, delivered, relayed, expanded
	Status         string // e.g. 5.1.1
	DiagnosticCode string
}
