package api

import (
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

type reportOverviewResponse struct {
	Period struct {
		Preset, From, To, Timezone, Group string
		Days                              int
	}
	TimeBasis string `json:"time_basis"`
	Current   struct {
		New              int `json:"new_conversations"`
		CustomerMessages int `json:"customer_messages"`
	}
	Series []struct {
		Date string
		New  int `json:"new_conversations"`
	}
	OpenNow int `json:"open_now"`
}

type reportTableResponse struct {
	Rows []struct {
		ID   *string
		Name string
		New  int `json:"new_conversations"`
	}
}

func (f *inboxFixture) overview(c *client, query string) reportOverviewResponse {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/reports/overview"+query, nil)
	expect(f.h.t, r, 200, "")
	var out reportOverviewResponse
	if err := json.Unmarshal(r.raw, &out); err != nil {
		f.h.t.Fatal(err)
	}
	return out
}

func (f *inboxFixture) table(c *client, kind, query string) map[string]int {
	f.h.t.Helper()
	r := c.do("GET", "/api/v1/reports/"+kind+query, nil)
	expect(f.h.t, r, 200, "")
	var out reportTableResponse
	if err := json.Unmarshal(r.raw, &out); err != nil {
		f.h.t.Fatal(err)
	}
	rows := map[string]int{}
	for _, row := range out.Rows {
		rows[row.Name] = row.New
	}
	return rows
}

func TestReportsAreScopedToReadableMailboxes(t *testing.T) {
	f := newInboxFixture(t)
	adminID := f.queryID(`SELECT id FROM users WHERE role = 'admin'`)
	f.exec(`UPDATE users SET name = 'Anna' WHERE id = $1`, f.agentID)
	f.exec(`UPDATE users SET name = 'Admin' WHERE id = $1`, adminID)
	for range 3 {
		f.conversation("a", convOpt{mailbox: f.mailboxA, assignee: f.agentID, team: f.team})
	}
	f.conversation("a-unassigned", convOpt{mailbox: f.mailboxA})
	for range 5 {
		f.conversation("b", convOpt{mailbox: f.mailboxB, assignee: adminID, team: f.otherTeam})
	}

	if got := f.overview(f.agent, "?period=today").Current.New; got != 4 {
		t.Errorf("agent sees %d new conversations, want the 4 in mailbox Alpha", got)
	}
	if got := f.overview(f.admin, "?period=today").Current.New; got != 9 {
		t.Errorf("admin sees %d new conversations, want 9", got)
	}
	if got := f.overview(f.readonly, "?period=today").Current.New; got != 4 {
		t.Errorf("a readonly user sees %d, want 4", got)
	}

	// A mailbox the user cannot read does not exist for them.
	r := f.agent.do("GET", "/api/v1/reports/overview?mailbox="+f.mailboxB, nil)
	expect(t, r, 404, "not_found")
	expect(t, f.agent.do("GET", "/api/v1/reports/mailboxes/export?mailbox="+f.mailboxB, nil), 404, "not_found")
	if got := f.overview(f.admin, "?period=today&mailbox="+f.mailboxB).Current.New; got != 5 {
		t.Errorf("admin filtering on Bravo: %d, want 5", got)
	}

	// Only admins look at another agent's numbers.
	expect(t, f.agent.do("GET", "/api/v1/reports/overview?agent="+adminID, nil), 403, "forbidden")
	expect(t, f.agent.do("GET", "/api/v1/reports/live?agent="+adminID, nil), 403, "forbidden")
	if got := f.overview(f.agent, "?period=today&agent="+f.agentID).Current.New; got != 3 {
		t.Errorf("agent filtering on themselves: %d, want 3", got)
	}
	if got := f.overview(f.admin, "?period=today&agent="+f.agentID).Current.New; got != 3 {
		t.Errorf("admin filtering on the agent: %d, want 3", got)
	}

	agents := f.table(f.agent, "agents", "?period=today")
	if len(agents) != 1 || agents["Anna"] != 3 {
		t.Errorf("the agent table for an agent must hold only their own row: %v", agents)
	}
	agents = f.table(f.admin, "agents", "?period=today")
	if len(agents) != 3 || agents["Anna"] != 3 || agents["Admin"] != 5 || agents[""] != 1 {
		t.Errorf("the agent table for an admin: %v", agents)
	}
	if mailboxes := f.table(f.agent, "mailboxes", "?period=today"); len(mailboxes) != 1 || mailboxes["Alpha"] != 4 {
		t.Errorf("mailbox table for an agent: %v", mailboxes)
	}
	if mailboxes := f.table(f.admin, "mailboxes", "?period=today"); len(mailboxes) != 2 || mailboxes["Bravo"] != 5 {
		t.Errorf("mailbox table for an admin: %v", mailboxes)
	}
}

func TestLiveReportCountsAndScope(t *testing.T) {
	f := newInboxFixture(t)
	adminID := f.queryID(`SELECT id FROM users WHERE role = 'admin'`)
	f.exec(`UPDATE users SET name = 'Anna' WHERE id = $1`, f.agentID)
	f.conversation("mine 1", convOpt{mailbox: f.mailboxA, assignee: f.agentID})
	f.conversation("mine 2", convOpt{mailbox: f.mailboxA, assignee: f.agentID})
	f.conversation("free", convOpt{mailbox: f.mailboxA})
	f.conversation("waiting", convOpt{mailbox: f.mailboxA, status: "waiting", assignee: f.agentID})
	f.conversation("closed", convOpt{mailbox: f.mailboxA, status: "closed"})
	risky := f.conversation("risk", convOpt{mailbox: f.mailboxA, assignee: f.agentID})
	f.exec(`UPDATE conversations SET sla_state = 'at_risk' WHERE id = $1`, risky)
	f.conversation("elsewhere", convOpt{mailbox: f.mailboxB, assignee: adminID})

	type live struct {
		Open, Unassigned, Waiting int
		SLAAtRisk                 int `json:"sla_at_risk"`
		SLABreached               int `json:"sla_breached"`
		Agents                    []struct {
			Name    string
			Open    int
			Waiting int
			Online  bool
		}
	}
	get := func(c *client) live {
		r := c.do("GET", "/api/v1/reports/live", nil)
		expect(t, r, 200, "")
		var out live
		if err := json.Unmarshal(r.raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	a := get(f.agent)
	if a.Open != 4 || a.Unassigned != 1 || a.Waiting != 1 || a.SLAAtRisk != 1 || a.SLABreached != 0 {
		t.Errorf("agent's live counts: %+v", a)
	}
	if len(a.Agents) != 1 || a.Agents[0].Name != "Anna" || a.Agents[0].Open != 3 || a.Agents[0].Waiting != 1 {
		t.Errorf("agent's own live row: %+v", a.Agents)
	}
	if ad := get(f.admin); ad.Open != 5 || len(ad.Agents) != 2 {
		t.Errorf("admin's live view: %+v", ad)
	}
}

func TestReportPeriodValidation(t *testing.T) {
	f := newInboxFixture(t)
	for name, q := range map[string]string{
		"unknown period":   "?period=yesterday",
		"custom no dates":  "?period=custom",
		"reversed":         "?period=custom&from=2026-05-02&to=2026-05-01",
		"too long":         "?period=custom&from=2025-01-01&to=2026-12-31",
		"bad grouping":     "?group=month",
		"bad mailbox id":   "?mailbox=nope",
		"bad team id":      "?team=nope",
		"bad label id":     "?label=nope",
		"bad csat grouper": "",
	} {
		if name == "bad csat grouper" {
			expect(t, f.admin.do("GET", "/api/v1/reports/csat?by=colour", nil), 422, "validation_failed")
			continue
		}
		if r := f.admin.do("GET", "/api/v1/reports/overview"+q, nil); r.status != 422 || r.errCode() != "validation_failed" {
			t.Errorf("%s: %d %s", name, r.status, r.raw)
		}
	}
	o := f.overview(f.admin, "?period=custom&from=2026-03-28&to=2026-03-30")
	if o.Period.Days != 3 || o.Period.Timezone != "Europe/Amsterdam" || len(o.Series) != 3 || o.Period.From != "2026-03-28" || o.Period.To != "2026-03-30" {
		t.Errorf("custom period: %+v", o.Period)
	}
}

func TestReportTimezoneSetting(t *testing.T) {
	f := newInboxFixture(t)
	r := f.admin.do("GET", "/api/v1/settings/reports", nil)
	expect(t, r, 200, "")
	if r.body["timezone"] != "Europe/Amsterdam" {
		t.Errorf("default timezone: %v", r.body["timezone"])
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus"} {
		expect(t, f.admin.do("PUT", "/api/v1/settings/reports", map[string]string{"timezone": bad}), 422, "validation_failed")
	}
	expect(t, f.agent.do("PUT", "/api/v1/settings/reports", map[string]string{"timezone": "UTC"}), 403, "forbidden")
	expect(t, f.admin.do("PUT", "/api/v1/settings/reports", map[string]string{"timezone": "America/New_York"}), 200, "")
	if tz := f.overview(f.agent, "").Period.Timezone; tz != "America/New_York" {
		t.Errorf("reports use %q after the change", tz)
	}
	if n := f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'settings.reports_changed'`); n != 1 {
		t.Errorf("%d audit entries for the change, want 1", n)
	}
}

func TestReportExportIsFormulaSafeAndAudited(t *testing.T) {
	f := newInboxFixture(t)
	f.exec(`UPDATE users SET name = '=HYPERLINK("http://evil.example","x")' WHERE id = $1`, f.agentID)
	f.conversation("a", convOpt{mailbox: f.mailboxA, assignee: f.agentID})

	r := f.admin.do("GET", "/api/v1/reports/agents/export?period=today", nil)
	expect(t, r, 200, "")
	if ct := r.header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if cd := r.header.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="rapportage-agents-`) {
		t.Errorf("content disposition %q", cd)
	}
	rows, err := csv.NewReader(strings.NewReader(string(r.raw))).ReadAll()
	if err != nil {
		t.Fatalf("not valid CSV: %v\n%s", err, r.raw)
	}
	if rows[0][0] != "naam" || rows[0][1] != "nieuwe_gesprekken" {
		t.Errorf("header %v", rows[0])
	}
	var found bool
	for _, row := range rows[1:] {
		if strings.Contains(row[0], "HYPERLINK") {
			found = true
			if !strings.HasPrefix(row[0], "'=") {
				t.Errorf("formula not neutralised: %q", row[0])
			}
		}
		if row[0] != "" && strings.ContainsRune("=+-@\t\r", rune(row[0][0])) {
			t.Errorf("cell %q starts with a formula character", row[0])
		}
	}
	if !found {
		t.Errorf("the agent row is missing:\n%s", r.raw)
	}
	if n := f.h.count(`SELECT count(*) FROM audit_log WHERE action = 'report.exported'`); n != 1 {
		t.Errorf("%d audit entries, want 1", n)
	}

	// An agent's export holds only their own row.
	rows, err = csv.NewReader(strings.NewReader(string(f.agent.do("GET", "/api/v1/reports/agents/export?period=today", nil).raw))).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Errorf("agent export: %v %v", rows, err)
	}
	expect(t, f.admin.do("GET", "/api/v1/reports/nothing/export", nil), 404, "not_found")
	for _, kind := range []string{"overview", "teams", "mailboxes", "labels", "csat", "live"} {
		if r := f.admin.do("GET", "/api/v1/reports/"+kind+"/export", nil); r.status != 200 || !strings.Contains(r.header.Get("Content-Type"), "text/csv") {
			t.Errorf("%s export: %d", kind, r.status)
		}
	}
}

func TestReportOverviewLifecycleAndLiveAttention(t *testing.T) {
	f := newInboxFixture(t)
	f.exec(`UPDATE users SET name = 'Anna' WHERE id = $1`, f.agentID)
	answered := f.conversation("answered", convOpt{mailbox: f.mailboxA, status: "closed", assignee: f.agentID})
	f.exec(`UPDATE conversations SET first_responded_at = created_at + interval '5 minutes', resolved_at = now() WHERE id = $1`, answered)
	late := f.conversation("late", convOpt{mailbox: f.mailboxA, assignee: f.agentID})
	f.exec(`UPDATE conversations SET first_response_due_at = now() - interval '10 minutes' WHERE id = $1`, late)
	soon := f.conversation("soon", convOpt{mailbox: f.mailboxA})
	f.exec(`UPDATE conversations SET first_response_due_at = now() + interval '25 minutes' WHERE id = $1`, soon)
	f.conversation("spam", convOpt{mailbox: f.mailboxA, status: "spam"})
	f.conversation("elsewhere", convOpt{mailbox: f.mailboxB})

	r := f.agent.do("GET", "/api/v1/reports/overview?period=today", nil)
	expect(t, r, 200, "")
	var o struct {
		Current struct {
			New int64 `json:"new_conversations"`
		}
		Lifecycle struct {
			Arrived, Spam int64
			Answered      struct{ Open, Waiting, Closed int64 }
			Unanswered    struct{ Open, Waiting, Closed int64 }
		}
		FirstResponseTarget struct {
			Count   int64
			Seconds *int64
		} `json:"first_response_target"`
		Series []struct{ Unanswered int64 }
	}
	if err := json.Unmarshal(r.raw, &o); err != nil {
		t.Fatal(err)
	}
	l := o.Lifecycle
	if l.Arrived != 4 || l.Spam != 1 || l.Answered.Closed != 1 || l.Unanswered.Open != 2 || o.Current.New != 3 {
		t.Errorf("lifecycle: %+v, new %d", l, o.Current.New)
	}
	if o.FirstResponseTarget.Count != 0 || o.FirstResponseTarget.Seconds != nil {
		t.Errorf("target without policies: %+v", o.FirstResponseTarget)
	}
	if len(o.Series) != 1 || o.Series[0].Unanswered != 2 {
		t.Errorf("series: %+v", o.Series)
	}

	r = f.agent.do("GET", "/api/v1/reports/live", nil)
	expect(t, r, 200, "")
	var live struct {
		Attention struct {
			Unanswered, Breached, Unassigned int64
			DueSoon                          int64 `json:"due_soon"`
			NextDueSeconds                   int64 `json:"next_due_seconds"`
			WindowMinutes                    int   `json:"window_minutes"`
			Holders                          []struct {
				Name  string
				Count int64
			}
		}
	}
	if err := json.Unmarshal(r.raw, &live); err != nil {
		t.Fatal(err)
	}
	a := live.Attention
	if a.Unanswered != 2 || a.Breached != 1 || a.DueSoon != 1 || a.Unassigned != 1 || a.WindowMinutes != 60 ||
		a.NextDueSeconds < 24*60 || a.NextDueSeconds > 25*60 {
		t.Errorf("attention: %+v", a)
	}
	if len(a.Holders) != 1 || a.Holders[0].Name != "Anna" || a.Holders[0].Count != 1 {
		t.Errorf("holders: %+v", a.Holders)
	}
	// Filters now narrow the live numbers too.
	r = f.admin.do("GET", "/api/v1/reports/live?team="+f.otherTeam, nil)
	expect(t, r, 200, "")
	if err := json.Unmarshal(r.raw, &live); err != nil || live.Attention.Unanswered != 0 {
		t.Errorf("team filter on live: %+v %v", live.Attention, err)
	}
}
