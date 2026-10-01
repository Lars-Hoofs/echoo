package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// APNsConfig holds the token-based credentials from the Apple developer account.
type APNsConfig struct {
	// KeyPEM is the contents of the AuthKey_XXXX.p8 file.
	KeyPEM string
	KeyID  string
	TeamID string
	// Topic is the app's bundle identifier.
	Topic string
	// Sandbox sends to the development environment, for builds run from Xcode.
	Sandbox bool
}

// APNs sends to the native iOS app.
type APNs struct {
	client *http.Client
	base   string
	cfg    APNsConfig
	key    *ecdsa.PrivateKey
	now    func() time.Time

	mu       sync.Mutex
	token    string
	issuedAt time.Time
}

func NewAPNs(cfg APNsConfig) (*APNs, error) {
	block, _ := pem.Decode([]byte(cfg.KeyPEM))
	if block == nil {
		return nil, errors.New("APNs key is not a PEM file (the AuthKey .p8)")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("APNs key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("APNs key is not an EC key")
	}
	base := "https://api.push.apple.com"
	if cfg.Sandbox {
		base = "https://api.sandbox.push.apple.com"
	}
	return &APNs{client: httpClient(), base: base, cfg: cfg, key: key, now: time.Now}, nil
}

// providerToken returns the signed token Apple wants on every request. Apple refuses tokens
// older than an hour and throttles new ones, so it is reused for 50 minutes.
func (a *APNs) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.token != "" && now.Sub(a.issuedAt) < 50*time.Minute {
		return a.token, nil
	}
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": a.cfg.KeyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": a.cfg.TeamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	token, err := signES256WithHeader(a.key, header, claims)
	if err != nil {
		return "", err
	}
	a.token, a.issuedAt = token, now
	return token, nil
}

type apnsPayload struct {
	APS struct {
		Alert struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"alert"`
		Sound    string `json:"sound"`
		ThreadID string `json:"thread-id,omitempty"`
	} `json:"aps"`
	URL string `json:"url"`
}

// Send delivers m to one device token.
func (a *APNs) Send(ctx context.Context, deviceToken string, m Message) error {
	var p apnsPayload
	p.APS.Alert.Title, p.APS.Alert.Body = m.Title, m.Body
	p.APS.Sound, p.APS.ThreadID, p.URL = "default", m.Tag, m.URL
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	token, err := a.providerToken()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/3/device/"+url.PathEscape(deviceToken), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("apns-topic", a.cfg.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", strconv.FormatInt(a.now().Add(ttl).Unix(), 10))
	if m.Tag != "" {
		req.Header.Set("apns-collapse-id", m.Tag)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var reason struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&reason)
	// Only an uninstalled app is gone. DeviceTokenNotForTopic and BadDeviceToken also follow from
	// a wrong ECHOO_APNS_TOPIC or ECHOO_APNS_ENVIRONMENT, and dropping devices for those would wipe
	// every iOS registration over a configuration mistake.
	if resp.StatusCode == http.StatusGone || reason.Reason == "Unregistered" {
		return ErrGone
	}
	return fmt.Errorf("APNs answered %d %s", resp.StatusCode, reason.Reason)
}
