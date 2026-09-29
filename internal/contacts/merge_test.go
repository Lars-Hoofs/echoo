package contacts

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestMerge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mbA, mbB := e.mailbox("A", "a@example.com"), e.mailbox("B", "b@example.com")
	actor := e.user("admin")
	org := e.id(`INSERT INTO organizations (name) VALUES ('Acme') RETURNING id`)

	primary := e.contact("", pgtype.UUID{}, "anna@acme.nl")
	e.exec(`UPDATE contacts SET custom_attributes = '{"tier":"gold"}' WHERE id = $1`, primary)
	secondary := e.contact("Anna de Vries", pgtype.UUID{}, "anna.devries@home.example", "anna2@home.example")
	e.exec(`UPDATE contacts SET phone = '0612345678', organization_id = $2, custom_attributes = '{"tier":"silver","seats":4}' WHERE id = $1`, secondary, org)
	convA := e.conversation(mbA, secondary, "Vraag", "open")
	convB := e.conversation(mbB, secondary, "Andere vraag", "closed")
	own := e.conversation(mbA, primary, "Eigen", "open")
	e.exec(`INSERT INTO crm_notes (contact_id, body) VALUES ($1, 'notitie van secondary')`, secondary)
	e.exec(`INSERT INTO crm_notes (contact_id, body) VALUES ($1, 'notitie van primary')`, primary)

	res, err := Merge(ctx, e.pool, admin(), Actor{UserID: actor.ID}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	if res.Addresses != 2 || res.Conversations != 2 || res.Notes != 1 {
		t.Fatalf("result = %+v", res)
	}

	if e.count(`SELECT count(*) FROM contacts WHERE id = $1`, secondary) != 0 {
		t.Error("secondary contact still exists")
	}
	if e.count(`SELECT count(*) FROM contact_addresses WHERE contact_id = $1`, primary) != 3 {
		t.Error("addresses were not moved")
	}
	if e.count(`SELECT count(*) FROM contact_addresses WHERE contact_id = $1 AND is_primary`, primary) != 1 {
		t.Error("the merged contact must keep exactly its own primary address")
	}
	for _, c := range []pgtype.UUID{convA, convB, own} {
		if e.count(`SELECT count(*) FROM conversations WHERE id = $1 AND contact_id = $2`, c, primary) != 1 {
			t.Errorf("conversation %s not on the primary", c)
		}
	}
	if e.count(`SELECT count(*) FROM crm_notes WHERE contact_id = $1`, primary) != 2 {
		t.Error("notes were not moved")
	}
	var name, phone, attrs string
	var orgID pgtype.UUID
	if err := e.pool.QueryRow(ctx, `SELECT name, phone, organization_id, custom_attributes::text FROM contacts WHERE id = $1`, primary).Scan(&name, &phone, &orgID, &attrs); err != nil {
		t.Fatal(err)
	}
	if name != "Anna de Vries" || phone != "0612345678" || orgID != org {
		t.Errorf("empty fields were not filled from the merged contact: %q %q %v", name, phone, orgID)
	}
	if attrs != `{"tier": "gold", "seats": 4}` && attrs != `{"seats": 4, "tier": "gold"}` {
		t.Errorf("attributes = %s: primary must win on tier and gain seats", attrs)
	}
	if e.count(`SELECT count(*) FROM conversation_events WHERE type = 'contact_merged' AND data ->> 'contact_id' = $1 AND data ->> 'merged_contact_id' = $2`, primary.String(), secondary.String()) != 2 {
		t.Error("one contact_merged event per moved conversation expected")
	}
	if e.count(`SELECT count(*) FROM audit_log WHERE action = 'contact.merged' AND target_id = $1 AND metadata ->> 'merged_contact_id' = $2`, primary.String(), secondary.String()) != 1 {
		t.Error("merge was not audited")
	}
}

func TestMergePrimaryWinsWhenBothHaveValues(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	orgA := e.id(`INSERT INTO organizations (name) VALUES ('A') RETURNING id`)
	orgB := e.id(`INSERT INTO organizations (name) VALUES ('B') RETURNING id`)
	primary := e.contact("Primary", pgtype.UUID{}, "p@x.nl")
	secondary := e.contact("Secondary", pgtype.UUID{}, "s@x.nl")
	e.exec(`UPDATE contacts SET phone = '1', organization_id = $2 WHERE id = $1`, primary, orgA)
	e.exec(`UPDATE contacts SET phone = '2', organization_id = $2 WHERE id = $1`, secondary, orgB)
	if _, err := Merge(ctx, e.pool, admin(), Actor{}, primary, secondary); err != nil {
		t.Fatal(err)
	}
	var name, phone string
	var org pgtype.UUID
	if err := e.pool.QueryRow(ctx, `SELECT name, phone, organization_id FROM contacts WHERE id = $1`, primary).Scan(&name, &phone, &org); err != nil {
		t.Fatal(err)
	}
	if name != "Primary" || phone != "1" || org != orgA {
		t.Fatalf("primary lost: %q %q %v", name, phone, org)
	}
}

func TestMergeRefusals(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mbA, mbB := e.mailbox("A", "a@example.com"), e.mailbox("B", "b@example.com")
	agent := e.user("agent")
	visible := e.contact("Visible", pgtype.UUID{}, "v@x.nl")
	e.conversation(mbA, visible, "s", "open")
	hidden := e.contact("Hidden", pgtype.UUID{}, "h@x.nl")
	e.conversation(mbB, hidden, "s", "open")
	view := Viewer{UserID: agent.ID, MailboxIDs: []pgtype.UUID{mbA}}

	if _, err := Merge(ctx, e.pool, view, Actor{}, visible, visible); !errors.Is(err, ErrMergeSelf) {
		t.Errorf("self merge: %v", err)
	}
	if _, err := Merge(ctx, e.pool, view, Actor{}, visible, hidden); !errors.Is(err, ErrNotFound) {
		t.Errorf("merging a contact the user cannot see: %v", err)
	}
	if _, err := Merge(ctx, e.pool, view, Actor{}, hidden, visible); !errors.Is(err, ErrNotFound) {
		t.Errorf("merging into a contact the user cannot see: %v", err)
	}
	if e.count(`SELECT count(*) FROM contacts`) != 2 || e.count(`SELECT count(*) FROM conversations WHERE contact_id = $1`, hidden) != 1 {
		t.Error("a refused merge changed data")
	}
	if _, err := Merge(ctx, e.pool, admin(), Actor{}, visible, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown contact: %v", err)
	}
}
