package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

func (e *env) runImport(admin dbq.User, csv string, opts ImportOptions) dbq.ContactImport {
	e.t.Helper()
	ctx := context.Background()
	defs, err := e.q.ListAttributeDefs(ctx, pgtype.Text{String: EntityContact, Valid: true})
	if err != nil {
		e.t.Fatal(err)
	}
	prepared, err := PrepareImport([]byte(csv), opts, defs)
	if err != nil {
		e.t.Fatal(err)
	}
	mapping, err := json.Marshal(prepared.Options)
	if err != nil {
		e.t.Fatal(err)
	}
	key, _, err := e.store.Put(ctx, []byte(csv))
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.q.InsertContactImport(ctx, dbq.InsertContactImportParams{
		CreatedBy: admin.ID, Filename: "contacten.csv", Dedupe: prepared.Options.Dedupe, Delimiter: prepared.Options.Delimiter,
		Mapping: mapping, SourceKey: key, TotalRows: int32(len(prepared.Table.Rows)),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	w := NewImportWorker(ImportDeps{Pool: e.pool, Blobs: e.store, IsFreeMailDomain: func(d string) bool { return d == "gmail.com" }})
	err = w.Work(ctx, &river.Job[jobs.ContactImport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactImport{ImportID: id.String()}})
	if err != nil {
		e.t.Fatal(err)
	}
	imp, err := e.q.GetContactImport(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	if imp.SourceKey != "" || e.blobExists(key) {
		e.t.Error("the uploaded file must be deleted once the import ended")
	}
	return imp
}

func (e *env) contactByEmail(email string) (name, phone, attrs string, org pgtype.UUID, ok bool) {
	e.t.Helper()
	err := e.pool.QueryRow(context.Background(), `SELECT c.name, c.phone, c.custom_attributes::text, c.organization_id
		FROM contacts c JOIN contact_addresses a ON a.contact_id = c.id WHERE a.email = $1`, email).Scan(&name, &phone, &attrs, &org)
	return name, phone, attrs, org, err == nil
}

func columns(targets ...string) []ColumnMap {
	out := make([]ColumnMap, len(targets))
	for i, t := range targets {
		out[i] = ColumnMap{Index: i, Target: t}
	}
	return out
}

func TestImportCreatesContactsWithSemicolonFile(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin")
	e.def(EntityContact, "seats", TypeNumber)
	e.id(`INSERT INTO organizations (name, domains) VALUES ('Acme', '{acme.nl}') RETURNING id`)

	imp := e.runImport(admin, "\xEF\xBB\xBFE-mail;Naam;Telefoon;Organisatie;Plekken\n"+
		"Anna@Acme.nl;Anna;0612345678;;12\n"+
		"bram@gmail.com;Bram;;Bouw BV;3,5\n"+
		"carol@gmail.com;Carol;;;\n",
		ImportOptions{Dedupe: DedupeSkip, Columns: columns("email", "name", "phone", "organization", "attribute:seats")})

	if imp.Status != "done" || imp.CreatedCount != 3 || imp.FailedCount != 0 || imp.ProcessedRows != 3 || imp.Delimiter != ";" {
		t.Fatalf("import = %+v", imp)
	}
	name, phone, attrs, org, ok := e.contactByEmail("anna@acme.nl")
	if !ok || name != "Anna" || phone != "0612345678" || !strings.Contains(attrs, `"seats": 12`) {
		t.Fatalf("anna = %q %q %s %v", name, phone, attrs, ok)
	}
	var orgName string
	if err := e.pool.QueryRow(t.Context(), `SELECT name FROM organizations WHERE id = $1`, org).Scan(&orgName); err != nil || orgName != "Acme" {
		t.Errorf("a row without an organization column value must match the domain: %q %v", orgName, err)
	}
	_, _, attrs, org, _ = e.contactByEmail("bram@gmail.com")
	if !org.Valid || !strings.Contains(attrs, `"seats": 3.5`) {
		t.Errorf("bram = %s %v: organization is created from the column, decimal comma parsed", attrs, org)
	}
	if _, _, _, org, _ = e.contactByEmail("carol@gmail.com"); org.Valid {
		t.Error("free-mail domains never get an organization")
	}
	if e.count(`SELECT count(*) FROM contacts WHERE created_by = $1`, admin.ID) != 3 {
		t.Error("imported contacts must record who created them")
	}
}

func TestImportDedupeModes(t *testing.T) {
	setup := func(t *testing.T) (*env, dbq.User) {
		e := newEnv(t)
		admin := e.user("admin")
		e.def(EntityContact, "tier", TypeText)
		id := e.contact("Oude naam", pgtype.UUID{}, "anna@example.com")
		e.exec(`UPDATE contacts SET phone = '111', custom_attributes = '{"tier":"old"}' WHERE id = $1`, id)
		return e, admin
	}
	csv := "email,name,phone,tier\nANNA@example.com,Nieuwe naam,,new\nbram@example.com,Bram,,\n"
	cols := columns("email", "name", "phone", "attribute:tier")

	t.Run("skip leaves the existing contact alone", func(t *testing.T) {
		e, admin := setup(t)
		imp := e.runImport(admin, csv, ImportOptions{Dedupe: DedupeSkip, Columns: cols})
		if imp.CreatedCount != 1 || imp.SkippedCount != 1 || imp.UpdatedCount != 0 {
			t.Fatalf("counts = %+v", imp)
		}
		name, phone, attrs, _, _ := e.contactByEmail("anna@example.com")
		if name != "Oude naam" || phone != "111" || !strings.Contains(attrs, "old") {
			t.Errorf("existing contact changed: %q %q %s", name, phone, attrs)
		}
	})
	t.Run("update fills only what the file provides", func(t *testing.T) {
		e, admin := setup(t)
		imp := e.runImport(admin, csv, ImportOptions{Dedupe: DedupeUpdate, Columns: cols})
		if imp.CreatedCount != 1 || imp.UpdatedCount != 1 || imp.SkippedCount != 0 {
			t.Fatalf("counts = %+v", imp)
		}
		name, phone, attrs, _, _ := e.contactByEmail("anna@example.com")
		if name != "Nieuwe naam" || phone != "111" || !strings.Contains(attrs, "new") {
			t.Errorf("update result: %q %q %s (an empty cell must not erase the phone number)", name, phone, attrs)
		}
		if e.count(`SELECT count(*) FROM contacts`) != 2 {
			t.Error("dedupe by email must not create a second contact for the same address in another case")
		}
	})
	t.Run("a duplicate inside the file is deduped too", func(t *testing.T) {
		e, admin := setup(t)
		imp := e.runImport(admin, "email\nx@example.com\nX@example.com\n", ImportOptions{Dedupe: DedupeSkip, Columns: columns("email")})
		if imp.CreatedCount != 1 || imp.SkippedCount != 1 {
			t.Fatalf("counts = %+v", imp)
		}
	})
}

func TestImportReportsBadRowsAndKeepsTheRest(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin")
	e.def(EntityContact, "seats", TypeNumber)

	csv := "email,name,seats\n" +
		"good@example.com,Goed,5\n" +
		"not-an-email,Fout mailadres,1\n" +
		"other@example.com,Fout getal,veel\n" +
		"=bad,\"=HYPERLINK(\"\"http://x\"\")\",2\n" +
		"fine@example.com," + strings.Repeat("x", 201) + ",1\n" +
		"ok@example.com,Ook goed,\n"
	imp := e.runImport(admin, csv, ImportOptions{Dedupe: DedupeSkip, Columns: columns("email", "name", "attribute:seats")})

	if imp.Status != "done" || imp.CreatedCount != 2 || imp.FailedCount != 4 || imp.ProcessedRows != 6 || !strings.HasPrefix(imp.ErrorKey, "sha256/") {
		t.Fatalf("import = %+v", imp)
	}
	if _, _, _, _, ok := e.contactByEmail("other@example.com"); ok {
		t.Error("a row with an invalid attribute must not be created")
	}
	if _, _, _, _, ok := e.contactByEmail("ok@example.com"); !ok {
		t.Error("rows after a failed row must still be imported")
	}

	rc, err := e.store.Open(t.Context(), imp.ErrorKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, rc); err != nil {
		t.Fatal(err)
	}
	table, err := ParseCSV([]byte(buf.String()), ";")
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 4 || table.Header[0] != "Regel" || table.Header[1] != "Fout" {
		t.Fatalf("report = %+v", table)
	}
	if table.Rows[0][0] != "3" || !strings.Contains(table.Rows[0][1], "e-mailadres") || table.Rows[0][2] != "not-an-email" {
		t.Errorf("first error row = %v (line numbers count the header as line 1)", table.Rows[0])
	}
	if !strings.Contains(table.Rows[1][1], "aangepast veld") || !strings.Contains(table.Rows[1][1], "seats") {
		t.Errorf("attribute error = %q", table.Rows[1][1])
	}
	if !strings.HasPrefix(table.Rows[2][2], "'=bad") {
		t.Errorf("the error report must neutralise formulas: %q", table.Rows[2][2])
	}
}

func TestImportRejectsBadMappings(t *testing.T) {
	e := newEnv(t)
	e.def(EntityContact, "seats", TypeNumber)
	defs, err := e.q.ListAttributeDefs(context.Background(), pgtype.Text{})
	if err != nil {
		t.Fatal(err)
	}
	csv := []byte("a,b\n1,2\n")
	for name, opts := range map[string]ImportOptions{
		"no email column":               {Dedupe: DedupeSkip, Columns: columns("name", "phone")},
		"bad dedupe":                    {Dedupe: "merge", Columns: columns("email")},
		"same field twice":              {Dedupe: DedupeSkip, Columns: columns("email", "email")},
		"index out of range":            {Dedupe: DedupeSkip, Columns: []ColumnMap{{Index: 0, Target: "email"}, {Index: 7, Target: "name"}}},
		"unknown target":                {Dedupe: DedupeSkip, Columns: columns("email", "password")},
		"unknown attribute":             {Dedupe: DedupeSkip, Columns: columns("email", "attribute:nope")},
		"attribute of the wrong entity": {Dedupe: DedupeSkip, Columns: columns("email", "attribute:")},
		"bad delimiter":                 {Dedupe: DedupeSkip, Delimiter: "|", Columns: columns("email")},
	} {
		if _, err := PrepareImport(csv, opts, defs); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := PrepareImport(csv, ImportOptions{Dedupe: DedupeSkip, Columns: columns("email", "attribute:seats")}, defs); err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}
}

func TestImportOfUnreadableFileFailsCleanly(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin")
	ctx := context.Background()
	id, err := e.q.InsertContactImport(ctx, dbq.InsertContactImportParams{
		CreatedBy: admin.ID, Dedupe: DedupeSkip, Delimiter: ",", Mapping: []byte(`{"dedupe":"skip","columns":[{"index":0,"target":"email"}]}`),
		SourceKey: "sha256/00/00/" + strings.Repeat("0", 64), TotalRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := NewImportWorker(ImportDeps{Pool: e.pool, Blobs: e.store})
	err = w.Work(ctx, &river.Job[jobs.ContactImport]{JobRow: &rivertype.JobRow{}, Args: jobs.ContactImport{ImportID: id.String()}})
	var cancel *rivertype.JobCancelError
	if !errors.As(err, &cancel) {
		t.Fatalf("err = %v, want a cancelled job (no retry storm)", err)
	}
	imp, err := e.q.GetContactImport(ctx, id)
	if err != nil || imp.Status != "failed" || imp.Error == "" {
		t.Fatalf("import = %+v, %v", imp, err)
	}
}

func TestImportLeavesContactsTheImporterCannotSeeAlone(t *testing.T) {
	e := newEnv(t)
	agent := e.user("agent")
	mb := e.mailbox("Support", "support@example.com")
	id := e.contact("Oude naam", pgtype.UUID{}, "anna@example.com")
	e.conversation(mb, id, "Vraag", "open")
	csv := "email,name\nanna@example.com,Nieuwe naam\n"
	for _, mode := range []string{DedupeSkip, DedupeUpdate} {
		imp := e.runImport(agent, csv, ImportOptions{Dedupe: mode, Columns: columns("email", "name")})
		if imp.FailedCount != 1 || imp.UpdatedCount != 0 {
			t.Fatalf("%s: counts = %+v", mode, imp)
		}
		if name, _, _, _, _ := e.contactByEmail("anna@example.com"); name != "Oude naam" {
			t.Fatalf("%s: a contact outside the importer's mailboxes was changed to %q", mode, name)
		}
	}

	team := e.id(`INSERT INTO teams (name) VALUES ('Support') RETURNING id`)
	e.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, team, agent.ID)
	e.exec(`INSERT INTO mailbox_access (mailbox_id, team_id, level) VALUES ($1, $2, 'read')`, mb, team)
	imp := e.runImport(agent, csv, ImportOptions{Dedupe: DedupeUpdate, Columns: columns("email", "name")})
	if imp.UpdatedCount != 1 {
		t.Fatalf("counts = %+v", imp)
	}
}

func TestImportDoesNotAttachOrganizationsTheImporterCannotSee(t *testing.T) {
	e := newEnv(t)
	agent := e.user("agent")
	org := e.id(`INSERT INTO organizations (name, domains) VALUES ('Secret BV', '{secret.example}') RETURNING id`)
	hiddenContact := e.contact("Insider", pgtype.UUID{}, "insider@secret.example")
	e.exec(`UPDATE contacts SET organization_id = $2 WHERE id = $1`, hiddenContact, org)
	e.conversation(e.mailbox("Private", "private@example.com"), hiddenContact, "Vraag", "open")

	// By name and by the domain of the address.
	csv := "email,organization\nnew@elsewhere.example,Secret BV\nnew@secret.example,\n"
	imp := e.runImport(agent, csv, ImportOptions{Dedupe: DedupeSkip, Columns: columns("email", "organization")})
	if imp.CreatedCount != 2 {
		t.Fatalf("counts = %+v", imp)
	}
	if n := e.count(`SELECT count(*) FROM contacts WHERE organization_id = $1`, org); n != 1 {
		t.Errorf("%d contacts belong to the organization, want only the one it already had", n)
	}
}
