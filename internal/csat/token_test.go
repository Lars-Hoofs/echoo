package csat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func testSigner() *Signer { return NewSigner([]byte("0123456789abcdef0123456789abcdef")) }

func conv(b byte) pgtype.UUID {
	id := pgtype.UUID{Valid: true}
	for i := range id.Bytes {
		id.Bytes[i] = b
	}
	return id
}

func TestTokenRoundTrip(t *testing.T) {
	s := testSigner()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	token := s.Issue(conv(7), now.Add(TokenTTL))
	id, exp, err := s.Verify(token, now)
	if err != nil || id != conv(7) || !exp.Equal(now.Add(TokenTTL)) {
		t.Fatalf("%v %v %v", id, exp, err)
	}
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("token %q is not URL safe", token)
	}
}

func TestTokenRejectsTamperingAndOtherKeys(t *testing.T) {
	s := testSigner()
	now := time.Now()
	token := s.Issue(conv(1), now.Add(time.Hour))

	flipped := []byte(token)
	flipped[10] ^= 1
	other := NewSigner([]byte("another-key-another-key-another!"))
	for name, tok := range map[string]string{
		"flipped byte": string(flipped),
		"truncated":    token[:len(token)-2],
		"extended":     token + "AA",
		"empty":        "",
		"not base64":   "!!!!",
		"other key":    other.Issue(conv(1), now.Add(time.Hour)),
	} {
		if _, _, err := s.Verify(tok, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func TestTokenExpiry(t *testing.T) {
	s := testSigner()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	token := s.Issue(conv(1), now.Add(time.Hour))
	if _, _, err := s.Verify(token, now.Add(time.Hour-time.Second)); err != nil {
		t.Errorf("just before expiry: %v", err)
	}
	if _, _, err := s.Verify(token, now.Add(time.Hour)); !errors.Is(err, ErrExpired) {
		t.Errorf("at expiry: %v", err)
	}
}

func TestCSRFIsBoundToTheToken(t *testing.T) {
	s := testSigner()
	a, b := s.Issue(conv(1), time.Now().Add(time.Hour)), s.Issue(conv(2), time.Now().Add(time.Hour))
	if !s.CheckCSRF(a, s.CSRF(a)) {
		t.Error("own value rejected")
	}
	if s.CheckCSRF(a, s.CSRF(b)) || s.CheckCSRF(a, "") || s.CheckCSRF(a, a) {
		t.Error("foreign value accepted")
	}
}

func TestCleanComment(t *testing.T) {
	if got, err := CleanComment("  Netjes\r\nopgelost  "); err != nil || got != "Netjes\nopgelost" {
		t.Errorf("%q %v", got, err)
	}
	if _, err := CleanComment(strings.Repeat("é", MaxComment+1)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("too long: %v", err)
	}
	if got, err := CleanComment(strings.Repeat("é", MaxComment)); err != nil || len([]rune(got)) != MaxComment {
		t.Errorf("at the limit: %v", err)
	}
	for _, bad := range []string{"a\x00b", "a\x1bb", "\xff"} {
		if _, err := CleanComment(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTokenSurvivesKeyRotation(t *testing.T) {
	oldKey, newKey := []byte("old-key-old-key-old-key-old-key!"), []byte("new-key-new-key-new-key-new-key!")
	now := time.Now()
	token := NewSigner(oldKey).Issue(conv(3), now.Add(time.Hour))

	rotated := NewSigner(newKey, oldKey)
	if id, _, err := rotated.Verify(token, now); err != nil || id != conv(3) {
		t.Fatalf("token from before the rotation: %v %v", id, err)
	}
	if !rotated.CheckCSRF(token, NewSigner(oldKey).CSRF(token)) {
		t.Error("a form rendered before the rotation must still post")
	}
	if _, _, err := rotated.Verify(rotated.Issue(conv(3), now.Add(time.Hour)), now); err != nil {
		t.Errorf("new tokens verify: %v", err)
	}
	if _, _, err := NewSigner(oldKey).Verify(rotated.Issue(conv(3), now.Add(time.Hour)), now); !errors.Is(err, ErrInvalid) {
		t.Error("new tokens are signed with the active key, not the old one")
	}
	if _, _, err := NewSigner(newKey).Verify(token, now); !errors.Is(err, ErrInvalid) {
		t.Errorf("once the old key is removed its tokens must fail: %v", err)
	}
	if NewSigner(newKey).CheckCSRF(token, NewSigner(oldKey).CSRF(token)) {
		t.Error("once the old key is removed its CSRF values must fail")
	}
}
