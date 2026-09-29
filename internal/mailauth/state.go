package mailauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"echoo/internal/keyring"
)

// StateTTL is how long an authorization attempt may take, from clicking the button to the
// provider's redirect back.
const StateTTL = 10 * time.Minute

var (
	ErrStateInvalid = errors.New("oauth state is invalid")
	ErrStateExpired = errors.New("oauth state has expired")
)

var stateAAD = []byte("mailauth.state")

// State is what the callback must know about the authorization attempt it completes.
type State struct {
	Provider string `json:"p"`
	// SessionID binds the attempt to the browser session that started it.
	SessionID string `json:"s"`
	// Verifier is the PKCE code verifier. The state travels through the provider and the
	// browser's address bar, so it is encrypted, not merely signed: a party that sees the
	// redirect must not learn the verifier.
	Verifier string `json:"v"`
	// MailboxID is set when an existing mailbox is being reconnected.
	MailboxID string `json:"m,omitempty"`
	// Name is the name for a new mailbox.
	Name    string `json:"n,omitempty"`
	Expires int64  `json:"e"`
}

// SealState encrypts and authenticates s for use as the OAuth state parameter.
func SealState(keys *keyring.Keyring, s State, now time.Time) (string, error) {
	s.Expires = now.Add(StateTTL).Unix()
	raw, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal oauth state: %w", err)
	}
	sealed, err := keys.Encrypt(raw, stateAAD)
	if err != nil {
		return "", fmt.Errorf("seal oauth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// OpenState verifies and decrypts a state parameter. It does not check the session or the
// provider; the caller compares those with the request.
func OpenState(keys *keyring.Keyring, param string, now time.Time) (State, error) {
	var s State
	sealed, err := base64.RawURLEncoding.DecodeString(param)
	if err != nil {
		return s, ErrStateInvalid
	}
	raw, err := keys.Decrypt(sealed, stateAAD)
	if err != nil {
		return s, ErrStateInvalid
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, ErrStateInvalid
	}
	if now.Unix() >= s.Expires {
		return s, ErrStateExpired
	}
	return s, nil
}
