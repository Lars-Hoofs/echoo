package api

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

// createRole makes a custom role through the API as c and returns its id.
func createRole(t *testing.T, c *client, name string, perms ...string) string {
	t.Helper()
	r := c.do("POST", "/api/v1/roles", map[string]any{"name": name, "permissions": perms})
	expect(t, r, 201, "")
	return r.body["role"].(map[string]any)["id"].(string)
}

// userWithRole creates a signed-in user who holds the custom role.
func (h *harness) userWithRole(roleID string) (*client, dbq.User) {
	h.t.Helper()
	id, ok := parseUUID(roleID)
	if !ok {
		h.t.Fatalf("bad role id %q", roleID)
	}
	u, pw := h.user("agent", fmt.Sprintf("custom-%d@example.com", h.ipSeq.Add(1)))
	u, err := h.q.SetUserCustomRole(context.Background(), dbq.SetUserCustomRoleParams{ID: u.ID, RoleID: id})
	if err != nil {
		h.t.Fatal(err)
	}
	c := h.client()
	if r := c.login(u.Email, pw); r.status != 200 {
		h.t.Fatalf("login custom user: %d %s", r.status, r.raw)
	}
	return c, u
}

func permissionsOf(t *testing.T, r response) []string {
	t.Helper()
	expect(t, r, 200, "")
	raw, _ := r.body["permissions"].([]any)
	out := make([]string, len(raw))
	for i, p := range raw {
		out[i], _ = p.(string)
	}
	return out
}

func TestMeReturnsEffectivePermissions(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	for _, role := range []string{"owner", "admin", "agent", "readonly"} {
		c := owner
		if role != "owner" {
			c, _ = h.loggedIn(role)
		}
		got := permissionsOf(t, c.do("GET", "/api/v1/me", nil))
		want := policy.RolePermissions(role)
		if role == "owner" {
			want = policy.RolePermissions(policy.RoleAdmin)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: /me permissions %v, want %v", role, got, want)
		}
	}

	c, _ := h.userWithRole(createRole(t, owner, "Teamleider", "conversations.read", "reports.view"))
	r := c.do("GET", "/api/v1/me", nil)
	if got := permissionsOf(t, r); !slices.Equal(got, []string{"conversations.read", "reports.view"}) {
		t.Errorf("custom role permissions: %v", got)
	}
	if r.body["custom_role_name"] != "Teamleider" || r.body["user"].(map[string]any)["role"] != "custom" {
		t.Errorf("custom role identity: %v", r.body)
	}
}

func TestRolesCRUD(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	agent, _ := h.loggedIn("agent")

	expect(t, agent.do("GET", "/api/v1/roles", nil), 403, "forbidden")

	list := owner.do("GET", "/api/v1/roles", nil)
	expect(t, list, 200, "")
	if builtin, _ := list.body["builtin"].([]any); len(builtin) != 4 {
		t.Fatalf("builtin roles: %v", list.body["builtin"])
	}
	if keys, _ := list.body["permissions"].([]any); len(keys) != len(policy.Permissions()) {
		t.Fatalf("permission keys: %v", keys)
	}

	id := createRole(t, owner, "Support lead", "conversations.read", "reports.view")
	expect(t, owner.do("POST", "/api/v1/roles", map[string]any{"name": "support LEAD", "permissions": []string{"reports.view"}}), 422, "validation_failed")

	bad := map[string]map[string]any{
		"unknown permission": {"name": "X", "permissions": []string{"conversations.fly"}},
		"empty permissions":  {"name": "X", "permissions": []string{}},
		"missing base":       {"name": "X", "permissions": []string{"conversations.write"}},
		"assign needs write": {"name": "X", "permissions": []string{"conversations.read", "conversations.assign"}},
		"empty name":         {"name": " ", "permissions": []string{"reports.view"}},
	}
	for name, body := range bad {
		if r := owner.do("POST", "/api/v1/roles", body); r.status != 422 {
			t.Errorf("%s: got %d %s", name, r.status, r.raw)
		}
	}

	member, u := h.userWithRole(id)
	expect(t, owner.do("PATCH", "/api/v1/roles/"+id, map[string]any{"permissions": []string{"conversations.read", "reports.view", "reports.view_all_agents"}}), 200, "")
	if got := permissionsOf(t, member.do("GET", "/api/v1/me", nil)); !slices.Contains(got, "reports.view_all_agents") {
		t.Errorf("a role change must reach its members at once: %v", got)
	}
	stored, err := h.q.GetUser(context.Background(), u.ID)
	if err != nil || !slices.Contains(stored.Permissions, "reports.view_all_agents") {
		t.Errorf("stored copy: %v %v", stored.Permissions, err)
	}

	expect(t, owner.do("DELETE", "/api/v1/roles/"+id, nil), 409, "role_in_use")
	spare := createRole(t, owner, "Spare", "reports.view")
	expect(t, owner.do("DELETE", "/api/v1/roles/"+spare, nil), 204, "")
	expect(t, owner.do("DELETE", "/api/v1/roles/"+spare, nil), 404, "not_found")
}

func TestCustomRoleGrantsAndDenies(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")

	cases := []struct {
		name  string
		perms []string
		allow map[string]string // "METHOD path" -> body marker, expected not to be 403
		deny  []string
	}{
		{
			name:  "reporter",
			perms: []string{"conversations.read", "reports.view"},
			allow: map[string]string{"GET /api/v1/reports/overview": "", "GET /api/v1/conversations": ""},
			deny: []string{
				"GET /api/v1/users", "GET /api/v1/mailboxes", "POST /api/v1/labels", "GET /api/v1/audit", "GET /api/v1/contacts",
				"GET /api/v1/webhooks", "GET /api/v1/settings/security", "GET /api/v1/rules", "GET /api/v1/roles",
			},
		},
		{
			name:  "auditor",
			perms: []string{"audit.view", "labels.manage"},
			allow: map[string]string{"GET /api/v1/audit": "", "POST /api/v1/labels": "", "GET /api/v1/users": ""},
			deny:  []string{"GET /api/v1/mailboxes", "GET /api/v1/teams", "GET /api/v1/reports/overview", "GET /api/v1/tokens", "GET /api/v1/sla-policies"},
		},
		{
			name:  "postmaster",
			perms: []string{"mailboxes.manage", "webhooks.manage"},
			allow: map[string]string{"GET /api/v1/mailboxes": "", "GET /api/v1/webhooks": "", "GET /api/v1/teams": ""},
			deny:  []string{"GET /api/v1/users", "GET /api/v1/audit", "GET /api/v1/settings/security", "GET /api/v1/rules"},
		},
		{
			name:  "crm reader",
			perms: []string{"contacts.read"},
			allow: map[string]string{"GET /api/v1/contacts": ""},
			deny:  []string{"GET /api/v1/contacts/export", "POST /api/v1/contacts"},
		},
		{
			name:  "automation and sla",
			perms: []string{"automation.manage", "sla.manage"},
			allow: map[string]string{"GET /api/v1/rules": "", "GET /api/v1/sla-policies": "", "GET /api/v1/users": ""},
			deny:  []string{"GET /api/v1/webhooks", "GET /api/v1/settings/security"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			user, _ := h.userWithRole(createRole(t, owner, c.name, c.perms...))
			for key := range c.allow {
				method, path := splitKey(key)
				if r := user.do(method, path, bodyFor(method)); r.status == 403 || r.status == 401 {
					t.Errorf("%s: got %d %s, want access", key, r.status, r.raw)
				}
			}
			for _, key := range c.deny {
				method, path := splitKey(key)
				if r := user.do(method, path, bodyFor(method)); r.status != 403 {
					t.Errorf("%s: got %d %s, want 403", key, r.status, r.raw)
				}
			}
		})
	}
}

func splitKey(key string) (method, path string) {
	for i := range key {
		if key[i] == ' ' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func bodyFor(method string) any {
	if method == "GET" || method == "DELETE" {
		return nil
	}
	return map[string]any{}
}

func TestCustomRoleWithoutReadSeesNoConversations(t *testing.T) {
	f := newInboxFixture(t)
	f.conversation("hello", convOpt{mailbox: f.mailboxA})
	owner, _ := f.h.loggedIn("owner")
	user, u := f.h.userWithRole(createRole(t, owner, "Reports only", "reports.view"))
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)`, f.team, u.ID)
	if got := subjects(f.list(user, "")); got != "" {
		t.Fatalf("a role without conversations.read saw %q", got)
	}
}

func TestAssignAndDeleteAreSeparateRights(t *testing.T) {
	f := newInboxFixture(t)
	owner, _ := f.h.loggedIn("owner")
	writeOnly, w := f.h.userWithRole(createRole(t, owner, "Answering", "conversations.read", "conversations.write"))
	full, x := f.h.userWithRole(createRole(t, owner, "Triage", "conversations.read", "conversations.write", "conversations.assign", "conversations.delete"))
	f.exec(`INSERT INTO team_members (team_id, user_id) VALUES ($1, $2), ($1, $3)`, f.team, w.ID, x.ID)
	id := f.conversation("hello", convOpt{mailbox: f.mailboxA})

	expect(t, f.patch(writeOnly, id, map[string]any{"status": "closed"}), 200, "")
	expect(t, f.patch(writeOnly, id, map[string]any{"assignee_user_id": w.ID.String()}), 403, "forbidden")
	expect(t, f.patch(writeOnly, id, map[string]any{"assignee_team_id": f.team}), 403, "forbidden")
	expect(t, f.patch(writeOnly, id, map[string]any{"status": "spam"}), 403, "forbidden")
	expect(t, writeOnly.do("POST", "/api/v1/conversations/bulk", map[string]any{"ids": []string{id}, "action": map[string]any{"assignee_user_id": w.ID.String()}}), 403, "forbidden")

	expect(t, f.patch(full, id, map[string]any{"assignee_user_id": x.ID.String()}), 200, "")
	expect(t, f.patch(full, id, map[string]any{"status": "spam"}), 200, "")
}

func TestOnlyTheOwnerHandsOutAccessControlRoles(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	adm, _ := h.loggedIn("admin")

	// An admin holds users.manage but may not create roles that grant it.
	expect(t, adm.do("POST", "/api/v1/roles", map[string]any{"name": "Escalate", "permissions": []string{"users.manage"}}), 403, "forbidden")
	expect(t, adm.do("POST", "/api/v1/roles", map[string]any{"name": "Escalate 2", "permissions": []string{"settings.manage"}}), 403, "forbidden")
	plain := adm.do("POST", "/api/v1/roles", map[string]any{"name": "Plain", "permissions": []string{"reports.view"}})
	expect(t, plain, 201, "")
	plainID := plain.body["role"].(map[string]any)["id"].(string)
	// Nor add such a permission later.
	expect(t, adm.do("PATCH", "/api/v1/roles/"+plainID, map[string]any{"permissions": []string{"reports.view", "users.manage"}}), 403, "forbidden")

	managerRole := createRole(t, owner, "People manager", "users.manage", "conversations.read", "conversations.write", "contacts.read")
	expect(t, adm.do("PATCH", "/api/v1/roles/"+managerRole, map[string]any{"name": "Renamed"}), 403, "forbidden")
	expect(t, adm.do("DELETE", "/api/v1/roles/"+managerRole, nil), 403, "forbidden")

	// Handing such a role to someone is the owner's too.
	target, _ := h.user("agent", "target@example.com")
	expect(t, adm.do("PATCH", "/api/v1/users/"+target.ID.String(), map[string]any{"custom_role_id": managerRole}), 403, "forbidden")
	expect(t, adm.do("POST", "/api/v1/users", map[string]any{"email": "new@example.com", "name": "New", "custom_role_id": managerRole}), 403, "forbidden")
	expect(t, owner.do("PATCH", "/api/v1/users/"+target.ID.String(), map[string]any{"custom_role_id": managerRole}), 200, "")

	// A user who holds the role cannot be managed by an admin.
	expect(t, adm.do("PATCH", "/api/v1/users/"+target.ID.String(), map[string]any{"role": "readonly"}), 403, "forbidden")
	expect(t, adm.do("POST", "/api/v1/users/"+target.ID.String()+"/reset-password", nil), 403, "forbidden")

	// The admin can hand out the plain role and create users with it.
	other, _ := h.user("agent", "other@example.com")
	expect(t, adm.do("PATCH", "/api/v1/users/"+other.ID.String(), map[string]any{"custom_role_id": plainID}), 200, "")
	created := adm.do("POST", "/api/v1/users", map[string]any{"email": "made@example.com", "name": "Made", "custom_role_id": plainID})
	expect(t, created, 201, "")
	if created.body["user"].(map[string]any)["role"] != "custom" {
		t.Fatalf("created user: %v", created.body)
	}
	// Both fields at once, or an unknown role, are validation errors.
	expect(t, adm.do("POST", "/api/v1/users", map[string]any{"email": "x@example.com", "name": "X", "role": "agent", "custom_role_id": plainID}), 422, "validation_failed")
	expect(t, adm.do("POST", "/api/v1/users", map[string]any{"email": "y@example.com", "name": "Y", "custom_role_id": "0199a000-0000-7000-8000-000000000000"}), 422, "validation_failed")
}

func TestCustomManagerCannotGrantWhatItLacks(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	manager, _ := h.userWithRole(createRole(t, owner, "Manager", "users.manage", "conversations.read", "conversations.write", "reports.view", "kb.write", "contacts.read", "contacts.write", "contacts.export"))

	expect(t, manager.do("POST", "/api/v1/roles", map[string]any{"name": "Too much", "permissions": []string{"audit.view"}}), 403, "forbidden")
	ok := manager.do("POST", "/api/v1/roles", map[string]any{"name": "Fine", "permissions": []string{"reports.view", "conversations.read"}})
	expect(t, ok, 201, "")

	// Agents can also assign and delete, which the manager lacks; readonly needs nothing more
	// than the manager has.
	expect(t, manager.do("POST", "/api/v1/users", map[string]any{"email": "a1@example.com", "name": "A", "role": "agent"}), 403, "forbidden")
	expect(t, manager.do("POST", "/api/v1/users", map[string]any{"email": "a2@example.com", "name": "A", "role": "admin"}), 403, "forbidden")
	expect(t, manager.do("POST", "/api/v1/users", map[string]any{"email": "a3@example.com", "name": "A", "role": "readonly"}), 201, "")
}

func TestUserJSONCarriesCustomRole(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	id := createRole(t, owner, "Viewer", "conversations.read")
	_, u := h.userWithRole(id)
	r := owner.do("GET", "/api/v1/users", nil)
	expect(t, r, 200, "")
	found := false
	for _, raw := range r.body["users"].([]any) {
		m := raw.(map[string]any)
		if m["id"] == u.ID.String() {
			found = true
			if m["role"] != "custom" || m["custom_role_id"] != id || m["can_write_conversations"] != false {
				t.Errorf("user: %v", m)
			}
		}
	}
	if !found {
		t.Fatal("user missing from list")
	}
}

func TestAPITokenHasNoMoreThanItsUsersRole(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	id := createRole(t, owner, "Auditor", "audit.view")
	user, _ := h.userWithRole(id)
	token, _ := h.newToken(user, "write")

	expect(t, h.bearer(token, "GET", "/api/v1/audit", nil), 200, "")
	expect(t, h.bearer(token, "GET", "/api/v1/webhooks", nil), 403, "forbidden")
	if got := permissionsOf(t, h.bearer(token, "GET", "/api/v1/me", nil)); !slices.Equal(got, []string{"audit.view"}) {
		t.Fatalf("token permissions: %v", got)
	}

	// The role is read on every request, so narrowing it applies to tokens that already exist.
	expect(t, owner.do("PATCH", "/api/v1/roles/"+id, map[string]any{"permissions": []string{"reports.view"}}), 200, "")
	expect(t, h.bearer(token, "GET", "/api/v1/audit", nil), 403, "forbidden")
}
