package push

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type sent struct {
	device      pgtype.UUID
	web, native Message
}

type fakeSender struct {
	mu   sync.Mutex
	got  []sent
	gone map[string]bool
}

func (f *fakeSender) Send(_ context.Context, d Device, web, native Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone[d.Endpoint] {
		return ErrGone
	}
	f.got = append(f.got, sent{device: d.ID, web: web, native: native})
	return nil
}

type dispatchEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *dbq.Queries
}

func (e *dispatchEnv) id(sql string, args ...any) pgtype.UUID {
	e.t.Helper()
	var id pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *dispatchEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *dispatchEnv) notify(n compose.Notification) {
	e.t.Helper()
	if err := compose.Notify(context.Background(), e.q, n); err != nil {
		e.t.Fatal(err)
	}
}

func TestDispatchPushesOnceToEveryDeviceOfTheUser(t *testing.T) {
	pool := testdb.New(t)
	e := &dispatchEnv{t: t, pool: pool, q: dbq.New(pool)}
	agent := e.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('sanne@example.com', 'Sanne', 'agent', 'x') RETURNING id`)
	other := e.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('joost@example.com', 'Joost', 'agent', 'x') RETURNING id`)
	box := e.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id`)
	team := e.id(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	e.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, box, team)
	e.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team, agent)
	conv := e.id(`INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 'Koelcel 3 staat op 9 graden') RETURNING id`, box)
	phone := e.id(`INSERT INTO push_devices (user_id, kind, endpoint) VALUES ($1, 'apns', 'iphone') RETURNING id`, agent)
	browser := e.id(`INSERT INTO push_devices (user_id, kind, endpoint, p256dh, auth) VALUES ($1, 'webpush', 'https://fcm.googleapis.com/x', '\x04', '\x00') RETURNING id`, agent)
	stale := e.id(`INSERT INTO push_devices (user_id, kind, endpoint) VALUES ($1, 'fcm', 'uninstalled') RETURNING id`, agent)
	e.exec(`INSERT INTO push_devices (user_id, kind, endpoint) VALUES ($1, 'fcm', 'joosts-phone')`, other)

	e.notify(compose.Notification{UserID: agent, Kind: compose.KindAssigned, ConversationID: conv, ActorID: other})
	sender := &fakeSender{gone: map[string]bool{"uninstalled": true}}
	d := NewDispatcher(pool, sender, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := d.Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(sender.got) != 2 {
		t.Fatalf("attempted %d, delivered %+v", n, sender.got)
	}
	for _, s := range sender.got {
		if s.device != phone && s.device != browser {
			t.Errorf("pushed to device %s of another user", s.device)
		}
		if s.web.Title != "Joost wees een gesprek aan je toe" || s.web.Body != "Gesprek #"+number(e, conv)+" · Koelcel 3 staat op 9 graden" {
			t.Errorf("web message = %+v", s.web)
		}
		if s.native.Body != "Gesprek #"+number(e, conv) || s.native.URL != "/inbox/alle/"+conv.String() {
			t.Errorf("native message = %+v; it must not carry the subject", s.native)
		}
	}
	var left int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM push_devices WHERE id = $1`, stale).Scan(&left)
	if left != 0 {
		t.Error("a device the push service called gone was kept")
	}
	var touched int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM push_devices WHERE last_push_at IS NOT NULL`).Scan(&touched)
	if touched != 2 {
		t.Errorf("devices with last_push_at = %d, want 2", touched)
	}

	// Handled rows are never pushed again.
	sender.got = nil
	if n, _ := d.Dispatch(t.Context()); n != 0 || len(sender.got) != 0 {
		t.Errorf("second run pushed %d", n)
	}
}

func number(e *dispatchEnv, conv pgtype.UUID) string {
	var n string
	_ = e.pool.QueryRow(context.Background(), `SELECT number::text FROM conversations WHERE id = $1`, conv).Scan(&n)
	return n
}

func TestDispatchSkipsWhatShouldNotBePushed(t *testing.T) {
	pool := testdb.New(t)
	e := &dispatchEnv{t: t, pool: pool, q: dbq.New(pool)}
	agent := e.id(`INSERT INTO users (email, name, role, password_hash, push_notify_replies) VALUES ('a@example.com', 'A', 'admin', 'x', false) RETURNING id`)
	gone := e.id(`INSERT INTO users (email, name, role, password_hash, deactivated_at) VALUES ('b@example.com', 'B', 'admin', 'x', now()) RETURNING id`)
	// An agent who was taken out of the mailbox's team but is still the assignee.
	outsider := e.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('c@example.com', 'C', 'agent', 'x') RETURNING id`)
	box := e.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Support', 'support@example.com') RETURNING id`)
	conv := e.id(`INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 'Onderwerp') RETURNING id`, box)
	trashed := e.id(`INSERT INTO conversations (mailbox_id, subject, deleted_at) VALUES ($1, 'Weg', now()) RETURNING id`, box)
	for _, u := range []pgtype.UUID{agent, gone, outsider} {
		e.exec(`INSERT INTO push_devices (user_id, kind, endpoint) VALUES ($1, 'apns', $2)`, u, u.String())
	}

	e.notify(compose.Notification{UserID: agent, Kind: compose.KindReply, ConversationID: conv})      // turned off
	e.notify(compose.Notification{UserID: agent, Kind: compose.KindCSAT, ConversationID: conv})       // never pushed
	e.notify(compose.Notification{UserID: agent, Kind: compose.KindMention, ConversationID: trashed}) // in the trash
	e.notify(compose.Notification{UserID: gone, Kind: compose.KindMention, ConversationID: conv})     // deactivated
	e.notify(compose.Notification{UserID: outsider, Kind: compose.KindReply, ConversationID: conv})   // no access to the mailbox
	// Too old: the dispatcher was down when it came in.
	e.exec(`INSERT INTO notifications (user_id, kind, conversation_id, created_at) VALUES ($1, 'mention', $2, now() - interval '2 hours')`, agent, conv)

	sender := &fakeSender{}
	if n, err := NewDispatcher(pool, sender, slog.New(slog.NewTextHandler(io.Discard, nil))).Dispatch(t.Context()); err != nil || n != 0 {
		t.Fatalf("pushed %d (%v): %+v", n, err, sender.got)
	}
	var pending int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM notifications WHERE push_handled_at IS NULL`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d notifications left in the queue", pending)
	}
}

func TestRevokingTheSessionRemovesItsDevices(t *testing.T) {
	pool := testdb.New(t)
	e := &dispatchEnv{t: t, pool: pool, q: dbq.New(pool)}
	user := e.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('a@example.com', 'A', 'agent', 'x') RETURNING id`)
	session := e.id(`INSERT INTO sessions (user_id, token_hash, csrf_token, mfa_pending, idle_expires_at, expires_at)
		VALUES ($1, '\x01', 'c', false, now() - interval '1 day', now() + interval '1 day') RETURNING id`, user)
	e.exec(`INSERT INTO push_devices (user_id, session_id, kind, endpoint) VALUES ($1, $2, 'apns', 'phone')`, user, session)

	// An idle-expired session keeps its device: on-call agents rarely open the app.
	var n int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM push_devices`).Scan(&n)
	if n != 1 {
		t.Fatal("device missing")
	}
	e.exec(`UPDATE sessions SET revoked_at = now() WHERE id = $1`, session)
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM push_devices`).Scan(&n)
	if n != 0 {
		t.Error("logging out left the device registered")
	}
}
