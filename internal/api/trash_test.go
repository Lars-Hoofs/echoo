package api

import (
	"context"
	"encoding/json"
	"testing"

	"echoo/internal/storage"
)

type trashListResponse struct {
	Conversations []struct {
		ID        string                     `json:"id"`
		Subject   string                     `json:"subject"`
		DeletedBy *struct{ ID, Name string } `json:"deleted_by"`
		Mailbox   struct{ ID, Name string }
	} `json:"conversations"`
	NextCursor *string `json:"next_cursor"`
}

func newTrashFixture(t *testing.T) *inboxFixture {
	t.Helper()
	f := newInboxFixture(t)
	store, err := storage.NewFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.h.srv.store = store
	return f
}

func (f *inboxFixture) trash(c *client, query string) trashListResponse {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/trash"+query, nil)
	expect(f.h.t, r, 200, "")
	var out trashListResponse
	if err := json.Unmarshal(r.raw, &out); err != nil {
		f.h.t.Fatal(err)
	}
	return out
}

func trashSubjects(l trashListResponse) []string {
	out := make([]string, len(l.Conversations))
	for i, c := range l.Conversations {
		out[i] = c.Subject
	}
	return out
}

// results maps id to "ok" or the error code of a bulk-shaped response.
func results(t *testing.T, r response) map[string]string {
	t.Helper()
	expect(t, r, 200, "")
	out := map[string]string{}
	list, _ := r.body["results"].([]any)
	for _, raw := range list {
		m := raw.(map[string]any)
		if m["ok"] == true {
			out[m["id"].(string)] = "ok"
		} else {
			out[m["id"].(string)] = m["code"].(string)
		}
	}
	return out
}

func TestTrashAndRestore(t *testing.T) {
	f := newTrashFixture(t)
	id := f.conversation("hello", convOpt{mailbox: f.mailboxA})
	other := f.conversation("stays", convOpt{mailbox: f.mailboxA})

	got := results(t, f.agent.do("POST", "/api/v1/trash", map[string]any{"ids": []string{id, id, "nope"}}))
	if len(got) != 2 || got[id] != "ok" || got["nope"] != "invalid_id" {
		t.Fatalf("trash results = %v", got)
	}
	if s := subjects(f.list(f.agent, "")); s != "stays" {
		t.Fatalf("open list = %q", s)
	}
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+id, nil), 404, "not_found")
	expect(t, f.patch(f.agent, id, map[string]any{"status": "closed"}), 404, "not_found")
	l := f.trash(f.agent, "")
	if len(l.Conversations) != 1 || l.Conversations[0].ID != id || l.Conversations[0].DeletedBy == nil || l.Conversations[0].DeletedBy.ID != f.agentID {
		t.Fatalf("trash = %+v", l)
	}
	if got := results(t, f.agent.do("POST", "/api/v1/trash", map[string]any{"ids": []string{id}})); got[id] != "not_found" {
		t.Fatalf("trashing twice = %v", got)
	}

	if got := results(t, f.agent.do("POST", "/api/v1/trash/restore", map[string]any{"ids": []string{id, other}})); got[id] != "ok" || got[other] != "not_found" {
		t.Fatalf("restore results = %v", got)
	}
	if s := subjects(f.list(f.agent, "")); s != "hello,stays" && s != "stays,hello" {
		t.Fatalf("open list after restore = %q", s)
	}
	if n := len(f.trash(f.agent, "").Conversations); n != 0 {
		t.Fatalf("trash after restore has %d", n)
	}
	deleted := f.h.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'deleted'`, id)
	restored := f.h.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND type = 'restored'`, id)
	if deleted != 1 || restored != 1 {
		t.Fatalf("events deleted = %d, restored = %d", deleted, restored)
	}
}

func TestTrashAuthorization(t *testing.T) {
	f := newTrashFixture(t)
	inA := f.conversation("in A", convOpt{mailbox: f.mailboxA})
	inB := f.conversation("in B", convOpt{mailbox: f.mailboxB})

	for _, path := range []string{"/api/v1/trash", "/api/v1/trash/restore", "/api/v1/trash/purge"} {
		expect(t, f.readonly.do("POST", path, map[string]any{"ids": []string{inA}}), 403, "forbidden")
	}
	expect(t, f.readonly.do("GET", "/api/v1/trash", nil), 403, "forbidden")

	// conversations.write without conversations.delete may not use the trash.
	role := createRole(t, f.admin, "Schrijver", "conversations.read", "conversations.write")
	writer, writerUser := f.h.userWithRole(role)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, writerUser.ID)
	expect(t, writer.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inA}}), 403, "forbidden")
	expect(t, writer.do("GET", "/api/v1/trash", nil), 403, "forbidden")

	// A mailbox the agent can only read, or not at all.
	reader, readerUser := f.h.loggedIn("agent")
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.otherTeam, readerUser.ID)
	if got := results(t, reader.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inB}})); got[inB] != "forbidden" {
		t.Fatalf("read-only mailbox = %v", got)
	}
	if got := results(t, f.agent.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inB}})); got[inB] != "not_found" {
		t.Fatalf("mailbox out of scope = %v", got)
	}

	// The admin trashes in B; the agent sees nothing of it and cannot restore or purge it.
	results(t, f.admin.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inB}}))
	if n := len(f.trash(f.agent, "").Conversations); n != 0 {
		t.Fatalf("agent sees %d trashed conversations of another mailbox", n)
	}
	if n := len(f.trash(reader, "").Conversations); n != 0 {
		t.Fatalf("reader sees %d trashed conversations of a read-only mailbox", n)
	}
	if got := results(t, f.agent.do("POST", "/api/v1/trash/restore", map[string]any{"ids": []string{inB}})); got[inB] != "not_found" {
		t.Fatalf("restore out of scope = %v", got)
	}
	if got := results(t, f.agent.do("POST", "/api/v1/trash/purge", map[string]any{"ids": []string{inB}})); got[inB] != "not_found" {
		t.Fatalf("purge out of scope = %v", got)
	}
	expect(t, f.agent.do("POST", "/api/v1/trash/empty", map[string]any{"mailbox_id": f.mailboxB}), 404, "not_found")
	if n := len(f.trash(f.admin, "").Conversations); n != 1 {
		t.Fatalf("admin trash has %d, want 1", n)
	}
}

func TestPurgeFromTrash(t *testing.T) {
	f := newTrashFixture(t)
	id := f.conversation("gone", convOpt{mailbox: f.mailboxA})
	sending := f.conversation("sending", convOpt{mailbox: f.mailboxA})
	open := f.conversation("open", convOpt{mailbox: f.mailboxA})
	msg := f.queryID(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction) VALUES ($1, $2, 'email', 'out') RETURNING id`, sending, f.mailboxA)
	f.exec(`INSERT INTO outbound (message_id, idempotency_key, status) VALUES ($1, uuidv7(), 'queued')`, msg)
	key, _, err := f.h.srv.store.Put(context.Background(), []byte("attachment"))
	if err != nil {
		t.Fatal(err)
	}
	withFile := f.queryID(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction) VALUES ($1, $2, 'email', 'in') RETURNING id`, id, f.mailboxA)
	f.exec(`INSERT INTO attachments (message_id, size_bytes, sha256, blob_key, disposition) VALUES ($1, 10, '\x00', $2, 'attachment')`, withFile, key)
	results(t, f.agent.do("POST", "/api/v1/trash", map[string]any{"ids": []string{id, sending}}))

	got := results(t, f.agent.do("POST", "/api/v1/trash/purge", map[string]any{"ids": []string{id, sending, open}}))
	if got[id] != "ok" || got[sending] != "sending" || got[open] != "not_found" {
		t.Fatalf("purge results = %v", got)
	}
	if f.h.count(`SELECT count(*) FROM conversations WHERE id = $1`, id) != 0 {
		t.Fatal("purged conversation still exists")
	}
	if _, err := f.h.srv.store.Open(context.Background(), key); err == nil {
		t.Fatal("attachment file still stored")
	}
	if n := f.audited("conversations.purged"); n != 1 {
		t.Fatalf("purge audit entries = %d", n)
	}
	if s := trashSubjects(f.trash(f.agent, "")); len(s) != 1 || s[0] != "sending" {
		t.Fatalf("trash after purge = %v", s)
	}
}

func TestEmptyTrash(t *testing.T) {
	f := newTrashFixture(t)
	a := f.conversation("a", convOpt{mailbox: f.mailboxA})
	b := f.conversation("b", convOpt{mailbox: f.mailboxA})
	inB := f.conversation("in B", convOpt{mailbox: f.mailboxB})
	results(t, f.agent.do("POST", "/api/v1/trash", map[string]any{"ids": []string{a, b}}))
	results(t, f.admin.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inB}}))

	r := f.agent.do("POST", "/api/v1/trash/empty", map[string]any{})
	expect(t, r, 200, "")
	if r.body["deleted"] != float64(2) {
		t.Fatalf("deleted = %v", r.body["deleted"])
	}
	// The agent's empty does not reach the mailbox it cannot write.
	if s := trashSubjects(f.trash(f.admin, "")); len(s) != 1 || s[0] != "in B" {
		t.Fatalf("admin trash = %v", s)
	}
	if n := f.audited("conversations.purged"); n != 1 {
		t.Fatalf("audit entries = %d, want one per mailbox batch", n)
	}
}

func TestTrashListPagesAndFilters(t *testing.T) {
	f := newTrashFixture(t)
	var ids []string
	for _, s := range []string{"one", "two", "three"} {
		ids = append(ids, f.conversation(s, convOpt{mailbox: f.mailboxA}))
	}
	inB := f.conversation("b", convOpt{mailbox: f.mailboxB})
	for _, id := range ids {
		results(t, f.admin.do("POST", "/api/v1/trash", map[string]any{"ids": []string{id}}))
	}
	results(t, f.admin.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inB}}))

	first := f.trash(f.admin, "?mailbox_id="+f.mailboxA+"&limit=2")
	if got := trashSubjects(first); len(got) != 2 || got[0] != "three" || got[1] != "two" || first.NextCursor == nil {
		t.Fatalf("first page = %v, cursor %v", got, first.NextCursor)
	}
	second := f.trash(f.admin, "?mailbox_id="+f.mailboxA+"&limit=2&cursor="+*first.NextCursor)
	if got := trashSubjects(second); len(got) != 1 || got[0] != "one" || second.NextCursor != nil {
		t.Fatalf("second page = %v", got)
	}
	expect(t, f.admin.do("GET", "/api/v1/trash?mailbox_id=x", nil), 400, "invalid_request")
}

func TestBlockedSenders(t *testing.T) {
	f := newTrashFixture(t)
	r := f.agent.do("POST", "/api/v1/blocked-senders", map[string]any{"mailbox_id": f.mailboxA, "pattern": " @Junk.Example "})
	expect(t, r, 201, "")
	if r.body["pattern"] != "junk.example" {
		t.Fatalf("pattern = %v", r.body["pattern"])
	}
	id := r.body["id"].(string)
	again := f.agent.do("POST", "/api/v1/blocked-senders", map[string]any{"mailbox_id": f.mailboxA, "pattern": "junk.example"})
	expect(t, again, 200, "")
	if again.body["id"] != id {
		t.Fatalf("second block made a new entry: %v", again.body)
	}
	expect(t, f.agent.do("POST", "/api/v1/blocked-senders", map[string]any{"mailbox_id": f.mailboxA, "pattern": "Spammer@Junk.example"}), 201, "")

	for pattern, code := range map[string]string{"": "required", "not a domain": "invalid", "a@b@c": "invalid", "localhost": "invalid"} {
		r := f.agent.do("POST", "/api/v1/blocked-senders", map[string]any{"mailbox_id": f.mailboxA, "pattern": pattern})
		expect(t, r, 422, "validation_failed")
		if fieldsOf(r)["pattern"] != code {
			t.Errorf("%q: fields = %v", pattern, fieldsOf(r))
		}
	}
	// Only mailboxes the user may write.
	expect(t, f.agent.do("POST", "/api/v1/blocked-senders", map[string]any{"mailbox_id": f.mailboxB, "pattern": "x.example"}), 404, "not_found")
	expect(t, f.readonly.do("GET", "/api/v1/blocked-senders", nil), 403, "forbidden")
	expect(t, f.admin.do("POST", "/api/v1/blocked-senders", map[string]any{"mailbox_id": f.mailboxB, "pattern": "b.example"}), 201, "")

	list := f.agent.do("GET", "/api/v1/blocked-senders", nil)
	expect(t, list, 200, "")
	if n := len(list.body["blocked_senders"].([]any)); n != 2 {
		t.Fatalf("agent sees %d entries, want the 2 of mailbox A", n)
	}
	if mbs := list.body["mailboxes"].([]any); len(mbs) != 1 || mbs[0].(map[string]any)["id"] != f.mailboxA {
		t.Fatalf("writable mailboxes = %v", mbs)
	}
	if n := f.audited("blocklist.added"); n != 3 {
		t.Fatalf("blocklist.added audits = %d", n)
	}

	expect(t, f.agent.do("DELETE", "/api/v1/blocked-senders/"+id, nil), 204, "")
	expect(t, f.agent.do("DELETE", "/api/v1/blocked-senders/"+id, nil), 404, "not_found")
	if n := f.audited("blocklist.removed"); n != 1 {
		t.Fatalf("blocklist.removed audits = %d", n)
	}
}

func TestTrashedConversationCanBeViewedReadOnly(t *testing.T) {
	f := newRenderFixture(t)
	conv := f.conversation("weg", convOpt{mailbox: f.mailboxA})
	msg := f.message(conv, f.mailboxA, msgOpt{html: "<p>inhoud</p>", text: "inhoud"})
	att := f.attachment(msg, "factuur.pdf", "application/pdf", "", "attachment", []byte("%PDF-1.4"))
	results(t, f.agent.do("POST", "/api/v1/trash", map[string]any{"ids": []string{conv}}))

	r := f.agent.do("GET", "/api/v1/trash/"+conv, nil)
	expect(t, r, 200, "")
	c := obj(r, "conversation")
	if c["subject"] != "weg" || c["can_write"] != false {
		t.Fatalf("conversation = %v", c)
	}
	if msgs, _ := r.body["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("messages = %v", r.body["messages"])
	}
	if by := obj(r, "deleted_by"); by["id"] != f.agentID || r.body["deleted_at"] == nil {
		t.Fatalf("deleted_by = %v, deleted_at = %v", by, r.body["deleted_at"])
	}
	events, _ := r.body["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["type"] != "deleted" {
		t.Fatalf("events = %v", events)
	}
	// The body and files stay reachable for whoever may see the trash.
	expect(t, f.agent.do("GET", "/render/messages/"+msg, nil), 200, "")
	expect(t, f.agent.do("GET", "/api/v1/attachments/"+att+"/download", nil), 200, "")

	// Everywhere else the conversation is gone.
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+conv, nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+conv+"/events", nil), 404, "not_found")

	// Reading the mailbox is not enough to see its trash.
	expect(t, f.readonly.do("GET", "/api/v1/trash/"+conv, nil), 403, "forbidden")
	expect(t, f.readonly.do("GET", "/render/messages/"+msg, nil), 404, "not_found")
	expect(t, f.readonly.do("GET", "/api/v1/attachments/"+att+"/download", nil), 404, "not_found")
	role := createRole(t, f.admin, "Schrijver", "conversations.read", "conversations.write")
	writer, writerUser := f.h.userWithRole(role)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, writerUser.ID)
	expect(t, writer.do("GET", "/render/messages/"+msg, nil), 404, "not_found")

	// Not in the trash, or in a mailbox out of scope.
	open := f.conversation("open", convOpt{mailbox: f.mailboxA})
	expect(t, f.agent.do("GET", "/api/v1/trash/"+open, nil), 404, "not_found")
	inB := f.conversation("in B", convOpt{mailbox: f.mailboxB})
	results(t, f.admin.do("POST", "/api/v1/trash", map[string]any{"ids": []string{inB}}))
	expect(t, f.agent.do("GET", "/api/v1/trash/"+inB, nil), 404, "not_found")
	expect(t, f.admin.do("GET", "/api/v1/trash/"+inB, nil), 200, "")
}
