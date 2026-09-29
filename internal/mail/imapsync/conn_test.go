package imapsync

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/emersion/go-imap/v2/imapserver"
)

func TestTestConnectionSuccess(t *testing.T) {
	for _, mode := range []TLSMode{TLSImplicit, TLSStartTLS} {
		t.Run(string(mode), func(t *testing.T) {
			srv := startServer(t, mode, nil)
			if err := TestConnection(context.Background(), srv.connConfig(mode)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTestConnectionErrors(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)

	t.Run("connection refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		cfg := srv.connConfig(TLSImplicit)
		cfg.Port = port
		if err := TestConnection(context.Background(), cfg); !errors.Is(err, ErrConnect) {
			t.Fatalf("got %v, want ErrConnect", err)
		}
	})

	t.Run("untrusted certificate", func(t *testing.T) {
		cfg := srv.connConfig(TLSImplicit)
		cfg.TLSConfig = nil
		if err := TestConnection(context.Background(), cfg); !errors.Is(err, ErrTLS) {
			t.Fatalf("got %v, want ErrTLS", err)
		}
	})

	t.Run("verification cannot be disabled", func(t *testing.T) {
		cfg := srv.connConfig(TLSImplicit)
		cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // proves the flag is overridden
		if err := TestConnection(context.Background(), cfg); !errors.Is(err, ErrTLS) {
			t.Fatalf("got %v, want ErrTLS", err)
		}
	})

	t.Run("bad password", func(t *testing.T) {
		cfg := srv.connConfig(TLSImplicit)
		cfg.Password = "wrong"
		err := TestConnection(context.Background(), cfg)
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("got %v, want ErrAuth", err)
		}
	})

	t.Run("missing inbox", func(t *testing.T) {
		if err := srv.user.Delete(inboxName); err != nil {
			t.Fatal(err)
		}
		if err := TestConnection(context.Background(), srv.connConfig(TLSImplicit)); !errors.Is(err, ErrNoInbox) {
			t.Fatalf("got %v, want ErrNoInbox", err)
		}
	})
}

type loginCounter struct {
	imapserver.Session
	logins *atomic.Int32
}

func (l loginCounter) Login(username, password string) error {
	l.logins.Add(1)
	return l.Session.Login(username, password)
}

func TestStartTLSUnsupportedNeverFallsBackToPlaintext(t *testing.T) {
	var logins atomic.Int32
	srv := startServer(t, "", func(s imapserver.Session) imapserver.Session { return loginCounter{s, &logins} })
	err := TestConnection(context.Background(), srv.connConfig(TLSStartTLS))
	if !errors.Is(err, ErrTLS) {
		t.Fatalf("got %v, want ErrTLS", err)
	}
	if n := logins.Load(); n != 0 {
		t.Fatalf("server saw %d login attempts over plaintext", n)
	}
}
