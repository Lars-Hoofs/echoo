// Package imapsync keeps every IMAP mailbox synchronised into raw_messages (ADR 0004): raw
// bytes go to blob storage first, the row and its parse job commit together, and the UID
// cursor only advances after that.
package imapsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/keyring"
	"echoo/internal/mailauth"
	"echoo/internal/storage"
)

const (
	stateConnected  = "connected"
	statePolling    = "polling"
	stateBackoff    = "backoff"
	stateAuthFailed = "auth_failed"

	maxErrorText = 300
)

var errNoCredentials = errors.New("no IMAP password configured")

// Deps are the collaborators shared by all supervisors.
type Deps struct {
	Pool    *pgxpool.Pool
	Store   storage.Store
	River   *river.Client[pgx.Tx]
	Keyring *keyring.Keyring
	// Tokens supplies access tokens for OAuth mailboxes; without it those mailboxes cannot connect.
	Tokens TokenSource
	Logger *slog.Logger
	Config Config
}

// TokenSource returns a valid OAuth access token for a mailbox.
type TokenSource interface {
	AccessToken(ctx context.Context, mailboxID pgtype.UUID) (string, error)
}

// Supervisor syncs the INBOX of one mailbox until its context is cancelled.
type Supervisor struct {
	deps  Deps
	cfg   Config
	id    pgtype.UUID
	idStr string
	log   *slog.Logger

	// state and stateErr mirror the last value written to the mailbox row.
	state    string
	stateErr string

	idleFailures int
	idleRetryAt  time.Time

	// afterPut runs between blob storage and the database transaction; tests use it to
	// simulate a crash at that point.
	afterPut func(uid imap.UID) error
}

func NewSupervisor(deps Deps, mailboxID pgtype.UUID) *Supervisor {
	idStr := mailboxID.String()
	return &Supervisor{
		deps:  deps,
		cfg:   deps.Config.withDefaults(),
		id:    mailboxID,
		idStr: idStr,
		log:   deps.Logger.With("component", "imapsync", "mailbox_id", idStr),
	}
}

// Run blocks until ctx is cancelled. It holds the mailbox's advisory lock for its whole
// lifetime and reconnects with backoff after failures.
func (s *Supervisor) Run(ctx context.Context) {
	var lock *advisoryLock
	defer func() {
		if lock == nil {
			return
		}
		if err := lock.release(s.cfg.LogoutTimeout); err != nil {
			s.log.Error("release advisory lock", "error", err)
		}
	}()

	attempt := 0
	for ctx.Err() == nil {
		if lock == nil {
			l, ok, err := acquireLock(ctx, s.deps.Pool, lockKey(s.id))
			switch {
			case err != nil:
				s.log.Error("acquire advisory lock", "error", err)
				sleep(ctx, s.cfg.LockRetryInterval)
				continue
			case !ok:
				s.log.Debug("mailbox is synced by another instance")
				sleep(ctx, s.cfg.LockRetryInterval)
				continue
			}
			lock = l
		}

		reached, err := s.runSession(ctx, lock)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, errLockLost):
			s.log.Warn("advisory lock lost", "error", err)
			if relErr := lock.release(s.cfg.LogoutTimeout); relErr != nil {
				s.log.Error("release advisory lock", "error", relErr)
			}
			lock = nil
		case errors.Is(err, mailauth.ErrReauthRequired):
			s.log.Warn("mailbox must be reconnected")
			s.setState(ctx, stateAuthFailed, mailauth.ReasonReauthRequired)
			attempt = 0
			sleep(ctx, s.cfg.AuthRetryInterval)
		case errors.Is(err, ErrAuth) || errors.Is(err, errNoCredentials):
			s.log.Warn("mailbox authentication failed")
			s.setState(ctx, stateAuthFailed, errorText(err))
			attempt = 0
			sleep(ctx, s.cfg.AuthRetryInterval)
		default:
			if reached {
				attempt = 0
			}
			delay := s.cfg.Backoff(attempt)
			attempt++
			s.log.Warn("mailbox sync failed", "error", err, "retry_in", delay)
			s.setState(ctx, stateBackoff, errorText(err))
			sleep(ctx, delay)
		}
	}
}

// runSession runs one connection until it fails or ctx is cancelled. reached reports whether
// the login and INBOX selection succeeded, which resets the backoff.
func (s *Supervisor) runSession(ctx context.Context, lock *advisoryLock) (reached bool, err error) {
	if err := lock.check(ctx); err != nil {
		return false, err
	}
	q := dbq.New(s.deps.Pool)
	mb, err := q.GetImapSyncMailbox(ctx, s.id)
	if err != nil {
		return false, fmt.Errorf("load mailbox: %w", err)
	}
	cfg, err := s.connConfig(ctx, mb)
	if err != nil {
		return false, err
	}

	notify := make(chan struct{}, 1)
	opts := &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages == nil {
				return
			}
			select {
			case notify <- struct{}{}:
			default:
			}
		},
	}}
	c, sel, err := openInbox(ctx, cfg, opts, !mb.MarkSeen)
	if err != nil {
		return false, err
	}
	defer logout(c, s.cfg.LogoutTimeout)
	go s.closeAfterCancel(ctx, c)

	folder, err := q.UpsertInboxFolder(ctx, s.id)
	if err != nil {
		return true, fmt.Errorf("load inbox folder: %w", err)
	}
	if folder.Uidvalidity != int64(sel.UIDValidity) {
		if folder.Uidvalidity != 0 {
			s.log.Info("UIDVALIDITY changed, resyncing inbox")
		}
		if err := q.ResetFolderUIDValidity(ctx, dbq.ResetFolderUIDValidityParams{ID: folder.ID, Uidvalidity: int64(sel.UIDValidity)}); err != nil {
			return true, fmt.Errorf("reset folder cursor: %w", err)
		}
		folder.Uidvalidity = int64(sel.UIDValidity)
		folder.LastUid = 0
	}
	sy := &inboxSync{s: s, c: c, folder: folder, markSeen: mb.MarkSeen}

	for {
		if err := lock.check(ctx); err != nil {
			return true, err
		}
		useIdle := s.shouldIdle(c)
		if useIdle {
			s.setState(ctx, stateConnected, "")
		} else {
			s.setState(ctx, statePolling, "")
		}
		select {
		case <-notify:
		default:
		}
		if err := sy.pass(ctx); err != nil {
			return true, err
		}
		if useIdle {
			err = s.waitIdle(ctx, c, notify)
		} else {
			err = s.waitPoll(ctx, c)
		}
		if err != nil {
			return true, err
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
	}
}

func (s *Supervisor) connConfig(ctx context.Context, mb dbq.Mailbox) (ConnConfig, error) {
	if mb.AuthType != "password" {
		if s.deps.Tokens == nil {
			return ConnConfig{}, errors.New("OAuth mailboxes are not configured")
		}
		token, err := s.deps.Tokens.AccessToken(ctx, mb.ID)
		if err != nil {
			return ConnConfig{}, err
		}
		return ConnConfig{
			Host: mb.ImapHost, Port: int(mb.ImapPort), TLS: TLSMode(mb.ImapTls),
			Username: mb.ImapUsername, AccessToken: token,
			Timeout: s.cfg.ConnectTimeout, AllowInternal: mb.AllowInternalHost, TLSConfig: s.cfg.TLSConfig,
		}, nil
	}
	if len(mb.ImapSecretEnc) == 0 {
		return ConnConfig{}, errNoCredentials
	}
	pw, err := s.deps.Keyring.Decrypt(mb.ImapSecretEnc, keyring.AAD("mailboxes", "imap_secret_enc", s.idStr))
	if err != nil {
		return ConnConfig{}, fmt.Errorf("decrypt IMAP password: %w", err)
	}
	return ConnConfig{
		Host:          mb.ImapHost,
		Port:          int(mb.ImapPort),
		TLS:           TLSMode(mb.ImapTls),
		Username:      mb.ImapUsername,
		Password:      string(pw),
		Timeout:       s.cfg.ConnectTimeout,
		AllowInternal: mb.AllowInternalHost,
		TLSConfig:     s.cfg.TLSConfig,
	}, nil
}

// closeAfterCancel force-closes a session that did not wind down within the grace period
// after its context was cancelled, e.g. one blocked in a FETCH.
func (s *Supervisor) closeAfterCancel(ctx context.Context, c *imapclient.Client) {
	select {
	case <-c.Closed():
		return
	case <-ctx.Done():
	}
	t := time.NewTimer(s.cfg.LogoutTimeout)
	defer t.Stop()
	select {
	case <-c.Closed():
	case <-t.C:
		_ = c.Close()
	}
}

func (s *Supervisor) shouldIdle(c *imapclient.Client) bool {
	if !c.Caps().Has(imap.CapIdle) {
		return false
	}
	return s.idleFailures < s.cfg.IdleMaxFailures || !time.Now().Before(s.idleRetryAt)
}

// waitIdle blocks in IDLE until new mail is announced, the IDLE period ends or ctx is
// cancelled. IDLE failures that leave the connection usable are counted and only returned
// as an error when the connection is gone.
func (s *Supervisor) waitIdle(ctx context.Context, c *imapclient.Client, notify <-chan struct{}) error {
	err := s.idle(ctx, c, notify)
	if err == nil {
		s.idleFailures = 0
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.idleFailures++
	if s.idleFailures >= s.cfg.IdleMaxFailures {
		s.idleRetryAt = time.Now().Add(s.cfg.IdleRetryInterval)
	}
	s.log.Warn("idle failed", "error", err, "consecutive_failures", s.idleFailures)
	select {
	case <-c.Closed():
		return fmt.Errorf("connection closed during idle: %w", err)
	default:
		return nil
	}
}

func (s *Supervisor) idle(ctx context.Context, c *imapclient.Client, notify <-chan struct{}) error {
	cmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("start idle: %w", err)
	}
	t := time.NewTimer(s.cfg.IdleRestart)
	defer t.Stop()
	var waitErr error
	select {
	case <-notify:
	case <-t.C:
	case <-ctx.Done():
	case <-c.Closed():
		waitErr = errors.New("connection closed")
	}
	return errors.Join(waitErr, cmd.Close(), cmd.Wait())
}

func (s *Supervisor) waitPoll(ctx context.Context, c *imapclient.Client) error {
	t := time.NewTimer(s.cfg.PollInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-c.Closed():
		return errors.New("connection closed")
	case <-t.C:
	}
	// A NOOP lets the server report messages that arrived since the last command.
	if err := c.Noop().Wait(); err != nil {
		return fmt.Errorf("noop: %w", err)
	}
	return nil
}

// setState records a sync state transition. A failed write is logged and not fatal: the
// state is informational and must not stop mail from flowing.
func (s *Supervisor) setState(ctx context.Context, state, errText string) {
	if s.state == state && s.stateErr == errText {
		return
	}
	err := dbq.New(s.deps.Pool).SetMailboxSyncState(ctx, dbq.SetMailboxSyncStateParams{ID: s.id, SyncState: state, SyncError: errText})
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("update sync state", "error", err, "state", state)
		}
		return
	}
	s.state, s.stateErr = state, errText
}

// errorText is what the mailbox settings screen shows. It contains no credentials: login
// failures are reported without the server's response text.
func errorText(err error) string {
	msg := err.Error()
	if len(msg) > maxErrorText {
		msg = msg[:maxErrorText]
	}
	return msg
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
