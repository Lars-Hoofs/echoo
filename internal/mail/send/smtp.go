package send

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"echoo/internal/mailauth"
	"echoo/internal/netguard"
)

// TLSMode matches the mailboxes.smtp_tls and imap_tls column values.
type TLSMode string

const (
	TLSImplicit TLSMode = "implicit"
	TLSStartTLS TLSMode = "starttls"
)

const (
	dialTimeout       = 15 * time.Second
	commandTimeout    = time.Minute
	submissionTimeout = 5 * time.Minute
)

// Outcome classifies the result of a delivery attempt.
type Outcome int

const (
	// Delivered means the server accepted the message with a 2xx reply.
	Delivered Outcome = iota + 1
	// TemporaryFailure means the message was certainly not accepted and may be retried.
	TemporaryFailure
	// PermanentFailure means the server rejected the message; retrying will not help.
	PermanentFailure
	// UnknownOutcome means the connection was lost after the message data was sent but
	// before the server's final reply: the message may or may not have been accepted.
	UnknownOutcome
)

func (o Outcome) String() string {
	switch o {
	case Delivered:
		return "delivered"
	case TemporaryFailure:
		return "temporary failure"
	case PermanentFailure:
		return "permanent failure"
	case UnknownOutcome:
		return "unknown outcome"
	}
	return "invalid outcome"
}

type SMTPConfig struct {
	Host     string
	Port     int
	TLS      TLSMode
	Username string
	Password string
	// AccessToken, when set, authenticates with XOAUTH2 instead of Password.
	AccessToken string
	// AllowInternal lifts the block on loopback, private and other internal destinations. It is
	// the mailbox owner's allow_internal_host setting.
	AllowInternal bool
	// TLSConfig supplies extra trust roots and the like. Certificate verification is always on.
	TLSConfig *tls.Config
}

// Result is the classified outcome of Deliver. Response is the server reply for outcomes
// that carry one, Err the underlying error for failures.
type Result struct {
	Outcome  Outcome
	Response string
	Err      error
}

// Deliver sends raw to rcpt over one SMTP connection. It never falls back to plaintext.
func Deliver(ctx context.Context, cfg SMTPConfig, from string, rcpt []string, raw []byte) Result {
	if err := checkRecipients(rcpt); err != nil {
		return Result{Outcome: PermanentFailure, Err: err}
	}
	if cfg.TLS != TLSImplicit && cfg.TLS != TLSStartTLS {
		return Result{Outcome: PermanentFailure, Err: fmt.Errorf("unsupported smtp tls mode %q", cfg.TLS)}
	}
	tlsCfg := verifiedTLS(cfg.TLSConfig, cfg.Host)

	conn, err := netguard.Dialer(cfg.AllowInternal, dialTimeout).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return Result{Outcome: TemporaryFailure, Err: fmt.Errorf("connect: %w", err)}
	}
	// Closing the connection is what makes a cancelled context interrupt blocked I/O.
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
		return classify(fmt.Errorf("tls: %w", err))
	}
	defer func() { _ = c.Close() }()
	c.CommandTimeout = commandTimeout
	c.SubmissionTimeout = submissionTimeout

	if cfg.Username != "" {
		auth, err := pickAuth(c, cfg)
		if err != nil {
			return Result{Outcome: PermanentFailure, Err: err}
		}
		if err := c.Auth(auth); err != nil {
			return classify(fmt.Errorf("auth: %w", err))
		}
	}
	if err := c.Mail(from, nil); err != nil {
		return classify(fmt.Errorf("mail from: %w", err))
	}
	for _, r := range rcpt {
		if err := c.Rcpt(r, nil); err != nil {
			return classify(fmt.Errorf("rcpt to: %w", err))
		}
	}
	data, err := c.Data()
	if err != nil {
		return classify(fmt.Errorf("data: %w", err))
	}
	if _, err := data.Write(raw); err != nil {
		return Result{Outcome: TemporaryFailure, Err: fmt.Errorf("write message: %w", err)}
	}
	resp, err := data.CloseWithResponse()
	if err != nil {
		var smtpErr *smtp.SMTPError
		if errors.As(err, &smtpErr) {
			return classify(fmt.Errorf("end of data: %w", err))
		}
		return Result{Outcome: UnknownOutcome, Err: fmt.Errorf("end of data: %w", err)}
	}
	// The message is accepted; a failing QUIT changes nothing about that.
	_ = c.Quit()
	return Result{Outcome: Delivered, Response: "250 " + resp.StatusText}
}

// verifiedTLS clones base and forces verification: a caller can add trust roots but never
// switch verification off.
func verifiedTLS(base *tls.Config, host string) *tls.Config {
	cfg := &tls.Config{}
	if base != nil {
		cfg = base.Clone()
	}
	cfg.InsecureSkipVerify = false
	cfg.ServerName = host
	if cfg.MinVersion < tls.VersionTLS12 {
		cfg.MinVersion = tls.VersionTLS12
	}
	return cfg
}

func implicitTLSClient(ctx context.Context, conn net.Conn, cfg *tls.Config) (*smtp.Client, error) {
	tconn := tls.Client(conn, cfg)
	if err := conn.SetDeadline(time.Now().Add(commandTimeout)); err != nil {
		return nil, err
	}
	if err := tconn.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	return smtp.NewClient(tconn), nil
}

func pickAuth(c *smtp.Client, cfg SMTPConfig) (sasl.Client, error) {
	if cfg.AccessToken != "" {
		if !c.SupportsAuth("XOAUTH2") {
			return nil, errors.New("server does not offer AUTH XOAUTH2")
		}
		return mailauth.NewXOAuth2Client(cfg.Username, cfg.AccessToken), nil
	}
	if c.SupportsAuth("PLAIN") {
		return sasl.NewPlainClient("", cfg.Username, cfg.Password), nil
	}
	if c.SupportsAuth("LOGIN") {
		return sasl.NewLoginClient(cfg.Username, cfg.Password), nil
	}
	return nil, errors.New("server offers neither AUTH PLAIN nor AUTH LOGIN")
}

// classify maps an error from a command before the message data was completed. Anything
// that is not an SMTP reply is a network or TLS problem and safe to retry: the server never
// saw the end of the message.
func classify(err error) Result {
	var smtpErr *smtp.SMTPError
	if errors.As(err, &smtpErr) {
		res := Result{Outcome: TemporaryFailure, Response: responseText(smtpErr), Err: err}
		if smtpErr.Code >= 500 {
			res.Outcome = PermanentFailure
		}
		return res
	}
	return Result{Outcome: TemporaryFailure, Err: err}
}

func responseText(e *smtp.SMTPError) string {
	if e.EnhancedCode == smtp.NoEnhancedCode {
		return fmt.Sprintf("%d %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%d %d.%d.%d %s", e.Code, e.EnhancedCode[0], e.EnhancedCode[1], e.EnhancedCode[2], e.Message)
}
