// Package csat sends customer satisfaction surveys after a conversation is resolved and records
// the answers. The survey page is public, so everything it accepts comes from a signed token.
package csat

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	// TokenTTL is how long the links in the survey email work.
	TokenTTL = 30 * 24 * time.Hour
	// ChangeWindow is how long after the first answer the customer can still change it.
	ChangeWindow = 7 * 24 * time.Hour

	tokenLen = 16 + 8 + sha256.Size
)

var (
	// ErrInvalid means the token is malformed or its signature does not match.
	ErrInvalid = errors.New("invalid survey token")
	// ErrExpired means the token is genuine but past its expiry.
	ErrExpired = errors.New("survey token expired")
)

// Signer issues and verifies survey tokens. A token carries the conversation id and an expiry
// under an HMAC, so nothing about a conversation can be guessed or enumerated from it.
type Signer struct{ keys [][]byte }

// NewSigner returns a Signer that signs with the first key and accepts tokens made with any of
// them. The links live for 30 days, so after a key rotation the previous keys must stay in the
// list (keyring.DeriveAll). The keys must be secret and stable across restarts.
func NewSigner(keys ...[]byte) *Signer { return &Signer{keys: keys} }

func mac(key []byte, purpose string, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write(data)
	return m.Sum(nil)
}

func (s *Signer) mac(purpose string, data []byte) []byte { return mac(s.keys[0], purpose, data) }

// valid reports whether sig is the MAC of data under any of the keys.
func (s *Signer) valid(purpose string, data, sig []byte) bool {
	ok := false
	for _, key := range s.keys {
		ok = hmac.Equal(sig, mac(key, purpose, data)) || ok
	}
	return ok
}

// Issue returns the token for conversation, valid until expires (second precision).
func (s *Signer) Issue(conversation pgtype.UUID, expires time.Time) string {
	buf := make([]byte, 0, tokenLen)
	buf = append(buf, conversation.Bytes[:]...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(expires.Unix())) //nolint:gosec // expiry dates are after 1970
	buf = append(buf, s.mac("token", buf)...)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Verify checks the signature first and the expiry second, so a forged token never learns
// whether the conversation exists.
func (s *Signer) Verify(token string, now time.Time) (pgtype.UUID, time.Time, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenLen {
		return pgtype.UUID{}, time.Time{}, ErrInvalid
	}
	body, sig := raw[:16+8], raw[16+8:]
	if !s.valid("token", body, sig) {
		return pgtype.UUID{}, time.Time{}, ErrInvalid
	}
	var conversation pgtype.UUID
	copy(conversation.Bytes[:], body[:16])
	conversation.Valid = true
	expires := time.Unix(int64(binary.BigEndian.Uint64(body[16:])), 0) //nolint:gosec // written by Issue from an int64
	if !now.Before(expires) {
		return pgtype.UUID{}, time.Time{}, ErrExpired
	}
	return conversation, expires, nil
}

// CSRF is the value a survey form must echo back. It is bound to the token, so a form built
// for one survey cannot be posted for another, and a third-party page cannot know it.
func (s *Signer) CSRF(token string) string {
	return base64.RawURLEncoding.EncodeToString(s.mac("csrf", []byte(token)))
}

// CheckCSRF compares in constant time.
func (s *Signer) CheckCSRF(token, value string) bool {
	sig, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && s.valid("csrf", []byte(token), sig)
}

// Hash is what is stored of a token.
func Hash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
