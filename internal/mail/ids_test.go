package mail

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestNormalizeMessageID(t *testing.T) {
	cases := map[string]string{
		"<AbC.123@Mail.Example.COM>":  "AbC.123@mail.example.com",
		"  <x@y>  ":                   "x@y",
		"no-at-sign":                  "",
		"<@example.com>":              "",
		"<a b@example.com>":           "",
		"<abc@>":                      "",
		"CAF=+x_y@mail.gmail.com":     "CAF=+x_y@mail.gmail.com",
		"<local@part@example.com>":    "local@part@example.com",
		"<DB6PR01MB@eurprd01.PROD.x>": "DB6PR01MB@eurprd01.prod.x",
	}
	for in, want := range cases {
		if got := NormalizeMessageID(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestMain(m *testing.M) {
	SetMessageIDKey([]byte("test-message-id-key-0123456789ab"))
	os.Exit(m.Run())
}

func TestOutboundMessageIDRoundTrip(t *testing.T) {
	conv := [16]byte{0x01, 0x99, 0xaa, 0xff}
	id, err := NewOutboundMessageID(conv, "Support.Example.NL")
	if err != nil {
		t.Fatal(err)
	}
	if NormalizeMessageID("<"+id+">") != id {
		t.Fatalf("outbound id %q is not normalized", id)
	}
	got, ok := ConversationFromMessageID(id)
	if !ok || got != conv {
		t.Fatalf("round trip failed: %x %v", got, ok)
	}
	for _, bad := range []string{"x@y", "echoo.zz@y", "echoo.0199aaff@y", "echoo." + id} {
		if _, ok := ConversationFromMessageID(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOutboundMessageIDTagVerification(t *testing.T) {
	conv := [16]byte{0x01, 0x99, 0xaa, 0xff}
	other := [16]byte{0x02}
	id, err := NewOutboundMessageID(conv, "support.example.nl")
	if err != nil {
		t.Fatal(err)
	}
	local, domain, _ := strings.Cut(id, "@")
	parts := strings.Split(local, ".")
	forged := []string{
		"echoo." + hex.EncodeToString(conv[:]) + "." + parts[2] + "@" + domain,                                 // legacy, no tag
		"echoo." + hex.EncodeToString(conv[:]) + "." + parts[2] + "." + strings.Repeat("0", 16) + "@" + domain, // wrong tag
		"echoo." + hex.EncodeToString(other[:]) + "." + parts[2] + "." + parts[3] + "@" + domain,               // tag of another conversation
		"echoo." + hex.EncodeToString(conv[:]) + "." + parts[2] + "." + parts[3] + "." + parts[3] + "@" + domain,
	}
	for _, f := range forged {
		if _, ok := ConversationFromMessageID(f); ok {
			t.Errorf("forged id %q accepted", f)
		}
	}
	SetMessageIDKey([]byte("another-key-0123456789abcdefghij"))
	defer SetMessageIDKey([]byte("test-message-id-key-0123456789ab"))
	if _, ok := ConversationFromMessageID(id); ok {
		t.Error("id verified under a different key")
	}
}

func TestMessageIDsSurviveKeyRotation(t *testing.T) {
	oldKey, newKey := []byte("old-message-id-key-0123456789abc"), []byte("new-message-id-key-0123456789abc")
	defer SetMessageIDKey([]byte("test-message-id-key-0123456789ab"))
	conv := [16]byte{9, 8, 7}

	SetMessageIDKey(oldKey)
	old, err := NewOutboundMessageID(conv, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	SetMessageIDKey(newKey, oldKey)
	if got, ok := ConversationFromMessageID(old); !ok || got != conv {
		t.Fatalf("a reply to mail sent before the rotation must still thread: %v %v", got, ok)
	}
	fresh, err := NewOutboundMessageID(conv, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := ConversationFromMessageID(fresh); !ok || got != conv {
		t.Fatal("ids made after the rotation verify")
	}
	SetMessageIDKey(oldKey)
	if _, ok := ConversationFromMessageID(fresh); ok {
		t.Fatal("ids are tagged with the active key")
	}
	SetMessageIDKey(newKey)
	if _, ok := ConversationFromMessageID(old); ok {
		t.Fatal("once the old key is removed its ids are no longer tokens")
	}
}
