package api

import (
	"testing"
	"time"
)

type viewBody = map[string]any

func createView(t *testing.T, c *client, body viewBody, status int) response {
	t.Helper()
	r := c.do("POST", "/api/v1/saved-views", body)
	expect(t, r, status, "")
	return r
}

func viewID(r response) string {
	v, _ := r.body["view"].(map[string]any)
	id, _ := v["id"].(string)
	return id
}

func listedViews(t *testing.T, c *client) map[string]map[string]any {
	t.Helper()
	r := c.do("GET", "/api/v1/saved-views", nil)
	expect(t, r, 200, "")
	out := map[string]map[string]any{}
	items, _ := r.body["views"].([]any)
	for _, it := range items {
		v := it.(map[string]any)
		out[v["name"].(string)] = v
	}
	return out
}

func TestSavedViewPersonalCRUD(t *testing.T) {
	f := newInboxFixture(t)
	created := createView(t, f.agent, viewBody{
		"name": "Mijn urgent", "scope": "personal",
		"filters": viewBody{"priorities": []string{"urgent"}, "assignee": "me"},
	}, 201)
	id := viewID(created)
	v := listedViews(t, f.agent)["Mijn urgent"]
	if v == nil || v["scope"] != "personal" || v["editable"] != true {
		t.Fatalf("listed view = %+v", v)
	}

	expect(t, f.agent.do("PATCH", "/api/v1/saved-views/"+id, viewBody{"name": "Mijn dringend", "filters": viewBody{"status": "waiting"}}), 200, "")
	got := listedViews(t, f.agent)["Mijn dringend"]
	if got == nil || got["filters"].(map[string]any)["status"] != "waiting" {
		t.Fatalf("after patch = %+v", got)
	}
	expect(t, f.agent.do("DELETE", "/api/v1/saved-views/"+id, nil), 204, "")
	if len(listedViews(t, f.agent)) != 0 {
		t.Error("view still listed after delete")
	}
	expect(t, f.agent.do("DELETE", "/api/v1/saved-views/"+id, nil), 404, "not_found")
}

func TestSavedViewIsPrivateToOwner(t *testing.T) {
	f := newInboxFixture(t)
	id := viewID(createView(t, f.agent, viewBody{"name": "Privé", "scope": "personal", "filters": viewBody{}}, 201))
	if len(listedViews(t, f.readonly)) != 0 || len(listedViews(t, f.admin)) != 0 {
		t.Error("another user sees a personal view")
	}
	for _, c := range []*client{f.readonly, f.admin} {
		expect(t, c.do("PATCH", "/api/v1/saved-views/"+id, viewBody{"name": "Gekaapt"}), 404, "not_found")
		expect(t, c.do("DELETE", "/api/v1/saved-views/"+id, nil), 404, "not_found")
	}
	if listedViews(t, f.agent)["Privé"] == nil {
		t.Error("owner lost the view")
	}
}

func TestSavedViewSharingIsAnAdminAction(t *testing.T) {
	f := newInboxFixture(t)
	expect(t, f.agent.do("POST", "/api/v1/saved-views", viewBody{"name": "Voor iedereen", "scope": "everyone", "filters": viewBody{}}), 403, "forbidden")
	expect(t, f.agent.do("POST", "/api/v1/saved-views", viewBody{"name": "Voor team", "scope": "team", "team_id": f.team, "filters": viewBody{}}), 403, "forbidden")

	everyone := viewID(createView(t, f.admin, viewBody{"name": "Voor iedereen", "scope": "everyone", "filters": viewBody{"status": "open"}}, 201))
	teamView := viewID(createView(t, f.admin, viewBody{"name": "Support", "scope": "team", "team_id": f.team, "filters": viewBody{}}, 201))
	billing := viewID(createView(t, f.admin, viewBody{"name": "Facturatie", "scope": "team", "team_id": f.otherTeam, "filters": viewBody{}}, 201))

	agentViews := listedViews(t, f.agent)
	if agentViews["Voor iedereen"] == nil || agentViews["Support"] == nil || agentViews["Facturatie"] != nil {
		t.Errorf("agent sees %v, want everyone + own team only", keys(agentViews))
	}
	if agentViews["Voor iedereen"]["editable"] != false {
		t.Error("shared view is editable for an agent")
	}
	if agentViews["Support"]["team"].(map[string]any)["name"] != "Support" {
		t.Errorf("team ref = %v", agentViews["Support"]["team"])
	}
	if len(listedViews(t, f.admin)) != 3 {
		t.Errorf("admin sees %v", keys(listedViews(t, f.admin)))
	}

	expect(t, f.agent.do("PATCH", "/api/v1/saved-views/"+everyone, viewBody{"name": "X"}), 403, "forbidden")
	expect(t, f.agent.do("DELETE", "/api/v1/saved-views/"+teamView, nil), 403, "forbidden")
	expect(t, f.agent.do("DELETE", "/api/v1/saved-views/"+billing, nil), 404, "not_found")
	expect(t, f.admin.do("PATCH", "/api/v1/saved-views/"+everyone, viewBody{"name": "Hernoemd"}), 200, "")
	expect(t, f.admin.do("DELETE", "/api/v1/saved-views/"+teamView, nil), 204, "")

	// A personal view cannot be shared by its owner, only by an admin acting on their own view.
	mine := viewID(createView(t, f.agent, viewBody{"name": "Van mij", "scope": "personal", "filters": viewBody{}}, 201))
	expect(t, f.agent.do("PATCH", "/api/v1/saved-views/"+mine, viewBody{"scope": "everyone"}), 403, "forbidden")
}

func keys(m map[string]map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSavedViewValidation(t *testing.T) {
	f := newInboxFixture(t)
	valid := viewBody{"name": "Ok", "scope": "personal", "filters": viewBody{}}
	tests := []struct {
		name string
		body viewBody
		want string
	}{
		{"missing name", viewBody{"scope": "personal", "filters": viewBody{}}, "name"},
		{"blank name", viewBody{"name": "  ", "scope": "personal", "filters": viewBody{}}, "name"},
		{"long name", viewBody{"name": string(make([]byte, 61)), "scope": "personal", "filters": viewBody{}}, "name"},
		{"missing filters", viewBody{"name": "x", "scope": "personal"}, "filters"},
		{"bad status", viewBody{"name": "x", "scope": "personal", "filters": viewBody{"status": "deleted"}}, "filters.status"},
		{"bad priority", viewBody{"name": "x", "scope": "personal", "filters": viewBody{"priorities": []string{"meh"}}}, "filters.priorities"},
		{"bad mailbox", viewBody{"name": "x", "scope": "personal", "filters": viewBody{"mailbox_ids": []string{"nope"}}}, "filters.mailbox_ids"},
		{"bad date", viewBody{"name": "x", "scope": "personal", "filters": viewBody{"after": "31-12-2026"}}, "filters.after"},
		{"bad assignee", viewBody{"name": "x", "scope": "personal", "filters": viewBody{"assignee": "bob"}}, "filters.assignee"},
		{"bad scope", viewBody{"name": "x", "scope": "galaxy", "filters": viewBody{}}, "scope"},
		{"team without id", viewBody{"name": "x", "scope": "team", "filters": viewBody{}}, "team_id"},
		{"team id on personal", viewBody{"name": "x", "scope": "personal", "team_id": f.team, "filters": viewBody{}}, "team_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := f.admin.do("POST", "/api/v1/saved-views", tc.body)
			expect(t, r, 422, "validation_failed")
			fields, _ := r.body["error"].(map[string]any)["fields"].(map[string]any)
			if _, ok := fields[tc.want]; !ok {
				t.Errorf("fields = %v, want %s", fields, tc.want)
			}
		})
	}
	expect(t, f.admin.do("POST", "/api/v1/saved-views", viewBody{"name": "x", "scope": "personal", "filters": viewBody{"kleur": "rood"}}), 400, "invalid_request")
	expect(t, f.admin.do("POST", "/api/v1/saved-views", viewBody{"name": "x", "scope": "team", "team_id": "00000000-0000-0000-0000-000000000000", "filters": viewBody{}}), 422, "validation_failed")

	createView(t, f.agent, valid, 201)
	r := f.agent.do("POST", "/api/v1/saved-views", valid)
	expect(t, r, 422, "validation_failed")
	other := f.readonly.do("POST", "/api/v1/saved-views", valid)
	expect(t, other, 201, "")
}

func TestSavedViewOpenCount(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		c := f.conversation("hoog", convOpt{mailbox: f.mailboxA, lastMessageAt: now})
		f.exec(`UPDATE conversations SET priority = 'high' WHERE id = $1`, c)
	}
	f.conversation("laag", convOpt{mailbox: f.mailboxA})
	f.conversation("hoog in B", convOpt{mailbox: f.mailboxB})
	f.exec(`UPDATE conversations SET priority = 'high' WHERE subject = 'hoog in B'`)
	createView(t, f.agent, viewBody{"name": "Hoog", "scope": "personal", "filters": viewBody{"priorities": []string{"high"}}}, 201)
	createView(t, f.agent, viewBody{"name": "Gesloten", "scope": "personal", "filters": viewBody{"status": "closed"}}, 201)

	views := listedViews(t, f.agent)
	if got := views["Hoog"]["open_count"]; got != float64(3) {
		t.Errorf("open_count = %v, want 3 (mailbox B must not count)", got)
	}
	if got := views["Gesloten"]["open_count"]; got != nil {
		t.Errorf("closed view open_count = %v, want null", got)
	}
}
