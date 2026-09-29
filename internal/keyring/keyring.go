// Package keyring encrypts credentials at rest with AES-256-GCM and a rotatable set of keys.
package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const formatVersion = 1

var keyIDPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// ErrDecrypt is returned for any ciphertext that cannot be authenticated. The cause is not
// exposed to avoid turning decryption into an oracle.
var ErrDecrypt = errors.New("keyring: cannot decrypt value")

// Keyring holds all known keys. The first key in the configuration encrypts; all keys decrypt.
type Keyring struct {
	activeID string
	// raws holds every raw key in configuration order; the first one is the active key.
	raws [][]byte
	keys map[string]cipher.AEAD
}

// Parse parses "id:base64key,id:base64key". Each key must be 32 bytes.
func Parse(spec string) (*Keyring, error) {
	k := &Keyring{keys: map[string]cipher.AEAD{}}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, b64, ok := strings.Cut(entry, ":")
		if !ok || !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("encryption key entries must look like id:base64 with id [a-z0-9]{1,16}")
		}
		if _, dup := k.keys[id]; dup {
			return nil, fmt.Errorf("encryption key id %q is listed twice", id)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("encryption key %q must be 32 bytes, base64 encoded", id)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		k.keys[id] = aead
		k.raws = append(k.raws, raw)
		if k.activeID == "" {
			k.activeID = id
		}
	}
	if k.activeID == "" {
		return nil, errors.New("no encryption keys configured")
	}
	return k, nil
}

// Derive returns a 32-byte key for a named purpose (e.g. URL signing), derived from the
// active key with HMAC-SHA256 so the encryption key itself is never used for anything else.
// It changes when the active key is rotated, so anything that must outlive a rotation (mailed
// links, Message-IDs in customers' mailboxes) has to be checked against DeriveAll instead.
func (k *Keyring) Derive(purpose string) []byte { return derive(k.raws[0], purpose) }

// DeriveAll returns the key Derive would give for every configured key, the active one first.
// Sign with the first and verify against all of them, so signatures made before a rotation
// keep working until their key is removed from the configuration.
func (k *Keyring) DeriveAll(purpose string) [][]byte {
	out := make([][]byte, len(k.raws))
	for i, raw := range k.raws {
		out[i] = derive(raw, purpose)
	}
	return out
}

func derive(raw []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte("echoo/derive/" + purpose))
	return mac.Sum(nil)
}

// AAD binds a ciphertext to the column and row it belongs to, so it cannot be copied elsewhere.
func AAD(table, column, rowID string) []byte {
	return []byte(table + "." + column + "." + rowID)
}

// Encrypt returns version || len(id) || id || nonce || ciphertext.
func (k *Keyring) Encrypt(plaintext, aad []byte) ([]byte, error) {
	aead := k.keys[k.activeID]
	out := make([]byte, 0, 2+len(k.activeID)+aead.NonceSize()+len(plaintext)+aead.Overhead())
	out = append(out, formatVersion, byte(len(k.activeID))) //nolint:gosec // key IDs are at most 16 bytes (keyIDPattern)
	out = append(out, k.activeID...)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

func (k *Keyring) Decrypt(ciphertext, aad []byte) ([]byte, error) {
	id, rest, err := splitHeader(ciphertext)
	if err != nil {
		return nil, ErrDecrypt
	}
	aead, ok := k.keys[id]
	if !ok || len(rest) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, sealed := rest[:aead.NonceSize()], rest[aead.NonceSize():]
	pt, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// ActiveID is the id of the key that encrypts new values.
func (k *Keyring) ActiveID() string { return k.activeID }

// IDs lists every configured key id, sorted.
func (k *Keyring) IDs() []string {
	ids := make([]string, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// NeedsRotation reports whether the value was encrypted with a key other than the active one.
func (k *Keyring) NeedsRotation(ciphertext []byte) bool {
	id, _, err := splitHeader(ciphertext)
	return err != nil || id != k.activeID
}

func splitHeader(b []byte) (id string, rest []byte, err error) {
	if len(b) < 2 || b[0] != formatVersion {
		return "", nil, ErrDecrypt
	}
	n := int(b[1])
	if len(b) < 2+n {
		return "", nil, ErrDecrypt
	}
	return string(b[2 : 2+n]), b[2+n:], nil
}
