package automation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *dbq.Queries
	e    *Engine
	// at overrides the engine clock when set.
	at time.Time

	mailbox, team, agentA, agentB, admin, readonly pgtype.UUID
	labelBilling, labelVIP                         pgtype.UUID
}

func (f *fixture) now() time.Time {
	if f.at.IsZero() {
		return time.Now()
	}
	return f.at
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, pool: testdb.New(t)}
	f.q = dbq.New(f.pool)
	client, err := river.NewClient(riverpgxv5.New(f.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.e = NewEngine(Deps{Pool: f.pool, Jobs: client, Now: f.now})
	f.mailbox = f.id(`INSERT INTO mailboxes (name, email_address, display_name) VALUES ('Support', 'support@shop.example', 'Shop Support') RETURNING id`)
	f.team = f.id(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, f.mailbox, f.team)
	user := func(email, role string) pgtype.UUID {
		return f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ($1, $2, $3, 'x') RETURNING id`, email, email, role)
	}
	f.agentA, f.agentB = user("a@shop.example", "agent"), user("b@shop.example", "agent")
	f.admin, f.readonly = user("admin@shop.example", "admin"), user("ro@shop.example", "readonly")
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($1, $3)`, f.team, f.agentA, f.agentB)
	f.labelBilling = f.id(`INSERT INTO labels (name, color_token) VALUES ('Facturen', 'blue') RETURNING id`)
	f.labelVIP = f.id(`INSERT INTO labels (name, color_token) VALUES ('VIP', 'amber') RETURNING id`)
	return f
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

func (f *fixture) str(sql string, args ...any) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

type inbound struct {
	subject, from, body string
	autoSubmitted, bulk bool
	attachment          bool
	receivedAt          time.Time
}

// conversation stores a conversation with one inbound message, the way ingest does.
func (f *fixture) conversation(m inbound) (conv, msg pgtype.UUID) {
	f.t.Helper()
	if m.from == "" {
		m.from = "jane@acme.example"
	}
	if m.receivedAt.IsZero() {
		m.receivedAt = time.Now()
	}
	conv = f.id(`INSERT INTO conversations (mailbox_id, subject, last_message_at, last_inbound_at, message_count)
		VALUES ($1, $2, $3, $3, 1) RETURNING id`, f.mailbox, m.subject, m.receivedAt)
	msg = f.id(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, from_name, subject,
			received_at, body_text, auto_submitted, is_bulk)
		VALUES ($1, $2, 'email', 'in', 'm-' || uuidv7()::text || '@acme.example', $3, 'Jane', $4, $5, $6, $7, $8) RETURNING id`,
		conv, f.mailbox, m.from, m.subject, m.receivedAt, m.body, m.autoSubmitted, m.bulk)
	if m.attachment {
		f.exec(`INSERT INTO attachments (message_id, filename, size_bytes, sha256, blob_key, disposition)
			VALUES ($1, 'a.pdf', 1, '\x00', 'k', 'attachment')`, msg)
	}
	return conv, msg
}

func (f *fixture) rule(name, trigger, conditions, actions string, stop bool) pgtype.UUID {
	f.t.Helper()
	def, err := ValidateRule(RuleDef{
		Name: name, Trigger: trigger, Conditions: json.RawMessage(conditions), Actions: json.RawMessage(actions),
		StopProcessing: stop, Enabled: true,
	})
	if err != nil {
		f.t.Fatalf("rule %s: %v", name, err)
	}
	return f.rawRule(def)
}

func (f *fixture) rawRule(def RuleDef) pgtype.UUID {
	f.t.Helper()
	r, err := f.q.AutoInsertRule(context.Background(), dbq.AutoInsertRuleParams{
		Name: def.Name, MailboxID: def.MailboxID, Trigger: def.Trigger, IdleHours: idleHours(def.IdleHours),
		Conditions: def.Conditions, Actions: def.Actions, StopProcessing: def.StopProcessing, Enabled: def.Enabled,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return r.ID
}

func idleHours(h int) pgtype.Int4 {
	if h == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(h), Valid: true}
}

func (f *fixture) evaluate(trigger string, conv, msg pgtype.UUID) {
	f.t.Helper()
	if err := f.e.Evaluate(context.Background(), Trigger{ConversationID: conv, Name: trigger, MessageID: msg}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) created(conv, msg pgtype.UUID) {
	f.evaluate(jobs.TriggerConversationCreated, conv, msg)
}

type convState struct {
	Status, Priority string
	Assignee, Team   pgtype.UUID
	Snoozed          bool
	Labels           []string
}

func (f *fixture) state(conv pgtype.UUID) convState {
	f.t.Helper()
	var s convState
	err := f.pool.QueryRow(context.Background(),
		`SELECT status, priority, assignee_user_id, assignee_team_id, snoozed_until IS NOT NULL FROM conversations WHERE id = $1`, conv).
		Scan(&s.Status, &s.Priority, &s.Assignee, &s.Team, &s.Snoozed)
	if err != nil {
		f.t.Fatal(err)
	}
	rows, err := f.pool.Query(context.Background(),
		`SELECT labels.name FROM conversation_labels JOIN labels ON labels.id = label_id WHERE conversation_id = $1 ORDER BY labels.name`, conv)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			f.t.Fatal(err)
		}
		s.Labels = append(s.Labels, n)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return s
}

type run struct {
	Matched bool
	Actions []ActionResult
	Error   string
	Trigger string
}

func (f *fixture) runs(rule pgtype.UUID) []run {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT matched, actions_applied, error, trigger FROM rule_runs WHERE rule_id = $1 ORDER BY created_at, id`, rule)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []run
	for rows.Next() {
		var r run
		var raw []byte
		if err := rows.Scan(&r.Matched, &raw, &r.Error, &r.Trigger); err != nil {
			f.t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &r.Actions); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) queuedRules() int {
	return f.count(`SELECT count(*) FROM river_job WHERE kind = 'rules.evaluate'`)
}

func (f *fixture) setStatus(conv pgtype.UUID, status string) {
	f.t.Helper()
	f.exec(`UPDATE conversations SET status = $2, resolved_at = CASE WHEN $2 = 'closed' THEN now() END WHERE id = $1`, conv, status)
}
