package automation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/jobs"
)

func (f *fixture) mode(mode string) {
	f.t.Helper()
	f.exec(`UPDATE mailboxes SET auto_assign_mode = $2 WHERE id = $1`, f.mailbox, mode)
}

func (f *fixture) availability(user pgtype.UUID, v string) {
	f.t.Helper()
	f.exec(`UPDATE users SET availability = $2 WHERE id = $1`, user, v)
}

func (f *fixture) newConversation() pgtype.UUID {
	f.t.Helper()
	conv, msg := f.conversation(inbound{subject: "Vraag"})
	f.created(conv, msg)
	return conv
}

func TestRoundRobinAlternatesBetweenAgents(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	a, b, c := f.state(f.newConversation()).Assignee, f.state(f.newConversation()).Assignee, f.state(f.newConversation()).Assignee
	if !a.Valid || !b.Valid || a == b || c != a {
		t.Fatalf("assignees = %v %v %v, want alternating between two agents", a, b, c)
	}
	if a != f.agentA && a != f.agentB {
		t.Fatalf("assigned to somebody outside the team: %v", a)
	}
}

func TestAutoAssignmentIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	if f.state(f.newConversation()).Assignee.Valid {
		t.Fatal("assigned although the mailbox mode is off")
	}
}

func TestAutoAssignmentEventIsAttributedToTheSystem(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	conv := f.newConversation()
	var noActor bool
	var source string
	if err := f.pool.QueryRow(context.Background(), `SELECT actor_user_id IS NULL, data->>'source' FROM conversation_events WHERE conversation_id = $1 AND type = 'assigned'`, conv).Scan(&noActor, &source); err != nil {
		t.Fatal(err)
	}
	if !noActor || source != "system" {
		t.Fatalf("noActor=%v source=%q", noActor, source)
	}
	if f.count(`SELECT count(*) FROM notifications WHERE conversation_id = $1 AND kind = 'assigned'`, conv) != 1 {
		t.Error("the new assignee is not notified")
	}
	if f.queuedRules() != 0 {
		t.Error("auto-assignment queued rules")
	}
}

func TestBalancedAssignsToTheLeastLoadedAgent(t *testing.T) {
	f := newFixture(t)
	f.mode("balanced")
	for i := 0; i < 3; i++ {
		conv, _ := f.conversation(inbound{subject: "bestaand"})
		f.exec(`UPDATE conversations SET assignee_user_id = $2 WHERE id = $1`, conv, f.agentA)
	}
	for i := 0; i < 3; i++ {
		if got := f.state(f.newConversation()).Assignee; got != f.agentB {
			t.Fatalf("conversation %d went to %v, want the agent with fewer open conversations", i, got)
		}
	}
	// Load is now 3 against 3: the agent auto-assigned longest ago is next.
	if got := f.state(f.newConversation()).Assignee; got != f.agentA {
		t.Fatalf("with equal load the agent who waited longest is next, got %v", got)
	}
}

func TestOnlyCountsOpenConversationsForLoad(t *testing.T) {
	f := newFixture(t)
	f.mode("balanced")
	for i := 0; i < 3; i++ {
		conv, _ := f.conversation(inbound{subject: "gesloten"})
		f.exec(`UPDATE conversations SET assignee_user_id = $2, status = 'closed' WHERE id = $1`, conv, f.agentA)
	}
	f.exec(`UPDATE users SET last_auto_assigned_at = now() WHERE id = $1`, f.agentB)
	if got := f.state(f.newConversation()).Assignee; got != f.agentA {
		t.Fatalf("closed conversations must not count as load, got %v", got)
	}
}

func TestBusyAndOfflineAgentsAreSkipped(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentA, "busy")
	f.availability(f.agentB, "offline")
	stuck := f.newConversation()
	if f.state(stuck).Assignee.Valid {
		t.Fatal("assigned to an agent who is not online")
	}

	f.availability(f.agentA, "online")
	for i := 0; i < 2; i++ {
		if got := f.state(f.newConversation()).Assignee; got != f.agentA {
			t.Fatalf("only agent A is online, got %v", got)
		}
	}

	// The periodic retry picks up the conversation that found nobody.
	if err := f.e.RetryAssignments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.state(stuck).Assignee; got != f.agentA {
		t.Fatalf("retry assigned to %v, want agent A", got)
	}
}

func TestRetryWaitsUntilSomebodyIsAvailable(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentA, "offline")
	f.availability(f.agentB, "offline")
	conv := f.newConversation()
	if err := f.e.RetryAssignments(context.Background()); err != nil || f.state(conv).Assignee.Valid {
		t.Fatalf("nobody is online: assignee %v err %v", f.state(conv).Assignee, err)
	}
	f.availability(f.agentB, "online")
	if err := f.e.RetryAssignments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.state(conv).Assignee != f.agentB {
		t.Fatal("the retry did not assign once an agent came online")
	}
}

func TestCapacityLimitsAutoAssignment(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentB, "offline")
	f.exec(`UPDATE users SET max_open = 2 WHERE id = $1`, f.agentA)

	first, second, third := f.newConversation(), f.newConversation(), f.newConversation()
	if f.state(first).Assignee != f.agentA || f.state(second).Assignee != f.agentA {
		t.Fatal("agent A has room for two")
	}
	if f.state(third).Assignee.Valid {
		t.Fatal("agent A is at capacity and must not get a third conversation")
	}

	f.setStatus(first, "closed")
	if err := f.e.RetryAssignments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.state(third).Assignee != f.agentA {
		t.Fatal("closing a conversation frees a place")
	}
}

func TestAutoAssignmentOnlyConsidersEligibleUsers(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentA, "offline")
	f.availability(f.agentB, "offline")
	// Read-only members, deactivated agents and agents outside the team are never picked.
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, f.readonly)
	f.exec(`INSERT INTO users (email, name, role, password_hash, deactivated_at) VALUES ('gone@shop.example', 'Gone', 'agent', 'x', now())`)
	f.exec(`INSERT INTO users (email, name, role, password_hash) VALUES ('out@shop.example', 'Out', 'agent', 'x')`)
	deactivated := f.id(`SELECT id FROM users WHERE email = 'gone@shop.example'`)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, deactivated)
	if f.state(f.newConversation()).Assignee.Valid {
		t.Fatal("an ineligible user was assigned")
	}
}

func TestAutoAssignmentStaysWithinTheTeamSetByARule(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	sales := f.id(`INSERT INTO teams (name) VALUES ('Sales') RETURNING id`)
	f.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'write')`, f.mailbox, sales)
	seller := f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('sales@shop.example', 'Sales', 'agent', 'x') RETURNING id`)
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, sales, seller)
	f.rule("Sales", jobs.TriggerConversationCreated, group("all", cond("subject", "contains", `"offerte"`)),
		actions(fmt.Sprintf(`{"type":"assign_team","team_id":%q}`, sales.String())), false)
	for i := 0; i < 3; i++ {
		conv, msg := f.conversation(inbound{subject: "Offerte aanvragen"})
		f.created(conv, msg)
		if got := f.state(conv).Assignee; got != seller {
			t.Fatalf("conversation %d went to %v, want the only member of team Sales", i, got)
		}
	}
}

func TestRoundRobinActionPicksAvailableAgent(t *testing.T) {
	f := newFixture(t)
	f.availability(f.agentA, "offline")
	rule := f.rule("Verdelen", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"assign_round_robin"}`), false)
	conv, msg := f.conversation(inbound{subject: "x"})
	f.created(conv, msg)
	if f.state(conv).Assignee != f.agentB {
		t.Fatal("the offline agent must be skipped")
	}
	f.availability(f.agentB, "offline")
	conv2, msg2 := f.conversation(inbound{subject: "y"})
	f.created(conv2, msg2)
	a := f.runs(rule)[1].Actions[0]
	if a.Result != "skipped" || a.Detail != "no agent available" || f.state(conv2).Assignee.Valid {
		t.Fatalf("action = %+v", a)
	}
}

func TestAssignedConversationsAreLeftAlone(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.rule("Vast", jobs.TriggerConversationCreated, allMatch, actions(fmt.Sprintf(`{"type":"assign_agent","user_id":%q}`, f.admin.String())), false)
	conv, msg := f.conversation(inbound{subject: "x"})
	f.created(conv, msg)
	if got := f.state(conv).Assignee; got != f.admin {
		t.Fatalf("assignee = %v, want the agent chosen by the rule", got)
	}
}

func TestRetryLeavesManuallyUnassignedAndOldConversationsAlone(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentA, "offline")
	f.availability(f.agentB, "offline")
	unassigned := f.newConversation()
	old := f.newConversation()
	f.exec(`UPDATE conversations SET created_at = now() - interval '8 days' WHERE id = $1`, old)
	f.exec(`INSERT INTO conversation_events (conversation_id, mailbox_id, type) VALUES ($1, $2, 'unassigned')`, unassigned, f.mailbox)
	f.availability(f.agentA, "online")
	if err := f.e.RetryAssignments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.state(unassigned).Assignee.Valid || f.state(old).Assignee.Valid {
		t.Fatal("the retry touched a conversation somebody unassigned or one that is too old")
	}
}

func TestRetryLeavesSnoozedAndClosedConversationsAlone(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentA, "offline")
	f.availability(f.agentB, "offline")
	snoozed, closed := f.newConversation(), f.newConversation()
	f.exec(`UPDATE conversations SET snoozed_until = $2 WHERE id = $1`, snoozed, time.Now().Add(time.Hour))
	f.setStatus(closed, "closed")
	f.availability(f.agentA, "online")
	if err := f.e.RetryAssignments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.state(snoozed).Assignee.Valid || f.state(closed).Assignee.Valid {
		t.Fatal("assigned a snoozed or closed conversation")
	}
}

func TestConcurrentAssignmentsRespectCapacity(t *testing.T) {
	f := newFixture(t)
	f.mode("round_robin")
	f.availability(f.agentB, "offline")
	f.exec(`UPDATE users SET max_open = 3 WHERE id = $1`, f.agentA)
	ids := make([]pgtype.UUID, 8)
	for i := range ids {
		ids[i], _ = f.conversation(inbound{subject: "x"})
	}
	done := make(chan error, len(ids))
	for _, id := range ids {
		go func() { done <- f.e.autoAssignNew(context.Background(), id) }()
	}
	for range ids {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count(`SELECT count(*) FROM conversations WHERE assignee_user_id = $1 AND status = 'open'`, f.agentA); n != 3 {
		t.Fatalf("agent A has %d open conversations with a capacity of 3", n)
	}
}
