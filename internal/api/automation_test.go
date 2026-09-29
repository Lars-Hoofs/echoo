package api

import (
	"fmt"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"echoo/internal/automation"
)

// automationFixture is an inbox fixture whose server can queue jobs, as in production.
func newAutomationFixture(t *testing.T) *inboxFixture {
	t.Helper()
	f := newInboxFixture(t)
	jobs, err := river.NewClient(riverpgxv5.New(f.h.pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	f.h.srv.jobs = jobs
	f.h.srv.inbox = f.h.srv.inbox.WithJobs(jobs)
	f.h.srv.engine = automation.NewEngine(automation.Deps{Pool: f.h.pool, Jobs: jobs})
	return f
}

func (f *inboxFixture) audited(action string) int {
	return f.h.count(`SELECT count(*) FROM audit_log WHERE action = $1`, action)
}

func obj(r response, key string) map[string]any {
	m, _ := r.body[key].(map[string]any)
	return m
}

func fieldsOf(r response) map[string]any {
	e, _ := r.body["error"].(map[string]any)
	m, _ := e["fields"].(map[string]any)
	return m
}

func TestAutomationSettingsAreForAdminsOnly(t *testing.T) {
	f := newAutomationFixture(t)
	validRule := map[string]any{"name": "R", "trigger": "message_received", "actions": []any{map[string]any{"type": "mark_spam"}}}
	for name, c := range map[string]*client{"agent": f.agent, "readonly": f.readonly} {
		for _, req := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/api/v1/rules", nil},
			{"POST", "/api/v1/rules", validRule},
			{"PUT", "/api/v1/rules/order", map[string]any{"ids": []string{}}},
			{"GET", "/api/v1/business-hours", nil},
			{"POST", "/api/v1/business-hours", map[string]any{"name": "K", "timezone": "Europe/Amsterdam", "weekly": map[string]any{"mon": []any{map[string]string{"start": "09:00", "end": "17:00"}}}}},
			{"GET", "/api/v1/sla-policies", nil},
			{"POST", "/api/v1/sla-policies", map[string]any{"name": "S", "first_response_minutes": 60}},
			{"GET", "/api/v1/assignment", nil},
			{"PUT", "/api/v1/mailboxes/" + f.mailboxA + "/automation", map[string]any{"auto_assign_mode": "round_robin"}},
			{"PUT", "/api/v1/users/" + f.agentID + "/capacity", map[string]any{"max_open": 5}},
			{"GET", "/api/v1/settings/automation", nil},
			{"PUT", "/api/v1/settings/automation", map[string]any{"auto_resolve_days": 7}},
		} {
			if r := c.do(req.method, req.path, req.body); r.status != 403 || r.errCode() != "forbidden" {
				t.Errorf("%s %s %s: got %d %s, want 403 forbidden", name, req.method, req.path, r.status, r.errCode())
			}
		}
	}
	if f.h.count(`SELECT count(*) FROM rules`) != 0 || f.h.count(`SELECT count(*) FROM sla_policies`) != 0 {
		t.Fatal("something was created by a user without admin rights")
	}
}

func TestRuleLifecycle(t *testing.T) {
	f := newAutomationFixture(t)
	label := f.queryID(`INSERT INTO labels (name, color_token) VALUES ('Facturen', 'blue') RETURNING id`)
	create := func(name string) response {
		return f.admin.do("POST", "/api/v1/rules", map[string]any{
			"name": name, "trigger": "conversation_created", "mailbox_id": f.mailboxA,
			"conditions": map[string]any{"match": "all", "items": []any{map[string]any{"field": "subject", "op": "contains", "value": "Factuur"}}},
			"actions":    []any{map[string]any{"type": "add_label", "label_id": label}},
		})
	}
	r := create("Facturen")
	expect(t, r, 201, "")
	rule := obj(r, "rule")
	first, _ := rule["id"].(string)
	if rule["position"] != float64(1) || rule["enabled"] != true || rule["stop_processing"] != false || rule["idle_hours"] != nil {
		t.Fatalf("rule = %v", rule)
	}
	second, _ := obj(create("Tweede"), "rule")["id"].(string)

	list := f.admin.do("GET", "/api/v1/rules", nil)
	expect(t, list, 200, "")
	rules, _ := list.body["rules"].([]any)
	if len(rules) != 2 || rules[0].(map[string]any)["id"] != first {
		t.Fatalf("rules = %v", rules)
	}

	// Patch validates the whole rule and keeps what is not mentioned.
	r = f.admin.do("PATCH", "/api/v1/rules/"+first, map[string]any{"enabled": false, "stop_processing": true, "mailbox_id": nil})
	expect(t, r, 200, "")
	if got := obj(r, "rule"); got["enabled"] != false || got["stop_processing"] != true || got["mailbox_id"] != nil || got["name"] != "Facturen" {
		t.Fatalf("patched rule = %v", got)
	}
	r = f.admin.do("PATCH", "/api/v1/rules/"+first, map[string]any{"trigger": "customer_idle"})
	expect(t, r, 422, "validation_failed")
	if fieldsOf(r)["idle_hours"] != "invalid" {
		t.Fatalf("fields = %v", fieldsOf(r))
	}
	expect(t, f.admin.do("PATCH", "/api/v1/rules/"+first, map[string]any{"trigger": "customer_idle", "idle_hours": 48}), 200, "")
	r = f.admin.do("PATCH", "/api/v1/rules/"+first, map[string]any{"trigger": "message_received"})
	expect(t, r, 200, "")
	if obj(r, "rule")["idle_hours"] != nil {
		t.Fatal("idle hours must be dropped when the trigger is no longer customer_idle")
	}
	expect(t, f.admin.do("PATCH", "/api/v1/rules/0199a000-0000-7000-8000-000000000000", map[string]any{"enabled": true}), 404, "not_found")

	// Ordering needs the complete list.
	expect(t, f.admin.do("PUT", "/api/v1/rules/order", map[string]any{"ids": []string{second, first}}), 204, "")
	list = f.admin.do("GET", "/api/v1/rules", nil)
	if rules, _ = list.body["rules"].([]any); rules[0].(map[string]any)["id"] != second {
		t.Fatalf("order not applied: %v", rules)
	}
	expect(t, f.admin.do("PUT", "/api/v1/rules/order", map[string]any{"ids": []string{second}}), 409, "rules_changed")
	expect(t, f.admin.do("PUT", "/api/v1/rules/order", map[string]any{"ids": []string{second, second}}), 422, "validation_failed")

	expect(t, f.admin.do("DELETE", "/api/v1/rules/"+second, nil), 204, "")
	expect(t, f.admin.do("DELETE", "/api/v1/rules/"+second, nil), 404, "not_found")
	for action, want := range map[string]int{"automation.rule_created": 2, "automation.rule_updated": 3, "automation.rule_deleted": 1, "automation.rules_reordered": 1} {
		if got := f.audited(action); got != want {
			t.Errorf("audit %s = %d, want %d", action, got, want)
		}
	}
}

func TestRuleValidation(t *testing.T) {
	f := newAutomationFixture(t)
	post := func(body map[string]any) response { return f.admin.do("POST", "/api/v1/rules", body) }
	good := func() map[string]any {
		return map[string]any{"name": "R", "trigger": "message_received", "actions": []any{map[string]any{"type": "mark_spam"}}}
	}

	// Unknown fields never pass, at the top level or inside conditions and actions.
	body := good()
	body["surprise"] = true
	expect(t, post(body), 400, "invalid_request")

	body = good()
	body["conditions"] = map[string]any{"match": "all", "items": []any{map[string]any{"field": "subject", "op": "contains", "value": "x", "extra": 1}}}
	r := post(body)
	expect(t, r, 422, "validation_failed")
	if fieldsOf(r)["conditions"] == nil {
		t.Fatalf("fields = %v", fieldsOf(r))
	}

	body = good()
	body["actions"] = []any{map[string]any{"type": "mark_spam", "user_id": "0199a000-0000-7000-8000-000000000001"}}
	r = post(body)
	expect(t, r, 422, "validation_failed")
	if fieldsOf(r)["actions.0.user_id"] != "not_allowed" {
		t.Fatalf("fields = %v", fieldsOf(r))
	}

	body = good()
	body["actions"] = []any{map[string]any{"type": "delete_everything"}}
	r = post(body)
	expect(t, r, 422, "validation_failed")

	body = good()
	body["trigger"] = "nope"
	body["name"] = " "
	r = post(body)
	if fieldsOf(r)["trigger"] != "invalid" || fieldsOf(r)["name"] != "invalid" {
		t.Fatalf("fields = %v", fieldsOf(r))
	}

	body = good()
	body["mailbox_id"] = "0199a000-0000-7000-8000-000000000009"
	expect(t, post(body), 422, "validation_failed")
	body["mailbox_id"] = "not-a-uuid"
	expect(t, post(body), 422, "validation_failed")

	delete(body, "mailbox_id")
	delete(body, "actions")
	expect(t, post(body), 422, "validation_failed")
	if f.h.count(`SELECT count(*) FROM rules`) != 0 {
		t.Fatal("an invalid rule was stored")
	}
}

func TestRuleRunLog(t *testing.T) {
	f := newAutomationFixture(t)
	r := f.admin.do("POST", "/api/v1/rules", map[string]any{"name": "R", "trigger": "message_received", "actions": []any{map[string]any{"type": "mark_spam"}}})
	id, _ := obj(r, "rule")["id"].(string)
	conv := f.conversation("Factuur", convOpt{mailbox: f.mailboxA})
	f.exec(`INSERT INTO rule_runs (rule_id, conversation_id, trigger, matched, actions_applied, error) VALUES ($1, $2, 'message_received', true, '[{"type":"mark_spam","result":"applied"}]', ''), ($1, $2, 'message_received', false, '[]', '')`, id, conv)

	r = f.admin.do("GET", "/api/v1/rules/"+id+"/runs", nil)
	expect(t, r, 200, "")
	runs, _ := r.body["runs"].([]any)
	if len(runs) != 2 {
		t.Fatalf("runs = %v", runs)
	}
	newest := runs[0].(map[string]any)
	if newest["matched"] != false || newest["conversation_id"] != conv || newest["conversation_number"] == nil {
		t.Fatalf("newest run = %v", newest)
	}
	acts, _ := runs[1].(map[string]any)["actions"].([]any)
	if len(acts) != 1 {
		t.Fatalf("actions = %v", acts)
	}
	expect(t, f.admin.do("GET", "/api/v1/rules/"+id+"/runs?limit=0", nil), 400, "invalid_request")
	expect(t, f.admin.do("GET", "/api/v1/rules/0199a000-0000-7000-8000-000000000000/runs", nil), 404, "not_found")
}

func weekly(days ...string) map[string]any {
	m := map[string]any{}
	for _, d := range days {
		m[d] = []any{map[string]string{"start": "09:00", "end": "17:00"}}
	}
	return m
}

func TestBusinessHours(t *testing.T) {
	f := newAutomationFixture(t)
	create := func(name string, extra map[string]any) response {
		body := map[string]any{"name": name, "timezone": "Europe/Amsterdam", "weekly": weekly("mon", "tue", "wed", "thu", "fri"), "holidays": []string{"2026-12-25", "2026-04-27"}}
		for k, v := range extra {
			body[k] = v
		}
		return f.admin.do("POST", "/api/v1/business-hours", body)
	}
	r := create("Kantoor", nil)
	expect(t, r, 201, "")
	office := obj(r, "business_hours")
	officeID, _ := office["id"].(string)
	if office["is_default"] != true {
		t.Fatal("the first schedule must become the default")
	}
	holidays, _ := office["holidays"].([]any)
	if len(holidays) != 2 || holidays[0] != "2026-04-27" {
		t.Fatalf("holidays = %v (sorted)", holidays)
	}

	r = create("Weekend", map[string]any{"weekly": map[string]any{"sat": []any{map[string]string{"start": "22:00", "end": "06:00"}}}})
	expect(t, r, 201, "")
	weekendID, _ := obj(r, "business_hours")["id"].(string)
	if obj(r, "business_hours")["is_default"] != false {
		t.Fatal("later schedules are not the default")
	}
	expect(t, create("kantoor", nil), 422, "validation_failed")

	invalid := map[string]map[string]any{
		"timezone": {"timezone": "Mars/Base"},
		"weekly":   {"weekly": weekly("monday")},
	}
	for field, body := range invalid {
		if r := create("Nieuw", body); r.status != 422 || fieldsOf(r)[field] == nil {
			t.Errorf("%s: got %d %s", field, r.status, r.raw)
		}
	}
	for name, body := range map[string]map[string]any{
		"empty":         {"weekly": map[string]any{}},
		"bad clock":     {"weekly": map[string]any{"mon": []any{map[string]string{"start": "9", "end": "17:00"}}}},
		"zero length":   {"weekly": map[string]any{"mon": []any{map[string]string{"start": "09:00", "end": "09:00"}}}},
		"bad holiday":   {"holidays": []string{"25-12-2026"}},
		"missing name":  {"name": nil},
		"unknown field": {"extra": 1},
	} {
		if r := create("Nieuw", body); r.status < 400 {
			t.Errorf("%s: accepted (%d)", name, r.status)
		}
	}

	// Making another schedule the default moves the flag; the default cannot be switched off.
	expect(t, f.admin.do("PATCH", "/api/v1/business-hours/"+weekendID, map[string]any{"is_default": true}), 200, "")
	if f.h.count(`SELECT count(*) FROM business_hours WHERE is_default AND id::text = $1`, weekendID) != 1 || f.h.count(`SELECT count(*) FROM business_hours WHERE is_default`) != 1 {
		t.Fatal("exactly the weekend schedule must be the default")
	}
	expect(t, f.admin.do("PATCH", "/api/v1/business-hours/"+weekendID, map[string]any{"is_default": false}), 422, "validation_failed")
	expect(t, f.admin.do("PATCH", "/api/v1/business-hours/"+officeID, map[string]any{"name": "Hoofdkantoor", "timezone": "Europe/London"}), 200, "")
	if f.h.count(`SELECT count(*) FROM business_hours WHERE name = 'Hoofdkantoor' AND timezone = 'Europe/London' AND jsonb_array_length(weekly->'mon') = 1`) != 1 {
		t.Fatal("patch must keep the hours that were not mentioned")
	}

	// The default cannot be deleted; one that a policy uses cannot either.
	expect(t, f.admin.do("DELETE", "/api/v1/business-hours/"+weekendID, nil), 422, "validation_failed")
	expect(t, f.admin.do("POST", "/api/v1/sla-policies", map[string]any{"name": "Kantoor SLA", "first_response_minutes": 60, "business_hours_id": officeID}), 201, "")
	expect(t, f.admin.do("DELETE", "/api/v1/business-hours/"+officeID, nil), 409, "in_use")
	f.exec(`DELETE FROM sla_policies`)
	expect(t, f.admin.do("DELETE", "/api/v1/business-hours/"+officeID, nil), 204, "")
	expect(t, f.admin.do("DELETE", "/api/v1/business-hours/"+officeID, nil), 404, "not_found")
	for action, want := range map[string]int{"automation.business_hours_created": 2, "automation.business_hours_updated": 2, "automation.business_hours_deleted": 1} {
		if got := f.audited(action); got != want {
			t.Errorf("audit %s = %d, want %d", action, got, want)
		}
	}
}

func TestSLAPolicies(t *testing.T) {
	f := newAutomationFixture(t)
	hours := obj(f.admin.do("POST", "/api/v1/business-hours", map[string]any{"name": "K", "timezone": "UTC", "weekly": weekly("mon")}), "business_hours")["id"].(string)

	r := f.admin.do("POST", "/api/v1/sla-policies", map[string]any{"name": "Standaard", "first_response_minutes": 60, "resolution_minutes": 1440, "business_hours_id": hours})
	expect(t, r, 201, "")
	policy := obj(r, "sla_policy")
	id, _ := policy["id"].(string)
	if policy["at_risk_percent"] != float64(80) || policy["first_response_minutes"] != float64(60) || policy["business_hours_id"] != hours {
		t.Fatalf("policy = %v", policy)
	}

	for name, body := range map[string]map[string]any{
		"no target":              {"name": "Leeg"},
		"zero minutes":           {"name": "Nul", "first_response_minutes": 0},
		"too many minutes":       {"name": "Lang", "resolution_minutes": 525601},
		"percent 0":              {"name": "P0", "first_response_minutes": 5, "at_risk_percent": 0},
		"percent 100":            {"name": "P100", "first_response_minutes": 5, "at_risk_percent": 100},
		"unknown business hours": {"name": "BH", "first_response_minutes": 5, "business_hours_id": "0199a000-0000-7000-8000-000000000001"},
		"duplicate name":         {"name": "standaard", "first_response_minutes": 5},
		"no name":                {"first_response_minutes": 5},
	} {
		if r := f.admin.do("POST", "/api/v1/sla-policies", body); r.status != 422 {
			t.Errorf("%s: got %d %s", name, r.status, r.raw)
		}
	}

	// null clears a target; the policy needs at least one left.
	r = f.admin.do("PATCH", "/api/v1/sla-policies/"+id, map[string]any{"resolution_minutes": nil, "at_risk_percent": 50, "business_hours_id": nil})
	expect(t, r, 200, "")
	if got := obj(r, "sla_policy"); got["resolution_minutes"] != nil || got["first_response_minutes"] != float64(60) || got["at_risk_percent"] != float64(50) || got["business_hours_id"] != nil {
		t.Fatalf("patched = %v", got)
	}
	expect(t, f.admin.do("PATCH", "/api/v1/sla-policies/"+id, map[string]any{"first_response_minutes": nil}), 422, "validation_failed")
	expect(t, f.admin.do("PATCH", "/api/v1/sla-policies/0199a000-0000-7000-8000-000000000000", map[string]any{"name": "x"}), 404, "not_found")

	list := f.admin.do("GET", "/api/v1/sla-policies", nil)
	if policies, _ := list.body["sla_policies"].([]any); len(policies) != 1 {
		t.Fatalf("policies = %v", policies)
	}
	expect(t, f.admin.do("DELETE", "/api/v1/sla-policies/"+id, nil), 204, "")
	expect(t, f.admin.do("DELETE", "/api/v1/sla-policies/"+id, nil), 404, "not_found")
	for action, want := range map[string]int{"automation.sla_policy_created": 1, "automation.sla_policy_updated": 1, "automation.sla_policy_deleted": 1} {
		if got := f.audited(action); got != want {
			t.Errorf("audit %s = %d, want %d", action, got, want)
		}
	}
}

func TestAssignmentSettings(t *testing.T) {
	f := newAutomationFixture(t)
	policy := obj(f.admin.do("POST", "/api/v1/sla-policies", map[string]any{"name": "S", "first_response_minutes": 30}), "sla_policy")["id"].(string)

	r := f.admin.do("GET", "/api/v1/assignment", nil)
	expect(t, r, 200, "")
	mailboxes, _ := r.body["mailboxes"].([]any)
	agents, _ := r.body["agents"].([]any)
	if len(mailboxes) != 2 || mailboxes[0].(map[string]any)["auto_assign_mode"] != "off" {
		t.Fatalf("mailboxes = %v", mailboxes)
	}
	var agent map[string]any
	for _, a := range agents {
		m := a.(map[string]any)
		if m["id"] == f.agentID {
			agent = m
		}
		if m["role"] == "readonly" {
			t.Errorf("a read-only user is listed as an agent: %v", m)
		}
	}
	if agent == nil || agent["availability"] != "online" || agent["max_open"] != nil || agent["open_count"] != float64(0) {
		t.Fatalf("agent = %v", agent)
	}

	r = f.admin.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/automation", map[string]any{"auto_assign_mode": "balanced", "default_sla_policy_id": policy})
	expect(t, r, 200, "")
	if got := obj(r, "mailbox"); got["auto_assign_mode"] != "balanced" || got["default_sla_policy_id"] != policy || got["business_hours_id"] != nil {
		t.Fatalf("mailbox = %v", got)
	}
	expect(t, f.admin.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/automation", map[string]any{"auto_assign_mode": "random"}), 422, "validation_failed")
	expect(t, f.admin.do("PUT", "/api/v1/mailboxes/"+f.mailboxA+"/automation", map[string]any{"auto_assign_mode": "off", "default_sla_policy_id": "0199a000-0000-7000-8000-000000000001"}), 422, "validation_failed")
	expect(t, f.admin.do("PUT", "/api/v1/mailboxes/0199a000-0000-7000-8000-000000000000/automation", map[string]any{"auto_assign_mode": "off"}), 404, "not_found")
	if f.h.count(`SELECT count(*) FROM mailboxes WHERE auto_assign_mode = 'balanced'`) != 1 {
		t.Fatal("an invalid update changed the mailbox")
	}

	expect(t, f.admin.do("PUT", "/api/v1/users/"+f.agentID+"/capacity", map[string]any{"max_open": 12}), 204, "")
	if f.h.count(`SELECT count(*) FROM users WHERE id::text = $1 AND max_open = 12`, f.agentID) != 1 {
		t.Fatal("capacity not stored")
	}
	expect(t, f.admin.do("PUT", "/api/v1/users/"+f.agentID+"/capacity", map[string]any{"max_open": nil}), 204, "")
	for _, bad := range []any{0, -1, 10001} {
		expect(t, f.admin.do("PUT", "/api/v1/users/"+f.agentID+"/capacity", map[string]any{"max_open": bad}), 422, "validation_failed")
	}
	expect(t, f.admin.do("PUT", "/api/v1/users/"+f.agentID+"/capacity", map[string]any{}), 422, "validation_failed")
	var readonlyID string
	if err := f.h.pool.QueryRow(t.Context(), `SELECT id::text FROM users WHERE role = 'readonly'`).Scan(&readonlyID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.admin.do("PUT", "/api/v1/users/"+readonlyID+"/capacity", map[string]any{"max_open": 3}), 404, "not_found")
	for action, want := range map[string]int{"automation.mailbox_changed": 1, "automation.capacity_changed": 2} {
		if got := f.audited(action); got != want {
			t.Errorf("audit %s = %d, want %d", action, got, want)
		}
	}
}

func TestAutomationSettingsAutoResolve(t *testing.T) {
	f := newAutomationFixture(t)
	r := f.admin.do("GET", "/api/v1/settings/automation", nil)
	expect(t, r, 200, "")
	if r.body["auto_resolve_days"] != float64(0) {
		t.Fatalf("default = %v, want off", r.body["auto_resolve_days"])
	}
	expect(t, f.admin.do("PUT", "/api/v1/settings/automation", map[string]any{"auto_resolve_days": 14}), 200, "")
	if r = f.admin.do("GET", "/api/v1/settings/automation", nil); r.body["auto_resolve_days"] != float64(14) {
		t.Fatalf("stored = %v", r.body)
	}
	for _, bad := range []any{-1, 366} {
		expect(t, f.admin.do("PUT", "/api/v1/settings/automation", map[string]any{"auto_resolve_days": bad}), 422, "validation_failed")
	}
	expect(t, f.admin.do("PUT", "/api/v1/settings/automation", map[string]any{"auto_resolve_days": 3, "other": 1}), 400, "invalid_request")
	if f.audited("automation.settings_changed") != 1 {
		t.Fatal("the change was not audited")
	}
}

func TestAvailability(t *testing.T) {
	f := newAutomationFixture(t)
	r := f.agent.do("GET", "/api/v1/me", nil)
	if u := obj(r, "user"); u["availability"] != "online" {
		t.Fatalf("user = %v", u)
	}
	expect(t, f.agent.do("PUT", "/api/v1/me/availability", map[string]any{"availability": "busy"}), 200, "")
	if u := obj(f.agent.do("GET", "/api/v1/me", nil), "user"); u["availability"] != "busy" {
		t.Fatalf("user = %v", u)
	}
	expect(t, f.agent.do("PUT", "/api/v1/me/availability", map[string]any{"availability": "away"}), 422, "validation_failed")
	expect(t, f.agent.do("PUT", "/api/v1/me/availability", map[string]any{}), 422, "validation_failed")
	// It only ever changes the caller's own account.
	if f.h.count(`SELECT count(*) FROM users WHERE availability = 'busy'`) != 1 {
		t.Fatal("availability of somebody else changed")
	}
}

func macroBody(name, scope string, actions ...map[string]any) map[string]any {
	list := make([]any, len(actions))
	for i, a := range actions {
		list[i] = a
	}
	return map[string]any{"name": name, "scope": scope, "actions": list}
}

func TestMacroPermissions(t *testing.T) {
	f := newAutomationFixture(t)
	spam := map[string]any{"type": "mark_spam"}

	r := f.agent.do("POST", "/api/v1/macros", macroBody("Mijn macro", "personal", spam))
	expect(t, r, 201, "")
	personal := obj(r, "macro")
	personalID, _ := personal["id"].(string)
	if personal["scope"] != "personal" || personal["owner"] != true {
		t.Fatalf("macro = %v", personal)
	}
	expect(t, f.agent.do("POST", "/api/v1/macros", macroBody("Voor iedereen", "global", spam)), 403, "forbidden")
	expect(t, f.readonly.do("POST", "/api/v1/macros", macroBody("Nee", "personal", spam)), 403, "forbidden")
	r = f.admin.do("POST", "/api/v1/macros", macroBody("Sluiten", "global", map[string]any{"type": "set_status", "status": "closed"}))
	expect(t, r, 201, "")
	globalID, _ := obj(r, "macro")["id"].(string)

	names := func(c *client) []string {
		r := c.do("GET", "/api/v1/macros", nil)
		expect(t, r, 200, "")
		var out []string
		for _, m := range r.body["macros"].([]any) {
			out = append(out, m.(map[string]any)["name"].(string))
		}
		return out
	}
	if got := fmt.Sprint(names(f.agent)); got != "[Sluiten Mijn macro]" {
		t.Fatalf("agent sees %s: workspace macros first, then personal", got)
	}
	if got := fmt.Sprint(names(f.admin)); got != "[Sluiten]" {
		t.Fatalf("admin sees %s: personal macros of others stay private", got)
	}
	if got := fmt.Sprint(names(f.readonly)); got != "[Sluiten]" {
		t.Fatalf("readonly sees %s", got)
	}

	expect(t, f.admin.do("PATCH", "/api/v1/macros/"+personalID, map[string]any{"name": "Gestolen"}), 404, "not_found")
	expect(t, f.admin.do("DELETE", "/api/v1/macros/"+personalID, nil), 404, "not_found")
	expect(t, f.agent.do("PATCH", "/api/v1/macros/"+globalID, map[string]any{"name": "Mijn"}), 403, "forbidden")
	expect(t, f.agent.do("DELETE", "/api/v1/macros/"+globalID, nil), 403, "forbidden")
	expect(t, f.agent.do("PATCH", "/api/v1/macros/"+personalID, map[string]any{"name": "Hernoemd", "scope": "global"}), 422, "validation_failed")
	r = f.agent.do("PATCH", "/api/v1/macros/"+personalID, map[string]any{"name": "Hernoemd", "actions": []any{map[string]any{"type": "set_priority", "priority": "high"}}})
	expect(t, r, 200, "")
	if got := obj(r, "macro"); got["name"] != "Hernoemd" {
		t.Fatalf("macro = %v", got)
	}
	expect(t, f.admin.do("PATCH", "/api/v1/macros/"+globalID, map[string]any{"name": "Afronden"}), 200, "")

	expect(t, f.agent.do("DELETE", "/api/v1/macros/"+personalID, nil), 204, "")
	expect(t, f.admin.do("DELETE", "/api/v1/macros/"+globalID, nil), 204, "")
	expect(t, f.agent.do("DELETE", "/api/v1/macros/"+personalID, nil), 404, "not_found")
	for action, want := range map[string]int{"automation.macro_created": 2, "automation.macro_updated": 2, "automation.macro_deleted": 2} {
		if got := f.audited(action); got != want {
			t.Errorf("audit %s = %d, want %d", action, got, want)
		}
	}
}

func TestMacroValidation(t *testing.T) {
	f := newAutomationFixture(t)
	reply := map[string]any{"type": "auto_reply", "text": "Hoi"}
	for name, body := range map[string]map[string]any{
		"auto reply is for rules": macroBody("M", "personal", reply),
		"no actions":              macroBody("M", "personal"),
		"unknown scope":           macroBody("M", "team", map[string]any{"type": "mark_spam"}),
		"no name":                 macroBody(" ", "personal", map[string]any{"type": "mark_spam"}),
		"unknown action":          macroBody("M", "personal", map[string]any{"type": "explode"}),
	} {
		if r := f.agent.do("POST", "/api/v1/macros", body); r.status != 422 {
			t.Errorf("%s: got %d %s", name, r.status, r.raw)
		}
	}
	if f.h.count(`SELECT count(*) FROM macros`) != 0 {
		t.Fatal("an invalid macro was stored")
	}
}

func TestRunMacro(t *testing.T) {
	f := newAutomationFixture(t)
	label := f.queryID(`INSERT INTO labels (name, color_token) VALUES ('VIP', 'amber') RETURNING id`)
	first := f.conversation("een", convOpt{mailbox: f.mailboxA})
	second := f.conversation("twee", convOpt{mailbox: f.mailboxA})
	readOnlyBox := f.conversation("lezen", convOpt{mailbox: f.mailboxB})
	// Bravo is readable but not writable for members of the Billing team.
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, f.otherTeam, f.agentID)
	r := f.agent.do("POST", "/api/v1/macros", macroBody("VIP", "personal",
		map[string]any{"type": "add_label", "label_id": label}, map[string]any{"type": "set_priority", "priority": "high"}))
	macro, _ := obj(r, "macro")["id"].(string)

	r = f.agent.do("POST", "/api/v1/macros/"+macro+"/run", map[string]any{"conversation_ids": []string{first, second, readOnlyBox, "0199a000-0000-7000-8000-000000000000"}})
	expect(t, r, 200, "")
	results, _ := r.body["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("results = %v", results)
	}
	codes := map[string]string{}
	for _, res := range results {
		m := res.(map[string]any)
		code, _ := m["code"].(string)
		codes[m["id"].(string)] = fmt.Sprint(m["ok"], code)
	}
	want := map[string]string{
		first: "true", second: "true", readOnlyBox: "falseforbidden", "0199a000-0000-7000-8000-000000000000": "falsenot_found",
	}
	for id, w := range want {
		if codes[id] != w {
			t.Errorf("%s: %s, want %s", id, codes[id], w)
		}
	}
	if f.h.count(`SELECT count(*) FROM conversations WHERE priority = 'high' AND id::text = ANY($1)`, []string{first, second}) != 2 ||
		f.h.count(`SELECT count(*) FROM conversation_labels`) != 2 {
		t.Fatal("the macro did not update both conversations")
	}
	if f.h.count(`SELECT count(*) FROM conversations WHERE id::text = $1 AND priority <> 'none'`, readOnlyBox) != 0 {
		t.Fatal("a conversation the agent may not change was updated")
	}

	// Somebody else's personal macro does not exist for you.
	expect(t, f.admin.do("POST", "/api/v1/macros/"+macro+"/run", map[string]any{"conversation_ids": []string{first}}), 404, "not_found")
	expect(t, f.readonly.do("POST", "/api/v1/macros/"+macro+"/run", map[string]any{"conversation_ids": []string{first}}), 403, "forbidden")
	expect(t, f.agent.do("POST", "/api/v1/macros/"+macro+"/run", map[string]any{"conversation_ids": []string{}}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/macros/"+macro+"/run", map[string]any{"conversation_ids": []string{"nope"}}), 422, "validation_failed")
	expect(t, f.agent.do("POST", "/api/v1/macros/0199a000-0000-7000-8000-000000000000/run", map[string]any{"conversation_ids": []string{first}}), 404, "not_found")
}

func TestConversationsExposeSLA(t *testing.T) {
	f := newAutomationFixture(t)
	policy := f.queryID(`INSERT INTO sla_policies (name, first_response_minutes, resolution_minutes) VALUES ('S', 60, 240) RETURNING id`)
	tracked := f.conversation("met SLA", convOpt{mailbox: f.mailboxA})
	untracked := f.conversation("zonder SLA", convOpt{mailbox: f.mailboxA})
	f.exec(`UPDATE conversations SET sla_policy_id = $2, sla_state = 'at_risk', first_response_due_at = '2026-03-02T10:00:00Z', resolution_due_at = '2026-03-02T14:00:00Z' WHERE id::text = $1`, tracked, policy)

	r := f.agent.do("GET", "/api/v1/conversations", nil)
	expect(t, r, 200, "")
	for _, c := range r.body["conversations"].([]any) {
		m := c.(map[string]any)
		switch m["subject"] {
		case "zonder SLA":
			if m["sla"] != nil {
				t.Errorf("sla of an untracked conversation = %v", m["sla"])
			}
		case "met SLA":
			sla, _ := m["sla"].(map[string]any)
			if sla["state"] != "at_risk" || sla["first_response_due_at"] != "2026-03-02T10:00:00Z" || sla["resolution_due_at"] != "2026-03-02T14:00:00Z" || sla["first_response_met_at"] != nil {
				t.Errorf("sla = %v", sla)
			}
		}
	}
	r = f.agent.do("GET", "/api/v1/conversations/"+tracked, nil)
	expect(t, r, 200, "")
	if sla, _ := obj(r, "conversation")["sla"].(map[string]any); sla["state"] != "at_risk" {
		t.Fatalf("detail sla = %v", sla)
	}
	_ = untracked
}
