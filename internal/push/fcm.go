package push

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	fcmScope    = "https://www.googleapis.com/auth/firebase.messaging"
	googleToken = "https://oauth2.googleapis.com/token" //nolint:gosec // a public endpoint URL, not a credential
	// androidChannel is the notification channel the Android app creates for these pushes.
	androidChannel = "echoo_urgent"
)

// FCM sends to the native Android app through Firebase Cloud Messaging (HTTP v1).
type FCM struct {
	client   *http.Client
	api      string
	tokenURL string
	project  string
	email    string
	key      *rsa.PrivateKey
	now      func() time.Time

	mu      sync.Mutex
	access  string
	expires time.Time
}

// NewFCM reads a Firebase service account key (the JSON file from the Firebase console).
func NewFCM(credentialsJSON string) (*FCM, error) {
	var sa struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(credentialsJSON), &sa); err != nil {
		return nil, fmt.Errorf("FCM credentials: %w", err)
	}
	if sa.Type != "service_account" || sa.ProjectID == "" || sa.ClientEmail == "" {
		return nil, errors.New("FCM credentials must be a service account key with project_id and client_email")
	}
	// The token endpoint is fixed: a key file that points elsewhere would send the signed
	// assertion to a third party.
	if sa.TokenURI != "" && sa.TokenURI != googleToken {
		return nil, fmt.Errorf("FCM credentials: unexpected token_uri %q", sa.TokenURI)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("FCM credentials: private_key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("FCM credentials: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("FCM credentials: private_key is not an RSA key")
	}
	return &FCM{
		client: httpClient(), api: "https://fcm.googleapis.com", tokenURL: googleToken,
		project: sa.ProjectID, email: sa.ClientEmail, key: key, now: time.Now,
	}, nil
}

// accessToken exchanges a signed assertion for an OAuth token and keeps it until shortly
// before it expires.
func (f *FCM) accessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if f.access != "" && now.Before(f.expires.Add(-5*time.Minute)) {
		return f.access, nil
	}
	claims, err := json.Marshal(map[string]any{
		"iss": f.email, "scope": fcmScope, "aud": googleToken,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	input := b64([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64(claims)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {input + "." + b64(sig)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		return "", fmt.Errorf("FCM token request answered %d", resp.StatusCode)
	}
	f.access, f.expires = tok.AccessToken, now.Add(time.Duration(tok.ExpiresIn)*time.Second)
	return f.access, nil
}

// Send delivers m to one registration token.
func (f *FCM) Send(ctx context.Context, deviceToken string, m Message) error {
	access, err := f.accessToken(ctx)
	if err != nil {
		return err
	}
	msg := map[string]any{"message": map[string]any{
		"token":        deviceToken,
		"notification": map[string]string{"title": m.Title, "body": m.Body},
		"data":         map[string]string{"url": m.URL, "tag": m.Tag},
		"android": map[string]any{
			"priority":     "HIGH",
			"ttl":          fmt.Sprintf("%ds", int(ttl.Seconds())),
			"collapse_key": m.Tag,
			"notification": map[string]string{"channel_id": androidChannel, "tag": m.Tag},
		},
	}}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.api+"/v1/projects/"+url.PathEscape(f.project)+"/messages:send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// The access token was revoked early; get a new one next time.
		f.mu.Lock()
		f.access = ""
		f.mu.Unlock()
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
	// FCM reports a token that no longer exists as 404, or as 400 with UNREGISTERED.
	if resp.StatusCode == http.StatusNotFound || bytes.Contains(raw, []byte("UNREGISTERED")) {
		return ErrGone
	}
	return fmt.Errorf("FCM answered %d", resp.StatusCode)
}
