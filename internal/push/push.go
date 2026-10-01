// Package push delivers notifications to phones and browsers: Web Push (RFC 8030 with VAPID,
// self-hosted, no account needed) for the installed web app, and APNs and FCM for the native
// iOS and Android apps when their credentials are configured.
package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/netguard"
)

// ttl is how long a push service keeps an undelivered message. Older news is stale: the app
// shows the notification list anyway when it opens.
const ttl = time.Hour

const (
	KindWebPush = "webpush"
	KindAPNs    = "apns"
	KindFCM     = "fcm"
)

// ErrGone means the device unsubscribed or uninstalled; its registration is dropped.
var ErrGone = errors.New("push device is gone")

// Message is what a device shows. It is built from notification metadata only, never from
// message text.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// URL is the app path the notification opens.
	URL string `json:"url"`
	// Tag groups notifications about one conversation, so a newer one replaces the older one.
	Tag string `json:"tag"`
}

// httpClient refuses internal addresses: Web Push endpoints come from clients, and the native
// services are public hosts anyway.
func httpClient() *http.Client {
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: netguard.Dialer(false, 10*time.Second).DialContext, ForceAttemptHTTP2: true},
		// A push service never redirects; following one would leave the allowlist.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// vapidAAD binds the encrypted VAPID key to its row.
var vapidAAD = keyring.AAD("push_vapid", "private_key_enc", "singleton")

// LoadWebPush returns the installation's Web Push sender, making its VAPID key pair the first
// time. subject is the base URL, which push services use to reach the operator.
func LoadWebPush(ctx context.Context, q *dbq.Queries, keys *keyring.Keyring, subject string) (*WebPush, error) {
	row, err := q.PushGetVapid(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := createVapid(ctx, q, keys); err != nil {
			return nil, err
		}
		row, err = q.PushGetVapid(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("load VAPID key: %w", err)
	}
	raw, err := keys.Decrypt(row.PrivateKeyEnc, vapidAAD)
	if err != nil {
		return nil, fmt.Errorf("decrypt VAPID key: %w", err)
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("parse VAPID key: %w", err)
	}
	return &WebPush{client: httpClient(), key: key, publicKey: row.PublicKey, subject: subject, now: time.Now}, nil
}

func createVapid(ctx context.Context, q *dbq.Queries, keys *keyring.Keyring) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	priv, err := key.Bytes()
	if err != nil {
		return err
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return err
	}
	enc, err := keys.Encrypt(priv, vapidAAD)
	if err != nil {
		return err
	}
	if err := q.PushInsertVapid(ctx, dbq.PushInsertVapidParams{PublicKey: pub, PrivateKeyEnc: enc}); err != nil {
		return fmt.Errorf("store VAPID key: %w", err)
	}
	return nil
}

// Options are the native push credentials; the zero value turns native push off.
type Options struct {
	APNs *APNsConfig
	// FCMCredentials is the Firebase service account key JSON, or empty.
	FCMCredentials string
}

// NewSenders prepares every configured push service. baseURL identifies the operator to Web
// Push services, which only accept an https URL or a mailto address.
func NewSenders(ctx context.Context, q *dbq.Queries, keys *keyring.Keyring, baseURL *url.URL, o Options) (Senders, error) {
	subject := baseURL.String()
	if baseURL.Scheme != "https" {
		subject = "mailto:postmaster@" + baseURL.Hostname()
	}
	web, err := LoadWebPush(ctx, q, keys, subject)
	if err != nil {
		return Senders{}, err
	}
	s := Senders{Web: web}
	if o.APNs != nil {
		if s.APNs, err = NewAPNs(*o.APNs); err != nil {
			return Senders{}, err
		}
	}
	if o.FCMCredentials != "" {
		if s.FCM, err = NewFCM(o.FCMCredentials); err != nil {
			return Senders{}, err
		}
	}
	return s, nil
}
