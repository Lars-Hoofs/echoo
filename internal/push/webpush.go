package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// recordSize is the rs field of the aes128gcm header; one record holds any payload we send.
	recordSize = 4096
	// maxBody is the largest encrypted body push services accept (RFC 8030 section 7.2).
	maxBody = 4096
)

// webPushHosts are the push services browsers hand out endpoints for. The endpoint comes from
// the client, so anything else is refused: it would make the server post to an address of the
// client's choosing.
var webPushHosts = []string{
	"fcm.googleapis.com",                // Chrome, Edge on Android, Samsung Internet
	"updates.push.services.mozilla.com", // Firefox
	"push.apple.com",                    // Safari on macOS and iOS (web.push.apple.com)
	"notify.windows.com",                // Edge on Windows
}

// ValidWebPushEndpoint checks an endpoint from a browser subscription.
func ValidWebPushEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return errors.New("endpoint must be an https URL without port or credentials")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range webPushHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("push service %s is not supported", host)
}

// encrypt seals plaintext for one browser as an aes128gcm body (RFC 8291 and RFC 8188).
// uaPublic is the subscription's p256dh key, authSecret its auth secret. The ephemeral key and
// salt are parameters so the RFC's test vector can be checked.
func encrypt(plaintext, uaPublic, authSecret []byte, ephemeral *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("p256dh key: %w", err)
	}
	if len(authSecret) != 16 {
		return nil, errors.New("auth secret must be 16 bytes")
	}
	shared, err := ephemeral.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPublic := ephemeral.PublicKey().Bytes()
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	ikm, err := hkdf.Key(sha256.New, shared, authSecret, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// 0x02 marks the last (and only) record.
	sealed := gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 0x02), nil)
	// Push services refuse a body over 4096 bytes: the header (salt, rs, idlen, key) counts too.
	if len(salt)+4+1+len(asPublic)+len(sealed) > maxBody {
		return nil, errors.New("payload too large for a push message")
	}

	var body bytes.Buffer
	body.Write(salt)
	_ = binary.Write(&body, binary.BigEndian, uint32(recordSize))
	body.WriteByte(byte(len(asPublic))) //nolint:gosec // an uncompressed P-256 key is always 65 bytes
	body.Write(asPublic)
	body.Write(sealed)
	return body.Bytes(), nil
}

// vapidHeader signs a VAPID JWT (RFC 8292) for the origin of endpoint.
func vapidHeader(key *ecdsa.PrivateKey, publicKey []byte, endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	if err != nil {
		return "", err
	}
	token, err := signES256(key, claims)
	if err != nil {
		return "", err
	}
	return "vapid t=" + token + ", k=" + b64(publicKey), nil
}

// signES256 makes a compact JWT with the fixed ES256 header.
func signES256(key *ecdsa.PrivateKey, claims []byte) (string, error) {
	return signES256WithHeader(key, []byte(`{"typ":"JWT","alg":"ES256"}`), claims)
}

func signES256WithHeader(key *ecdsa.PrivateKey, header, claims []byte) (string, error) {
	signingInput := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + b64(sig), nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// WebPush sends to browser subscriptions.
type WebPush struct {
	client    *http.Client
	key       *ecdsa.PrivateKey
	publicKey []byte
	// subject identifies the sender to push services: the installation's base URL.
	subject string
	now     func() time.Time
}

// PublicKey is the application server key browsers subscribe with.
func (w *WebPush) PublicKey() []byte { return w.publicKey }

// Send delivers payload to one subscription. ErrGone means the subscription no longer exists.
func (w *WebPush) Send(ctx context.Context, endpoint string, p256dh, auth, payload []byte) error {
	if err := ValidWebPushEndpoint(endpoint); err != nil {
		return err
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	body, err := encrypt(payload, p256dh, auth, ephemeral, salt)
	if err != nil {
		return err
	}
	authz, err := vapidHeader(w.key, w.publicKey, endpoint, w.subject, w.now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", authz)
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 300:
		return fmt.Errorf("push service answered %d", resp.StatusCode)
	}
	return nil
}
