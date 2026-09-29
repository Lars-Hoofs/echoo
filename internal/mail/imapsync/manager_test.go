package imapsync

import (
	"context"
	"testing"
	"time"
)

func TestManagerReloadStartsAndStops(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	srv.deliver(t, 2)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	m := NewManager(h.deps)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	ctx := context.Background()

	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitRaw(2)
	h.waitState(stateConnected)

	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	srv.waitIdling(t)
	srv.deliver(t, 1)
	h.waitRaw(3)

	if _, err := h.pool.Exec(ctx, "UPDATE mailboxes SET disabled_at = now() WHERE id = $1", h.mailboxID); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitState("disabled")
	if !h.lockFree() {
		t.Fatal("lock still held after the mailbox was disabled")
	}
	srv.deliver(t, 1)
	time.Sleep(200 * time.Millisecond)
	if got := h.rawCount(); got != 3 {
		t.Fatalf("raw messages = %d, want 3 (disabled mailbox must not sync)", got)
	}
}

func TestManagerReloadRestartsOnSettingsChange(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{password: "wrong"}, nil)
	m := NewManager(h.deps)
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	ctx := context.Background()

	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitState(stateAuthFailed)

	if _, err := h.pool.Exec(ctx, "UPDATE mailboxes SET imap_secret_enc = $2 WHERE id = $1", h.mailboxID, h.encrypt("wrong2")); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	srv.deliver(t, 1)
	if _, err := h.pool.Exec(ctx, "UPDATE mailboxes SET imap_secret_enc = $2 WHERE id = $1", h.mailboxID, h.encrypt(testPassword)); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h.waitRaw(1)
	h.waitState(stateConnected)
}

func TestManagerShutdownReleasesLocks(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	m := NewManager(h.deps)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitState(stateConnected)

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.lockFree() {
		t.Fatal("lock still held after shutdown")
	}
	if err := m.Reload(context.Background()); err == nil {
		t.Fatal("Reload succeeded after Shutdown")
	}
}

func TestManagerShutdownRespectsContext(t *testing.T) {
	srv := startServer(t, TLSImplicit, nil)
	h := newHarness(t, srv, mailboxOpts{}, nil)
	m := NewManager(h.deps)
	if err := m.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.waitState(stateConnected)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// An already-expired context may or may not win the race against a fast stop; either way
	// Shutdown must return promptly and the supervisors must finish on their own afterwards.
	_ = m.Shutdown(ctx)
	waitFor(t, "lock release", h.lockFree)
}
