package mail

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
)

// NormalizeMessageID strips whitespace, comments-free angle brackets and lower-cases the
// domain part, which is case-insensitive; the local part is kept as sent. It returns "" for
// values that are not plausible Message-IDs.
func NormalizeMessageID(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "<")
	v = strings.TrimSuffix(v, ">")
	v = strings.TrimSpace(v)
	at := strings.LastIndexByte(v, '@')
	if at <= 0 || at == len(v)-1 || len(v) > 998 || strings.ContainsAny(v, " \t\r\n<>") {
		return ""
	}
	return v[:at] + "@" + strings.ToLower(v[at+1:])
}

// HashMessageID is the stable key stored in messages.message_id_hash and thread_refs.
func HashMessageID(normalized string) []byte {
	sum := sha256.Sum256([]byte(normalized))
	return sum[:]
}

const (
	outboundPrefix = "echoo."
	tagBytes       = 8
)

// idKey authenticates outbound Message-IDs. It is process-wide because Message-IDs are made in
// many places (replies, surveys, automations, campaigns) that share no common dependency.
var idKeys atomic.Pointer[[][]byte]

// SetMessageIDKey installs the keys for outbound Message-IDs: the first tags new ids, all of
// them verify. Customers keep replying to old mail for years, so after a key rotation the
// previous keys must stay in the list (keyring.DeriveAll). It must be called once at startup,
// before any message is queued or ingested.
func SetMessageIDKey(keys ...[]byte) {
	stored := make([][]byte, len(keys))
	for i, k := range keys {
		stored[i] = slices.Clone(k)
	}
	idKeys.Store(&stored)
}

// idTags returns the tag of each configured key, the signing key's first.
func idTags(convHex, randHex string) ([]string, error) {
	keys := idKeys.Load()
	if keys == nil || len(*keys) == 0 {
		return nil, errors.New("message id key is not configured")
	}
	tags := make([]string, len(*keys))
	for i, key := range *keys {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(convHex + "." + randHex))
		tags[i] = hex.EncodeToString(mac.Sum(nil)[:tagBytes])
	}
	return tags, nil
}

// NewOutboundMessageID returns a Message-ID (without angle brackets) that carries the
// conversation ID and an HMAC tag, so replies from clients that drop References still thread
// correctly while a sender cannot forge the token for a conversation they only know by ID.
func NewOutboundMessageID(conversationID [16]byte, domain string) (string, error) {
	var r [8]byte
	if _, err := rand.Read(r[:]); err != nil {
		return "", err
	}
	convHex, randHex := hex.EncodeToString(conversationID[:]), hex.EncodeToString(r[:])
	tags, err := idTags(convHex, randHex)
	if err != nil {
		return "", err
	}
	return outboundPrefix + convHex + "." + randHex + "." + tags[0] + "@" + strings.ToLower(domain), nil
}

// ConversationFromMessageID extracts the conversation ID from a Message-ID produced by
// NewOutboundMessageID, and only when its tag verifies. Ids without a valid tag, including
// those from before tagging existed, are not tokens. Callers must still check that the
// conversation belongs to the mailbox.
func ConversationFromMessageID(normalized string) ([16]byte, bool) {
	var id [16]byte
	local, _, ok := strings.Cut(normalized, "@")
	if !ok || !strings.HasPrefix(local, outboundPrefix) {
		return id, false
	}
	parts := strings.Split(strings.TrimPrefix(local, outboundPrefix), ".")
	if len(parts) != 3 || len(parts[0]) != 32 || len(parts[1]) != 16 {
		return id, false
	}
	tags, err := idTags(parts[0], parts[1])
	if err != nil {
		return id, false
	}
	verified := 0
	for _, want := range tags {
		verified |= subtle.ConstantTimeCompare([]byte(want), []byte(parts[2]))
	}
	if verified != 1 {
		return id, false
	}
	if _, err := hex.Decode(id[:], []byte(parts[0])); err != nil {
		return id, false
	}
	return id, true
}
