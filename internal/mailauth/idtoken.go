package mailauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// AccountEmail reads the mailbox address from the id_token that the provider's token endpoint
// returned. The token was received directly from the endpoint over TLS in exchange for our
// client secret, so per OpenID Connect Core 3.1.3.7 its signature does not need checking; the
// audience is compared as a sanity check.
func AccountEmail(idToken, clientID string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", errors.New("id_token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("id_token payload is not base64")
	}
	var claims struct {
		Audience          audience `json:"aud"`
		Email             string   `json:"email"`
		EmailVerified     *bool    `json:"email_verified"`
		PreferredUsername string   `json:"preferred_username"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("id_token payload is not JSON")
	}
	if !claims.Audience.contains(clientID) {
		return "", errors.New("id_token audience does not match the client")
	}
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return "", errors.New("the provider reports the email address as unverified")
	}
	// Microsoft omits "email" for some account types; the sign-in name is the mailbox then.
	for _, v := range []string{claims.Email, claims.PreferredUsername} {
		if v = strings.ToLower(strings.TrimSpace(v)); strings.Contains(v, "@") {
			return v, nil
		}
	}
	return "", errors.New("id_token has no email address")
}

// audience decodes the aud claim, which is a string or a list of strings.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) contains(v string) bool {
	for _, x := range a {
		if x == v {
			return true
		}
	}
	return false
}
