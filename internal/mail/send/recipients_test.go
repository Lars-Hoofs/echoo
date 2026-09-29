package send

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
)

func TestRestrictedRecipientsNeverReachTheServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()

	RestrictRecipients([]string{"Thermacon.nl"})
	t.Cleanup(func() { RestrictRecipients(nil) })

	addr := ln.Addr().(*net.TCPAddr)
	cfg := SMTPConfig{Host: "127.0.0.1", Port: addr.Port, TLS: TLSImplicit, AllowInternal: true}
	res := Deliver(context.Background(), cfg, "support@thermacon.nl", []string{"collega@THERMACON.nl", "klant@gmail.com"}, []byte("x"))
	if res.Outcome != PermanentFailure || !errors.Is(res.Err, ErrRecipientNotAllowed) {
		t.Fatalf("outside recipient: %v %v", res.Outcome, res.Err)
	}
	if n := accepted.Load(); n != 0 {
		t.Fatalf("server saw %d connections for a refused delivery", n)
	}
}

func TestCheckRecipients(t *testing.T) {
	if err := checkRecipients([]string{"klant@gmail.com"}); err != nil {
		t.Fatalf("unrestricted: %v", err)
	}
	RestrictRecipients([]string{"thermacon.nl"})
	t.Cleanup(func() { RestrictRecipients(nil) })
	for _, r := range []string{"a@thermacon.nl", "B@Thermacon.NL"} {
		if err := checkRecipients([]string{r}); err != nil {
			t.Errorf("%s refused: %v", r, err)
		}
	}
	for _, r := range []string{"a@sub.thermacon.nl", "a@thermacon.nl.evil.com", "a@evilthermacon.nl", "thermacon.nl", ""} {
		if err := checkRecipients([]string{r}); !errors.Is(err, ErrRecipientNotAllowed) {
			t.Errorf("%q allowed", r)
		}
	}
}
