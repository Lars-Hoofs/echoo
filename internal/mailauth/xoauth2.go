package mailauth

import (
	"errors"
	"strings"

	"github.com/emersion/go-sasl"
)

// NewXOAuth2Client returns the SASL client for the XOAUTH2 mechanism that Google and Microsoft
// use for IMAP and SMTP; go-sasl only ships the standardized OAUTHBEARER.
func NewXOAuth2Client(user, accessToken string) sasl.Client {
	return &xoauth2{user: user, token: accessToken}
}

type xoauth2 struct{ user, token string }

func (c *xoauth2) Start() (string, []byte, error) {
	if strings.ContainsAny(c.user+c.token, "\x00\x01\r\n") {
		return "", nil, errors.New("mailauth: XOAUTH2 credentials contain control characters")
	}
	return "XOAUTH2", []byte("user=" + c.user + "\x01auth=Bearer " + c.token + "\x01\x01"), nil
}

// Next answers the JSON error challenge a server sends when it rejects the token. The
// mechanism requires an empty response, after which the server ends the exchange with its
// failure reply, which carries the real error to the caller.
func (c *xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }
