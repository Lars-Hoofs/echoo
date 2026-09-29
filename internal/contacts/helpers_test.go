package contacts

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/db/dbq"
	"echoo/internal/storage"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	q     *dbq.Queries
	store *storage.FS
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := testdb.New(t)
	return &env{t: t, pool: pool, q: dbq.New(pool), store: store}
}

func (e *env) id(sql string, args ...any) pgtype.UUID {
	e.t.Helper()
	var id pgtype.UUID
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) user(role string) dbq.User {
	e.t.Helper()
	u, err := e.q.CreateUser(context.Background(), dbq.CreateUserParams{
		Email: fmt.Sprintf("%s-%d@example.com", role, time.Now().UnixNano()), Name: role, Role: role, PasswordHash: "x",
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) mailbox(name, address string) pgtype.UUID {
	return e.id(`INSERT INTO mailboxes (name, email_address) VALUES ($1, $2) RETURNING id`, name, address)
}

func (e *env) contact(name string, createdBy pgtype.UUID, emails ...string) pgtype.UUID {
	e.t.Helper()
	id := e.id(`INSERT INTO contacts (name, created_by) VALUES ($1, $2) RETURNING id`, name, createdBy)
	for i, email := range emails {
		e.exec(`INSERT INTO contact_addresses (contact_id, email, is_primary) VALUES ($1, $2, $3)`, id, email, i == 0)
	}
	return id
}

func (e *env) conversation(mailbox, contact pgtype.UUID, subject, status string) pgtype.UUID {
	return e.id(`INSERT INTO conversations (mailbox_id, contact_id, subject, subject_normalized, status, preview)
		VALUES ($1, $2, $3, $3, $4, $3) RETURNING id`, mailbox, contact, subject, status)
}

type msgOpt struct {
	direction, fromAddr, fromName, subject, body string
	to                                           []string
	raw                                          []byte
	attachments                                  map[string][]byte // filename -> content
}

// message inserts a message with an optional raw copy and attachments stored in the blob store.
func (e *env) message(conv, mailbox pgtype.UUID, o msgOpt) pgtype.UUID {
	e.t.Helper()
	if o.direction == "" {
		o.direction = "in"
	}
	to := []map[string]string{}
	for _, a := range o.to {
		to = append(to, map[string]string{"name": "", "address": a})
	}
	toJSON, err := json.Marshal(to)
	if err != nil {
		e.t.Fatal(err)
	}
	var raw pgtype.UUID
	if o.raw != nil {
		key, sum, err := e.store.Put(context.Background(), o.raw)
		if err != nil {
			e.t.Fatal(err)
		}
		raw = e.id(`INSERT INTO raw_messages (mailbox_id, source, sha256, size_bytes, blob_key, uidvalidity, uid)
			VALUES ($1, 'imap', $2, $3, $4, 1, $5) RETURNING id`, mailbox, sum, len(o.raw), key, time.Now().UnixNano())
	}
	id := e.id(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, raw_message_id, message_id_hash,
			from_addr, from_name, to_addrs, subject, body_text, body_html, message_id_header)
		VALUES ($1, $2, 'email', $3, $4, sha256($5::bytea), $6, $7, $8::jsonb, $9, $10, '<p>' || $10 || '</p>', '<id@x>') RETURNING id`,
		conv, mailbox, o.direction, raw, []byte(fmt.Sprint(time.Now().UnixNano())), o.fromAddr, o.fromName, string(toJSON), o.subject, o.body)
	for name, content := range o.attachments {
		key, sum, err := e.store.Put(context.Background(), content)
		if err != nil {
			e.t.Fatal(err)
		}
		e.exec(`INSERT INTO attachments (message_id, filename, sniffed_type, size_bytes, sha256, blob_key, disposition)
			VALUES ($1, $2, 'application/pdf', $3, $4, $5, 'attachment')`, id, name, len(content), sum, key)
	}
	e.exec(`UPDATE conversations SET message_count = message_count + 1 WHERE id = $1`, conv)
	return id
}

func (e *env) def(entity, key, typ string, options ...string) dbq.CustomAttributeDef {
	e.t.Helper()
	if options == nil {
		options = []string{}
	}
	d, err := e.q.InsertAttributeDef(context.Background(), dbq.InsertAttributeDefParams{
		Entity: entity, Key: key, Label: key, Type: typ, Options: options,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return dbq.CustomAttributeDef{ID: d.ID, Entity: d.Entity, Key: d.Key, Label: d.Label, Type: d.Type, Options: d.Options}
}

func (e *env) blobExists(key string) bool {
	e.t.Helper()
	rc, err := e.store.Open(context.Background(), key)
	if err != nil {
		return false
	}
	_ = rc.Close()
	return true
}

func admin() Viewer { return Viewer{Admin: true} }

func names(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}
