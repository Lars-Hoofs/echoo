package api

import (
	"strings"
	"testing"
	"time"
)

func TestListAdvancedFilters(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	mine := f.conversation("mine-high", convOpt{mailbox: f.mailboxA, assignee: f.agentID, lastMessageAt: now})
	f.exec(`UPDATE conversations SET priority = 'high', has_attachments = true WHERE id = $1`, mine)
	f.conversation("free-normal", convOpt{mailbox: f.mailboxA, lastMessageAt: now.Add(-time.Hour)})
	urgent := f.conversation("free-urgent", convOpt{mailbox: f.mailboxA, lastMessageAt: now.Add(-2 * time.Hour)})
	f.exec(`UPDATE conversations SET priority = 'urgent' WHERE id = $1`, urgent)
	f.conversation("old", convOpt{mailbox: f.mailboxA, lastMessageAt: now.AddDate(0, 0, -20)})
	f.conversation("closed", convOpt{mailbox: f.mailboxA, status: "closed", lastMessageAt: now})
	f.conversation("b-only", convOpt{mailbox: f.mailboxB, lastMessageAt: now.Add(-3 * time.Hour)})
	org := f.queryID(`INSERT INTO organizations (name) VALUES ('Bedrijf') RETURNING id`)
	klant := f.queryID(`INSERT INTO contacts (name, organization_id) VALUES ('Klant Twee', $1) RETURNING id`, org)
	f.conversation("from-klant", convOpt{mailbox: f.mailboxA, contact: klant, lastMessageAt: now.Add(-4 * time.Hour)})
	label := f.queryID(`INSERT INTO labels (name, color_token) VALUES ('Vip', 'red') RETURNING id`)
	f.exec(`INSERT INTO conversation_labels (conversation_id, label_id) SELECT id, $1 FROM conversations WHERE subject IN ('free-normal', 'b-only')`, label)
	cutoff := now.AddDate(0, 0, -10).Format(time.DateOnly)

	tests := []struct {
		name, query, want string
		client            *client
	}{
		{"priority", "?priority=high", "mine-high", f.agent},
		{"priorities any of", "?priority=high&priority=urgent", "mine-high,free-urgent", f.agent},
		{"attachment", "?has_attachment=true", "mine-high", f.agent},
		{"assignee me", "?assignee=me", "mine-high", f.agent},
		{"assignee id", "?assignee=" + f.agentID, "mine-high", f.agent},
		{"assignee none", "?assignee=none", "free-normal,free-urgent,from-klant,old", f.agent},
		{"mine view plus none is empty", "?view=mine&assignee=none", "", f.agent},
		{"mine view plus me", "?view=mine&assignee=me", "mine-high", f.agent},
		{"after", "?after=" + cutoff, "mine-high,free-normal,free-urgent,from-klant", f.agent},
		{"before", "?before=" + cutoff, "old", f.agent},
		{"closed status", "?priority=none&status=closed", "closed", f.agent},
		{"contact", "?contact_id=" + klant, "from-klant", f.agent},
		{"organization", "?organization_id=" + org, "from-klant", f.agent},
		{"label", "?label_id=" + label + "&priority=none", "free-normal", f.agent},
		{"several mailboxes as admin", "?mailbox_id=" + f.mailboxA + "&mailbox_id=" + f.mailboxB + "&priority=none", "free-normal,b-only,from-klant,old", f.admin},
		{"mailbox outside scope", "?mailbox_id=" + f.mailboxB + "&priority=none", "", f.agent},
		{"scope holds with several mailboxes", "?mailbox_id=" + f.mailboxA + "&mailbox_id=" + f.mailboxB + "&priority=none", "free-normal,from-klant,old", f.agent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := subjects(f.list(tc.client, tc.query))
			if sorted(got) != sorted(tc.want) {
				t.Errorf("list %s = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

func sorted(csv string) string {
	if csv == "" {
		return ""
	}
	parts := strings.Split(csv, ",")
	for i := range parts {
		for j := i + 1; j < len(parts); j++ {
			if parts[j] < parts[i] {
				parts[i], parts[j] = parts[j], parts[i]
			}
		}
	}
	return strings.Join(parts, ",")
}

func TestListAdvancedFilterPagination(t *testing.T) {
	f := newInboxFixture(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		c := f.conversation("p"+string(rune('a'+i)), convOpt{mailbox: f.mailboxA, lastMessageAt: now.Add(-time.Duration(i) * time.Minute)})
		f.exec(`UPDATE conversations SET priority = 'low' WHERE id = $1`, c)
	}
	first := f.list(f.agent, "?priority=low&limit=2")
	if subjects(first) != "pa,pb" || first.NextCursor == nil {
		t.Fatalf("first page = %q, cursor %v", subjects(first), first.NextCursor)
	}
	second := f.list(f.agent, "?priority=low&limit=2&cursor="+*first.NextCursor)
	if subjects(second) != "pc,pd" || second.NextCursor == nil {
		t.Fatalf("second page = %q", subjects(second))
	}
	third := f.list(f.agent, "?priority=low&limit=2&cursor="+*second.NextCursor)
	if subjects(third) != "pe" || third.NextCursor != nil {
		t.Fatalf("third page = %q", subjects(third))
	}
}

func TestListAdvancedFilterValidation(t *testing.T) {
	f := newInboxFixture(t)
	for _, q := range []string{
		"?priority=extreme", "?assignee=iemand", "?after=gisteren", "?before=2026-13-01", "?has_attachment=yes",
		"?contact_id=x", "?organization_id=x", "?mailbox_id=a&mailbox_id=b",
	} {
		expect(t, f.agent.do("GET", "/api/v1/conversations"+q, nil), 400, "invalid_request")
	}
}
