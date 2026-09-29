package imapsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"echoo/internal/netguard"
)

func TestInitialSync(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 120)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	h.run(h.supervisor())

	h.waitRaw(120)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 120 })
	if got := h.jobCount(); got != 120 {
		t.Fatalf("parse jobs = %d, want 120", got)
	}
	if got := h.count("SELECT count(DISTINCT raw_messages.id) FROM raw_messages JOIN river_job ON river_job.args->>'raw_message_id' = raw_messages.id::text"); got != 120 {
		t.Fatalf("jobs matching raw messages = %d, want 120", got)
	}
	if got := h.store.size(); got != 120 {
		t.Fatalf("blobs = %d, want 120", got)
	}
	h.waitState(stateConnected)
	if got := h.count("SELECT count(*) FROM mailboxes WHERE id = $1 AND last_synced_at IS NOT NULL", h.mailboxID); got != 1 {
		t.Fatal("last_synced_at not set")
	}
	if uv, _ := h.folder(); uv == 0 {
		t.Fatal("uidvalidity not recorded")
	}
}

func TestRestartResumesFromCursor(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 5)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	stop := h.run(h.supervisor())
	h.waitRaw(5)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 5 })
	stop()

	srv.deliver(t, 3)
	h.run(h.supervisor())
	h.waitRaw(8)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 8 })
	if got := h.jobCount(); got != 8 {
		t.Fatalf("parse jobs = %d, want 8", got)
	}
	if got := h.store.puts.Load(); got != 8 {
		t.Fatalf("blob writes = %d, want 8 (no refetch of stored messages)", got)
	}
}

func TestCrashBetweenPutAndCommitRefetches(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 3)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	sup := h.supervisor()
	var failed atomic.Bool
	sup.afterPut = func(uid imap.UID) error {
		if uid == 2 && failed.CompareAndSwap(false, true) {
			return errors.New("simulated crash after Put")
		}
		return nil
	}
	h.run(sup)

	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 3 })
	if !failed.Load() {
		t.Fatal("hook never fired")
	}
	if got := h.rawCount(); got != 3 {
		t.Fatalf("raw messages = %d, want 3", got)
	}
	if got := h.jobCount(); got != 3 {
		t.Fatalf("parse jobs = %d, want 3", got)
	}
	if got := h.store.puts.Load(); got <= 3 {
		t.Fatalf("blob writes = %d, want a refetch (> 3)", got)
	}
}

func TestCursorDoesNotAdvanceOnFailedBatch(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 3)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	sup := h.supervisor()
	sup.afterPut = func(imap.UID) error { return errors.New("always failing") }
	h.run(sup)

	h.waitState(stateBackoff)
	time.Sleep(200 * time.Millisecond)
	if _, last := h.folder(); last != 0 {
		t.Fatalf("cursor = %d, want 0", last)
	}
	if got := h.rawCount(); got != 0 {
		t.Fatalf("raw messages = %d, want 0", got)
	}
}

func TestUIDValidityChangeResyncs(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 3)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	stop := h.run(h.supervisor())
	h.waitRaw(3)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 3 })
	oldValidity, _ := h.folder()
	stop()

	if err := srv.user.Delete(inboxName); err != nil {
		t.Fatal(err)
	}
	if err := srv.user.Create(inboxName, nil); err != nil {
		t.Fatal(err)
	}
	srv.deliver(t, 2)
	h.run(h.supervisor())

	h.waitRaw(5)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 2 })
	if uv, _ := h.folder(); uv == oldValidity {
		t.Fatal("uidvalidity was not updated")
	}
	if got := h.jobCount(); got != 5 {
		t.Fatalf("parse jobs = %d, want 5", got)
	}
}

func TestIdlePicksUpNewMail(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	h.run(h.supervisor())
	h.waitState(stateConnected)
	srv.waitIdling(t)

	srv.deliver(t, 1)
	h.waitRaw(1)
	srv.waitIdling(t)
	srv.deliver(t, 2)
	h.waitRaw(3)
}

func TestIdleRenewal(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, func(c *Config) { c.IdleRestart = 30 * time.Millisecond })
	h.run(h.supervisor())
	h.waitState(stateConnected)
	srv.waitIdleEntries(t, 5)
	srv.waitIdling(t)
	srv.deliver(t, 1)
	h.waitRaw(1)
	if state, _ := h.syncState(); state != stateConnected {
		t.Fatalf("state = %s, want connected", state)
	}
}

type failingIdle struct {
	imapserver.Session
	calls *atomic.Int32
}

func (f failingIdle) Idle(*imapserver.UpdateWriter, <-chan struct{}) error {
	f.calls.Add(1)
	return errors.New("idle not working")
}

func failingIdleServer(t *testing.T) (*testServer, *atomic.Int32) {
	var calls atomic.Int32
	srv := startServer(t, TLSImplicit, func(s imapserver.Session) imapserver.Session { return failingIdle{s, &calls} })
	return srv, &calls
}

func TestPollingFallbackAfterIdleFailures(t *testing.T) {
	srv, _ := failingIdleServer(t)
	h := newHarness(t, srv, mailboxOpts{}, func(c *Config) {
		c.IdleRestart = 20 * time.Millisecond
		c.PollInterval = 30 * time.Millisecond
		c.IdleRetryInterval = time.Hour
	})
	h.run(h.supervisor())

	h.waitState(statePolling)
	srv.deliver(t, 2)
	h.waitRaw(2)
}

func TestIdleRetriedAfterPollingPeriod(t *testing.T) {
	srv, calls := failingIdleServer(t)
	h := newHarness(t, srv, mailboxOpts{}, func(c *Config) {
		c.IdleRestart = 20 * time.Millisecond
		c.PollInterval = 20 * time.Millisecond
		c.IdleRetryInterval = 100 * time.Millisecond
	})
	h.run(h.supervisor())
	h.waitState(statePolling)
	waitFor(t, "idle retry", func() bool { return calls.Load() > int32(defaultIdleMaxFailures) })
}

func TestSeenFlagUntouchedByDefault(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 3)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	h.run(h.supervisor())
	h.waitRaw(3)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 3 })

	for i, flags := range srv.flags(t) {
		if slices.Contains(flags, imap.FlagSeen) {
			t.Fatalf("message %d was marked \\Seen", i+1)
		}
	}
}

func TestMarkSeenWhenConfigured(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 3)
	h := newHarness(t, srv, mailboxOpts{markSeen: true}, nil)
	h.run(h.supervisor())
	h.waitRaw(3)

	waitFor(t, "\\Seen on all messages", func() bool {
		flags := srv.flags(t)
		return len(flags) == 3 && !slices.ContainsFunc(flags, func(f []imap.Flag) bool { return !slices.Contains(f, imap.FlagSeen) })
	})
}

func TestAuthFailureStopsFastRetries(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 1)
	h := newHarness(t, srv, mailboxOpts{password: "wrong-password"}, nil)
	h.run(h.supervisor())

	h.waitState(stateAuthFailed)
	state, msg := h.syncState()
	if state != stateAuthFailed || msg == "" {
		t.Fatalf("state = %q error = %q", state, msg)
	}
	if strings.Contains(msg, "wrong-password") || strings.Contains(msg, testPassword) {
		t.Fatalf("sync_error leaks credentials: %q", msg)
	}
	time.Sleep(200 * time.Millisecond)
	if state, _ := h.syncState(); state != stateAuthFailed {
		t.Fatalf("state = %q, want auth_failed to stick (retry interval is an hour)", state)
	}
	if got := h.rawCount(); got != 0 {
		t.Fatalf("raw messages = %d, want 0", got)
	}
}

func TestBackoffOnUnreachableServer(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	if _, err := h.pool.Exec(context.Background(), "UPDATE mailboxes SET imap_port = 1 WHERE id = $1", h.mailboxID); err != nil {
		t.Fatal(err)
	}
	h.run(h.supervisor())
	h.waitState(stateBackoff)
	if _, msg := h.syncState(); msg == "" {
		t.Fatal("sync_error is empty")
	}
}

func TestAdvisoryLockAllowsOnlyOneSupervisor(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	stopFirst := h.run(h.supervisor())
	h.waitState(stateConnected)

	if h.lockFree() {
		t.Fatal("lock is free while the first supervisor runs")
	}

	h.run(h.supervisor())
	srv.waitIdling(t)
	srv.deliver(t, 1)
	h.waitRaw(1)
	time.Sleep(100 * time.Millisecond)
	if got := h.rawCount(); got != 1 {
		t.Fatalf("raw messages = %d, want 1", got)
	}

	entries := srv.idle.entries.Load()
	stopFirst()
	srv.waitIdleEntries(t, entries+1)
	srv.waitIdling(t)
	srv.deliver(t, 1)
	h.waitRaw(2)
	if got := h.jobCount(); got != 2 {
		t.Fatalf("parse jobs = %d, want 2", got)
	}
}

func TestLockReleasedOnStop(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	stop := h.run(h.supervisor())
	h.waitState(stateConnected)
	stop()
	if !h.lockFree() {
		t.Fatal("lock still held after the supervisor stopped")
	}
}

func TestSecondSupervisorWaitsWithoutConnecting(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 1)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	l, ok, err := acquireLock(context.Background(), h.pool, lockKey(h.mailboxID))
	if err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	stop := h.run(h.supervisor())
	time.Sleep(200 * time.Millisecond)
	if got := h.rawCount(); got != 0 {
		t.Fatalf("supervisor synced without holding the lock (%d messages)", got)
	}
	if err := l.release(time.Second); err != nil {
		t.Fatal(err)
	}
	h.waitRaw(1)
	stop()
}

func TestStopWhileIdleLogsOutCleanly(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	stop := h.run(h.supervisor())
	h.waitState(stateConnected)
	srv.waitIdling(t)

	start := time.Now()
	stop()
	if time.Since(start) > 5*time.Second {
		t.Fatalf("stop took %s", time.Since(start))
	}
}

func TestOversizedMessageIsSkipped(t *testing.T) {
	const limit = 2000
	srv := startServer(t, TLSImplicit, nil)
	huge := "From: bulk@example.org\r\nSubject: huge\r\n\r\n" + strings.Repeat("x", 10*limit)
	srv.deliverRaw(t, []byte(huge))
	srv.deliver(t, 1)
	h := newHarness(t, srv, mailboxOpts{}, func(c *Config) { c.MaxMessageBytes = limit })
	h.run(h.supervisor())

	h.waitRaw(2)
	waitFor(t, "cursor", func() bool { _, last := h.folder(); return last == 2 })
	if got := h.count("SELECT count(*) FROM raw_messages WHERE parse_status = 'skipped'"); got != 1 {
		t.Fatalf("skipped rows = %d, want 1", got)
	}
	var reason string
	var size int
	err := h.pool.QueryRow(context.Background(), "SELECT parse_error, size_bytes FROM raw_messages WHERE parse_status = 'skipped'").Scan(&reason, &size)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("message too large: %d bytes", len(huge)); reason != want || size != len(huge) {
		t.Fatalf("reason = %q size = %d, want %q / %d", reason, size, want, len(huge))
	}
	if got := h.jobCount(); got != 1 {
		t.Fatalf("parse jobs = %d, want 1 (only the normal message)", got)
	}
	if got := h.count("SELECT count(*) FROM raw_messages WHERE parse_status = 'pending'"); got != 1 {
		t.Fatalf("pending rows = %d, want 1", got)
	}
	var key string
	if err := h.pool.QueryRow(context.Background(), "SELECT blob_key FROM raw_messages WHERE parse_status = 'skipped'").Scan(&key); err != nil {
		t.Fatal(err)
	}
	rc, err := h.store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !strings.Contains(string(stored), "Subject: huge") || strings.Contains(string(stored), "xxxx") {
		t.Fatalf("placeholder blob should hold only the headers, got %d bytes", len(stored))
	}
}

func TestBodyReadIsCappedWhenServerUnderstatesSize(t *testing.T) {
	const limit = 2000
	srv := startServer(t, TLSImplicit, nil)
	srv.deliverRaw(t, []byte("Subject: liar\r\n\r\n"+strings.Repeat("y", 10*limit)))
	h := newHarness(t, srv, mailboxOpts{}, func(c *Config) { c.MaxMessageBytes = limit })
	c, _, err := openInbox(context.Background(), srv.connConfig(TLSImplicit), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer logout(c, time.Second)

	// Calling fetchBodies directly skips the RFC822.SIZE pre-check, as if the server had lied.
	y := &inboxSync{s: h.supervisor(), c: c}
	oversized := map[imap.UID]int64{}
	if err := y.fetchBodies(context.Background(), []imap.UID{1}, oversized); err != nil {
		t.Fatal(err)
	}
	if _, ok := oversized[1]; !ok {
		t.Fatal("overflowing body was not reported as oversized")
	}
	if got := h.store.puts.Load(); got != 0 {
		t.Fatalf("blob writes = %d, want 0", got)
	}
}

func TestInternalDestinationWithoutOptInIsNeverContacted(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{blockInternal: true}, nil)
	h.run(h.supervisor())

	h.waitState(stateBackoff)
	if _, msg := h.syncState(); !strings.Contains(msg, netguard.ErrInternal.Error()) {
		t.Fatalf("sync_error = %q", msg)
	}
	if n := srv.conns.Load(); n != 0 {
		t.Fatalf("server saw %d connections, want 0", n)
	}
}
