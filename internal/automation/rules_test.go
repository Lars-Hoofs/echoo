package automation

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/jobs"
	"echoo/internal/webhooks"
)

const allMatch = `{"match":"all","items":[]}`

func addLabel(id pgtype.UUID) string {
	return fmt.Sprintf(`{"type":"add_label","label_id":%q}`, id.String())
}
func removeLabel(id pgtype.UUID) string {
	return fmt.Sprintf(`{"type":"remove_label","label_id":%q}`, id.String())
}

func actions(list ...string) string { return "[" + strings.Join(list, ",") + "]" }

func TestSubjectRuleAddsLabelAndRecordsTheRun(t *testing.T) {
	f := newFixture(t)
	rule := f.rule("Facturen", jobs.TriggerConversationCreated,
		group("all", cond("subject", "contains", `"factuur"`)), actions(addLabel(f.labelBilling)), false)
	hit, hitMsg := f.conversation(inbound{subject: "Vraag over FACTUUR 12"})
	miss, missMsg := f.conversation(inbound{subject: "Openingstijden"})

	f.created(hit, hitMsg)
	f.created(miss, missMsg)

	if got := f.state(hit).Labels; len(got) != 1 || got[0] != "Facturen" {
		t.Fatalf("labels = %v", got)
	}
	if got := f.state(miss).Labels; len(got) != 0 {
		t.Fatalf("labels of a non-matching conversation = %v", got)
	}
	runs := f.runs(rule)
	if len(runs) != 2 || !runs[0].Matched || runs[1].Matched {
		t.Fatalf("runs = %+v", runs)
	}
	if runs[0].Trigger != jobs.TriggerConversationCreated || len(runs[0].Actions) != 1 || runs[0].Actions[0].Result != "applied" || runs[0].Error != "" {
		t.Fatalf("matched run = %+v", runs[0])
	}
	// The timeline says a rule did it, not a person.
	var noActor bool
	var source, ruleID string
	err := f.pool.QueryRow(context.Background(),
		`SELECT actor_user_id IS NULL, data->>'source', data->>'rule_id' FROM conversation_events WHERE conversation_id = $1 AND type = 'labeled'`, hit).
		Scan(&noActor, &source, &ruleID)
	if err != nil || !noActor || source != "rule" || ruleID != rule.String() {
		t.Fatalf("label event: noActor=%v source=%q rule=%q err=%v", noActor, source, ruleID, err)
	}
}

func TestRulesRunInOrderAndStopProcessing(t *testing.T) {
	f := newFixture(t)
	first := f.rule("Hoog", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"set_priority","priority":"high"}`), true)
	second := f.rule("Urgent", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"set_priority","priority":"urgent"}`), false)
	conv, msg := f.conversation(inbound{subject: "x"})

	f.created(conv, msg)

	if got := f.state(conv).Priority; got != "high" {
		t.Fatalf("priority = %s, want high: the second rule must not run after stop_processing", got)
	}
	if len(f.runs(first)) != 1 || len(f.runs(second)) != 0 {
		t.Fatalf("runs: first %d, second %d", len(f.runs(first)), len(f.runs(second)))
	}

	// Without a match the chain continues, even for a rule that would stop.
	f2 := newFixture(t)
	stopper := f2.rule("Nooit", jobs.TriggerConversationCreated, group("all", cond("subject", "contains", `"zzz"`)), actions(`{"type":"set_priority","priority":"low"}`), true)
	next := f2.rule("Wel", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"set_priority","priority":"normal"}`), false)
	conv2, msg2 := f2.conversation(inbound{subject: "x"})
	f2.created(conv2, msg2)
	if got := f2.state(conv2).Priority; got != "normal" {
		t.Fatalf("priority = %s, want normal", got)
	}
	if runs := f2.runs(stopper); len(runs) != 1 || runs[0].Matched {
		t.Fatalf("stopper runs = %+v", runs)
	}
	_ = next
}

func TestRulesOnlyRunForTheirTriggerMailboxAndWhenEnabled(t *testing.T) {
	f := newFixture(t)
	other := f.id(`INSERT INTO mailboxes (name, email_address) VALUES ('Other', 'other@shop.example') RETURNING id`)
	wrongTrigger := f.rule("Reply", jobs.TriggerMessageReceived, allMatch, actions(`{"type":"set_priority","priority":"high"}`), false)
	wrongMailbox := f.rawRule(RuleDef{
		Name: "Other", Trigger: jobs.TriggerConversationCreated, MailboxID: other, Conditions: []byte(allMatch),
		Actions: []byte(actions(`{"type":"set_priority","priority":"high"}`)), Enabled: true,
	})
	disabled := f.rawRule(RuleDef{
		Name: "Off", Trigger: jobs.TriggerConversationCreated, Conditions: []byte(allMatch),
		Actions: []byte(actions(`{"type":"set_priority","priority":"high"}`)), Enabled: false,
	})
	ownMailbox := f.rawRule(RuleDef{
		Name: "Mine", Trigger: jobs.TriggerConversationCreated, MailboxID: f.mailbox, Conditions: []byte(allMatch),
		Actions: []byte(actions(`{"type":"set_priority","priority":"low"}`)), Enabled: true,
	})
	conv, msg := f.conversation(inbound{subject: "x"})

	f.created(conv, msg)

	if got := f.state(conv).Priority; got != "low" {
		t.Fatalf("priority = %s, want low", got)
	}
	for name, id := range map[string]pgtype.UUID{"trigger": wrongTrigger, "mailbox": wrongMailbox, "disabled": disabled} {
		if n := len(f.runs(id)); n != 0 {
			t.Errorf("%s rule ran %d time(s)", name, n)
		}
	}
	if len(f.runs(ownMailbox)) != 1 {
		t.Error("the rule of the mailbox did not run")
	}
}

func TestEveryAction(t *testing.T) {
	f := newFixture(t)
	policy := f.id(`INSERT INTO sla_policies (name, first_response_minutes) VALUES ('Snel', 60) RETURNING id`)
	f.exec(`INSERT INTO webhooks (url, secret_enc, events) VALUES ('https://example.com/h', '\x00', $1)`, []string{webhooks.ConversationAutomation})

	tests := []struct {
		name    string
		actions string
		prepare func(conv pgtype.UUID)
		check   func(t *testing.T, conv pgtype.UUID)
	}{
		{"assign agent", actions(fmt.Sprintf(`{"type":"assign_agent","user_id":%q}`, f.agentB.String())), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.state(conv).Assignee != f.agentB {
					t.Error("not assigned to agent B")
				}
			}},
		{"assign team", actions(fmt.Sprintf(`{"type":"assign_team","team_id":%q}`, f.team.String())), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.state(conv).Team != f.team {
					t.Error("team not set")
				}
			}},
		{"assign round robin within a team", actions(fmt.Sprintf(`{"type":"assign_round_robin","team_id":%q}`, f.team.String())), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if a := f.state(conv).Assignee; a != f.agentA && a != f.agentB {
					t.Errorf("assignee = %v", a)
				}
			}},
		{"set priority", actions(`{"type":"set_priority","priority":"urgent"}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.state(conv).Priority != "urgent" {
					t.Error("priority not set")
				}
			}},
		{"add label", actions(addLabel(f.labelVIP)), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if l := f.state(conv).Labels; len(l) != 1 || l[0] != "VIP" {
					t.Errorf("labels = %v", l)
				}
			}},
		{"remove label", actions(removeLabel(f.labelVIP)),
			func(conv pgtype.UUID) {
				f.exec(`INSERT INTO conversation_labels (conversation_id, label_id) VALUES ($1, $2)`, conv, f.labelVIP)
			},
			func(t *testing.T, conv pgtype.UUID) {
				if l := f.state(conv).Labels; len(l) != 0 {
					t.Errorf("labels = %v", l)
				}
			}},
		{"set status", actions(`{"type":"set_status","status":"waiting"}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.state(conv).Status != "waiting" {
					t.Error("status not set")
				}
			}},
		{"set status closed", actions(`{"type":"set_status","status":"closed"}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.state(conv).Status != "closed" {
					t.Error("status not set")
				}
			}},
		{"mark spam", actions(`{"type":"mark_spam"}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.state(conv).Status != "spam" {
					t.Error("not marked as spam")
				}
			}},
		{"snooze", actions(`{"type":"snooze","hours":3}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if !f.state(conv).Snoozed {
					t.Error("not snoozed")
				}
			}},
		{"apply SLA policy", actions(fmt.Sprintf(`{"type":"apply_sla","policy_id":%q}`, policy.String())), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.count(`SELECT count(*) FROM conversations WHERE id = $1 AND sla_policy_id = $2 AND first_response_due_at IS NOT NULL AND resolution_due_at IS NULL AND sla_state = 'ok'`, conv, policy) != 1 {
					t.Error("policy not applied")
				}
			}},
		{"add note", actions(`{"type":"add_note","text":"Klant is <VIP>\n\nBel terug."}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				var name, text, html string
				err := f.pool.QueryRow(context.Background(), `SELECT from_name, body_text, body_html FROM messages WHERE conversation_id = $1 AND kind = 'note'`, conv).Scan(&name, &text, &html)
				if err != nil {
					t.Fatal(err)
				}
				if name != "Regel: Alles" || !strings.Contains(html, "&lt;VIP&gt;") || !strings.Contains(html, "<p>Bel terug.</p>") || !strings.Contains(text, "<VIP>") {
					t.Errorf("note = %q %q %q", name, text, html)
				}
			}},
		{"send webhook", actions(`{"type":"send_webhook"}`), nil,
			func(t *testing.T, conv pgtype.UUID) {
				if f.count(`SELECT count(*) FROM webhook_events WHERE type = 'conversation.automation' AND conversation_id = $1`, conv) != 1 {
					t.Error("no webhook event recorded")
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f.exec(`DELETE FROM rules`)
			rule := f.rule("Alles", jobs.TriggerMessageReceived, allMatch, tc.actions, false)
			conv, msg := f.conversation(inbound{subject: tc.name})
			if tc.prepare != nil {
				tc.prepare(conv)
			}
			f.evaluate(jobs.TriggerMessageReceived, conv, msg)
			runs := f.runs(rule)
			if len(runs) != 1 || runs[0].Error != "" {
				t.Fatalf("runs = %+v", runs)
			}
			for _, a := range runs[0].Actions {
				if a.Result != "applied" {
					t.Fatalf("action %+v", a)
				}
			}
			tc.check(t, conv)
		})
	}
}

func TestFailingActionIsRecordedAndTheRestStillRuns(t *testing.T) {
	f := newFixture(t)
	gone := f.id(`SELECT uuidv7()`)
	rule := f.rule("Kapot", jobs.TriggerConversationCreated, allMatch,
		actions(addLabel(gone), `{"type":"set_priority","priority":"high"}`), false)
	conv, msg := f.conversation(inbound{subject: "x"})

	f.created(conv, msg)

	if f.state(conv).Priority != "high" {
		t.Error("the action after the failing one did not run")
	}
	runs := f.runs(rule)
	if len(runs) != 1 || runs[0].Actions[0].Result != "failed" || runs[0].Actions[1].Result != "applied" {
		t.Fatalf("runs = %+v", runs)
	}
	if !strings.HasPrefix(runs[0].Error, "add_label: ") {
		t.Errorf("error = %q", runs[0].Error)
	}
}

func TestAssignmentActionsRespectMailboxAccess(t *testing.T) {
	f := newFixture(t)
	outsider := f.id(`INSERT INTO users (email, name, role, password_hash) VALUES ('out@shop.example', 'Out', 'agent', 'x') RETURNING id`)
	rule := f.rule("Toewijzen", jobs.TriggerConversationCreated, allMatch,
		actions(fmt.Sprintf(`{"type":"assign_agent","user_id":%q}`, outsider.String())), false)
	conv, msg := f.conversation(inbound{subject: "x"})
	f.created(conv, msg)
	runs := f.runs(rule)
	if runs[0].Actions[0].Result != "failed" || f.state(conv).Assignee.Valid {
		t.Fatalf("an agent without access to the mailbox was assigned: %+v", runs[0])
	}
}

func TestRulesDoNotTriggerRules(t *testing.T) {
	f := newFixture(t)
	updated := f.rule("Bij wijziging", jobs.TriggerConversationUpdated, group("all", cond("status", "is", `["waiting"]`)),
		actions(addLabel(f.labelVIP)), false)
	f.rule("Zet wachtend", jobs.TriggerConversationCreated, allMatch,
		actions(`{"type":"set_status","status":"waiting"}`, addLabel(f.labelBilling), fmt.Sprintf(`{"type":"assign_agent","user_id":%q}`, f.agentA.String())), false)
	conv, msg := f.conversation(inbound{subject: "x"})

	f.created(conv, msg)

	s := f.state(conv)
	if s.Status != "waiting" || len(s.Labels) != 1 || s.Labels[0] != "Facturen" {
		t.Fatalf("state = %+v", s)
	}
	if n := f.queuedRules(); n != 0 {
		t.Fatalf("%d rules job(s) queued by rule actions", n)
	}
	if n := len(f.runs(updated)); n != 0 {
		t.Fatalf("the update rule ran %d time(s) for changes made by a rule", n)
	}

	// The same rule does run when the update trigger is evaluated, as a person's change would.
	f.evaluate(jobs.TriggerConversationUpdated, conv, pgtype.UUID{})
	if len(f.runs(updated)) != 1 || len(f.state(conv).Labels) != 2 {
		t.Fatalf("update rule did not run for the update trigger: %+v", f.state(conv))
	}
	if n := f.queuedRules(); n != 0 {
		t.Fatalf("%d rules job(s) queued", n)
	}
}

func TestRuleWithBrokenStoredJSONIsRecordedNotRetried(t *testing.T) {
	f := newFixture(t)
	rule := f.rawRule(RuleDef{Name: "Stuk", Trigger: jobs.TriggerConversationCreated, Conditions: []byte(`{"nonsense":true}`), Actions: []byte(`[]`), Enabled: true})
	conv, msg := f.conversation(inbound{subject: "x"})
	f.created(conv, msg)
	runs := f.runs(rule)
	if len(runs) != 1 || runs[0].Matched || !strings.Contains(runs[0].Error, "stored conditions are invalid") {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestDeletedConversationIsIgnored(t *testing.T) {
	f := newFixture(t)
	f.rule("Alles", jobs.TriggerConversationCreated, allMatch, actions(`{"type":"set_priority","priority":"high"}`), false)
	conv, msg := f.conversation(inbound{subject: "x"})
	f.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, conv)
	f.created(conv, msg)
	if f.count(`SELECT count(*) FROM rule_runs`) != 0 {
		t.Error("a deleted conversation was evaluated")
	}
}

func TestMessageConditionsUseTheTriggerMessage(t *testing.T) {
	f := newFixture(t)
	rule := f.rule("Bijlage", jobs.TriggerMessageReceived,
		group("all", cond("has_attachment", "is", `true`), cond("body", "contains", `"contract"`)), actions(addLabel(f.labelBilling)), false)
	conv, first := f.conversation(inbound{subject: "x", body: "Zie contract", attachment: true})
	second := f.id(`INSERT INTO messages (conversation_id, mailbox_id, kind, direction, message_id_header, from_addr, subject, body_text)
		VALUES ($1, $2, 'email', 'in', 'second@acme.example', 'jane@acme.example', 'x', 'Nog een vraag') RETURNING id`, conv, f.mailbox)

	f.evaluate(jobs.TriggerMessageReceived, conv, second)
	if len(f.state(conv).Labels) != 0 {
		t.Fatal("the second message has no attachment and no contract")
	}
	f.evaluate(jobs.TriggerMessageReceived, conv, first)
	if len(f.state(conv).Labels) != 1 {
		t.Fatal("the first message matches")
	}
	if runs := f.runs(rule); len(runs) != 2 || runs[0].Matched || !runs[1].Matched {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestContactOrganizationCondition(t *testing.T) {
	f := newFixture(t)
	org := f.id(`INSERT INTO organizations (name, domains) VALUES ('Acme BV', '{acme.example}') RETURNING id`)
	contact := f.id(`INSERT INTO contacts (name, organization_id) VALUES ('Jane', $1) RETURNING id`, org)
	f.rule("Acme", jobs.TriggerConversationCreated, group("all", cond("organization", "equals", `"acme bv"`)), actions(addLabel(f.labelVIP)), false)
	conv, msg := f.conversation(inbound{subject: "x"})
	f.exec(`UPDATE conversations SET contact_id = $2 WHERE id = $1`, conv, contact)
	stranger, strangerMsg := f.conversation(inbound{subject: "y"})

	f.created(conv, msg)
	f.created(stranger, strangerMsg)

	if len(f.state(conv).Labels) != 1 || len(f.state(stranger).Labels) != 0 {
		t.Fatalf("labels: %v / %v", f.state(conv).Labels, f.state(stranger).Labels)
	}
}

func TestApplyingTheSamePolicyAgainKeepsTheDeadlines(t *testing.T) {
	f := newFixture(t)
	policy := f.id(`INSERT INTO sla_policies (name, first_response_minutes) VALUES ('Snel', 60) RETURNING id`)
	rule := f.rule("SLA", jobs.TriggerMessageReceived, allMatch, actions(fmt.Sprintf(`{"type":"apply_sla","policy_id":%q}`, policy.String())), false)
	conv, msg := f.conversation(inbound{subject: "x"})

	f.evaluate(jobs.TriggerMessageReceived, conv, msg)
	first := f.sla(conv).FirstDue
	f.at = time.Now().Add(20 * time.Minute)
	f.evaluate(jobs.TriggerMessageReceived, conv, msg)

	if !first.Valid || !sameInstant(f.sla(conv).FirstDue, first.Time) {
		t.Fatal("a second evaluation moved the deadline")
	}
	if a := f.runs(rule)[1].Actions[0]; a.Result != "skipped" || a.Detail != "policy already applied" {
		t.Fatalf("second run = %+v", a)
	}
}
