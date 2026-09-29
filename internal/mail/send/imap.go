package send

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

// imapSessionTimeout bounds a whole IMAP session; a Sent-folder check or APPEND is short.
const imapSessionTimeout = 2 * time.Minute

type IMAPConfig struct {
	Host     string
	Port     int
	TLS      TLSMode
	Username string
	Password string
	// AccessToken, when set, authenticates with XOAUTH2 instead of Password.
	AccessToken string
	Folder      string
	// AllowInternal lifts the block on loopback, private and other internal destinations. It is
	// the mailbox owner's allow_internal_host setting.
	AllowInternal bool
	TLSConfig     *tls.Config
}

// openIMAP connects, logs in and selects nothing; the caller must Close the client.
func openIMAP(ctx context.Context, cfg IMAPConfig) (*imapclient.Client, error) {
	if cfg.TLS != TLSImplicit && cfg.TLS != TLSStartTLS {
		return nil, fmt.Errorf("unsupported imap tls mode %q", cfg.TLS)
	}
	tlsCfg := verifiedTLS(cfg.TLSConfig, cfg.Host)
	conn, err := netguard.Dialer(cfg.AllowInternal, dialTimeout).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(imapSessionTimeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	opts := &imapclient.Options{TLSConfig: tlsCfg}
	var c *imapclient.Client
	if cfg.TLS == TLSImplicit {
		tconn := tls.Client(conn, tlsCfg)
		if err := tconn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("tls: %w", err)
		}
		c = imapclient.New(tconn, opts)
	} else {
		c, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			return nil, fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.AccessToken != "" {
		err = c.Authenticate(mailauth.NewXOAuth2Client(cfg.Username, cfg.AccessToken))
	} else {
		err = c.Login(cfg.Username, cfg.Password).Wait()
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("login: %w", err), c.Close())
	}
	return c, nil
}

func closeIMAP(c *imapclient.Client, err *error) {
	if logoutErr := c.Logout().Wait(); logoutErr != nil && *err == nil {
		*err = fmt.Errorf("logout: %w", logoutErr)
	}
	if closeErr := c.Close(); closeErr != nil && *err == nil {
		*err = closeErr
	}
}

// InSentFolder reports whether a message with the given Message-ID (without angle brackets)
// exists in cfg.Folder.
func InSentFolder(ctx context.Context, cfg IMAPConfig, messageID string) (found bool, err error) {
	c, err := openIMAP(ctx, cfg)
	if err != nil {
		return false, err
	}
	defer closeIMAP(c, &err)
	if _, err := c.Select(cfg.Folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return false, fmt.Errorf("select %q: %w", cfg.Folder, err)
	}
	data, err := c.UIDSearch(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: "<" + messageID + ">"}},
	}, nil).Wait()
	if err != nil {
		return false, fmt.Errorf("search: %w", err)
	}
	return len(data.AllUIDs()) > 0, nil
}

// AppendToSent stores a copy of raw in cfg.Folder, flagged as seen.
func AppendToSent(ctx context.Context, cfg IMAPConfig, raw []byte, date time.Time) (err error) {
	c, err := openIMAP(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeIMAP(c, &err)
	cmd := c.Append(cfg.Folder, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}, Time: date})
	if _, err := cmd.Write(raw); err != nil {
		return errors.Join(fmt.Errorf("append: %w", err), cmd.Close())
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	if _, err := cmd.Wait(); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return nil
}
