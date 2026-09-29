// Package campaigns sends one-off email campaigns to contact segments: it resolves the
// recipients, paces the sending through the normal send queue, and handles unsubscribing.
package campaigns

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
	// TokenTTL is how long the unsubscribe link in a campaign mail keeps working. It is long
	// on purpose: people open old mail, and an expired link is a reason to mark it as spam.
	TokenTTL = 2 * 365 * 24 * time.Hour

	tokenBody = 16 + 8
	tokenLen  = tokenBody + sha256.Size
)

var (
	// ErrInvalidToken means the token is malformed or its signature does not match.
	ErrInvalidToken = errors.New("invalid unsubscribe token")
	// ErrExpiredToken means the token is genuine but past its expiry.
	ErrExpiredToken = errors.New("unsubscribe token expired")
)

// An unsubscribe token is recipient id || expiry || HMAC-SHA256 under the recipient's own
// secret, base64url encoded. The id in it only says whose secret to check; nothing about a
// contact can be guessed or enumerated from it.

func mac(secret pgtype.UUID, body []byte) []byte {
	m := hmac.New(sha256.New, secret.Bytes[:])
	m.Write(body)
	return m.Sum(nil)
}

func issueToken(recipient, secret pgtype.UUID, expires time.Time) string {
	buf := make([]byte, 0, tokenLen)
	buf = append(buf, recipient.Bytes[:]...)
	buf = binary.BigEndian.AppendUint64(buf, uint64(expires.Unix())) //nolint:gosec // a Unix time after 1970 is positive
	buf = append(buf, mac(secret, buf)...)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// parsedToken is a well-formed token whose signature has not been checked yet.
type parsedToken struct {
	recipient pgtype.UUID
	body, sig []byte
}

func parseToken(token string) (parsedToken, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenLen {
		return parsedToken{}, ErrInvalidToken
	}
	t := parsedToken{body: raw[:tokenBody], sig: raw[tokenBody:]}
	copy(t.recipient.Bytes[:], t.body[:16])
	t.recipient.Valid = true
	return t, nil
}

// verify checks the signature before the expiry, so a forged token never learns anything.
func (t parsedToken) verify(secret pgtype.UUID, now time.Time) error {
	if !hmac.Equal(t.sig, mac(secret, t.body)) {
		return ErrInvalidToken
	}
	if !now.Before(time.Unix(int64(binary.BigEndian.Uint64(t.body[16:])), 0)) { //nolint:gosec // only ever compared with now
		return ErrExpiredToken
	}
	return nil
}
