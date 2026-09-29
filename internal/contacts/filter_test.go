package contacts

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

func TestFilterAndSearch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mb := e.mailbox("Support", "help@example.com")
	acme := e.id(`INSERT INTO organizations (name, domains) VALUES ('Acme', '{acme.nl}') RETURNING id`)

	anna := e.contact("Anna de Vries", pgtype.UUID{}, "anna@acme.nl")
	e.exec(`UPDATE contacts SET organization_id = $1, custom_attributes = '{"tier":"gold","seats":12,"renewal":"2026-03-01","vip":true,"note":"100% klant"}' WHERE id = $2`, acme, anna)
	e.conversation(mb, anna, "Offerte", "open")
	bram := e.contact("Bram", pgtype.UUID{}, "bram@gmail.com")
	e.conversation(mb, bram, "Klacht", "closed")
	e.exec(`UPDATE contacts SET custom_attributes = '{"tier":"silver","seats":3,"vip":false}', last_activity_at = now() - interval '90 days' WHERE id = $1`, bram)
	carol := e.contact("", pgtype.UUID{}, "carol@acme.nl", "carol@home.example")
	e.exec(`UPDATE contacts SET organization_id = $1 WHERE id = $2`, acme, carol)
	annaConv := e.id(`SELECT id FROM conversations WHERE contact_id = $1`, anna)
	e.exec(`UPDATE conversations SET custom_attributes = '{"channel":"phone"}' WHERE id = $1`, annaConv)

	e.def(EntityContact, "tier", TypeList, "gold", "silver")
	e.def(EntityContact, "seats", TypeNumber)
	e.def(EntityContact, "renewal", TypeDate)
	e.def(EntityContact, "vip", TypeBoolean)
	e.def(EntityContact, "note", TypeText)
	e.def(EntityConversation, "channel", TypeList, "phone", "mail")
	defs, err := e.q.ListAttributeDefs(ctx, pgtype.Text{})
	if err != nil {
		t.Fatal(err)
	}

	run := func(search string, f *Filter) []string {
		t.Helper()
		if f != nil {
			if err := f.Validate(defs); err != nil {
				t.Fatalf("validate: %v", err)
			}
		}
		rows, err := List(ctx, e.pool, admin(), ListParams{Search: search, Filter: f, Sort: SortName, Limit: 50}, defs)
		if err != nil {
			t.Fatal(err)
		}
		got := names(rows)
		slices.Sort(got)
		return got
	}
	all := func(cs ...Condition) *Filter { return &Filter{Match: MatchAll, Conditions: cs} }
	anyOf := func(cs ...Condition) *Filter { return &Filter{Match: MatchAny, Conditions: cs} }

	cases := []struct {
		name   string
		search string
		filter *Filter
		want   []string
	}{
		{"everything", "", nil, []string{"", "Anna de Vries", "Bram"}},
		{"search name", "anna", nil, []string{"Anna de Vries"}},
		{"search any address", "home.example", nil, []string{""}},
		{"search organization", "acme", nil, []string{"", "Anna de Vries"}},
		{"search escapes wildcards", "%", nil, []string{}},
		{"name contains", "", all(Condition{Field: FieldName, Op: "contains", Value: "VRIES"}), []string{"Anna de Vries"}},
		{"name is not set", "", all(Condition{Field: FieldName, Op: "is_not_set"}), []string{""}},
		{"name starts with", "", all(Condition{Field: FieldName, Op: "starts_with", Value: "br"}), []string{"Bram"}},
		{"email equals any address", "", all(Condition{Field: FieldEmail, Op: "equals", Value: "carol@home.example"}), []string{""}},
		{"email not contains", "", all(Condition{Field: FieldEmail, Op: "not_contains", Value: "acme"}), []string{"Bram"}},
		{"domain equals", "", all(Condition{Field: FieldDomain, Op: "equals", Value: "gmail.com"}), []string{"Bram"}},
		{"organization equals", "", all(Condition{Field: FieldOrganization, Op: "equals", Value: "acme"}), []string{"", "Anna de Vries"}},
		{"no organization", "", all(Condition{Field: FieldOrganization, Op: "is_not_set"}), []string{"Bram"}},
		{"open conversation", "", all(Condition{Field: FieldHasOpenConversation, Op: "is_true"}), []string{"Anna de Vries"}},
		{"no open conversation", "", all(Condition{Field: FieldHasOpenConversation, Op: "is_false"}), []string{"", "Bram"}},
		{"active within days", "", all(Condition{Field: FieldLastActivity, Op: "within_days", Value: "30"}), []string{"", "Anna de Vries"}},
		{"inactive for days", "", all(Condition{Field: FieldLastActivity, Op: "older_than_days", Value: "30"}), []string{"Bram"}},
		{"list attribute", "", all(Condition{Field: FieldAttribute, Key: "tier", Op: "equals", Value: "gold"}), []string{"Anna de Vries"}},
		{"number greater", "", all(Condition{Field: FieldAttribute, Key: "seats", Op: "gt", Value: "5"}), []string{"Anna de Vries"}},
		{"number less", "", all(Condition{Field: FieldAttribute, Key: "seats", Op: "lt", Value: "5"}), []string{"Bram"}},
		{"date before", "", all(Condition{Field: FieldAttribute, Key: "renewal", Op: "before", Value: "2026-04-01"}), []string{"Anna de Vries"}},
		{"date on or after", "", all(Condition{Field: FieldAttribute, Key: "renewal", Op: "on_or_after", Value: "2026-04-01"}), []string{}},
		{"boolean true", "", all(Condition{Field: FieldAttribute, Key: "vip", Op: "is_true"}), []string{"Anna de Vries"}},
		{"boolean false", "", all(Condition{Field: FieldAttribute, Key: "vip", Op: "is_false"}), []string{"Bram"}},
		{"attribute not set", "", all(Condition{Field: FieldAttribute, Key: "tier", Op: "is_not_set"}), []string{""}},
		{"text attribute holds wildcard literal", "", all(Condition{Field: FieldAttribute, Key: "note", Op: "contains", Value: "100%"}), []string{"Anna de Vries"}},
		{"conversation attribute", "", all(Condition{Field: FieldConversationAttribute, Key: "channel", Op: "equals", Value: "phone"}), []string{"Anna de Vries"}},
		{"match any", "", anyOf(Condition{Field: FieldName, Op: "equals", Value: "bram"}, Condition{Field: FieldDomain, Op: "equals", Value: "acme.nl"}), []string{"", "Anna de Vries", "Bram"}},
		{"match all narrows", "", all(Condition{Field: FieldDomain, Op: "equals", Value: "acme.nl"}, Condition{Field: FieldName, Op: "is_set"}), []string{"Anna de Vries"}},
		{"search plus filter", "acme", all(Condition{Field: FieldName, Op: "is_set"}), []string{"Anna de Vries"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := run(c.search, c.filter)
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFilterRejectsBadInput(t *testing.T) {
	defs := []dbq.CustomAttributeDef{
		{Entity: EntityContact, Key: "seats", Type: TypeNumber},
		{Entity: EntityConversation, Key: "channel", Type: TypeList},
	}
	bad := map[string]string{
		"unknown field":             `{"conditions":[{"field":"password","op":"equals","value":"x"}]}`,
		"unknown op":                `{"conditions":[{"field":"name","op":"regex","value":"x"}]}`,
		"op not for field":          `{"conditions":[{"field":"name","op":"gt","value":"1"}]}`,
		"missing value":             `{"conditions":[{"field":"name","op":"equals"}]}`,
		"unknown attribute":         `{"conditions":[{"field":"attribute","key":"nope","op":"equals","value":"x"}]}`,
		"attribute of other entity": `{"conditions":[{"field":"attribute","key":"channel","op":"equals","value":"x"}]}`,
		"number not numeric":        `{"conditions":[{"field":"attribute","key":"seats","op":"gt","value":"many"}]}`,
		"bad date":                  `{"conditions":[{"field":"last_activity","op":"before","value":"yesterday"}]}`,
		"bad days":                  `{"conditions":[{"field":"last_activity","op":"within_days","value":"-3"}]}`,
		"bad match":                 `{"match":"either","conditions":[]}`,
		"key on plain field":        `{"conditions":[{"field":"name","key":"x","op":"equals","value":"y"}]}`,
		"sql in key":                `{"conditions":[{"field":"attribute","key":"seats'; DROP TABLE contacts;--","op":"is_set"}]}`,
	}
	for name, raw := range bad {
		if _, err := ParseFilter([]byte(raw), defs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseFilter([]byte(`{"conditions":[{"field":"attribute","key":"seats","op":"gt","value":"5"}]}`), defs); err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
}

func TestListKeysetPaginationForEverySort(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mb := e.mailbox("Support", "help@example.com")
	for i, n := range []string{"Bea", "", "Ada", "Cor", "", "Dirk", "ada"} {
		id := e.contact(n, pgtype.UUID{}, "c"+string(rune('a'+i))+"@example.com")
		e.conversation(mb, id, "s", "open")
	}
	for _, sort := range []string{SortName, SortActivity, SortCreated} {
		for _, desc := range []bool{false, true} {
			p := ListParams{Sort: sort, Desc: desc, Limit: 2}
			var seen []string
			for range 10 {
				rows, err := List(ctx, e.pool, admin(), p, nil)
				if err != nil {
					t.Fatal(err)
				}
				more := len(rows) > p.Limit
				if more {
					rows = rows[:p.Limit]
				}
				for _, r := range rows {
					seen = append(seen, r.ID.String())
				}
				if !more {
					break
				}
				if p.Cursor, err = NextCursor(rows, p); err != nil {
					t.Fatal(err)
				}
			}
			unique := slices.Compact(slices.Sorted(slices.Values(seen)))
			if len(seen) != 7 || len(unique) != 7 {
				t.Fatalf("sort %s desc=%v: %d rows, %d unique: duplicates or gaps", sort, desc, len(seen), len(unique))
			}
		}
	}
	if _, err := List(ctx, e.pool, admin(), ListParams{Sort: SortName, Limit: 2, Cursor: "garbage"}, nil); err == nil {
		t.Fatal("garbage cursor accepted")
	}
	c, err := encodeCursor(cursor{Sort: SortCreated, Keys: []string{"2026-01-01T00:00:00Z"}, ID: "0199a000-0000-7000-8000-000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := List(ctx, e.pool, admin(), ListParams{Sort: SortName, Limit: 2, Cursor: c}, nil); err == nil {
		t.Fatal("cursor of another sort accepted")
	}
}

func TestVisibilityRule(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mbA, mbB := e.mailbox("A", "a@example.com"), e.mailbox("B", "b@example.com")
	agent, other := e.user("agent"), e.user("agent")

	onlyA := e.contact("Only A", pgtype.UUID{}, "a@customer.nl")
	e.conversation(mbA, onlyA, "s", "open")
	onlyB := e.contact("Only B", pgtype.UUID{}, "b@customer.nl")
	e.conversation(mbB, onlyB, "s", "open")
	both := e.contact("Both", pgtype.UUID{}, "both@customer.nl")
	e.conversation(mbA, both, "s", "open")
	e.conversation(mbB, both, "s", "open")
	mine := e.contact("Mine", agent.ID, "mine@customer.nl")
	theirs := e.contact("Theirs", other.ID, "theirs@customer.nl")
	orphan := e.contact("Nobody", pgtype.UUID{}, "orphan@customer.nl")
	deletedOnly := e.contact("Deleted conversation only", other.ID, "deleted@customer.nl")
	deleted := e.conversation(mbA, deletedOnly, "s", "open")
	e.exec(`UPDATE conversations SET deleted_at = now() WHERE id = $1`, deleted)

	agentView := Viewer{UserID: agent.ID, MailboxIDs: []pgtype.UUID{mbA}}
	rows, err := List(ctx, e.pool, agentView, ListParams{Sort: SortName, Limit: 50}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := names(rows)
	slices.Sort(got)
	if want := []string{"Both", "Mine", "Only A"}; !slices.Equal(got, want) {
		t.Fatalf("agent sees %q, want %q", got, want)
	}
	for _, r := range rows {
		if r.Name == "Both" && r.ConversationCount != 1 {
			t.Fatalf("agent counts %d conversations of Both, want only the readable one", r.ConversationCount)
		}
	}
	for id, want := range map[pgtype.UUID]bool{onlyA: true, onlyB: false, both: true, mine: true, theirs: false, orphan: false, deletedOnly: false} {
		visible, err := agentView.ContactVisible(ctx, e.q, id)
		if err != nil {
			t.Fatal(err)
		}
		if visible != want {
			t.Errorf("ContactVisible(%s) = %v, want %v", id, visible, want)
		}
	}
	total, err := Count(ctx, e.pool, agentView, ListParams{}, nil)
	if err != nil || total != 3 {
		t.Fatalf("count = %d, %v", total, err)
	}
	all, err := List(ctx, e.pool, admin(), ListParams{Sort: SortName, Limit: 50}, nil)
	if err != nil || len(all) != 7 {
		t.Fatalf("admin sees %d contacts, %v", len(all), err)
	}
	search, err := List(ctx, e.pool, agentView, ListParams{Search: "b@customer", Sort: SortName, Limit: 50}, nil)
	if err != nil || len(search) != 0 {
		t.Fatalf("search reveals the contact of mailbox B: %v %v", names(search), err)
	}
}

func TestResolveSegmentBatchesAndScope(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mbA, mbB := e.mailbox("A", "a@example.com"), e.mailbox("B", "b@example.com")
	owner, other := e.user("agent"), e.user("agent")
	for i := range 5 {
		id := e.contact("Customer", pgtype.UUID{}, "c"+string(rune('a'+i))+"@acme.nl")
		e.conversation(mbA, id, "s", "open")
	}
	hidden := e.contact("Hidden", pgtype.UUID{}, "hidden@acme.nl")
	e.conversation(mbB, hidden, "s", "open")
	e.contact("Other domain", pgtype.UUID{}, "x@other.nl")

	filter := `{"match":"all","conditions":[{"field":"domain","op":"equals","value":"acme.nl"}]}`
	personal := e.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('Acme', $1, false, $2) RETURNING id`, owner.ID, filter)
	shared := e.id(`INSERT INTO contact_segments (name, owner_user_id, shared, filter) VALUES ('Acme shared', $1, true, $2) RETURNING id`, owner.ID, filter)

	var batches []int
	var total int
	ownerView := Viewer{UserID: owner.ID, MailboxIDs: []pgtype.UUID{mbA}}
	err := ResolveSegment(ctx, e.q, e.pool, personal, ownerView, 2, func(ids []pgtype.UUID) error {
		batches = append(batches, len(ids))
		total += len(ids)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || !slices.Equal(batches, []int{2, 2, 1}) {
		t.Fatalf("batches %v total %d, want [2 2 1] and 5 (the contact of mailbox B is out of scope)", batches, total)
	}
	otherView := Viewer{UserID: other.ID, MailboxIDs: []pgtype.UUID{mbA}}
	err = ResolveSegment(ctx, e.q, e.pool, personal, otherView, 2, func([]pgtype.UUID) error { return nil })
	if !errors.Is(err, ErrSegmentNotFound) {
		t.Fatalf("personal segment of someone else: %v", err)
	}
	if err := ResolveSegment(ctx, e.q, e.pool, shared, otherView, 10, func(ids []pgtype.UUID) error { total += len(ids); return nil }); err != nil {
		t.Fatalf("shared segment: %v", err)
	}
	if total != 10 {
		t.Fatalf("shared segment resolved %d, want 5 more", total-5)
	}
}
