package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEncryptMatchesRFC8291 checks the example of RFC 8291, Appendix A, byte for byte.
func TestEncryptMatchesRFC8291(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(unb64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := unb64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := unb64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := unb64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"), uaPublic, auth, asPriv, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64(got) != want {
		t.Fatalf("body =\n%s\nwant\n%s", b64(got), want)
	}
}

// decrypt is the browser's side of encrypt, to check a round trip with fresh keys.
func decrypt(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs != recordSize {
		t.Fatalf("rs = %d", rs)
	}
	asPublic := body[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ua.ECDH(as)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), asPublic...)
	ikm, _ := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 0x02 {
		t.Fatalf("missing last-record delimiter: %x", plain)
	}
	return plain[:len(plain)-1]
}

func TestEncryptRoundTrip(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	as, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	msg := []byte(`{"title":"Nieuw antwoord","body":"#12 · Warmtepomp"}`)
	body, err := encrypt(msg, ua.PublicKey().Bytes(), auth, as, make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, body, ua, auth); string(got) != string(msg) {
		t.Fatalf("decrypted %q", got)
	}
	if _, err := encrypt(msg, []byte("short"), auth, as, make([]byte, 16)); err == nil {
		t.Error("a malformed p256dh key was accepted")
	}
	if _, err := encrypt(msg, ua.PublicKey().Bytes(), auth[:8], as, make([]byte, 16)); err == nil {
		t.Error("a short auth secret was accepted")
	}
}

func TestVapidHeaderVerifies(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, _ := key.PublicKey.Bytes()
	now := time.Unix(1_800_000_000, 0)
	h, err := vapidHeader(key, pub, "https://fcm.googleapis.com/fcm/send/abc", "https://support.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	token, k, ok := strings.Cut(strings.TrimPrefix(h, "vapid t="), ", k=")
	if !ok || k != b64(pub) {
		t.Fatalf("header = %s", h)
	}
	parts := strings.Split(token, ".")
	var claims map[string]any
	if err := json.Unmarshal(unb64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "https://fcm.googleapis.com" || claims["sub"] != "https://support.example.com" || claims["exp"] != float64(now.Add(12*time.Hour).Unix()) {
		t.Errorf("claims = %v", claims)
	}
	sig := unb64(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("signature does not verify")
	}
}

func TestValidWebPushEndpoint(t *testing.T) {
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/QFx",
		"https://wns2-db5p.notify.windows.com/w/?token=x",
	} {
		if err := ValidWebPushEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://fcm.googleapis.com/fcm/send/abc",
		"https://evil.example.com/push",
		"https://fcm.googleapis.com.evil.example/x",
		"https://fcm.googleapis.com:8443/x",
		"https://user:pw@fcm.googleapis.com/x",
		"https://127.0.0.1/x",
		"not a url",
	} {
		if err := ValidWebPushEndpoint(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
