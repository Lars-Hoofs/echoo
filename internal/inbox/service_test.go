package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t                 *testing.T
	pool              *pgxpool.Pool
	svc               *Service
	mailbox, other    pgtype.UUID
	agent, writer, ro pgtype.UUID
	team, foreignTeam pgtype.UUID
	actor             Actor
	conv              pgtype.UUID
}

func (f *fixture) id(sql string, args ...any) pgtype.UUID {
	f.t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, pool: testdb.New(t)}
	f.svc = NewService(f.pool)
	f.mailbox = f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('A', 'a@example.com') RETURNING id`)
	f.other = f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('B', 'b@example.com') RETURNING id`)
	user := func(email, role string) pgtype.UUID {
		return f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ($1, $1, $2, 'x') RETURNING id`, email, role)
	}
	f.agent = user("agent@example.com", "agent")
	f.writer = user("writer@example.com", "agent")
	f.ro = user("ro@example.com", "readonly")
	f.team = f.id(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	f.foreignTeam = f.id(`INSERT INTO teams (name) VALUES ('Other') RETURNING id`)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, f.mailbox, f.team)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, f.other, f.foreignTeam)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($1, $3), ($1, $4)`, f.team, f.agent, f.writer, f.ro)
	f.conv = f.conversation(f.mailbox)
	f.actor = Actor{UserID: f.agent, Read: []pgtype.UUID{f.mailbox}, Write: []pgtype.UUID{f.mailbox}}
	return f
}

func (f *fixture) conversation(mailbox pgtype.UUID) pgtype.UUID {
	return f.id(`INSERT INTO conversations (mailbox_id, subject) VALUES ($1, 's') RETURNING id`, mailbox)
}

func (f *fixture) label(name string) pgtype.UUID {
	return f.id(`INSERT INTO labels (name, color_token) VALUES ($1, 'blue') RETURNING id`, name)
}

func (f *fixture) events(conv pgtype.UUID) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT type FROM conversation_events WHERE conversation_id = $1 ORDER BY created_at, id`, conv)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, typ)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) version(conv pgtype.UUID) int32 {
	f.t.Helper()
	var v int32
	if err := f.pool.QueryRow(context.Background(), `SELECT version FROM conversations WHERE id = $1`, conv).Scan(&v); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func assertEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestStatusLifecycleWritesEventsAndBumpsVersion(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	v, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, StatusClosed)
	if err != nil || v != 2 {
		t.Fatalf("close: version %d, err %v", v, err)
	}
	var resolved pgtype.Timestamptz
	if err := f.pool.QueryRow(ctx, `SELECT resolved_at FROM conversations WHERE id = $1`, f.conv).Scan(&resolved); err != nil || !resolved.Valid {
		t.Fatalf("resolved_at not set: %v %v", resolved, err)
	}
	if _, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, StatusOpen); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT resolved_at FROM conversations WHERE id = $1`, f.conv).Scan(&resolved); err != nil || resolved.Valid {
		t.Fatalf("resolved_at not cleared: %v %v", resolved, err)
	}
	if _, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, StatusWaiting); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, f.events(f.conv), "resolved", "reopened", "status_changed")

	// Setting the current status is a no-op: no event, no version bump.
	before := f.version(f.conv)
	if v, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, StatusWaiting); err != nil || v != before {
		t.Fatalf("noop: version %d want %d, err %v", v, before, err)
	}
	assertEvents(t, f.events(f.conv), "resolved", "reopened", "status_changed")

	if _, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, "bogus"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("invalid status: %v", err)
	}
}

func TestEventRecordsActor(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.SetPriority(t.Context(), f.actor, f.conv, nil, "urgent"); err != nil {
		t.Fatal(err)
	}
	var actor pgtype.UUID
	var data []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT actor_user_id, data FROM conversation_events WHERE conversation_id = $1`, f.conv).Scan(&actor, &data); err != nil {
		t.Fatal(err)
	}
	if actor != f.agent {
		t.Fatalf("actor = %v, want %v", actor, f.agent)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil || got["from"] != "none" || got["to"] != "urgent" {
		t.Fatalf("data = %s, %v", data, err)
	}
	if _, err := f.svc.SetPriority(t.Context(), f.actor, f.conv, nil, "extreme"); !errors.Is(err, ErrInvalidPriority) {
		t.Fatalf("invalid priority: %v", err)
	}
}

func TestVersionConflict(t *testing.T) {
	f := newFixture(t)
	stale := int32(1)
	if _, err := f.svc.SetPriority(t.Context(), f.actor, f.conv, &stale, "high"); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.SetPriority(t.Context(), f.actor, f.conv, &stale, "low")
	var conflict *VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current != 2 {
		t.Fatalf("err = %v, want conflict at version 2", err)
	}
	assertEvents(t, f.events(f.conv), "priority_changed")
}

func TestAssignRequiresWriteAccessToMailbox(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, f.writer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, f.ro); !errors.Is(err, ErrAssigneeNoAccess) {
		t.Fatalf("readonly: err = %v", err)
	}
	outsider := f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('out@example.com', 'o', 'agent', 'x') RETURNING id`)
	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, outsider); !errors.Is(err, ErrAssigneeNoAccess) {
		t.Fatalf("outsider: err = %v", err)
	}
	f.exec(`UPDATE users SET deactivated_at = now() WHERE id = $1`, f.agent)
	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, f.agent); !errors.Is(err, ErrAssigneeNoAccess) {
		t.Fatalf("deactivated: err = %v", err)
	}
	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, pgtype.UUID{}); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, f.events(f.conv), "assigned", "unassigned")
}

func TestAssignTeamRequiresMailboxAccess(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.AssignTeam(t.Context(), f.actor, f.conv, nil, f.foreignTeam); !errors.Is(err, ErrTeamNoAccess) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.svc.AssignTeam(t.Context(), f.actor, f.conv, nil, f.team); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.AssignTeam(t.Context(), f.actor, f.conv, nil, pgtype.UUID{}); err != nil {
		t.Fatal(err)
	}
	assertEvents(t, f.events(f.conv), "team_assigned", "team_assigned")
}

func TestLabelsAreIdempotentAndReplaceable(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	a, b := f.label("a"), f.label("b")
	if _, err := f.svc.AddLabels(ctx, f.actor, f.conv, nil, []pgtype.UUID{a, b}); err != nil {
		t.Fatal(err)
	}
	before := f.version(f.conv)
	if v, err := f.svc.AddLabels(ctx, f.actor, f.conv, nil, []pgtype.UUID{a}); err != nil || v != before {
		t.Fatalf("re-adding: version %d want %d, err %v", v, before, err)
	}
	if _, err := f.svc.RemoveLabels(ctx, f.actor, f.conv, nil, []pgtype.UUID{a}); err != nil {
		t.Fatal(err)
	}
	set := []pgtype.UUID{a}
	if _, err := f.svc.Apply(ctx, f.actor, f.conv, nil, Change{SetLabels: &set}); err != nil {
		t.Fatal(err)
	}
	var names []string
	rows, err := f.pool.Query(ctx, `SELECT labels.name FROM conversation_labels JOIN labels ON labels.id = label_id WHERE conversation_id = $1`, f.conv)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	if fmt.Sprint(names) != "[a]" {
		t.Fatalf("labels = %v", names)
	}
	assertEvents(t, f.events(f.conv), "labeled", "labeled", "unlabeled", "labeled", "unlabeled")

	missing := f.label("tmp")
	f.exec(`DELETE FROM labels WHERE id = $1`, missing)
	if _, err := f.svc.AddLabels(ctx, f.actor, f.conv, nil, []pgtype.UUID{missing}); !errors.Is(err, ErrUnknownLabel) {
		t.Fatalf("unknown label: %v", err)
	}
}

func TestSnoozeAndWake(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.svc.Snooze(ctx, f.actor, f.conv, nil, time.Now().Add(-time.Minute)); !errors.Is(err, ErrInvalidSnooze) {
		t.Fatalf("past snooze: %v", err)
	}
	if _, err := f.svc.Snooze(ctx, f.actor, f.conv, nil, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Not due yet: nothing wakes.
	if n, err := f.svc.WakeSnoozed(ctx); err != nil || n != 0 {
		t.Fatalf("wake early: %d %v", n, err)
	}
	f.exec(`UPDATE conversations SET snoozed_until = now() - interval '1 minute' WHERE id = $1`, f.conv)
	before := f.version(f.conv)
	if n, err := f.svc.WakeSnoozed(ctx); err != nil || n != 1 {
		t.Fatalf("wake: %d %v", n, err)
	}
	var until pgtype.Timestamptz
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT snoozed_until, status FROM conversations WHERE id = $1`, f.conv).Scan(&until, &status); err != nil {
		t.Fatal(err)
	}
	if until.Valid || status != StatusOpen || f.version(f.conv) != before+1 {
		t.Fatalf("after wake: until=%v status=%s version=%d", until, status, f.version(f.conv))
	}
	assertEvents(t, f.events(f.conv), "snoozed", "woke")

	if _, err := f.svc.Snooze(ctx, f.actor, f.conv, nil, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Unsnooze(ctx, f.actor, f.conv, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetStatus(ctx, f.actor, f.conv, nil, StatusClosed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Snooze(ctx, f.actor, f.conv, nil, time.Now().Add(time.Hour)); !errors.Is(err, ErrNotSnoozable) {
		t.Fatalf("snooze closed: %v", err)
	}
}

func TestClosingEndsSnooze(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Snooze(t.Context(), f.actor, f.conv, nil, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetStatus(t.Context(), f.actor, f.conv, nil, StatusClosed); err != nil {
		t.Fatal(err)
	}
	var until pgtype.Timestamptz
	if err := f.pool.QueryRow(t.Context(), `SELECT snoozed_until FROM conversations WHERE id = $1`, f.conv).Scan(&until); err != nil || until.Valid {
		t.Fatalf("snooze survived closing: %v %v", until, err)
	}
}

func TestScopeIsEnforcedInSQL(t *testing.T) {
	f := newFixture(t)
	foreign := f.conversation(f.other)
	if _, err := f.svc.SetStatus(t.Context(), f.actor, foreign, nil, StatusClosed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out of scope: %v", err)
	}
	readOnly := Actor{UserID: f.agent, Read: []pgtype.UUID{f.mailbox}}
	if _, err := f.svc.SetStatus(t.Context(), readOnly, f.conv, nil, StatusClosed); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read-only mailbox: %v", err)
	}
	assertEvents(t, f.events(foreign))
	assertEvents(t, f.events(f.conv))
}

func TestBulkReportsPerConversation(t *testing.T) {
	f := newFixture(t)
	second := f.conversation(f.mailbox)
	foreign := f.conversation(f.other)
	closed := StatusClosed
	res, err := f.svc.Bulk(t.Context(), f.actor, []pgtype.UUID{f.conv, foreign, second, f.conv}, Change{Status: &closed})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("results = %d, want 3 (duplicate dropped)", len(res))
	}
	if res[0].Err != nil || !errors.Is(res[1].Err, ErrNotFound) || res[2].Err != nil {
		t.Fatalf("results = %+v", res)
	}
	assertEvents(t, f.events(second), "resolved")
	assertEvents(t, f.events(foreign))

	tooMany := make([]pgtype.UUID, MaxBulk+1)
	if _, err := f.svc.Bulk(t.Context(), f.actor, tooMany, Change{Status: &closed}); !errors.Is(err, ErrTooManyIDs) {
		t.Fatalf("too many: %v", err)
	}
}

func TestNotifyIsEmittedOnCommitWithoutContent(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+EventChannel); err != nil {
		t.Fatal(err)
	}

	version, err := f.svc.SetPriority(ctx, f.actor, f.conv, nil, "high")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := conn.Conn().WaitForNotification(waitCtx)
	if err != nil {
		t.Fatalf("no notification: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(n.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": EventConversationUpdated, "conversation_id": f.conv.String(),
		"mailbox_id": f.mailbox.String(), "version": float64(version),
	}
	if len(payload) != len(want) {
		t.Fatalf("payload = %v", payload)
	}
	for k, v := range want {
		if payload[k] != v {
			t.Fatalf("payload[%s] = %v, want %v", k, payload[k], v)
		}
	}

	// A change rejected by the domain rules emits nothing.
	if _, err := f.svc.Assign(ctx, f.actor, f.conv, nil, f.ro); !errors.Is(err, ErrAssigneeNoAccess) {
		t.Fatal(err)
	}
	quick, cancelQuick := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelQuick()
	if n, err := conn.Conn().WaitForNotification(quick); err == nil {
		t.Fatalf("unexpected notification %s", n.Payload)
	}
}

func TestWakeEmitsNotify(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.exec(`UPDATE conversations SET snoozed_until = now() - interval '1 second' WHERE id = $1`, f.conv)
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+EventChannel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.WakeSnoozed(ctx); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := conn.Conn().WaitForNotification(waitCtx); err != nil {
		t.Fatalf("no notification: %v", err)
	}
}
