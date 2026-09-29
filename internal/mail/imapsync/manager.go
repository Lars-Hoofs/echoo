package imapsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

var errShutdown = errors.New("imap sync manager is shut down")

// connSettings are the mailbox fields a running supervisor was started with; a change means
// the supervisor must be restarted to reconnect with the new values.
type connSettings struct {
	host     string
	port     int32
	tls      string
	username string
	authType string
	secret   string
	markSeen bool
	// oauthConnectedAt changes when an admin reconnects the account, which must restart a
	// supervisor that sleeps after a failed login. Routine token refreshes do not change it.
	oauthConnectedAt string
}

func settingsOf(mb dbq.Mailbox) connSettings {
	return connSettings{
		host:             mb.ImapHost,
		port:             mb.ImapPort,
		tls:              mb.ImapTls,
		username:         mb.ImapUsername,
		authType:         mb.AuthType,
		secret:           string(mb.ImapSecretEnc),
		markSeen:         mb.MarkSeen,
		oauthConnectedAt: mb.OauthConnectedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

type running struct {
	settings connSettings
	cancel   context.CancelFunc
	done     chan struct{}
}

// Manager runs one Supervisor per enabled IMAP mailbox.
type Manager struct {
	deps Deps

	runCtx    context.Context
	runCancel context.CancelFunc

	mu       sync.Mutex
	shutdown bool
	running  map[[16]byte]*running
}

func NewManager(deps Deps) *Manager {
	runCtx, cancel := context.WithCancel(context.Background())
	return &Manager{deps: deps, runCtx: runCtx, runCancel: cancel, running: make(map[[16]byte]*running)}
}

// Reload starts supervisors for new mailboxes, restarts those whose connection settings
// changed and stops those that were disabled or deleted. ctx bounds the reload itself; the
// supervisors outlive it and stop on Shutdown.
func (m *Manager) Reload(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return errShutdown
	}

	q := dbq.New(m.deps.Pool)
	mailboxes, err := q.ListImapSyncMailboxes(ctx)
	if err != nil {
		return fmt.Errorf("list mailboxes: %w", err)
	}
	wanted := make(map[[16]byte]dbq.Mailbox, len(mailboxes))
	for _, mb := range mailboxes {
		wanted[mb.ID.Bytes] = mb
	}

	var errs []error
	for id, r := range m.running {
		mb, ok := wanted[id]
		if ok && r.settings == settingsOf(mb) {
			continue
		}
		if err := m.stop(ctx, r); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(m.running, id)
		if !ok {
			if err := q.MarkMailboxSyncDisabled(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
				errs = append(errs, fmt.Errorf("mark mailbox disabled: %w", err))
			}
		}
	}
	for id, mb := range wanted {
		if _, ok := m.running[id]; !ok {
			m.running[id] = m.start(mb)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) start(mb dbq.Mailbox) *running {
	ctx, cancel := context.WithCancel(m.runCtx)
	r := &running{settings: settingsOf(mb), cancel: cancel, done: make(chan struct{})}
	sup := NewSupervisor(m.deps, mb.ID)
	go func() {
		defer close(r.done)
		sup.Run(ctx)
	}()
	return r
}

func (m *Manager) stop(ctx context.Context, r *running) error {
	r.cancel()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop supervisor: %w", ctx.Err())
	}
}

// Shutdown stops all supervisors, which log out and release their locks, and waits for them
// until ctx expires. Supervisors that are still busy are force-closed after
// Config.LogoutTimeout regardless.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shutdown = true
	m.runCancel()
	var errs []error
	for id, r := range m.running {
		if err := m.stop(ctx, r); err != nil {
			errs = append(errs, err)
			continue
		}
		delete(m.running, id)
	}
	return errors.Join(errs...)
}
