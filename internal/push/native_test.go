package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testAPNs(t *testing.T, h http.HandlerFunc) *APNs {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	a, err := NewAPNs(APNsConfig{
		KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		KeyID:  "ABC123DEFG", TeamID: "TEAM123456", Topic: "nl.echoo.app",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a.client, a.base = srv.Client(), srv.URL
	return a
}

func TestAPNsRequest(t *testing.T) {
	var got *http.Request
	var body map[string]any
	a := testAPNs(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		_ = json.NewDecoder(r.Body).Decode(&body)
	})
	m := Message{Title: "Nieuw antwoord van de klant", Body: "Gesprek #12", URL: "/inbox/alle/x", Tag: "conversation-x"}
	if err := a.Send(t.Context(), "a1b2c3", m); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/3/device/a1b2c3" || got.Header.Get("apns-topic") != "nl.echoo.app" ||
		got.Header.Get("apns-push-type") != "alert" || got.Header.Get("apns-collapse-id") != "conversation-x" {
		t.Errorf("request = %s %v", got.URL, got.Header)
	}
	aps := body["aps"].(map[string]any)
	if aps["alert"].(map[string]any)["title"] != m.Title || body["url"] != m.URL {
		t.Errorf("body = %v", body)
	}

	// The provider token is an ES256 JWT with the key id and team, reused between requests.
	token := strings.TrimPrefix(got.Header.Get("Authorization"), "bearer ")
	parts := strings.Split(token, ".")
	var header, claims map[string]any
	_ = json.Unmarshal(unb64(t, parts[0]), &header)
	_ = json.Unmarshal(unb64(t, parts[1]), &claims)
	if header["kid"] != "ABC123DEFG" || header["alg"] != "ES256" || claims["iss"] != "TEAM123456" {
		t.Errorf("token header %v claims %v", header, claims)
	}
	sig := unb64(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&a.key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("provider token does not verify")
	}
	again, _ := a.providerToken()
	a.now = func() time.Time { return time.Now().Add(55 * time.Minute) }
	renewed, _ := a.providerToken()
	if again != token || renewed == token {
		t.Error("the provider token is not reused for 50 minutes and then renewed")
	}
}

func TestAPNsGoneDevices(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"410": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusGone) },
		"Unregistered": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"reason":"Unregistered"}`)
		},
	} {
		if err := testAPNs(t, h).Send(t.Context(), "x", Message{}); !errors.Is(err, ErrGone) {
			t.Errorf("%s: err = %v, want ErrGone", name, err)
		}
	}
	// A wrong topic or environment is a configuration error; the device must stay.
	for _, reason := range []string{"DeviceTokenNotForTopic", "BadDeviceToken"} {
		h := func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"reason":"`+reason+`"}`)
		}
		if err := testAPNs(t, h).Send(t.Context(), "x", Message{}); err == nil || errors.Is(err, ErrGone) {
			t.Errorf("%s: err = %v, want a plain error", reason, err)
		}
	}
	err := testAPNs(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }).Send(t.Context(), "x", Message{})
	if err == nil || errors.Is(err, ErrGone) {
		t.Errorf("a throttled request must be an error but not gone: %v", err)
	}
}

func TestNewAPNsRejectsBadKeys(t *testing.T) {
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(rsaKey)
	for name, pemText := range map[string]string{
		"not pem": "hello",
		"rsa":     string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	} {
		if _, err := NewAPNs(APNsConfig{KeyPEM: pemText}); err == nil {
			t.Errorf("%s key accepted", name)
		}
	}
}

func serviceAccount(t *testing.T, tokenURI string) (string, *rsa.PrivateKey) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "echoo-test", "client_email": "push@echoo-test.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": tokenURI,
	})
	return string(sa), key
}

func TestFCMRequest(t *testing.T) {
	tokenRequests := 0
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenRequests++
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || strings.Count(r.Form.Get("assertion"), ".") != 2 {
				t.Errorf("token form = %v", r.Form)
			}
			_, _ = io.WriteString(w, `{"access_token":"ya29.test","expires_in":3600}`)
		case "/v1/projects/echoo-test/messages:send":
			if r.Header.Get("Authorization") != "Bearer ya29.test" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewDecoder(r.Body).Decode(&sent)
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	}))
	defer srv.Close()
	creds, _ := serviceAccount(t, "")
	f, err := NewFCM(creds)
	if err != nil {
		t.Fatal(err)
	}
	f.client, f.api, f.tokenURL = srv.Client(), srv.URL, srv.URL+"/token"
	m := Message{Title: "SLA-termijn in gevaar", Body: "Gesprek #7", URL: "/inbox/alle/y", Tag: "conversation-y"}
	for range 2 {
		if err := f.Send(t.Context(), "fcm-token", m); err != nil {
			t.Fatal(err)
		}
	}
	if tokenRequests != 1 {
		t.Errorf("token requests = %d, want the access token reused", tokenRequests)
	}
	msg := sent["message"].(map[string]any)
	android := msg["android"].(map[string]any)
	if msg["token"] != "fcm-token" || msg["notification"].(map[string]any)["title"] != m.Title ||
		msg["data"].(map[string]any)["url"] != m.URL || android["priority"] != "HIGH" ||
		android["notification"].(map[string]any)["channel_id"] != androidChannel {
		t.Errorf("message = %v", sent)
	}
}

func TestFCMGoneAndCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = io.WriteString(w, `{"access_token":"t","expires_in":3600}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`)
	}))
	defer srv.Close()
	creds, _ := serviceAccount(t, googleToken)
	f, err := NewFCM(creds)
	if err != nil {
		t.Fatal(err)
	}
	f.client, f.api, f.tokenURL = srv.Client(), srv.URL, srv.URL+"/token"
	if err := f.Send(t.Context(), "old", Message{}); !errors.Is(err, ErrGone) {
		t.Errorf("err = %v, want ErrGone", err)
	}

	evil, _ := serviceAccount(t, "https://evil.example.com/token")
	if _, err := NewFCM(evil); err == nil {
		t.Error("a key file with a foreign token_uri was accepted")
	}
	if _, err := NewFCM(`{"type":"authorized_user"}`); err == nil {
		t.Error("a non service account key was accepted")
	}
}
