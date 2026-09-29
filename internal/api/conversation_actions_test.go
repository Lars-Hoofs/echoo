package api

import (
	"encoding/json"
	"testing"
	"time"
)

func (f *inboxFixture) patch(c *client, id string, body map[string]any) response {
	return c.do("PATCH", "/api/v1/conversations/"+id, body)
}

func TestPatchConversationStatusAssignAndPriority(t *testing.T) {
	f := newInboxFixture(t)
	id := f.conversation("hello", convOpt{mailbox: f.mailboxA})

	r := f.patch(f.agent, id, map[string]any{"status": "closed", "priority": "high", "assignee_user_id": f.agentID})
	expect(t, r, 200, "")
	if r.body["version"] != float64(2) {
		t.Fatalf("version = %v", r.body["version"])
	}
	if got := subjects(f.list(f.agent, "?status=closed")); got != "hello" {
		t.Fatalf("closed list = %q", got)
	}
	if got := subjects(f.list(f.agent, "")); got != "" {
		t.Fatalf("open list = %q", got)
	}
	got := f.list(f.agent, "?status=closed").Conversations[0]
	if got.Assignee == nil || got.Assignee.ID != f.agentID {
		t.Fatalf("assignee = %+v", got.Assignee)
	}

	// null unassigns.
	expect(t, f.patch(f.agent, id, map[string]any{"assignee_user_id": nil}), 200, "")
	if f.list(f.agent, "?status=closed").Conversations[0].Assignee != nil {
		t.Fatal("assignee not cleared")
	}
	expect(t, f.patch(f.agent, id, map[string]any{"status": "nonsense"}), 422, "validation_failed")
	expect(t, f.patch(f.agent, id, map[string]any{"assignee_user_id": "not-a-uuid"}), 422, "validation_failed")
}

func TestPatchConversationVersionConflict(t *testing.T) {
	f := newInboxFixture(t)
	id := f.conversation("hello", convOpt{mailbox: f.mailboxA})
	expect(t, f.patch(f.agent, id, map[string]any{"priority": "low", "expected_version": 1}), 200, "")
	r := f.patch(f.agent, id, map[string]any{"priority": "high", "expected_version": 1})
	expect(t, r, 409, "version_conflict")
	e, _ := r.body["error"].(map[string]any)
	if e["current_version"] != float64(2) {
		t.Fatalf("current_version = %v", e["current_version"])
	}
}

func TestPatchConversationAuthorization(t *testing.T) {
	f := newInboxFixture(t)
	inA := f.conversation("in A", convOpt{mailbox: f.mailboxA})
	inB := f.conversation("in B", convOpt{mailbox: f.mailboxB})

	// Readonly users are refused for every id, whether it exists or not.
	expect(t, f.patch(f.readonly, inA, map[string]any{"status": "closed"}), 403, "forbidden")
	expect(t, f.patch(f.readonly, "0199a000-0000-7000-8000-000000000000", map[string]any{"status": "closed"}), 403, "forbidden")

	// Outside the read scope looks the same as a missing conversation.
	expect(t, f.patch(f.agent, inB, map[string]any{"status": "closed"}), 404, "not_found")
	expect(t, f.patch(f.agent, "0199a000-0000-7000-8000-000000000000", map[string]any{"status": "closed"}), 404, "not_found")

	// An agent whose team can only read the mailbox may see the conversation but not change it.
	reader, readerUser := f.h.loggedIn("agent")
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.otherTeam, readerUser.ID)
	expect(t, f.patch(reader, inB, map[string]any{"status": "closed"}), 403, "forbidden")
	expect(t, reader.do("PUT", "/api/v1/conversations/"+inB+"/labels", map[string]any{"label_ids": []string{}}), 403, "forbidden")
	expect(t, reader.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": []string{inB}, "action": map[string]any{"status": "closed"}}), 200, "")
	if got := subjects(f.list(f.admin, "?status=closed")); got != "" {
		t.Fatalf("closed by a read-only member: %q", got)
	}
}

func TestAssigneeMustHaveWriteAccess(t *testing.T) {
	f := newInboxFixture(t)
	id := f.conversation("hello", convOpt{mailbox: f.mailboxA})
	_, outsider := f.h.loggedIn("agent")
	readonlyID := f.queryID(`SELECT id FROM users WHERE role = 'readonly'`)

	expect(t, f.patch(f.agent, id, map[string]any{"assignee_user_id": outsider.ID.String()}), 422, "assignee_no_access")
	expect(t, f.patch(f.agent, id, map[string]any{"assignee_user_id": readonlyID}), 422, "assignee_no_access")
	expect(t, f.patch(f.agent, id, map[string]any{"assignee_team_id": f.otherTeam}), 422, "team_no_access")
	expect(t, f.patch(f.agent, id, map[string]any{"assignee_team_id": f.team}), 200, "")
}

func TestAssigneesEndpoint(t *testing.T) {
	f := newInboxFixture(t)
	r := f.agent.do("GET", "/api/v1/assignees?mailbox_id="+f.mailboxA, nil)
	expect(t, r, 200, "")
	var out struct {
		Users []struct{ ID string } `json:"users"`
		Teams []struct{ ID string } `json:"teams"`
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, u := range out.Users {
		ids[u.ID] = true
	}
	// The agent and the admin may write; the readonly team member and other agents may not.
	if !ids[f.agentID] || len(out.Users) != 2 || len(out.Teams) != 1 || out.Teams[0].ID != f.team {
		t.Fatalf("assignees = %s", r.raw)
	}
	expect(t, f.agent.do("GET", "/api/v1/assignees?mailbox_id="+f.mailboxB, nil), 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/assignees", nil), 400, "invalid_request")
}

func TestLabelsCRUDAndAssignment(t *testing.T) {
	f := newInboxFixture(t)
	r := f.admin.do("POST", "/api/v1/labels", map[string]any{"name": "Factuur", "color_token": "blue", "description": "Betalingen"})
	expect(t, r, 201, "")
	label, _ := r.body["label"].(map[string]any)
	labelID, _ := label["id"].(string)

	expect(t, f.admin.do("POST", "/api/v1/labels", map[string]any{"name": "factuur", "color_token": "red"}), 422, "validation_failed")
	expect(t, f.admin.do("POST", "/api/v1/labels", map[string]any{"name": "X", "color_token": "#ff0000"}), 422, "validation_failed")
	expect(t, f.admin.do("POST", "/api/v1/labels", map[string]any{"color_token": "red"}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/labels", map[string]any{"name": "Nope", "color_token": "red"}), 403, "forbidden")
	expect(t, f.agent.do("GET", "/api/v1/labels", nil), 200, "")

	a := f.conversation("a", convOpt{mailbox: f.mailboxA})
	f.conversation("b", convOpt{mailbox: f.mailboxA})
	expect(t, f.agent.do("PUT", "/api/v1/conversations/"+a+"/labels", map[string]any{"label_ids": []string{labelID}}), 200, "")
	expect(t, f.agent.do("PUT", "/api/v1/conversations/"+a+"/labels", map[string]any{"label_ids": []string{"0199a000-0000-7000-8000-000000000000"}}), 422, "validation_failed")
	expect(t, f.agent.do("PUT", "/api/v1/conversations/"+a+"/labels", map[string]any{}), 422, "validation_failed")

	l := f.list(f.agent, "?label_id="+labelID)
	if subjects(l) != "a" {
		t.Fatalf("label filter = %q", subjects(l))
	}
	var withLabels struct {
		Conversations []struct {
			Subject string
			Labels  []struct {
				ID, Name   string
				ColorToken string `json:"color_token"`
			} `json:"labels"`
		}
	}
	if err := json.Unmarshal(f.agent.do("GET", "/api/v1/conversations", nil).raw, &withLabels); err != nil {
		t.Fatal(err)
	}
	for _, c := range withLabels.Conversations {
		if c.Subject == "a" && (len(c.Labels) != 1 || c.Labels[0].Name != "Factuur" || c.Labels[0].ColorToken != "blue") {
			t.Fatalf("labels of a = %+v", c.Labels)
		}
		if c.Subject == "b" && len(c.Labels) != 0 {
			t.Fatalf("labels of b = %+v", c.Labels)
		}
	}

	expect(t, f.admin.do("PATCH", "/api/v1/labels/"+labelID, map[string]any{"color_token": "green"}), 200, "")
	expect(t, f.admin.do("DELETE", "/api/v1/labels/"+labelID, nil), 204, "")
	expect(t, f.admin.do("DELETE", "/api/v1/labels/"+labelID, nil), 404, "not_found")
	if got := subjects(f.list(f.agent, "?label_id="+labelID)); got != "" {
		t.Fatalf("deleted label still filters: %q", got)
	}
	var audited int
	if err := f.h.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action LIKE 'label.%'`).Scan(&audited); err != nil || audited != 3 {
		t.Fatalf("audit rows = %d, %v", audited, err)
	}
}

func TestSnoozeHidesFromOpenListAndShowsUnderSnoozed(t *testing.T) {
	f := newInboxFixture(t)
	id := f.conversation("later", convOpt{mailbox: f.mailboxA})
	f.conversation("now", convOpt{mailbox: f.mailboxA})

	until := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	expect(t, f.patch(f.agent, id, map[string]any{"snoozed_until": until}), 200, "")
	if got := subjects(f.list(f.agent, "")); got != "now" {
		t.Fatalf("open list = %q", got)
	}
	snoozed := f.list(f.agent, "?status=snoozed")
	if subjects(snoozed) != "later" {
		t.Fatalf("snoozed list = %q", subjects(snoozed))
	}
	if r := f.agent.do("GET", "/api/v1/inbox/summary", nil); r.body["counts"].(map[string]any)["all"] != float64(1) {
		t.Fatalf("summary counts a snoozed conversation: %s", r.raw)
	}

	expect(t, f.patch(f.agent, id, map[string]any{"snoozed_until": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}), 422, "validation_failed")
	expect(t, f.patch(f.agent, id, map[string]any{"snoozed_until": nil}), 200, "")
	if got := subjects(f.list(f.agent, "?status=snoozed")); got != "" {
		t.Fatalf("snoozed list after unsnooze = %q", got)
	}
	expect(t, f.patch(f.agent, id, map[string]any{"status": "closed"}), 200, "")
	expect(t, f.patch(f.agent, id, map[string]any{"snoozed_until": until}), 422, "not_snoozable")
}

func TestBulkConversationsReportsPartialResults(t *testing.T) {
	f := newInboxFixture(t)
	a := f.conversation("a", convOpt{mailbox: f.mailboxA})
	b := f.conversation("b", convOpt{mailbox: f.mailboxA})
	foreign := f.conversation("foreign", convOpt{mailbox: f.mailboxB})

	r := f.agent.do("POST", "/api/v1/conversations/bulk", map[string]any{
		"ids": []string{a, foreign, b, "nope"}, "action": map[string]any{"status": "closed"},
	})
	expect(t, r, 200, "")
	var out struct {
		Results []struct {
			ID   string
			OK   bool
			Code string
		}
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, res := range out.Results {
		if res.OK {
			codes[res.ID] = "ok"
		} else {
			codes[res.ID] = res.Code
		}
	}
	if codes[a] != "ok" || codes[b] != "ok" || codes[foreign] != "not_found" || codes["nope"] != "invalid_id" || len(out.Results) != 4 {
		t.Fatalf("results = %s", r.raw)
	}
	if got := subjects(f.list(f.agent, "?status=closed")); got != "b,a" {
		t.Fatalf("closed = %q", got)
	}

	action := map[string]any{"status": "open"}
	expect(t, f.readonly.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": []string{a}, "action": action}), 403, "forbidden")
	expect(t, f.agent.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": []string{}, "action": action}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": []string{a}, "action": map[string]any{}}), 422, "validation_failed")
	tooMany := make([]string, 501)
	for i := range tooMany {
		tooMany[i] = a
	}
	expect(t, f.agent.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": tooMany, "action": action}), 422, "validation_failed")

	// An assignee without access fails per conversation and leaves the rest of the batch alone.
	_, outsider := f.h.loggedIn("agent")
	r = f.agent.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": []string{a}, "action": map[string]any{"assignee_user_id": outsider.ID.String()}})
	expect(t, r, 200, "")
	if err := json.Unmarshal(r.raw, &out); err != nil || out.Results[0].OK || out.Results[0].Code != "assignee_no_access" {
		t.Fatalf("results = %s", r.raw)
	}
}

func TestConversationEventsTimeline(t *testing.T) {
	f := newInboxFixture(t)
	id := f.conversation("hello", convOpt{mailbox: f.mailboxA})
	inB := f.conversation("in B", convOpt{mailbox: f.mailboxB})
	expect(t, f.patch(f.agent, id, map[string]any{"assignee_user_id": f.agentID}), 200, "")
	expect(t, f.patch(f.agent, id, map[string]any{"status": "closed"}), 200, "")

	r := f.readonly.do("GET", "/api/v1/conversations/"+id+"/events", nil)
	expect(t, r, 200, "")
	var out struct {
		Events []struct {
			Type  string
			Actor *struct{ ID, Name string }
			User  *struct{ ID, Name string }
		}
	}
	if err := json.Unmarshal(r.raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 2 || out.Events[0].Type != "assigned" || out.Events[1].Type != "resolved" {
		t.Fatalf("events = %s", r.raw)
	}
	if e := out.Events[0]; e.Actor == nil || e.Actor.ID != f.agentID || e.Actor.Name == "" || e.User == nil || e.User.ID != f.agentID {
		t.Fatalf("assigned event = %+v", e)
	}
	expect(t, f.agent.do("GET", "/api/v1/conversations/"+inB+"/events", nil), 404, "not_found")
}
