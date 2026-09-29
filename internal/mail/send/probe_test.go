package send

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"echoo/internal/netguard"
)

func TestProbe(t *testing.T) {
	cert, roots := selfSignedCert(t)
	trust := &tls.Config{RootCAs: roots}

	cases := []struct {
		name string
		mode tlsMode
		cfg  SMTPConfig
		want error
	}{
		{"implicit TLS", serverImplicit, SMTPConfig{TLS: TLSImplicit, Username: smtpUser, Password: smtpPass}, nil},
		{"STARTTLS", serverStartTLS, SMTPConfig{TLS: TLSStartTLS, Username: smtpUser, Password: smtpPass}, nil},
		{"no authentication", serverImplicit, SMTPConfig{TLS: TLSImplicit}, nil},
		{"wrong password", serverImplicit, SMTPConfig{TLS: TLSImplicit, Username: smtpUser, Password: "nope"}, ErrProbeAuth},
		{"STARTTLS required but not offered", serverPlaintext, SMTPConfig{TLS: TLSStartTLS, Username: smtpUser, Password: smtpPass}, ErrProbeTLS},
		{"TLS against a plaintext server", serverPlaintext, SMTPConfig{TLS: TLSImplicit, Username: smtpUser, Password: smtpPass}, ErrProbeTLS},
		{"untrusted certificate", serverImplicit, SMTPConfig{TLS: TLSImplicit, Username: smtpUser, Password: smtpPass, TLSConfig: &tls.Config{}}, ErrProbeTLS},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeSMTP(t, cert, tc.mode)
			cfg := tc.cfg
			cfg.Host, cfg.Port, cfg.AllowInternal = "127.0.0.1", srv.port, true
			if cfg.TLSConfig == nil {
				cfg.TLSConfig = trust
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err := Probe(ctx, cfg, "")
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if mails, _ := srv.counts(); mails != 0 {
				t.Fatalf("probe sent %d mails", mails)
			}
		})
	}

	t.Run("connection refused", func(t *testing.T) {
		srv := newFakeSMTP(t, cert, serverImplicit)
		port := srv.port
		cfg := SMTPConfig{Host: "127.0.0.1", Port: port + 1, TLS: TLSImplicit, AllowInternal: true, TLSConfig: trust}
		if err := Probe(t.Context(), cfg, ""); !errors.Is(err, ErrProbeConnect) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("dial address is separate from the verified name", func(t *testing.T) {
		srv := newFakeSMTP(t, cert, serverImplicit)
		cfg := SMTPConfig{Host: "mail.example.invalid", Port: srv.port, TLS: TLSImplicit, AllowInternal: true, TLSConfig: trust}
		if err := Probe(t.Context(), cfg, "127.0.0.1"); !errors.Is(err, ErrProbeTLS) {
			t.Fatalf("certificate for another name must fail verification, got %v", err)
		}
	})

	t.Run("internal destination is refused unless allowed", func(t *testing.T) {
		srv := newFakeSMTP(t, cert, serverImplicit)
		cfg := SMTPConfig{Host: "127.0.0.1", Port: srv.port, TLS: TLSImplicit, TLSConfig: trust}
		err := Probe(t.Context(), cfg, "")
		if !errors.Is(err, ErrProbeConnect) || !errors.Is(err, netguard.ErrInternal) {
			t.Fatalf("got %v", err)
		}
		if n := srv.conns.Load(); n != 0 {
			t.Fatalf("server saw %d connections", n)
		}
	})
}
