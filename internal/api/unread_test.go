package api

import (
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func (f *composerFixture) unreadInList(c *client, conv string) bool {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/conversations?view=all", nil)
	expect(f.h.t, r, 200, "")
	for _, item := range r.body["conversations"].([]any) {
		m := item.(map[string]any)
		if m["id"] == conv {
			return m["unread"].(bool)
		}
	}
	f.h.t.Fatalf("conversation %s not in list: %s", conv, r.raw)
	return false
}

func (f *composerFixture) unreadMine(c *client) float64 {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/inbox/summary", nil)
	expect(f.h.t, r, 200, "")
	return obj(r, "counts")["unread_mine"].(float64)
}

func TestReadStateFollowsCustomerMessagesAndOtherPeoplesNotes(t *testing.T) {
	f := newComposerFixture(t)
	conv := "/api/v1/conversations/" + f.conv

	if !f.unreadInList(f.agent, f.conv) || f.unreadMine(f.agent) != 1 {
		t.Fatal("a customer message nobody opened must be unread for the assignee")
	}
	detail := f.agent.do("GET", conv, nil)
	if obj(detail, "conversation")["unread"] != true {
		t.Fatalf("detail does not say unread: %s", detail.raw)
	}
	if f.unreadInList(f.admin, f.conv) != true {
		t.Fatal("unread is per user: the admin has not opened it either")
	}

	version := obj(detail, "conversation")["version"]
	expect(t, f.agent.do("POST", conv+"/read", nil), 204, "")
	expect(t, f.agent.do("POST", conv+"/read", nil), 204, "")
	if f.unreadInList(f.agent, f.conv) || f.unreadMine(f.agent) != 0 {
		t.Fatal("opening must mark the conversation read")
	}
	if !f.unreadInList(f.admin, f.conv) {
		t.Fatal("reading is personal; the admin's state must not change")
	}
	if got := obj(f.agent.do("GET", conv, nil), "conversation")["version"]; got != version {
		t.Fatalf("reading bumped the version from %v to %v, which would notify other agents", version, got)
	}

	expect(t, f.admin.do("POST", conv+"/notes", map[string]any{"html": "<p>Ik kijk ernaar.</p>"}), 201, "")
	if !f.unreadInList(f.agent, f.conv) || f.unreadMine(f.agent) != 1 {
		t.Fatal("a note by someone else must make the conversation unread again")
	}

	expect(t, f.agent.do("POST", conv+"/notes", map[string]any{"html": "<p>Eigen notitie.</p>"}), 201, "")
	if f.unreadInList(f.agent, f.conv) {
		t.Fatal("adding a note implies having read the conversation")
	}
}

func TestOwnNoteDoesNotMakeConversationUnread(t *testing.T) {
	f := newComposerFixture(t)
	conv := "/api/v1/conversations/" + f.conv
	expect(t, f.admin.do("POST", conv+"/read", nil), 204, "")
	expect(t, f.admin.do("POST", conv+"/notes", map[string]any{"html": "<p>Notitie.</p>"}), 201, "")
	if f.unreadInList(f.admin, f.conv) {
		t.Fatal("a user's own note must not count as unread")
	}
}

func TestReplyMarksConversationRead(t *testing.T) {
	f := newComposerFixture(t)
	if !f.unreadInList(f.agent, f.conv) {
		t.Fatal("precondition: unread")
	}
	expect(t, f.send(f.agent, f.conv, f.replyBody(nil)), 201, "")
	if f.unreadInList(f.agent, f.conv) {
		t.Fatal("answering must mark the conversation read")
	}
}

func TestMarkUnreadSurvivesUntilNextRead(t *testing.T) {
	f := newComposerFixture(t)
	conv := "/api/v1/conversations/" + f.conv
	expect(t, f.agent.do("POST", conv+"/read", nil), 204, "")
	expect(t, f.agent.do("POST", conv+"/unread", nil), 204, "")
	expect(t, f.agent.do("POST", conv+"/unread", nil), 204, "")
	if !f.unreadInList(f.agent, f.conv) || f.unreadMine(f.agent) != 1 {
		t.Fatal("mark as unread must stick even without newer messages")
	}
	expect(t, f.agent.do("POST", conv+"/read", nil), 204, "")
	if f.unreadInList(f.agent, f.conv) {
		t.Fatal("reading must clear the explicit unread mark")
	}
}

func TestUnreadCountOnlyCountsMyOpenConversations(t *testing.T) {
	f := newComposerFixture(t)
	f.conversation("Gesloten", convOpt{mailbox: f.mailboxA, status: "closed", assignee: f.agentID})
	f.conversation("Van iemand anders", convOpt{mailbox: f.mailboxA})
	for _, id := range []string{
		f.queryID(`SELECT id FROM conversations WHERE subject = 'Gesloten'`),
		f.queryID(`SELECT id FROM conversations WHERE subject = 'Van iemand anders'`),
	} {
		f.exec(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, from_addr, subject, body_text)
			VALUES ($1, $2, 'email', 'in', 'x@customer.nl', 's', 'b')`, id, f.mailboxA)
	}
	if got := f.unreadMine(f.agent); got != 1 {
		t.Fatalf("unread_mine = %v, want 1 (closed and unassigned conversations do not count)", got)
	}
}

func TestReadStateIsScoped(t *testing.T) {
	f := newComposerFixture(t)
	hidden := f.conversation("Alleen B", convOpt{mailbox: f.mailboxB})
	for _, action := range []string{"read", "unread"} {
		expect(t, f.agent.do("POST", "/api/v1/conversations/"+hidden+"/"+action, nil), 404, "")
		expect(t, f.agent.do("POST", "/api/v1/conversations/not-a-uuid/"+action, nil), 404, "")
	}
	if n := f.h.count(`SELECT count(*) FROM conversation_reads WHERE conversation_id = $1`, hidden); n != 0 {
		t.Fatalf("out-of-scope calls stored %d read rows", n)
	}
	// Read access is enough to keep your own read state, including for readonly users.
	expect(t, f.readonly.do("POST", "/api/v1/conversations/"+f.conv+"/read", nil), 204, "")
	expect(t, f.h.client().do("POST", "/api/v1/conversations/"+f.conv+"/read", nil), 401, "")
}

func TestComposerStatusChangeQueuesRulesOnce(t *testing.T) {
	f := newComposerFixture(t)
	rc, err := river.NewClient(riverpgxv5.New(f.h.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.h.srv.inbox = f.h.srv.inbox.WithJobs(rc)
	queued := func() int {
		return f.h.count(`SELECT count(*) FROM river_job WHERE kind = 'rules.evaluate' AND args->>'trigger' = 'conversation_updated'`)
	}

	expect(t, f.send(f.agent, f.conv, f.replyBody(nil)), 201, "")
	if got := queued(); got != 0 {
		t.Fatalf("a reply that keeps the status queued %d rule jobs", got)
	}
	expect(t, f.send(f.agent, f.conv, f.replyBody(map[string]any{"status_after": "closed"})), 201, "")
	if got := queued(); got != 1 {
		t.Fatalf("a reply that closes the conversation queued %d rule jobs, want 1", got)
	}
	expect(t, f.send(f.agent, f.conv, f.replyBody(map[string]any{"status_after": "closed"})), 201, "")
	if got := queued(); got != 1 {
		t.Fatalf("a reply that leaves the status as is queued another job: %d", got)
	}
}

func TestReplyNotificationSettingsRoundTrip(t *testing.T) {
	h := newHarness(t)
	c, _ := h.loggedIn("agent")
	put := func(body map[string]any) map[string]any {
		r := c.do("PUT", "/api/v1/me/notification-settings", body)
		expect(t, r, 200, "")
		return obj(r, "email")
	}
	if got := put(map[string]any{"mentions": false, "assignments": false, "replies": true}); got["replies"] != true {
		t.Fatalf("replies not saved: %v", got)
	}
	if got := put(map[string]any{"mentions": true, "assignments": false}); got["replies"] != true || got["mentions"] != true {
		t.Fatalf("omitting replies must keep the stored choice: %v", got)
	}
	got := obj(c.do("GET", "/api/v1/me/notification-settings", nil), "email")
	if got["replies"] != true {
		t.Fatalf("GET lost the replies setting: %v", got)
	}
}
