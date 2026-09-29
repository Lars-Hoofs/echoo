package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// NewToken returns a 256-bit random token for the client and the SHA-256 hash to store.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

const recoveryCodeCount = 10

// NewRecoveryCodes returns codes formatted as xxxx-xxxx-xxxx-xxxx (80 bits each) and their
// hashes. 80 bits makes a plain SHA-256 hash safe against offline guessing, so verification
// does not need a slow hash per code.
func NewRecoveryCodes() (codes []string, hashes [][]byte, err error) {
	for range recoveryCodeCount {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, nil, err
		}
		raw := strings.ToLower(b32.EncodeToString(b))
		codes = append(codes, raw[0:4]+"-"+raw[4:8]+"-"+raw[8:12]+"-"+raw[12:16])
		hashes = append(hashes, HashRecoveryCode(raw))
	}
	return codes, hashes, nil
}

// HashRecoveryCode normalizes user input (case, dashes, spaces) before hashing.
func HashRecoveryCode(input string) []byte {
	norm := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(input)))
	sum := sha256.Sum256([]byte(norm))
	return sum[:]
}

// LooksLikeRecoveryCode distinguishes recovery codes from 6-digit TOTP codes.
func LooksLikeRecoveryCode(input string) bool {
	return len(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(input))) == 16
}
