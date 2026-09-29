package imapsync

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"echoo/internal/mailauth"
	"echoo/internal/netguard"
)

type TLSMode string

const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
)

const inboxName = "INBOX"

// Failure classes of a connection attempt, matched with errors.Is. Their messages never contain
// credentials or server-supplied text from the login exchange.
var (
	ErrConnect = errors.New("cannot connect to the IMAP server")
	ErrTLS     = errors.New("TLS negotiation failed")
	ErrAuth    = errors.New("authentication failed")
	ErrNoInbox = errors.New("INBOX not found")
)

// ConnConfig describes one IMAP account.
type ConnConfig struct {
	Host     string
	Port     int
	TLS      TLSMode
	Username string
	Password string
	// AccessToken, when set, authenticates with XOAUTH2 instead of Password.
	AccessToken string
	// Timeout bounds dialing, the TLS handshake and the greeting. Zero means 30 seconds.
	Timeout time.Duration
	// AllowInternal lifts the block on loopback, private and other internal destinations. It is
	// the mailbox owner's allow_internal_host setting.
	AllowInternal bool
	// TLSConfig is only for tests, to trust a self-signed certificate. Nil means system roots.
	TLSConfig *tls.Config
}

// TestConnection connects, logs in, selects INBOX and logs out. The error matches ErrConnect,
// ErrTLS, ErrAuth or ErrNoInbox.
func TestConnection(ctx context.Context, cfg ConnConfig) error {
	c, _, err := openInbox(ctx, cfg, nil, true)
	if err != nil {
		return err
	}
	logout(c, defaultLogoutTimeout)
	return nil
}

// openInbox returns a client that is logged in with INBOX selected. Read-only sessions use
// EXAMINE, so nothing can change flags on the server.
func openInbox(ctx context.Context, cfg ConnConfig, opts *imapclient.Options, readOnly bool) (*imapclient.Client, *imap.SelectData, error) {
	c, err := dial(ctx, cfg, opts)
	if err != nil {
		return nil, nil, err
	}
	if err := authenticate(c, cfg); err != nil {
		_ = c.Close()
		var ie *imap.Error
		if errors.As(err, &ie) && ie.Type == imap.StatusResponseTypeNo && ie.Code != imap.ResponseCodeUnavailable {
			return nil, nil, ErrAuth
		}
		return nil, nil, fmt.Errorf("%w: login: %w", ErrConnect, err)
	}
	sel, err := c.Select(inboxName, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		_ = c.Close()
		var ie *imap.Error
		if errors.As(err, &ie) && ie.Type == imap.StatusResponseTypeNo {
			return nil, nil, fmt.Errorf("%w: %w", ErrNoInbox, err)
		}
		return nil, nil, fmt.Errorf("%w: select: %w", ErrConnect, err)
	}
	return c, sel, nil
}

func authenticate(c *imapclient.Client, cfg ConnConfig) error {
	if cfg.AccessToken != "" {
		return c.Authenticate(mailauth.NewXOAuth2Client(cfg.Username, cfg.AccessToken))
	}
	return c.Login(cfg.Username, cfg.Password).Wait()
}

func dial(ctx context.Context, cfg ConnConfig, opts *imapclient.Options) (*imapclient.Client, error) {
	if opts == nil {
		opts = &imapclient.Options{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	tlsCfg := cfg.TLSConfig.Clone()
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsCfg.InsecureSkipVerify = false
	if tlsCfg.ServerName == "" {
		tlsCfg.ServerName = cfg.Host
	}

	raw, err := netguard.Dialer(cfg.AllowInternal, timeout).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}
	if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}

	var c *imapclient.Client
	switch cfg.TLS {
	case TLSImplicit:
		tc := tls.Client(raw, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("%w: %w", ErrTLS, err)
		}
		if err := raw.SetDeadline(time.Time{}); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("%w: %w", ErrConnect, err)
		}
		c = imapclient.New(tc, opts)
	case TLSStartTLS:
		o := *opts
		o.TLSConfig = tlsCfg
		// NewStartTLS fails when the server does not offer STARTTLS; there is no plaintext fallback.
		c, err = imapclient.NewStartTLS(raw, &o)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrTLS, err)
		}
		if err := raw.SetDeadline(time.Time{}); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("%w: %w", ErrConnect, err)
		}
	default:
		_ = raw.Close()
		return nil, fmt.Errorf("unsupported TLS mode %q", cfg.TLS)
	}
	if err := c.WaitGreeting(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: greeting: %w", ErrConnect, err)
	}
	return c, nil
}

// logout ends the session politely and always closes the connection.
func logout(c *imapclient.Client, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The server closes the connection after LOGOUT, so Wait often reports EOF; the
		// connection is closed below either way.
		_ = c.Logout().Wait()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	_ = c.Close()
}
