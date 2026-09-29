package policy

import (
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/testdb"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func TestMailboxScope(t *testing.T) {
	ctx := t.Context()
	q := dbq.New(testdb.New(t))

	newUser := func(role, email string) dbq.User {
		t.Helper()
		u, err := q.CreateUser(ctx, dbq.CreateUserParams{Email: email, Name: email, Role: role, PasswordHash: "x"})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	newMailbox := func(n int) pgtype.UUID {
		t.Helper()
		mb, err := q.InsertMailbox(ctx, dbq.InsertMailboxParams{
			Name: fmt.Sprintf("mb%d", n), EmailAddress: fmt.Sprintf("mb%d@example.com", n),
			ImapHost: "imap.example.com", ImapPort: 993, ImapTls: "implicit", ImapUsername: "u",
			SmtpHost: "smtp.example.com", SmtpPort: 465, SmtpTls: "implicit",
		})
		if err != nil {
			t.Fatal(err)
		}
		return mb.ID
	}
	newTeam := func(name string, members ...dbq.User) pgtype.UUID {
		t.Helper()
		team, err := q.CreateTeam(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]pgtype.UUID, len(members))
		for i, m := range members {
			ids[i] = m.ID
		}
		if err := q.AddTeamMembers(ctx, dbq.AddTeamMembersParams{TeamID: team.ID, UserIds: ids}); err != nil {
			t.Fatal(err)
		}
		return team.ID
	}
	grant := func(mailbox, team pgtype.UUID, level string) {
		t.Helper()
		err := q.AddMailboxAccess(ctx, dbq.AddMailboxAccessParams{MailboxID: mailbox, TeamIds: []pgtype.UUID{team}, Levels: []string{level}})
		if err != nil {
			t.Fatal(err)
		}
	}

	owner, admin := newUser(RoleOwner, "owner@example.com"), newUser(RoleAdmin, "admin@example.com")
	agent, readonly := newUser(RoleAgent, "agent@example.com"), newUser(RoleReadonly, "readonly@example.com")
	outsider := newUser(RoleAgent, "outsider@example.com")
	a, b, c, d := newMailbox(1), newMailbox(2), newMailbox(3), newMailbox(4)

	// The agent reaches a through a read team and a write team (write wins), b read-only, and
	// never c or d. The readonly user is on a write team but must still not write.
	readTeam := newTeam("read", agent)
	writeTeam := newTeam("write", agent, readonly)
	grant(a, readTeam, "read")
	grant(a, writeTeam, "write")
	grant(b, readTeam, "read")
	grant(c, newTeam("other"), "write")

	sortIDs := func(ids []pgtype.UUID) []pgtype.UUID {
		out := slices.Clone(ids)
		slices.SortFunc(out, func(x, y pgtype.UUID) int { return slices.Compare(x.Bytes[:], y.Bytes[:]) })
		return out
	}
	all := sortIDs([]pgtype.UUID{a, b, c, d})
	for _, tc := range []struct {
		name        string
		user        dbq.User
		read, write []pgtype.UUID
	}{
		{"owner", owner, all, all},
		{"admin", admin, all, all},
		{"agent", agent, sortIDs([]pgtype.UUID{a, b}), []pgtype.UUID{a}},
		{"readonly", readonly, []pgtype.UUID{a}, []pgtype.UUID{}},
		{"no team", outsider, []pgtype.UUID{}, []pgtype.UUID{}},
	} {
		got, err := MailboxScope(ctx, q, tc.user)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(sortIDs(got.Read), tc.read) || !slices.Equal(sortIDs(got.Write), tc.write) {
			t.Errorf("%s: read %v write %v, want read %v write %v", tc.name, got.Read, got.Write, tc.read, tc.write)
		}
	}
}

func TestScopeWidens(t *testing.T) {
	id := func(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }
	base := Scope{Read: []pgtype.UUID{id(1), id(2)}, Write: []pgtype.UUID{id(1)}}
	for name, tc := range map[string]struct {
		after Scope
		want  bool
	}{
		"same":           {base, false},
		"fewer":          {Scope{Read: []pgtype.UUID{id(1)}, Write: []pgtype.UUID{}}, false},
		"new mailbox":    {Scope{Read: []pgtype.UUID{id(1), id(2), id(3)}, Write: []pgtype.UUID{id(1)}}, true},
		"read to write":  {Scope{Read: []pgtype.UUID{id(1), id(2)}, Write: []pgtype.UUID{id(1), id(2)}}, true},
		"swap a mailbox": {Scope{Read: []pgtype.UUID{id(1), id(3)}, Write: []pgtype.UUID{id(1)}}, true},
		"nothing at all": {Scope{Read: []pgtype.UUID{}, Write: []pgtype.UUID{}}, false},
	} {
		if got := tc.after.Widens(base); got != tc.want {
			t.Errorf("%s: Widens = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPrivilegedCoversPermissionsThatWidenAccess(t *testing.T) {
	for _, p := range []Permission{UsersManage, SettingsManage, TeamsManage, MailboxesManage, ContactsErase, ContactsImport, WebhooksManage, AutomationManage} {
		if !Privileged([]string{string(p)}) {
			t.Errorf("%s must be privileged", p)
		}
	}
	for _, p := range []Permission{ConversationsRead, ContactsExport, LabelsManage, CampaignsManage, SLAManage, AuditView} {
		if Privileged([]string{string(p)}) {
			t.Errorf("%s must not be privileged", p)
		}
	}
}
