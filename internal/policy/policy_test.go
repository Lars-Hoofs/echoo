package policy

import (
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

func user(id byte, role string) dbq.User {
	return dbq.User{ID: pgtype.UUID{Bytes: [16]byte{id}, Valid: true}, Role: role}
}

func TestCanManageUser(t *testing.T) {
	owner, admin, admin2 := user(1, RoleOwner), user(2, RoleAdmin), user(3, RoleAdmin)
	agent, readonly := user(4, RoleAgent), user(5, RoleReadonly)

	cases := []struct {
		name          string
		actor, target dbq.User
		want          bool
	}{
		{"owner manages admin", owner, admin, true},
		{"owner manages agent", owner, agent, true},
		{"owner cannot manage self", owner, owner, false},
		{"admin manages agent", admin, agent, true},
		{"admin manages readonly", admin, readonly, true},
		{"admin cannot manage admin", admin, admin2, false},
		{"admin cannot manage owner", admin, owner, false},
		{"admin cannot manage self", admin, admin, false},
		{"agent manages nobody", agent, readonly, false},
		{"readonly manages nobody", readonly, agent, false},
	}
	for _, c := range cases {
		if got := CanManageUser(c.actor, c.target); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestCanAssignRole(t *testing.T) {
	owner, admin, agent := user(1, RoleOwner), user(2, RoleAdmin), user(3, RoleAgent)
	for _, c := range []struct {
		actor dbq.User
		role  string
		want  bool
	}{
		{owner, RoleOwner, false},
		{owner, RoleAdmin, true},
		{owner, RoleReadonly, true},
		{admin, RoleAdmin, false},
		{admin, RoleAgent, true},
		{agent, RoleAgent, false},
		{owner, "superuser", false},
	} {
		if got := CanAssignRole(c.actor, c.role); got != c.want {
			t.Errorf("%s assigns %s: got %v", c.actor.Role, c.role, got)
		}
	}
}

// legacy holds, per permission, the rule the four built-in roles were held to before
// permissions existed (CanAdminister, CanModifyConversations, CanEditArticles and the
// checks that let every signed-in user through). The presets must reproduce it exactly.
var legacy = map[Permission]func(role string) bool{
	ConversationsRead:   everyone,
	ConversationsWrite:  writers,
	ConversationsAssign: writers,
	ConversationsDelete: writers,
	ContactsRead:        everyone,
	ContactsWrite:       writers,
	ContactsExport:      everyone,
	ContactsImport:      admins,
	ContactsErase:       admins,
	ContactsModerate:    admins,
	ReportsView:         everyone,
	ReportsViewAll:      admins,
	KBWrite:             writers,
	KBManage:            admins,
	LabelsManage:        admins,
	TemplatesManage:     admins,
	AutomationManage:    admins,
	SLAManage:           admins,
	MailboxesManage:     admins,
	UsersManage:         admins,
	TeamsManage:         admins,
	SettingsManage:      admins,
	AuditView:           admins,
	APITokensManageAll:  admins,
	WebhooksManage:      admins,
	CampaignsManage:     admins,
}

func everyone(string) bool     { return true }
func writers(role string) bool { return role != RoleReadonly }
func admins(role string) bool  { return role == RoleOwner || role == RoleAdmin }

func TestBuiltInRolesKeepTheirRights(t *testing.T) {
	if len(legacy) != len(all) {
		t.Fatalf("legacy table covers %d permissions, the list has %d", len(legacy), len(all))
	}
	for _, role := range []string{RoleOwner, RoleAdmin, RoleAgent, RoleReadonly} {
		for _, p := range all {
			rule, ok := legacy[p]
			if !ok {
				t.Fatalf("permission %s is missing from the legacy table", p)
			}
			if got := Has(user(1, role), p); got != rule(role) {
				t.Errorf("%s / %s: got %v, want %v", role, p, got, rule(role))
			}
		}
		if got, want := len(Effective(user(1, role))), countAllowed(role); got != want {
			t.Errorf("%s: effective list has %d entries, want %d", role, got, want)
		}
	}
}

func countAllowed(role string) int {
	n := 0
	for _, rule := range legacy {
		if rule(role) {
			n++
		}
	}
	return n
}

func TestPresetsRespectRequirements(t *testing.T) {
	for _, role := range []string{RoleOwner, RoleAdmin, RoleAgent, RoleReadonly} {
		if p, needs := MissingRequirement(RolePermissions(role)); p != "" {
			t.Errorf("%s holds %s without %s", role, p, needs)
		}
	}
}

func custom(id byte, perms ...Permission) dbq.User {
	u := user(id, RoleCustom)
	u.Permissions = toStrings(perms)
	return u
}

func TestCustomRoleRights(t *testing.T) {
	lead := custom(1, ConversationsRead, ConversationsAssign, ReportsView)
	for p, want := range map[Permission]bool{
		ConversationsRead: true, ConversationsAssign: true, ReportsView: true,
		ConversationsWrite: false, MailboxesManage: false, UsersManage: false,
	} {
		if got := Has(lead, p); got != want {
			t.Errorf("%s: got %v, want %v", p, got, want)
		}
	}
	if got := Effective(lead); len(got) != 3 || got[0] != "conversations.read" || got[2] != "reports.view" {
		t.Errorf("effective list: %v", got)
	}
	if SeesAll(lead) {
		t.Error("a custom role must not see every mailbox")
	}
}

func TestEscalationRules(t *testing.T) {
	owner, admin := user(1, RoleOwner), user(2, RoleAdmin)
	manager := custom(3, UsersManage, ConversationsRead, ConversationsWrite, ContactsRead)
	lead := custom(4, ConversationsRead, ReportsView)
	other := user(5, RoleAgent)

	grant := []struct {
		name  string
		actor dbq.User
		perms []string
		want  bool
	}{
		{"owner grants anything", owner, RolePermissions(RoleAdmin), true},
		{"admin grants ordinary permissions", admin, []string{"reports.view", "conversations.read"}, true},
		{"admin cannot grant users.manage", admin, []string{"users.manage"}, false},
		{"admin cannot grant settings.manage", admin, []string{"settings.manage"}, false},
		{"manager grants what it holds", manager, []string{"conversations.read", "conversations.write"}, true},
		{"manager cannot grant what it lacks", manager, []string{"reports.view"}, false},
		{"manager cannot grant users.manage, even though it holds it", manager, []string{"users.manage"}, false},
		{"without users.manage nothing is granted", lead, []string{"reports.view"}, false},
		{"agent grants nothing", other, []string{"conversations.read"}, false},
	}
	for _, c := range grant {
		if got := CanGrant(c.actor, c.perms); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}

	if CanAssignRole(manager, RoleAgent) {
		t.Error("manager lacks reports.view and kb.write, so it cannot hand out agent")
	}
	if CanAssignRole(manager, RoleReadonly) {
		t.Error("readonly needs reports.view, which the manager lacks")
	}
	if CanManageUser(manager, lead) {
		t.Error("manager lacks reports.view, so it cannot manage a user who has it")
	}
	if !CanManageUser(owner, manager) || CanManageUser(admin, manager) {
		t.Error("only the owner manages a user who holds users.manage")
	}
	if CanManageUser(manager, admin) || CanManageUser(manager, owner) {
		t.Error("nobody below the owner manages admins or the owner")
	}
}

// The frontend keeps its own copy of the permission list (web/src/lib/permissions.ts); a key
// added here without it would be invisible in the role editor.
func TestFrontendNamesEveryPermission(t *testing.T) {
	src, err := os.ReadFile("../../web/src/lib/permissions.ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if !strings.Contains(string(src), "'"+string(p)+"'") {
			t.Errorf("web/src/lib/permissions.ts does not mention %s", p)
		}
	}
}
