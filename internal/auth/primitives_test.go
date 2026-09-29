package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var fastArgon = Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}

func TestPasswordHashVerify(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(fastArgon, 2)
	enc, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", enc)
	}
	if ok, rehash, err := h.Verify(ctx, "correct horse battery", enc); !ok || rehash || err != nil {
		t.Fatalf("verify: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := h.Verify(ctx, "wrong horse battery", enc); ok {
		t.Fatal("wrong password accepted")
	}
	if ok, _, err := h.Verify(ctx, "anything", ""); ok || err != nil {
		t.Fatalf("unknown account must fail without error: ok=%v err=%v", ok, err)
	}

	stronger := NewHasher(Argon2Params{MemoryKiB: 128, Time: 1, Threads: 1}, 1)
	if ok, rehash, _ := stronger.Verify(ctx, "correct horse battery", enc); !ok || !rehash {
		t.Fatalf("expected rehash on parameter change: ok=%v rehash=%v", ok, rehash)
	}
}

func TestValidatePassword(t *testing.T) {
	if !errors.Is(ValidatePassword("elevenchars"), ErrPasswordTooShort) {
		t.Fatal("11 chars accepted")
	}
	if ValidatePassword("twelve chars") != nil {
		t.Fatal("12 chars rejected")
	}
	// Length counts characters, not bytes.
	if ValidatePassword(strings.Repeat("é", 12)) != nil {
		t.Fatal("12 multibyte chars rejected")
	}
	if !errors.Is(ValidatePassword(strings.Repeat("a", 129)), ErrPasswordTooLong) {
		t.Fatal("129 chars accepted")
	}
}

// Test vectors from RFC 6238 appendix B (SHA1), truncated to 6 digits.
func TestTOTPRFCVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := map[int64]string{
		59:          "287082",
		1111111109:  "081804",
		1111111111:  "050471",
		1234567890:  "005924",
		2000000000:  "279037",
		20000000000: "353130",
	}
	for unix, want := range vectors {
		if got := TOTPCode(secret, TOTPStep(time.Unix(unix, 0))); got != want {
			t.Errorf("t=%d: got %s want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTPSkew(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	step := TOTPStep(now)
	for d, want := range map[int64]bool{-2: false, -1: true, 0: true, 1: true, 2: false} {
		got, ok := VerifyTOTP(secret, TOTPCode(secret, step+d), now)
		if ok != want || (ok && got != step+d) {
			t.Errorf("offset %d: ok=%v step=%d", d, ok, got)
		}
	}
	if _, ok := VerifyTOTP(secret, "12345", now); ok {
		t.Error("short code accepted")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil || len(codes) != 10 || len(hashes) != 10 {
		t.Fatalf("codes=%d hashes=%d err=%v", len(codes), len(hashes), err)
	}
	c := codes[0]
	if len(c) != 19 || !LooksLikeRecoveryCode(c) || LooksLikeRecoveryCode("123456") {
		t.Fatalf("unexpected format %q", c)
	}
	variant := " " + strings.ToUpper(strings.ReplaceAll(c, "-", "")) + " "
	if string(HashRecoveryCode(variant)) != string(hashes[0]) {
		t.Fatal("normalization mismatch")
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	for i, want := range []bool{true, true, false} {
		if got := l.Allow("a"); got != want {
			t.Fatalf("attempt %d: got %v, want %v", i+1, got, want)
		}
	}
	if !l.Allow("b") {
		t.Fatal("keys must be independent")
	}
	now = now.Add(time.Minute)
	if !l.Allow("a") {
		t.Fatal("window did not reset")
	}
}
