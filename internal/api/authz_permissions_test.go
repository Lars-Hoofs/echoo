package api

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

var builtInRoles = []string{policy.RoleOwner, policy.RoleAdmin, policy.RoleAgent, policy.RoleReadonly}

func roleUser(role string) dbq.User {
	return dbq.User{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, Role: role}
}

func hasAny(u dbq.User, needs []policy.Permission) bool {
	for _, p := range needs {
		if policy.Has(u, p) {
			return true
		}
	}
	return false
}

// TestPermissionsKeepBuiltInAccessOnManagementRoutes proves the mapping from routes to
// permissions for the routes that were behind requireAdmin: each built-in role passes exactly
// when it passed before, which was owner and admin only.
func TestPermissionsKeepBuiltInAccessOnManagementRoutes(t *testing.T) {
	for key, level := range routes {
		needs, mapped := routeNeeds[key]
		if level != admin {
			if mapped {
				t.Errorf("%s is not an admin route but has a permission mapping", key)
			}
			continue
		}
		if !mapped {
			t.Errorf("admin route %s has no permission mapping", key)
			continue
		}
		for _, role := range builtInRoles {
			want := role == policy.RoleOwner || role == policy.RoleAdmin
			if got := hasAny(roleUser(role), needs); got != want {
				t.Errorf("%s as %s: got %v, want %v (needs %v)", key, role, got, want, needs)
			}
		}
	}
	for key := range routeNeeds {
		if _, ok := routes[key]; !ok {
			t.Errorf("permission mapping for %s, which is not a route", key)
		}
	}
}

func nonReadonly(role string) bool { return role != policy.RoleReadonly }
func signedIn(string) bool         { return true }

type gate struct {
	perm policy.Permission
	// before is who passed the handler's own check before permissions existed.
	before func(role string) bool
}

// gates are the routes every signed-in user reaches but whose handler holds the caller to a
// permission. Together with routeNeeds this covers each route that checked the role.
var gates = map[string]gate{
	"PATCH /api/v1/conversations/{id}":                             {policy.ConversationsWrite, nonReadonly},
	"PUT /api/v1/conversations/{id}/labels":                        {policy.ConversationsWrite, nonReadonly},
	"POST /api/v1/conversations/bulk":                              {policy.ConversationsWrite, nonReadonly},
	"PATCH /api/v1/conversations/{id}/attributes":                  {policy.ConversationsWrite, nonReadonly},
	"POST /api/v1/uploads":                                         {policy.ConversationsWrite, nonReadonly},
	"PUT /api/v1/me/signatures":                                    {policy.ConversationsWrite, nonReadonly},
	"POST /api/v1/templates":                                       {policy.ConversationsWrite, nonReadonly},
	"POST /api/v1/macros":                                          {policy.ConversationsWrite, nonReadonly},
	"POST /api/v1/macros/{id}/run":                                 {policy.ConversationsWrite, nonReadonly},
	"POST /api/v1/contacts":                                        {policy.ContactsWrite, nonReadonly},
	"PATCH /api/v1/contacts/{id}":                                  {policy.ContactsWrite, nonReadonly},
	"POST /api/v1/contacts/{id}/merge":                             {policy.ContactsWrite, nonReadonly},
	"POST /api/v1/contacts/{id}/notes":                             {policy.ContactsWrite, nonReadonly},
	"POST /api/v1/organizations":                                   {policy.ContactsWrite, nonReadonly},
	"PATCH /api/v1/organizations/{id}":                             {policy.ContactsWrite, nonReadonly},
	"POST /api/v1/organizations/{id}/notes":                        {policy.ContactsWrite, nonReadonly},
	"POST /api/v1/contact-segments":                                {policy.ContactsWrite, nonReadonly},
	"GET /api/v1/contacts":                                         {policy.ContactsRead, signedIn},
	"GET /api/v1/contacts/{id}":                                    {policy.ContactsRead, signedIn},
	"GET /api/v1/organizations":                                    {policy.ContactsRead, signedIn},
	"GET /api/v1/contacts/export":                                  {policy.ContactsExport, signedIn},
	"GET /api/v1/reports/overview":                                 {policy.ReportsView, signedIn},
	"GET /api/v1/reports/agents":                                   {policy.ReportsView, signedIn},
	"POST /api/v1/kb/articles":                                     {policy.KBWrite, nonReadonly},
	"PATCH /api/v1/kb/articles/{id}":                               {policy.KBWrite, nonReadonly},
	"DELETE /api/v1/kb/articles/{id}":                              {policy.KBWrite, nonReadonly},
	"POST /api/v1/kb/articles/{id}/status":                         {policy.KBWrite, nonReadonly},
	"POST /api/v1/kb/images":                                       {policy.KBWrite, nonReadonly},
	"POST /api/v1/kb/articles/{id}/revisions/{revisionId}/restore": {policy.KBWrite, nonReadonly},
}

func TestPermissionsKeepBuiltInAccessOnGatedRoutes(t *testing.T) {
	for key, g := range gates {
		if level, ok := routes[key]; !ok || level != account {
			t.Errorf("%s is not an account-level route", key)
			continue
		}
		for _, role := range builtInRoles {
			if got, want := policy.Has(roleUser(role), g.perm), g.before(role); got != want {
				t.Errorf("%s as %s: got %v, want %v", key, role, got, want)
			}
		}
	}
}

// TestGatedRoutesRefuseReadonlyOverHTTP checks the handlers really use the permission: a
// readonly user, who lacks every write permission, is refused before anything else happens.
func TestGatedRoutesRefuseReadonlyOverHTTP(t *testing.T) {
	h := newHarness(t)
	readonly, _ := h.loggedIn("readonly")
	for key, g := range gates {
		if g.before(policy.RoleReadonly) {
			continue
		}
		method, route, _ := strings.Cut(key, " ")
		path := strings.ReplaceAll(concretePath(route), "{mid}", "0199a000-0000-7000-8000-000000000000")
		path = strings.ReplaceAll(strings.ReplaceAll(path, "{noteId}", "0199a000-0000-7000-8000-000000000000"), "{revisionId}", "0199a000-0000-7000-8000-000000000000")
		var body any
		if method != "GET" && method != "DELETE" {
			body = map[string]any{}
		}
		if key == "POST /api/v1/templates" {
			// The scope is validated first; the permission is checked right after.
			body = map[string]any{"scope": "personal"}
		}
		if r := readonly.do(method, path, body); r.status != 403 {
			t.Errorf("readonly %s: got %d %s, want 403", key, r.status, r.raw)
		}
	}
}
