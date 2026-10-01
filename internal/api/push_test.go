package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"echoo/internal/push"
)

func withWebPush(t *testing.T, h *harness) {
	t.Helper()
	senders, err := push.NewSenders(context.Background(), h.q, h.keys, h.srv.cfg.BaseURL, push.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h.srv.push = senders
}

func browserKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(secret)
}

func TestPushConfigAndDevices(t *testing.T) {
	h := newHarness(t)
	withWebPush(t, h)
	c, user := h.loggedIn("agent")

	cfg := c.do("GET", "/api/v1/push/config", nil)
	expect(t, cfg, 200, "")
	key, _ := cfg.body["web_push_public_key"].(string)
	if raw, err := base64.RawURLEncoding.DecodeString(key); err != nil || len(raw) != 65 || cfg.body["apns"] != false || cfg.body["fcm"] != false {
		t.Fatalf("config = %v", cfg.body)
	}
	// The key pair is made once and kept.
	h.srv.push = push.Senders{}
	withWebPush(t, h)
	if again := c.do("GET", "/api/v1/push/config", nil); again.body["web_push_public_key"] != key {
		t.Fatal("the VAPID key changed between loads")
	}

	p256dh, auth := browserKeys(t)
	sub := map[string]any{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/fcm/send/abc", "keys": map[string]any{"p256dh": p256dh, "auth": auth}, "label": "Chrome op Android"}
	r := c.do("POST", "/api/v1/push/devices", sub)
	expect(t, r, 201, "")
	if r.body["current"] != true || r.body["label"] != "Chrome op Android" {
		t.Fatalf("device = %v", r.body)
	}
	// Subscribing the same browser again replaces the registration.
	expect(t, c.do("POST", "/api/v1/push/devices", sub), 201, "")
	// A label is optional.
	p2, a2 := browserKeys(t)
	unnamed := map[string]any{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/fcm/send/unnamed", "keys": map[string]any{"p256dh": p2, "auth": a2}}
	r = c.do("POST", "/api/v1/push/devices", unnamed)
	expect(t, r, 201, "")
	expect(t, c.do("DELETE", "/api/v1/push/devices/"+r.body["id"].(string), nil), 204, "")
	list := c.do("GET", "/api/v1/push/devices", nil)
	devices, _ := list.body["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["current"] != true {
		t.Fatalf("devices = %v", list.body)
	}

	// Another user sees none of it and cannot remove it.
	other, _ := h.loggedIn("agent")
	if d := other.do("GET", "/api/v1/push/devices", nil).body["devices"].([]any); len(d) != 0 {
		t.Fatalf("other user sees %v", d)
	}
	id := devices[0].(map[string]any)["id"].(string)
	expect(t, other.do("DELETE", "/api/v1/push/devices/"+id, nil), 404, "not_found")

	// Logging out removes the device registered by that session.
	expect(t, c.do("POST", "/api/v1/auth/logout", nil), 204, "")
	if n := h.count(`SELECT count(*) FROM push_devices WHERE user_id = $1`, user.ID); n != 0 {
		t.Fatalf("%d devices left after logout", n)
	}
}

func TestPushDeviceValidation(t *testing.T) {
	h := newHarness(t)
	withWebPush(t, h)
	c, _ := h.loggedIn("agent")
	p256dh, auth := browserKeys(t)
	cases := map[string]struct {
		body  map[string]any
		field string
	}{
		"unknown push service": {map[string]any{"kind": "webpush", "endpoint": "https://evil.example.com/p", "keys": map[string]any{"p256dh": p256dh, "auth": auth}}, "endpoint"},
		"internal address":     {map[string]any{"kind": "webpush", "endpoint": "https://127.0.0.1/p", "keys": map[string]any{"p256dh": p256dh, "auth": auth}}, "endpoint"},
		"bad p256dh":           {map[string]any{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/x", "keys": map[string]any{"p256dh": "abc", "auth": auth}}, "keys.p256dh"},
		"bad auth":             {map[string]any{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/x", "keys": map[string]any{"p256dh": p256dh, "auth": "abc"}}, "keys.auth"},
		"apns not configured":  {map[string]any{"kind": "apns", "endpoint": strings.Repeat("ab", 32)}, "kind"},
		"fcm not configured":   {map[string]any{"kind": "fcm", "endpoint": strings.Repeat("x", 40)}, "kind"},
		"unknown kind":         {map[string]any{"kind": "sms", "endpoint": "x"}, "kind"},
		"endpoint too long":    {map[string]any{"kind": "webpush", "endpoint": "https://fcm.googleapis.com/" + strings.Repeat("x", 2048), "keys": map[string]any{"p256dh": p256dh, "auth": auth}}, "endpoint"},
	}
	for name, tc := range cases {
		r := c.do("POST", "/api/v1/push/devices", tc.body)
		expect(t, r, 422, "validation_failed")
		if fieldsOf(r)[tc.field] == nil {
			t.Errorf("%s: fields = %v, want %s", name, fieldsOf(r), tc.field)
		}
	}
}

func TestPushPreferences(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")
	r := c.do("GET", "/api/v1/me/notification-settings", nil)
	expect(t, r, 200, "")
	p := obj(r, "push")
	if p["mentions"] != true || p["assignments"] != true || p["replies"] != true || p["sla"] != true {
		t.Fatalf("defaults = %v", p)
	}
	r = c.do("PUT", "/api/v1/me/notification-settings", map[string]any{
		"mentions": true, "assignments": false, "replies": false,
		"push": map[string]any{"mentions": true, "assignments": true, "replies": false, "sla": true},
	})
	expect(t, r, 200, "")
	if obj(r, "push")["replies"] != false || obj(r, "email")["assignments"] != false {
		t.Fatalf("saved = %v", r.body)
	}
	if got := obj(c.do("GET", "/api/v1/me/notification-settings", nil), "push"); got["replies"] != false || got["sla"] != true {
		t.Fatalf("reloaded = %v", got)
	}
	// Sending only push keeps the email choices.
	r = c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"push": map[string]any{"mentions": true, "assignments": true, "replies": false, "sla": false}})
	expect(t, r, 200, "")
	if obj(r, "email")["assignments"] != false || obj(r, "push")["sla"] != false {
		t.Fatalf("push-only save = %v", r.body)
	}
	// Leaving push out keeps the stored push choices.
	expect(t, c.do("PUT", "/api/v1/me/notification-settings", map[string]any{"mentions": true, "assignments": true}), 200, "")
	if got := obj(c.do("GET", "/api/v1/me/notification-settings", nil), "push"); got["replies"] != false {
		t.Fatalf("push reset by an email-only save: %v", got)
	}
}

func TestPushTestReportsPerDevice(t *testing.T) {
	h := newHarness(t)
	c, user := h.loggedIn("agent")
	// An iOS device while APNs is not configured on this server: the test says it failed.
	h.exec(`INSERT INTO push_devices (user_id, kind, endpoint) VALUES ($1, 'apns', 'abc')`, user.ID)
	r := c.do("POST", "/api/v1/push/test", nil)
	expect(t, r, 200, "")
	res := r.body["results"].([]any)
	if len(res) != 1 || res[0].(map[string]any)["ok"] != false || res[0].(map[string]any)["error"] != "failed" {
		t.Fatalf("results = %v", res)
	}
}
