// Package sysmail sends the emails Echoo originates itself: invitations, password resets and
// notifications. They leave through one configured mailbox or a dedicated SMTP relay.
package sysmail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/config"
	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/keyring"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
)

// ErrNotConfigured means neither ECHOO_SYSTEM_MAILBOX nor ECHOO_SMTP_URL is set.
var ErrNotConfigured = errors.New("system mail is not configured")

// errUndeliverable marks failures that no retry can fix, such as a system mailbox that was
// deleted or disabled.
var errUndeliverable = errors.New("system mail cannot be delivered")

const (
	SourceMailbox = "mailbox"
	SourceSMTP    = "smtp"

	maxAttempts = 8
)

var payloadAAD = []byte("sysmail.payload")

// Message is one system email. Text is required; HTML is optional.
type Message struct {
	To, Subject, Text, HTML string
}

// Status tells the admin UI whether system mail works.
type Status struct {
	Available bool
	// Source is SourceMailbox or SourceSMTP, empty when nothing is configured.
	Source string
}

// Sender delivers system mail.
type Sender struct {
	pool    *pgxpool.Pool
	q       *dbq.Queries
	keys    *keyring.Keyring
	tokens  send.TokenSource
	mailbox string
	relay   *config.SMTPRelay
	tls     *tls.Config
	now     func() time.Time
}

// New creates a sender from the configuration. tlsCfg adds trust roots (tests); certificate
// verification always stays on.
func New(pool *pgxpool.Pool, keys *keyring.Keyring, tokens send.TokenSource, cfg *config.Config, tlsCfg *tls.Config) *Sender {
	return &Sender{
		pool: pool, q: dbq.New(pool), keys: keys, tokens: tokens,
		mailbox: cfg.SystemMailbox, relay: cfg.SystemSMTP, tls: tlsCfg, now: time.Now,
	}
}

// Configured reports whether a source is configured, without checking that it works.
func (s *Sender) Configured() bool { return s.mailbox != "" || s.relay != nil }

// Status reports whether system mail can be sent right now.
func (s *Sender) Status(ctx context.Context) (Status, error) {
	switch {
	case s.relay != nil:
		return Status{Available: true, Source: SourceSMTP}, nil
	case s.mailbox != "":
		mb, err := s.q.GetMailboxByAddress(ctx, s.mailbox)
		if errors.Is(err, pgx.ErrNoRows) {
			return Status{Source: SourceMailbox}, nil
		}
		if err != nil {
			return Status{}, fmt.Errorf("load system mailbox: %w", err)
		}
		return Status{Available: !mb.DisabledAt.Valid && mb.SmtpHost != "", Source: SourceMailbox}, nil
	}
	return Status{}, nil
}

// Enqueue queues the message in tx, so it is sent only if the caller's transaction commits.
func (s *Sender) Enqueue(ctx context.Context, tx pgx.Tx, client *river.Client[pgx.Tx], m Message) error {
	if !s.Configured() {
		return ErrNotConfigured
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal system mail: %w", err)
	}
	sealed, err := s.keys.Encrypt(raw, payloadAAD)
	if err != nil {
		return fmt.Errorf("seal system mail: %w", err)
	}
	if _, err := client.InsertTx(ctx, tx, jobs.SysmailSend{Payload: sealed}, &river.InsertOpts{MaxAttempts: maxAttempts}); err != nil {
		return fmt.Errorf("enqueue system mail: %w", err)
	}
	return nil
}

// Send delivers one message now. The queued path calls it from the worker.
func (s *Sender) Send(ctx context.Context, to, subject, textBody, htmlBody string) error {
	from, cfg, err := s.route(ctx)
	if err != nil {
		return err
	}
	_, domain, ok := strings.Cut(from.Address, "@")
	if !ok {
		return fmt.Errorf("%w: sender address has no domain", errUndeliverable)
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("generate message id: %w", err)
	}
	out := send.Outgoing{
		From: from, To: []mail.Address{{Address: to}}, Subject: subject,
		MessageID: "sys." + hex.EncodeToString(id[:]) + "@" + domain,
		Date:      s.now().UTC(), Text: textBody, HTML: htmlBody,
		// Keeps auto-responders from answering an invitation.
		AutoReplied: true,
	}
	raw, err := send.Build(out)
	if err != nil {
		return fmt.Errorf("%w: %w", errUndeliverable, err)
	}
	res := send.Deliver(ctx, cfg, from.Address, out.Recipients(), raw)
	switch res.Outcome {
	case send.Delivered:
		return nil
	case send.PermanentFailure:
		return fmt.Errorf("%w: %s: %w", errUndeliverable, res.Response, res.Err)
	default:
		// A lost final reply may duplicate an email on retry, which is harmless here.
		return fmt.Errorf("deliver system mail: %s: %w", res.Outcome, res.Err)
	}
}

func (s *Sender) route(ctx context.Context) (mail.Address, send.SMTPConfig, error) {
	if s.relay != nil {
		r := s.relay
		return mail.Address{Address: r.From}, send.SMTPConfig{
			Host: r.Host, Port: r.Port, TLS: send.TLSMode(r.TLS), Username: r.Username, Password: r.Password,
			AllowInternal: r.AllowInternal, TLSConfig: s.tls,
		}, nil
	}
	if s.mailbox == "" {
		return mail.Address{}, send.SMTPConfig{}, ErrNotConfigured
	}
	mb, err := s.q.GetMailboxByAddress(ctx, s.mailbox)
	if errors.Is(err, pgx.ErrNoRows) {
		return mail.Address{}, send.SMTPConfig{}, fmt.Errorf("%w: system mailbox does not exist", errUndeliverable)
	}
	if err != nil {
		return mail.Address{}, send.SMTPConfig{}, fmt.Errorf("load system mailbox: %w", err)
	}
	if mb.DisabledAt.Valid {
		return mail.Address{}, send.SMTPConfig{}, fmt.Errorf("%w: system mailbox is disabled", errUndeliverable)
	}
	cfg, err := send.MailboxSMTP(ctx, s.keys, s.tokens, s.tls, mb)
	if err != nil {
		if send.IsUnsendable(err) {
			err = fmt.Errorf("%w: %w", errUndeliverable, err)
		}
		return mail.Address{}, send.SMTPConfig{}, err
	}
	return mail.Address{Name: mb.DisplayName, Address: mb.EmailAddress}, cfg, nil
}

// Worker runs sysmail.send jobs.
type Worker struct {
	river.WorkerDefaults[jobs.SysmailSend]
	s *Sender
}

func NewWorker(s *Sender) *Worker { return &Worker{s: s} }

func (w *Worker) Work(ctx context.Context, job *river.Job[jobs.SysmailSend]) error {
	raw, err := w.s.keys.Decrypt(job.Args.Payload, payloadAAD)
	if err != nil {
		return river.JobCancel(fmt.Errorf("decrypt system mail: %w", err))
	}
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return river.JobCancel(fmt.Errorf("decode system mail: %w", err))
	}
	if m.To == "" {
		return nil // decoy queued for an unknown password-reset address
	}
	err = w.s.Send(ctx, m.To, m.Subject, m.Text, m.HTML)
	if errors.Is(err, errUndeliverable) || errors.Is(err, ErrNotConfigured) {
		return river.JobCancel(err)
	}
	return err
}
