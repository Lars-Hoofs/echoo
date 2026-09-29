package send

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/emersion/go-smtp"

	"echoo/internal/netguard"
)

// Failure classes of Probe, matched with errors.Is. Their messages never contain credentials.
var (
	ErrProbeConnect = errors.New("cannot connect to the SMTP server")
	ErrProbeTLS     = errors.New("TLS negotiation failed")
	ErrProbeAuth    = errors.New("authentication failed")
)

// Probe connects, negotiates TLS and, when cfg.Username is set, authenticates, without
// sending any mail. dialHost, when not empty, is the address that is dialed while cfg.Host
// stays the name the certificate is verified against, so a caller can pin an IP it validated.
// The error matches ErrProbeConnect, ErrProbeTLS or ErrProbeAuth.
func Probe(ctx context.Context, cfg SMTPConfig, dialHost string) error {
	if cfg.TLS != TLSImplicit && cfg.TLS != TLSStartTLS {
		return fmt.Errorf("unsupported smtp tls mode %q", cfg.TLS)
	}
	if dialHost == "" {
		dialHost = cfg.Host
	}
	tlsCfg := verifiedTLS(cfg.TLSConfig, cfg.Host)

	conn, err := netguard.Dialer(cfg.AllowInternal, dialTimeout).DialContext(ctx, "tcp", net.JoinHostPort(dialHost, strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProbeConnect, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	var c *smtp.Client
	if cfg.TLS == TLSImplicit {
		c, err = implicitTLSClient(ctx, conn, tlsCfg)
	} else {
		c, err = smtp.NewClientStartTLS(conn, tlsCfg)
	}
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("%w: %w", ErrProbeTLS, err)
	}
	defer func() { _ = c.Close() }()
	c.CommandTimeout = commandTimeout

	if cfg.Username != "" {
		auth, err := pickAuth(c, cfg)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrProbeAuth, err)
		}
		if err := c.Auth(auth); err != nil {
			var smtpErr *smtp.SMTPError
			if errors.As(err, &smtpErr) {
				return fmt.Errorf("%w: %d", ErrProbeAuth, smtpErr.Code)
			}
			return fmt.Errorf("%w: auth: %w", ErrProbeConnect, err)
		}
	}
	// The probe already succeeded; a failing QUIT changes nothing about that.
	_ = c.Quit()
	return nil
}
