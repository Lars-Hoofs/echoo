package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	MinPasswordLength = 12
	MaxPasswordLength = 128
)

var (
	ErrPasswordTooShort = errors.New("password too short")
	ErrPasswordTooLong  = errors.New("password too long")
)

// ValidatePassword enforces length only; composition rules make passwords worse (NIST 800-63B).
func ValidatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < MinPasswordLength:
		return ErrPasswordTooShort
	case n > MaxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}

// Argon2Params follow the OWASP recommendation for argon2id (19 MiB, 2 passes, 1 lane).
type Argon2Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

var DefaultArgon2 = Argon2Params{MemoryKiB: 19 * 1024, Time: 2, Threads: 1}

// Hasher hashes passwords and caps how many hashes run at once, so a burst of logins cannot
// push memory far beyond the idle budget.
type Hasher struct {
	params Argon2Params
	sem    chan struct{}
	// dummy is verified against for unknown accounts so their timing matches real ones.
	dummy string
}

func NewHasher(params Argon2Params, maxConcurrent int) *Hasher {
	h := &Hasher{params: params, sem: make(chan struct{}, maxConcurrent)}
	d, err := h.hash("echoo-timing-equalizer")
	if err != nil {
		panic(fmt.Sprintf("auth: cannot initialize password hasher: %v", err))
	}
	h.dummy = d
	return h
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

func (h *Hasher) Hash(ctx context.Context, pw string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return h.hash(pw)
}

func (h *Hasher) hash(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := h.params
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks pw against an encoded hash. An empty encoded hash runs the dummy comparison
// and always fails, so callers can use it for unknown accounts.
func (h *Hasher) Verify(ctx context.Context, pw, encoded string) (ok bool, needsRehash bool, err error) {
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()

	unknown := encoded == ""
	if unknown {
		encoded = h.dummy
	}
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want))) //nolint:gosec // len(want) <= 64, checked in decodeHash
	match := subtle.ConstantTimeCompare(got, want) == 1
	if unknown {
		return false, false, nil
	}
	return match, match && p != h.params, nil
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("auth: unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errors.New("auth: unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return p, nil, nil, fmt.Errorf("auth: bad argon2 parameters: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, fmt.Errorf("auth: bad salt: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return p, nil, nil, errors.New("auth: bad hash")
	}
	return p, salt, key, nil
}
