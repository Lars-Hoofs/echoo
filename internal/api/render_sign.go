package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// renderURLTTL is how long a signed attachment or proxy URL stays valid. The rendered document
// is fetched fresh on every view, so a short lifetime costs nothing.
const renderURLTTL = time.Hour

var errNoSigningKey = errors.New("render URLs need an encryption keyring")

// The iframe is sandboxed without allow-same-origin, so requests from inside it are cross-site
// and carry no session cookie (SameSite=Lax). Subresources therefore authenticate with a
// signature that is only issued after the message render itself passed the mailbox scope check.
func (s *Server) renderSignature(purpose, subject string, exp int64) (string, error) {
	if s.keys == nil {
		return "", errNoSigningKey
	}
	mac := hmac.New(sha256.New, s.keys.Derive("render-url"))
	mac.Write([]byte(purpose + "\x00" + subject + "\x00" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifyRenderSignature(purpose, subject string, q url.Values) bool {
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want, err := s.renderSignature(purpose, subject, exp)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(q.Get("sig")))
}

func (s *Server) signedAttachmentURL(id string) (string, error) {
	exp := time.Now().Add(renderURLTTL).Unix()
	sig, err := s.renderSignature("attachment", id, exp)
	if err != nil {
		return "", err
	}
	return "/render/attachments/" + id + "?exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig, nil
}

func (s *Server) signedProxyURL(remote string) (string, error) {
	exp := time.Now().Add(renderURLTTL).Unix()
	sig, err := s.renderSignature("proxy", remote, exp)
	if err != nil {
		return "", err
	}
	return "/render/proxy?url=" + url.QueryEscape(remote) + "&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + sig, nil
}

// senderAllowed reports whether addr or its domain is on the allowlist.
func senderAllowed(patterns []string, addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		return false
	}
	for _, p := range patterns {
		if p == addr || p == addr[at:] {
			return true
		}
	}
	return false
}
