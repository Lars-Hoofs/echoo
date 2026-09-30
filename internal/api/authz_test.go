package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

type access int

const (
	public  access = iota // no session
	pending               // pending 2FA session is enough
	account               // any fully signed-in user, including readonly
	admin                 // needs a permission (see permissions.go): owner and admin hold all of them
	owner                 // owner only
)

// routes classifies every API route. TestEveryRouteIsClassified fails when a route is added
// without an entry here, so authorization is decided consciously for each one.
var routes = map[string]access{
	"GET /healthz":                                                public,
	"GET /readyz":                                                 public,
	"POST /api/v1/auth/login":                                     public,
	"GET /api/v1/auth/sso":                                        public,
	"GET /auth/sso/start":                                         public,
	"GET /auth/sso/callback":                                      public,
	"GET /api/v1/roles":                                           admin,
	"POST /api/v1/roles":                                          admin,
	"PATCH /api/v1/roles/{id}":                                    admin,
	"DELETE /api/v1/roles/{id}":                                   admin,
	"GET /api/v1/settings/sso":                                    owner,
	"PUT /api/v1/settings/sso":                                    owner,
	"GET /api/v1/settings/retention":                              admin,
	"PUT /api/v1/settings/retention":                              admin,
	"POST /api/v1/settings/retention/preview":                     admin,
	"GET /api/v1/admin/keys":                                      owner,
	"POST /api/v1/auth/mfa":                                       pending,
	"POST /api/v1/auth/logout":                                    pending,
	"GET /api/v1/me":                                              account,
	"PATCH /api/v1/me":                                            account,
	"POST /api/v1/me/password":                                    account,
	"POST /api/v1/me/totp/setup":                                  account,
	"POST /api/v1/me/totp/enable":                                 account,
	"POST /api/v1/me/totp/disable":                                account,
	"POST /api/v1/me/recovery-codes":                              account,
	"GET /api/v1/me/sessions":                                     account,
	"DELETE /api/v1/me/sessions/{id}":                             account,
	"POST /api/v1/me/sessions/revoke-others":                      account,
	"GET /api/v1/inbox/summary":                                   account,
	"GET /api/v1/conversations":                                   account,
	"GET /api/v1/conversations/{id}":                              account,
	"POST /api/v1/conversations/{id}/messages/{mid}/allow-images": account,
	"GET /api/v1/attachments/{id}/download":                       account,
	"GET /render/messages/{id}":                                   account,
	// The two below authenticate by signature, not session: see render_sign.go.
	"GET /render/attachments/{id}":                                 public,
	"GET /render/proxy":                                            public,
	"PATCH /api/v1/conversations/{id}":                             account,
	"PUT /api/v1/conversations/{id}/labels":                        account,
	"POST /api/v1/conversations/bulk":                              account,
	"GET /api/v1/trash":                                            account,
	"GET /api/v1/trash/{id}":                                       account,
	"POST /api/v1/trash":                                           account,
	"POST /api/v1/trash/restore":                                   account,
	"POST /api/v1/trash/purge":                                     account,
	"POST /api/v1/trash/empty":                                     account,
	"GET /api/v1/blocked-senders":                                  account,
	"POST /api/v1/blocked-senders":                                 account,
	"DELETE /api/v1/blocked-senders/{id}":                          account,
	"GET /api/v1/conversations/{id}/events":                        account,
	"GET /api/v1/assignees":                                        account,
	"GET /api/v1/labels":                                           account,
	"GET /api/v1/search":                                           account,
	"GET /api/v1/saved-views":                                      account,
	"POST /api/v1/saved-views":                                     account,
	"PATCH /api/v1/saved-views/{id}":                               account,
	"DELETE /api/v1/saved-views/{id}":                              account,
	"POST /api/v1/labels":                                          admin,
	"PATCH /api/v1/labels/{id}":                                    admin,
	"DELETE /api/v1/labels/{id}":                                   admin,
	"GET /api/v1/events":                                           account,
	"POST /api/v1/conversations/{id}/presence":                     account,
	"GET /api/v1/conversations/{id}/presence":                      account,
	"GET /api/v1/agents/online":                                    account,
	"GET /api/v1/users":                                            admin,
	"POST /api/v1/users":                                           admin,
	"PATCH /api/v1/users/{id}":                                     admin,
	"POST /api/v1/users/{id}/reset-password":                       admin,
	"POST /api/v1/users/{id}/reset-mfa":                            admin,
	"GET /api/v1/teams":                                            admin,
	"POST /api/v1/teams":                                           admin,
	"PATCH /api/v1/teams/{id}":                                     admin,
	"DELETE /api/v1/teams/{id}":                                    admin,
	"GET /api/v1/teams/{id}/members":                               admin,
	"PUT /api/v1/teams/{id}/members":                               admin,
	"GET /api/v1/mailboxes":                                        admin,
	"POST /api/v1/mailboxes":                                       admin,
	"POST /api/v1/mailboxes/test":                                  admin,
	"GET /api/v1/mailboxes/{id}":                                   admin,
	"PATCH /api/v1/mailboxes/{id}":                                 admin,
	"POST /api/v1/mailboxes/{id}/disable":                          admin,
	"POST /api/v1/mailboxes/{id}/enable":                           admin,
	"GET /api/v1/mailboxes/{id}/access":                            admin,
	"PUT /api/v1/mailboxes/{id}/access":                            admin,
	"GET /api/v1/audit":                                            admin,
	"GET /api/v1/settings/security":                                admin,
	"PUT /api/v1/settings/security":                                admin,
	"POST /api/v1/conversations":                                   account,
	"GET /api/v1/conversations/{id}/reply-defaults":                account,
	"GET /api/v1/conversations/{id}/mentionable":                   account,
	"POST /api/v1/conversations/{id}/replies":                      account,
	"POST /api/v1/conversations/{id}/replies/{messageId}/cancel":   account,
	"POST /api/v1/conversations/{id}/forward":                      account,
	"POST /api/v1/conversations/{id}/notes":                        account,
	"POST /api/v1/conversations/{id}/read":                         account,
	"POST /api/v1/conversations/{id}/unread":                       account,
	"GET /api/v1/conversations/{id}/draft":                         account,
	"PUT /api/v1/conversations/{id}/draft":                         account,
	"DELETE /api/v1/conversations/{id}/draft":                      account,
	"POST /api/v1/uploads":                                         account,
	"DELETE /api/v1/uploads/{id}":                                  account,
	"GET /api/v1/notifications":                                    account,
	"POST /api/v1/notifications/read":                              account,
	"GET /api/v1/templates":                                        account,
	"POST /api/v1/templates":                                       account,
	"PATCH /api/v1/templates/{id}":                                 account,
	"DELETE /api/v1/templates/{id}":                                account,
	"GET /api/v1/templates/{id}/render":                            account,
	"GET /api/v1/me/signatures":                                    account,
	"PUT /api/v1/me/signatures":                                    account,
	"GET /api/v1/signatures/effective":                             account,
	"GET /api/v1/mailboxes/{id}/signature":                         admin,
	"PUT /api/v1/mailboxes/{id}/signature":                         admin,
	"GET /api/v1/settings/email":                                   admin,
	"PUT /api/v1/settings/email":                                   admin,
	"GET /api/v1/openapi.yaml":                                     account,
	"GET /api/v1/me/tokens":                                        account,
	"POST /api/v1/me/tokens":                                       account,
	"DELETE /api/v1/me/tokens/{id}":                                account,
	"GET /api/v1/audit/export":                                     admin,
	"GET /api/v1/tokens":                                           admin,
	"DELETE /api/v1/tokens/{id}":                                   admin,
	"GET /api/v1/webhooks":                                         admin,
	"POST /api/v1/webhooks":                                        admin,
	"PATCH /api/v1/webhooks/{id}":                                  admin,
	"DELETE /api/v1/webhooks/{id}":                                 admin,
	"POST /api/v1/webhooks/{id}/test":                              admin,
	"GET /api/v1/webhooks/{id}/deliveries":                         admin,
	"POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/resend":    admin,
	"GET /api/v1/jobs":                                             admin,
	"POST /api/v1/jobs/{id}/retry":                                 admin,
	"GET /api/v1/raw-messages":                                     admin,
	"POST /api/v1/raw-messages/{id}/retry":                         admin,
	"GET /api/v1/contacts":                                         account,
	"POST /api/v1/contacts":                                        account,
	"GET /api/v1/contacts/export":                                  account,
	"GET /api/v1/contacts/{id}":                                    account,
	"PATCH /api/v1/contacts/{id}":                                  account,
	"POST /api/v1/contacts/{id}/merge":                             account,
	"GET /api/v1/contacts/{id}/conversations":                      account,
	"GET /api/v1/contacts/{id}/timeline":                           account,
	"GET /api/v1/contacts/{id}/notes":                              account,
	"POST /api/v1/contacts/{id}/notes":                             account,
	"PATCH /api/v1/contacts/{id}/notes/{noteId}":                   account,
	"DELETE /api/v1/contacts/{id}/notes/{noteId}":                  account,
	"POST /api/v1/contacts/{id}/data-export":                       admin,
	"POST /api/v1/contacts/{id}/erase":                             admin,
	"GET /api/v1/data-exports/{id}":                                admin,
	"GET /api/v1/data-exports/{id}/download":                       admin,
	"GET /api/v1/organizations":                                    account,
	"POST /api/v1/organizations":                                   account,
	"GET /api/v1/organizations/{id}":                               account,
	"PATCH /api/v1/organizations/{id}":                             account,
	"GET /api/v1/organizations/{id}/conversations":                 account,
	"GET /api/v1/organizations/{id}/notes":                         account,
	"POST /api/v1/organizations/{id}/notes":                        account,
	"PATCH /api/v1/organizations/{id}/notes/{noteId}":              account,
	"DELETE /api/v1/organizations/{id}/notes/{noteId}":             account,
	"GET /api/v1/custom-attributes":                                account,
	"POST /api/v1/custom-attributes":                               admin,
	"PATCH /api/v1/custom-attributes/{id}":                         admin,
	"DELETE /api/v1/custom-attributes/{id}":                        admin,
	"GET /api/v1/conversations/{id}/attributes":                    account,
	"PATCH /api/v1/conversations/{id}/attributes":                  account,
	"GET /api/v1/contact-segments":                                 account,
	"POST /api/v1/contact-segments":                                account,
	"PATCH /api/v1/contact-segments/{id}":                          account,
	"DELETE /api/v1/contact-segments/{id}":                         account,
	"POST /api/v1/contact-imports":                                 admin,
	"GET /api/v1/contact-imports/{id}":                             admin,
	"GET /api/v1/contact-imports/{id}/errors":                      admin,
	"GET /api/v1/mailboxes/oauth/providers":                        admin,
	"GET /api/v1/mailboxes/oauth/{provider}/start":                 admin,
	"GET /oauth/callback/{provider}":                               account,
	"GET /api/v1/settings/system-mail":                             admin,
	"POST /api/v1/invitations":                                     admin,
	"POST /api/v1/users/{id}/invitation/resend":                    admin,
	"DELETE /api/v1/users/{id}/invitation":                         admin,
	"GET /api/v1/invitations/{token}":                              public,
	"POST /api/v1/invitations/{token}/accept":                      public,
	"POST /api/v1/auth/password-reset":                             public,
	"GET /api/v1/auth/password-reset/{token}":                      public,
	"POST /api/v1/auth/password-reset/{token}":                     public,
	"GET /api/v1/me/notification-settings":                         account,
	"PUT /api/v1/me/notification-settings":                         account,
	"PUT /api/v1/me/availability":                                  account,
	"GET /api/v1/macros":                                           account,
	"POST /api/v1/macros":                                          account,
	"PATCH /api/v1/macros/{id}":                                    account,
	"DELETE /api/v1/macros/{id}":                                   account,
	"POST /api/v1/macros/{id}/run":                                 account,
	"GET /api/v1/rules":                                            admin,
	"POST /api/v1/rules":                                           admin,
	"PUT /api/v1/rules/order":                                      admin,
	"PATCH /api/v1/rules/{id}":                                     admin,
	"DELETE /api/v1/rules/{id}":                                    admin,
	"GET /api/v1/rules/{id}/runs":                                  admin,
	"GET /api/v1/business-hours":                                   admin,
	"POST /api/v1/business-hours":                                  admin,
	"PATCH /api/v1/business-hours/{id}":                            admin,
	"DELETE /api/v1/business-hours/{id}":                           admin,
	"GET /api/v1/sla-policies":                                     admin,
	"POST /api/v1/sla-policies":                                    admin,
	"PATCH /api/v1/sla-policies/{id}":                              admin,
	"DELETE /api/v1/sla-policies/{id}":                             admin,
	"GET /api/v1/assignment":                                       admin,
	"PUT /api/v1/mailboxes/{id}/automation":                        admin,
	"PUT /api/v1/users/{id}/capacity":                              admin,
	"GET /api/v1/settings/automation":                              admin,
	"PUT /api/v1/settings/automation":                              admin,
	"GET /api/v1/kb/portal":                                        account,
	"PUT /api/v1/kb/portal":                                        admin,
	"GET /api/v1/kb/categories":                                    account,
	"POST /api/v1/kb/categories":                                   admin,
	"PATCH /api/v1/kb/categories/{id}":                             admin,
	"DELETE /api/v1/kb/categories/{id}":                            admin,
	"GET /api/v1/kb/articles":                                      account,
	"POST /api/v1/kb/articles":                                     account,
	"GET /api/v1/kb/articles/{id}":                                 account,
	"PATCH /api/v1/kb/articles/{id}":                               account,
	"DELETE /api/v1/kb/articles/{id}":                              account,
	"POST /api/v1/kb/articles/{id}/status":                         account,
	"GET /api/v1/kb/articles/{id}/revisions":                       account,
	"POST /api/v1/kb/articles/{id}/revisions/{revisionId}/restore": account,
	"POST /api/v1/kb/images":                                       account,
	// The public help center: no session, see kb_public.go.
	"GET /hulp":                         public,
	"GET /hulp/":                        public,
	"GET /hulp/c/{slug}":                public,
	"GET /hulp/a/{slug}":                public,
	"POST /hulp/a/{slug}/feedback":      public,
	"GET /hulp/zoeken":                  public,
	"GET /hulp/sitemap.xml":             public,
	"GET /hulp/robots.txt":              public,
	"GET /hulp/static/{name}":           public,
	"GET /hulp/i/{id}":                  public,
	"GET /robots.txt":                   public,
	"GET /fonts/{name}":                 public,
	"GET /api/v1/reports/overview":      account,
	"GET /api/v1/reports/agents":        account,
	"GET /api/v1/reports/teams":         account,
	"GET /api/v1/reports/mailboxes":     account,
	"GET /api/v1/reports/labels":        account,
	"GET /api/v1/reports/csat":          account,
	"GET /api/v1/reports/live":          account,
	"GET /api/v1/reports/{kind}/export": account,
	"GET /api/v1/settings/reports":      admin,
	"PUT /api/v1/settings/reports":      admin,
	"GET /api/v1/mailboxes/{id}/csat":   admin,
	"PUT /api/v1/mailboxes/{id}/csat":   admin,
	// The survey pages authenticate by a signed token in the path, not by session.
	"GET /tevredenheid/{token}":                     public,
	"POST /tevredenheid/{token}":                    public,
	"GET /api/v1/campaigns":                         admin,
	"POST /api/v1/campaigns":                        admin,
	"POST /api/v1/campaigns/preview":                admin,
	"GET /api/v1/campaigns/{id}":                    admin,
	"PATCH /api/v1/campaigns/{id}":                  admin,
	"DELETE /api/v1/campaigns/{id}":                 admin,
	"POST /api/v1/campaigns/{id}/test":              admin,
	"POST /api/v1/campaigns/{id}/start":             admin,
	"POST /api/v1/campaigns/{id}/pause":             admin,
	"POST /api/v1/campaigns/{id}/resume":            admin,
	"POST /api/v1/campaigns/{id}/cancel":            admin,
	"GET /api/v1/campaigns/{id}/recipients":         admin,
	"GET /api/v1/campaigns/{id}/report":             admin,
	"POST /api/v1/contacts/{id}/resubscribe":        admin,
	"POST /api/v1/contacts/{id}/reactivate-address": admin,
	// The unsubscribe pages authenticate by a token in the path, not by session.
	"GET /afmelden/{token}":  public,
	"POST /afmelden/{token}": public,
}

func TestEveryRouteIsClassified(t *testing.T) {
	h := newHarness(t)
	mux := h.srv.Handler()
	seen := map[string]bool{}
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(strings.ReplaceAll(route, "/*/", "/"), "/*")
		key := method + " " + route
		seen[key] = true
		if _, ok := routes[key]; !ok {
			t.Errorf("route %s is not classified in the authorization table", key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range routes {
		if !seen[key] {
			t.Errorf("classified route %s does not exist", key)
		}
	}
}

func concretePath(route string) string {
	return strings.ReplaceAll(route, "{id}", "0199a000-0000-7000-8000-000000000000")
}

func TestRoutesRejectInsufficientAccess(t *testing.T) {
	h := newHarness(t)
	anon := h.client()
	agent, _ := h.loggedIn("agent")
	readonly, _ := h.loggedIn("readonly")
	adm, _ := h.loggedIn("admin")

	for key, level := range routes {
		method, route, _ := strings.Cut(key, " ")
		if !strings.HasPrefix(route, "/api/") {
			continue
		}
		path := concretePath(route)
		body := any(nil)
		if method != "GET" && method != "DELETE" {
			body = map[string]any{}
		}
		if level >= pending {
			if r := anon.do(method, path, body); r.status != 401 {
				t.Errorf("anonymous %s: got %d, want 401", key, r.status)
			}
		}
		if level >= admin {
			denied := map[string]*client{"agent": agent, "readonly": readonly}
			if level == owner {
				denied["admin"] = adm
			}
			for name, c := range denied {
				if r := c.do(method, path, body); r.status != 403 || r.errCode() != "forbidden" {
					t.Errorf("%s %s: got %d %s, want 403 forbidden", name, key, r.status, r.errCode())
				}
			}
		}
	}
}

func TestAdminCannotManageAdminsOrOwner(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.loggedIn("owner")
	adm, adminUser := h.loggedIn("admin")
	other, _ := h.user("admin", "other-admin@example.com")
	agent, _ := h.user("agent", "agent@example.com")
	ownerUser := mustUserByEmail(t, h, "owner")

	for _, target := range []string{other.ID.String(), ownerUser.ID.String(), adminUser.ID.String()} {
		expect(t, adm.do("PATCH", "/api/v1/users/"+target, map[string]string{"role": "agent"}), 403, "forbidden")
		expect(t, adm.do("POST", "/api/v1/users/"+target+"/reset-password", nil), 403, "forbidden")
		expect(t, adm.do("POST", "/api/v1/users/"+target+"/reset-mfa", nil), 403, "forbidden")
	}
	// Admins cannot create or promote to admin; nobody can grant ownership.
	expect(t, adm.do("POST", "/api/v1/users", map[string]string{"email": "x@example.com", "name": "X", "role": "admin"}), 403, "forbidden")
	expect(t, adm.do("PATCH", "/api/v1/users/"+agent.ID.String(), map[string]string{"role": "admin"}), 403, "forbidden")
	expect(t, owner.do("PATCH", "/api/v1/users/"+agent.ID.String(), map[string]string{"role": "owner"}), 403, "forbidden")

	// What is allowed does work.
	expect(t, adm.do("PATCH", "/api/v1/users/"+agent.ID.String(), map[string]string{"role": "readonly"}), 200, "")
	expect(t, owner.do("PATCH", "/api/v1/users/"+other.ID.String(), map[string]string{"role": "agent"}), 200, "")
}

func mustUserByEmail(t *testing.T, h *harness, role string) dbq.User {
	t.Helper()
	users, err := h.q.ListUsers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Role == role {
			return u
		}
	}
	t.Fatalf("no %s", role)
	return dbq.User{}
}

func dbqDeactivate(id pgtype.UUID) dbq.SetUserDeactivatedParams {
	return dbq.SetUserDeactivatedParams{ID: id, Deactivated: true}
}
