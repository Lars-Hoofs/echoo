package api

import (
	"testing"
	"time"

	"echoo/internal/policy"
)

func teamID(t *testing.T, c *client, name string) string {
	t.Helper()
	r := c.do("POST", "/api/v1/teams", map[string]string{"name": name})
	expect(t, r, 201, "")
	return r.body["team"].(map[string]any)["id"].(string)
}

func TestPrivilegedPermissionsAreOwnerGrantable(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	admin, _ := h.loggedIn("admin")
	for _, p := range []string{
		"teams.manage", "mailboxes.manage", "contacts.erase", "contacts.import", "webhooks.manage", "automation.manage",
	} {
		perms := []string{"conversations.read", "contacts.read", p}
		expect(t, admin.do("POST", "/api/v1/roles", map[string]any{"name": "R " + p, "permissions": perms}), 403, "forbidden")
		expect(t, owner.do("POST", "/api/v1/roles", map[string]any{"name": "R " + p, "permissions": perms}), 201, "")
	}
	for _, p := range policy.Permissions() {
		want := p == policy.UsersManage || p == policy.SettingsManage || p == policy.TeamsManage || p == policy.MailboxesManage ||
			p == policy.ContactsErase || p == policy.ContactsImport || p == policy.WebhooksManage || p == policy.AutomationManage
		if got := policy.Privileged([]string{string(p)}); got != want {
			t.Errorf("Privileged(%s) = %v, want %v", p, got, want)
		}
	}
}

// Roles that hold teams.manage or mailboxes.manage from before those were privileged must not
// be able to put themselves into a mailbox.
func TestTeamAndMailboxManagersCannotWidenOwnScope(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	admin, _ := h.loggedIn("admin")
	mailbox := createMailbox(t, admin, nil)["id"].(string)
	other := createMailbox(t, admin, map[string]any{"email_address": "other@example.com"})["id"].(string)
	team := teamID(t, admin, "Mine")
	foreign := teamID(t, admin, "Foreign")
	expect(t, admin.do("PUT", "/api/v1/mailboxes/"+other+"/access", map[string]any{"access": []map[string]string{{"team_id": foreign, "level": "write"}}}), 204, "")

	role := createRole(t, owner, "Manager", "conversations.read", "conversations.write", "teams.manage", "mailboxes.manage")
	mgr, me := h.userWithRole(role)
	expect(t, admin.do("PUT", "/api/v1/teams/"+team+"/members", map[string]any{"user_ids": []string{uuidStr(me.ID)}}), 204, "")
	expect(t, admin.do("PUT", "/api/v1/mailboxes/"+mailbox+"/access", map[string]any{"access": []map[string]string{{"team_id": team, "level": "read"}}}), 204, "")

	// Adding oneself to a team that reaches another mailbox.
	expect(t, mgr.do("PUT", "/api/v1/teams/"+foreign+"/members", map[string]any{"user_ids": []string{uuidStr(me.ID)}}), 403, "forbidden")
	if n := h.count(`SELECT count(*) FROM team_members WHERE team_id = $1 AND user_id = $2`, foreign, uuidStr(me.ID)); n != 0 {
		t.Fatalf("membership of the foreign team was kept: %d", n)
	}
	// Granting one's own team more access, or another mailbox.
	expect(t, mgr.do("PUT", "/api/v1/mailboxes/"+mailbox+"/access", map[string]any{"access": []map[string]string{{"team_id": team, "level": "write"}}}), 403, "forbidden")
	expect(t, mgr.do("PUT", "/api/v1/mailboxes/"+other+"/access", map[string]any{"access": []map[string]string{{"team_id": foreign, "level": "write"}, {"team_id": team, "level": "read"}}}), 403, "forbidden")
	if n := h.count(`SELECT count(*) FROM mailbox_access WHERE mailbox_id = $1 AND team_id = $2`, other, team); n != 0 {
		t.Fatalf("own team got access to the other mailbox: %d", n)
	}
	// Narrowing and adding other people is still fine.
	expect(t, mgr.do("PUT", "/api/v1/mailboxes/"+mailbox+"/access", map[string]any{"access": []map[string]string{}}), 204, "")
	stranger, _ := h.user("agent", "stranger@example.com")
	expect(t, mgr.do("PUT", "/api/v1/teams/"+foreign+"/members", map[string]any{"user_ids": []string{uuidStr(stranger.ID)}}), 204, "")
}

func TestContactErasureAndExportNeedToSeeEverything(t *testing.T) {
	f := newContactFixture(t)
	id := f.contactIn("Anna", "anna@customer.nl", f.mailboxA)
	owner, _ := f.h.loggedIn("owner")
	role := createRole(t, owner, "Privacy officer", "conversations.read", "contacts.read", "contacts.erase")
	officer, me := f.h.userWithRole(role)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, me.ID)

	// The contact is visible to the officer through the team, yet a team grant does not reach
	// every conversation of the contact, so exports and erasure stay with those who see all.
	expect(t, officer.do("GET", "/api/v1/contacts/"+id, nil), 200, "")
	expect(t, officer.do("POST", "/api/v1/contacts/"+id+"/data-export", nil), 403, "forbidden")
	expect(t, officer.do("POST", "/api/v1/contacts/"+id+"/erase", map[string]any{"confirmation": "anna@customer.nl"}), 403, "forbidden")
	if f.h.count(`SELECT count(*) FROM data_exports`) != 0 || f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.erased'`) != 0 {
		t.Error("a refused request left traces")
	}
}

func TestAutomationManagersStayInsideTheirMailboxes(t *testing.T) {
	f := newAutomationFixture(t)
	owner, _ := f.h.loggedIn("owner")
	role := createRole(t, owner, "Automator", "conversations.read", "conversations.write", "automation.manage")
	c, me := f.h.userWithRole(role)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, me.ID)
	rule := func(mailbox any) map[string]any {
		return map[string]any{"name": "R", "trigger": "message_received", "mailbox_id": mailbox, "actions": []any{map[string]any{"type": "mark_spam"}}}
	}

	expect(t, c.do("POST", "/api/v1/rules", rule(nil)), 403, "forbidden")
	expect(t, c.do("POST", "/api/v1/rules", rule(f.mailboxB)), 403, "forbidden")
	mine := c.do("POST", "/api/v1/rules", rule(f.mailboxA))
	expect(t, mine, 201, "")
	mineID := obj(mine, "rule")["id"].(string)
	global := obj(f.admin.do("POST", "/api/v1/rules", rule(nil)), "rule")["id"].(string)
	foreign := obj(f.admin.do("POST", "/api/v1/rules", rule(f.mailboxB)), "rule")["id"].(string)

	list := c.do("GET", "/api/v1/rules", nil)
	expect(t, list, 200, "")
	if got := list.body["rules"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != mineID {
		t.Errorf("rules listed for a mailbox manager: %s", list.raw)
	}
	expect(t, c.do("PATCH", "/api/v1/rules/"+mineID, map[string]any{"mailbox_id": nil}), 403, "forbidden")
	expect(t, c.do("PATCH", "/api/v1/rules/"+mineID, map[string]any{"mailbox_id": f.mailboxB}), 403, "forbidden")
	for _, id := range []string{global, foreign} {
		expect(t, c.do("PATCH", "/api/v1/rules/"+id, map[string]any{"enabled": false}), 403, "forbidden")
		expect(t, c.do("DELETE", "/api/v1/rules/"+id, nil), 403, "forbidden")
		expect(t, c.do("GET", "/api/v1/rules/"+id+"/runs", nil), 403, "forbidden")
	}
	expect(t, c.do("PUT", "/api/v1/rules/order", map[string]any{"ids": []string{mineID, global, foreign}}), 403, "forbidden")
	expect(t, c.do("PUT", "/api/v1/mailboxes/"+f.mailboxB+"/automation", map[string]any{"auto_assign_mode": "round_robin"}), 403, "forbidden")
	expect(t, c.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/automation", map[string]any{"auto_assign_mode": "round_robin"}), 200, "")
	expect(t, c.do("PUT", "/api/v1/settings/automation", map[string]any{"auto_resolve_days": 7}), 403, "forbidden")
	assignment := c.do("GET", "/api/v1/assignment", nil)
	expect(t, assignment, 200, "")
	if boxes := assignment.body["mailboxes"].([]any); len(boxes) != 1 || boxes[0].(map[string]any)["id"] != f.mailboxA || len(assignment.body["agents"].([]any)) != 0 {
		t.Errorf("assignment for a mailbox manager: %s", assignment.raw)
	}
	expect(t, c.do("PUT", "/api/v1/users/"+f.agentID+"/capacity", map[string]any{"max_open": 5}), 403, "forbidden")
	expect(t, c.do("DELETE", "/api/v1/rules/"+mineID, nil), 204, "")
}

func TestWebhookContentNeedsFullAccess(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	admin, _ := h.loggedIn("admin")
	c, _ := h.userWithRole(createRole(t, owner, "Hooks", "webhooks.manage"))
	body := func(content bool) map[string]any {
		return map[string]any{"url": "https://hooks.example.com/x", "events": []string{"message.created"}, "include_content": content}
	}

	r := c.do("POST", "/api/v1/webhooks", body(true))
	expect(t, r, 422, "validation_failed")
	if fieldCode(r, "include_content") != "needs_full_access" {
		t.Errorf("create: %s", r.raw)
	}
	created := c.do("POST", "/api/v1/webhooks", body(false))
	expect(t, created, 201, "")
	path := "/api/v1/webhooks/" + created.body["webhook"].(map[string]any)["id"].(string)
	r = c.do("PATCH", path, map[string]any{"include_content": true})
	expect(t, r, 422, "validation_failed")
	if fieldCode(r, "include_content") != "needs_full_access" {
		t.Errorf("update: %s", r.raw)
	}
	expect(t, admin.do("PATCH", path, map[string]any{"include_content": true}), 200, "")
	expect(t, c.do("PATCH", path, map[string]any{"enabled": false}), 200, "")
}

// Two requests of one actor that are harmless alone but widen the actor's scope together (join
// a team that has no mailbox yet, and give that team a mailbox) must not both succeed.
func TestScopeGuardSerializesRequestsOfOneActor(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	admin, _ := h.loggedIn("admin")
	mailbox := createMailbox(t, admin, nil)["id"].(string)
	team := teamID(t, admin, "Empty")
	mgr, me := h.userWithRole(createRole(t, owner, "Manager", "conversations.read", "teams.manage", "mailboxes.manage"))

	// Hold the actor's row the way a concurrent guarded request does.
	tx, err := h.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, me.ID); err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 2)
	go func() {
		statuses <- mgr.do("PUT", "/api/v1/teams/"+team+"/members", map[string]any{"user_ids": []string{uuidStr(me.ID)}}).status
	}()
	go func() {
		statuses <- mgr.do("PUT", "/api/v1/mailboxes/"+mailbox+"/access", map[string]any{"access": []map[string]string{{"team_id": team, "level": "read"}}}).status
	}()
	select {
	case s := <-statuses:
		t.Fatalf("a guarded request finished (%d) while the actor row was locked", s)
	case <-time.After(500 * time.Millisecond):
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	first, second := <-statuses, <-statuses
	if first+second != 204+403 || first == second {
		t.Fatalf("statuses %d and %d, want exactly one refused", first, second)
	}
}

func TestRulesCannotSendTemplatesTheActorMayNotUse(t *testing.T) {
	f := newAutomationFixture(t)
	secret := f.queryID(`INSERT INTO templates (name, scope, owner_user_id, subject, body_html) VALUES ('Prive', 'personal', $1, 's', '<p>x</p>') RETURNING id`, f.agentID)
	global := f.queryID(`INSERT INTO templates (name, scope, subject, body_html) VALUES ('Algemeen', 'global', 's', '<p>x</p>') RETURNING id`)
	rule := func(tpl string) map[string]any {
		return map[string]any{"name": "R", "trigger": "message_received", "mailbox_id": f.mailboxA, "actions": []any{map[string]any{"type": "auto_reply", "template_id": tpl}}}
	}
	r := f.admin.do("POST", "/api/v1/rules", rule(secret))
	expect(t, r, 422, "validation_failed")
	if fieldCode(r, "actions") != "unknown_template" {
		t.Errorf("create: %s", r.raw)
	}
	ok := f.admin.do("POST", "/api/v1/rules", rule(global))
	expect(t, ok, 201, "")
	id := obj(ok, "rule")["id"].(string)
	expect(t, f.admin.do("PATCH", "/api/v1/rules/"+id, map[string]any{"actions": []any{map[string]any{"type": "auto_reply", "template_id": secret}}}), 422, "validation_failed")
	expect(t, f.admin.do("PATCH", "/api/v1/rules/"+id, map[string]any{"enabled": false}), 200, "")
}
