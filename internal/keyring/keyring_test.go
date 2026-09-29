package keyring

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func newKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestRoundTripAndRotation(t *testing.T) {
	k1 := "k1:" + newKey(t)
	old, err := Parse(k1)
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD("users", "totp_secret_enc", "u1")
	ct, err := old.Encrypt([]byte("hunter2"), aad)
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := Parse("k2:" + newKey(t) + "," + k1)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := rotated.Decrypt(ct, aad)
	if err != nil || string(pt) != "hunter2" {
		t.Fatalf("decrypt with old key: %q %v", pt, err)
	}
	if !rotated.NeedsRotation(ct) {
		t.Fatal("value on k1 should need rotation")
	}
	ct2, err := rotated.Encrypt(pt, aad)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.NeedsRotation(ct2) {
		t.Fatal("value on active key should not need rotation")
	}
}

func TestDecryptRejectsTampering(t *testing.T) {
	k, err := Parse("k1:" + newKey(t))
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD("users", "totp_secret_enc", "u1")
	ct, err := k.Encrypt([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}

	flipped := bytes.Clone(ct)
	flipped[len(flipped)-1] ^= 1
	cases := map[string]struct {
		ct  []byte
		aad []byte
	}{
		"other row":   {ct, AAD("users", "totp_secret_enc", "u2")},
		"flipped bit": {flipped, aad},
		"truncated":   {ct[:10], aad},
		"empty":       {nil, aad},
	}
	for name, c := range cases {
		if _, err := k.Decrypt(c.ct, c.aad); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: expected ErrDecrypt, got %v", name, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, spec := range []string{"", "k1", "K1:" + newKey(t), "k1:short", "k1:" + newKey(t) + ",k1:" + newKey(t)} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("expected error for %q", spec)
		}
	}
}

func TestDeriveIsStableAndPurposeBound(t *testing.T) {
	k, err := Parse("a:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.Derive("x"), k.Derive("x")) || bytes.Equal(k.Derive("x"), k.Derive("y")) {
		t.Fatal("Derive must be deterministic and differ per purpose")
	}
	other, err := Parse("a:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k.Derive("x"), other.Derive("x")) {
		t.Fatal("Derive must depend on the key")
	}
}

func TestDeriveAllKeepsKeysOfBeforeARotation(t *testing.T) {
	oldKey, newKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	before, err := Parse("old:" + oldKey)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := Parse("new:" + newKey + ",old:" + oldKey)
	if err != nil {
		t.Fatal(err)
	}
	all := rotated.DeriveAll("x")
	if len(all) != 2 || !bytes.Equal(all[0], rotated.Derive("x")) || !bytes.Equal(all[1], before.Derive("x")) {
		t.Fatal("DeriveAll must list the active key first and keep the previous keys")
	}
	if bytes.Equal(rotated.Derive("x"), before.Derive("x")) {
		t.Fatal("rotating must change what new signatures use")
	}
}
