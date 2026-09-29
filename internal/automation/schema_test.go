package automation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	id1 = "0199a000-0000-7000-8000-000000000001"
	id2 = "0199a000-0000-7000-8000-000000000002"
)

func mustConditions(t *testing.T, doc string) Conditions {
	t.Helper()
	c, err := ParseConditions([]byte(doc))
	if err != nil {
		t.Fatalf("ParseConditions(%s): %v", doc, err)
	}
	return c
}

func cond(field, op, value string) string {
	return `{"field":"` + field + `","op":"` + op + `","value":` + value + `}`
}

func group(match string, items ...string) string {
	return `{"match":"` + match + `","items":[` + strings.Join(items, ",") + `]}`
}

func TestParseConditionsRejectsInvalidDocuments(t *testing.T) {
	tests := map[string]string{
		"not json":                   `{`,
		"unknown top-level field":    `{"match":"all","items":[],"extra":1}`,
		"unknown condition field":    group("all", `{"field":"subject","op":"contains","value":"x","extra":1}`),
		"unknown field name":         group("all", cond("sender", "equals", `"x"`)),
		"op not valid for field":     group("all", cond("subject", "is", `"x"`)),
		"text op on bool field":      group("all", cond("has_attachment", "contains", `true`)),
		"text value of wrong type":   group("all", cond("subject", "contains", `5`)),
		"empty text value":           group("all", cond("subject", "contains", `"  "`)),
		"control character in text":  group("all", cond("subject", "contains", `"a\nb"`)),
		"too long text":              group("all", cond("subject", "contains", `"`+strings.Repeat("a", 201)+`"`)),
		"bool value of wrong type":   group("all", cond("has_attachment", "is", `"yes"`)),
		"empty id list":              group("all", cond("mailbox", "is", `[]`)),
		"mailbox id is not a uuid":   group("all", cond("mailbox", "is", `["nope"]`)),
		"unknown status":             group("all", cond("status", "is", `["archived"]`)),
		"unknown priority":           group("all", cond("priority", "is", `["critical"]`)),
		"label needs has_any":        group("all", cond("label", "is", `["`+id1+`"]`)),
		"bad match":                  group("some", cond("subject", "contains", `"x"`)),
		"missing match":              `{"items":[]}`,
		"group mixed with condition": `{"match":"all","field":"subject","items":[]}`,
		"a condition at the root":    cond("subject", "contains", `"x"`),
		"empty nested group":         group("all", `{"match":"any","items":[]}`),
		"nested too deep":            group("all", group("any", group("all", cond("subject", "contains", `"x"`)))),
		"trailing data":              group("all") + ` {}`,
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConditions([]byte(doc))
			var ve ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
}

func TestParseConditionsLimits(t *testing.T) {
	items := make([]string, maxConditions+1)
	for i := range items {
		items[i] = cond("subject", "contains", `"x"`)
	}
	if _, err := ParseConditions([]byte(group("all", items[:maxConditions]...))); err != nil {
		t.Fatalf("%d conditions must be accepted: %v", maxConditions, err)
	}
	if _, err := ParseConditions([]byte(group("all", items...))); err == nil {
		t.Fatalf("%d conditions must be rejected", maxConditions+1)
	}
}

func TestConditionsRoundTrip(t *testing.T) {
	doc := group("any", cond("from_domain", "ends_with", `"@Acme.example"`), group("all", cond("has_attachment", "is", `true`), cond("label", "has_any", `["`+strings.ToUpper(id1)+`"]`)))
	c := mustConditions(t, doc)
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseConditions(out)
	if err != nil {
		t.Fatalf("the stored form must parse again: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), id1) || strings.Contains(string(out), strings.ToUpper(id1)) {
		t.Errorf("ids are stored normalized: %s", out)
	}
	if again.root.Items[0].text != "acme.example" {
		t.Errorf("domain not normalized: %q", again.root.Items[0].text)
	}
	empty, _ := json.Marshal(Conditions{})
	if string(empty) != `{"match":"all","items":[]}` {
		t.Errorf("empty conditions marshal to %s", empty)
	}
}

func facts() *Facts {
	mailbox := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	return &Facts{
		ConversationSubject: "Factuur 2024-113", MailboxID: mailbox, Status: "open", Priority: "normal",
		Organization: "Acme BV", Labels: map[string]bool{id1: true},
		HasMessage: true, FromAddress: "jane@acme.example", Body: "Hallo, mijn FACTUUR klopt niet.", HasAttachment: true,
		ReceivedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC), Now: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		WithinHours: func(t time.Time) bool { return t.Hour() >= 9 && t.Hour() < 17 },
	}
}

func TestConditionsMatch(t *testing.T) {
	mailbox := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}.String()
	tests := []struct {
		name string
		cond string
		want bool
	}{
		{"subject contains, case-insensitive", cond("subject", "contains", `"FACTUUR"`), true},
		{"subject contains miss", cond("subject", "contains", `"offerte"`), false},
		{"subject equals", cond("subject", "equals", `"factuur 2024-113"`), true},
		{"subject equals is exact", cond("subject", "equals", `"factuur"`), false},
		{"subject starts with", cond("subject", "starts_with", `"fact"`), true},
		{"subject ends with", cond("subject", "ends_with", `"113"`), true},
		{"from address equals", cond("from_address", "equals", `"JANE@acme.example"`), true},
		{"from address contains", cond("from_address", "contains", `"acme"`), true},
		{"from address ends with", cond("from_address", "ends_with", `"@acme.example"`), true},
		{"from domain equals", cond("from_domain", "equals", `"acme.example"`), true},
		{"from domain accepts an at sign", cond("from_domain", "equals", `"@acme.example"`), true},
		{"from domain ends with", cond("from_domain", "ends_with", `"example"`), true},
		{"from domain contains miss", cond("from_domain", "contains", `"gmail"`), false},
		{"body contains, case-insensitive", cond("body", "contains", `"factuur klopt"`), true},
		{"body contains miss", cond("body", "contains", `"refund"`), false},
		{"organization equals", cond("organization", "equals", `"acme bv"`), true},
		{"organization contains", cond("organization", "contains", `"acme"`), true},
		{"organization miss", cond("organization", "equals", `"other"`), false},
		{"mailbox is", cond("mailbox", "is", `["`+mailbox+`"]`), true},
		{"mailbox is another", cond("mailbox", "is", `["`+id2+`"]`), false},
		{"status in list", cond("status", "is", `["waiting","open"]`), true},
		{"status not in list", cond("status", "is", `["closed"]`), false},
		{"priority", cond("priority", "is", `["normal"]`), true},
		{"label present", cond("label", "has_any", `["`+id2+`","`+id1+`"]`), true},
		{"label absent", cond("label", "has_any", `["`+id2+`"]`), false},
		{"has attachment", cond("has_attachment", "is", `true`), true},
		{"has no attachment", cond("has_attachment", "is", `false`), false},
		{"no assignee", cond("has_assignee", "is", `false`), true},
		{"has assignee", cond("has_assignee", "is", `true`), false},
		{"no team", cond("has_team", "is", `false`), true},
		{"not auto-submitted", cond("auto_submitted", "is", `false`), true},
		{"auto-submitted", cond("auto_submitted", "is", `true`), false},
		{"received within business hours", cond("within_business_hours", "is", `true`), true},
		{"received outside business hours", cond("within_business_hours", "is", `false`), false},
		{"negated text", `{"field":"subject","op":"contains","value":"offerte","negate":true}`, true},
		{"negated hit", `{"field":"subject","op":"contains","value":"factuur","negate":true}`, false},
		{"negated label", `{"field":"label","op":"has_any","value":["` + id2 + `"],"negate":true}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := mustConditions(t, group("all", tc.cond))
			if got := c.Match(facts()); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConditionGroups(t *testing.T) {
	yes := cond("subject", "contains", `"factuur"`)
	no := cond("subject", "contains", `"offerte"`)
	tests := []struct {
		name string
		doc  string
		want bool
	}{
		{"empty group matches", group("all"), true},
		{"empty any group matches", group("any"), true},
		{"all true", group("all", yes, yes), true},
		{"all with a miss", group("all", yes, no), false},
		{"any with a hit", group("any", no, yes), true},
		{"any without a hit", group("any", no, no), false},
		{"nested any inside all", group("all", yes, group("any", no, yes)), true},
		{"nested any inside all misses", group("all", yes, group("any", no, no)), false},
		{"nested all inside any", group("any", no, group("all", yes, yes)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustConditions(t, tc.doc).Match(facts()); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
	if !(Conditions{}).Match(facts()) {
		t.Error("the zero value matches everything")
	}
}

func TestMessageConditionsWithoutAMessage(t *testing.T) {
	f := facts()
	f.HasMessage = false
	for name, want := range map[string]bool{
		cond("from_address", "contains", `"a"`):                      false,
		cond("from_domain", "contains", `"a"`):                       false,
		cond("body", "contains", `"a"`):                              false,
		cond("has_attachment", "is", `false`):                        true,
		cond("has_attachment", "is", `true`):                         false,
		cond("auto_submitted", "is", `false`):                        true,
		cond("subject", "contains", `"factuur"`):                     true,
		`{"field":"body","op":"contains","value":"a","negate":true}`: true,
	} {
		if got := mustConditions(t, group("all", name)).Match(f); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}

func TestBusinessHoursUsesTheMessageTime(t *testing.T) {
	f := facts()
	f.ReceivedAt = time.Date(2026, 3, 2, 20, 0, 0, 0, time.UTC)
	c := mustConditions(t, group("all", cond("within_business_hours", "is", `true`)))
	if c.Match(f) {
		t.Error("the message arrived at 20:00, outside hours, although now is inside")
	}
	f.HasMessage = false
	if !c.Match(f) {
		t.Error("without a message the current time counts")
	}
}

func TestParseActions(t *testing.T) {
	valid := []string{
		`[{"type":"assign_agent","user_id":"` + id1 + `"}]`,
		`[{"type":"assign_team","team_id":"` + id1 + `"}]`,
		`[{"type":"assign_round_robin"}]`,
		`[{"type":"assign_round_robin","team_id":"` + id1 + `"}]`,
		`[{"type":"set_priority","priority":"high"}]`,
		`[{"type":"add_label","label_id":"` + id1 + `"},{"type":"remove_label","label_id":"` + id2 + `"}]`,
		`[{"type":"set_status","status":"waiting"}]`,
		`[{"type":"mark_spam"}]`,
		`[{"type":"snooze","hours":24}]`,
		`[{"type":"apply_sla","policy_id":"` + id1 + `"}]`,
		`[{"type":"add_note","text":"Klant is VIP"}]`,
		`[{"type":"send_webhook"}]`,
		`[{"type":"auto_reply","template_id":"` + id1 + `"}]`,
		`[{"type":"auto_reply","text":"Bedankt voor je bericht.","subject":"Ontvangen"}]`,
	}
	for _, doc := range valid {
		if _, err := ParseActions([]byte(doc), true); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
	invalid := map[string]string{
		"empty list":              `[]`,
		"not a list":              `{"type":"mark_spam"}`,
		"unknown type":            `[{"type":"delete_conversation"}]`,
		"unknown field":           `[{"type":"mark_spam","extra":1}]`,
		"missing user":            `[{"type":"assign_agent"}]`,
		"field of another action": `[{"type":"mark_spam","user_id":"` + id1 + `"}]`,
		"bad uuid":                `[{"type":"add_label","label_id":"x"}]`,
		"bad priority":            `[{"type":"set_priority","priority":"critical"}]`,
		"spam via set_status":     `[{"type":"set_status","status":"spam"}]`,
		"unknown status":          `[{"type":"set_status","status":"archived"}]`,
		"snooze zero":             `[{"type":"snooze","hours":0}]`,
		"snooze too long":         `[{"type":"snooze","hours":8761}]`,
		"empty note":              `[{"type":"add_note","text":" "}]`,
		"reply without content":   `[{"type":"auto_reply"}]`,
		"reply with both":         `[{"type":"auto_reply","template_id":"` + id1 + `","text":"x"}]`,
		"subject with template":   `[{"type":"auto_reply","template_id":"` + id1 + `","subject":"x"}]`,
		"note too long":           `[{"type":"add_note","text":"` + strings.Repeat("a", maxReplyRunes+1) + `"}]`,
		"subject with newline":    `[{"type":"auto_reply","text":"x","subject":"a\nb"}]`,
	}
	for name, doc := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := ParseActions([]byte(doc), true)
			var ve ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
	many := "[" + strings.TrimSuffix(strings.Repeat(`{"type":"mark_spam"},`, maxActions+1), ",") + "]"
	if _, err := ParseActions([]byte(many), true); err == nil {
		t.Error("too many actions must be rejected")
	}
	if _, err := ParseActions([]byte(`[{"type":"auto_reply","text":"x"}]`), false); err == nil {
		t.Error("macros cannot send an auto reply")
	}
}

func TestValidateRule(t *testing.T) {
	ok := RuleDef{Name: " Facturen ", Trigger: "message_received", Actions: json.RawMessage(`[{"type":"add_note","text":"x"}]`), Enabled: true}
	got, err := ValidateRule(ok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Facturen" || string(got.Conditions) != `{"match":"all","items":[]}` {
		t.Errorf("normalized rule = %+v %s", got, got.Conditions)
	}

	bad := map[string]func(*RuleDef){
		"blank name":          func(r *RuleDef) { r.Name = " " },
		"unknown trigger":     func(r *RuleDef) { r.Trigger = "message_sent" },
		"idle without hours":  func(r *RuleDef) { r.Trigger = TriggerCustomerIdle },
		"idle hours too many": func(r *RuleDef) { r.Trigger, r.IdleHours = TriggerCustomerIdle, 721 },
		"hours on other":      func(r *RuleDef) { r.IdleHours = 3 },
		"no actions":          func(r *RuleDef) { r.Actions = nil },
		"invalid condition":   func(r *RuleDef) { r.Conditions = json.RawMessage(`{"match":"all","items":[{"field":"x"}]}`) },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			r := ok
			mutate(&r)
			var ve ValidationError
			if _, err := ValidateRule(r); !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
	idle := ok
	idle.Trigger, idle.IdleHours = TriggerCustomerIdle, 48
	if _, err := ValidateRule(idle); err != nil {
		t.Errorf("idle rule: %v", err)
	}
}

func TestReplySubject(t *testing.T) {
	for in, want := range map[string]string{"Order": "Re: Order", "Re: Order": "Re: Order", "RE: order": "RE: order", "": "Re: "} {
		if got := replySubject(in); got != want {
			t.Errorf("replySubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextToHTMLEscapes(t *testing.T) {
	got := textToHTML("Hallo <b>wereld</b> & co\nregel twee\n\nNieuwe alinea")
	want := "<p>Hallo &lt;b&gt;wereld&lt;/b&gt; &amp; co<br>regel twee</p><p>Nieuwe alinea</p>"
	if got != want {
		t.Errorf("got %s", got)
	}
}

func TestNoReplyAddresses(t *testing.T) {
	for _, local := range []string{"noreply", "no-reply", "no_reply", "no.reply", "donotreply", "do-not-reply", "mailer-daemon", "postmaster", "bounce", "bounces"} {
		if !noReplyLocal.MatchString(local) {
			t.Errorf("%s must be treated as no-reply", local)
		}
	}
	for _, local := range []string{"jane", "reply", "info", "noreplyfan", "support"} {
		if noReplyLocal.MatchString(local) {
			t.Errorf("%s is a real address", local)
		}
	}
}
