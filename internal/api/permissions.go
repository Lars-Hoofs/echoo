package api

import (
	"context"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

// adminRoutes lists, per permission, the workspace-management routes it opens. A route that is
// registered in the permission-guarded group but missing here is refused for everybody, so a
// forgotten entry fails closed. authz_test.go checks that both lists agree.
var adminRoutes = map[policy.Permission][]string{
	policy.LabelsManage: {"POST /api/v1/labels", "PATCH /api/v1/labels/{id}", "DELETE /api/v1/labels/{id}"},
	policy.UsersManage: {
		"POST /api/v1/users", "PATCH /api/v1/users/{id}", "POST /api/v1/users/{id}/reset-password",
		"POST /api/v1/users/{id}/reset-mfa", "POST /api/v1/invitations", "POST /api/v1/users/{id}/invitation/resend",
		"DELETE /api/v1/users/{id}/invitation",
		"GET /api/v1/roles", "POST /api/v1/roles", "PATCH /api/v1/roles/{id}", "DELETE /api/v1/roles/{id}",
	},
	policy.TeamsManage: {
		"POST /api/v1/teams", "PATCH /api/v1/teams/{id}", "DELETE /api/v1/teams/{id}",
		"GET /api/v1/teams/{id}/members", "PUT /api/v1/teams/{id}/members",
	},
	policy.MailboxesManage: {
		"POST /api/v1/mailboxes", "POST /api/v1/mailboxes/test", "GET /api/v1/mailboxes/{id}", "PATCH /api/v1/mailboxes/{id}",
		"POST /api/v1/mailboxes/{id}/disable", "POST /api/v1/mailboxes/{id}/enable",
		"GET /api/v1/mailboxes/{id}/access", "PUT /api/v1/mailboxes/{id}/access",
		"GET /api/v1/mailboxes/{id}/signature", "PUT /api/v1/mailboxes/{id}/signature",
		"GET /api/v1/mailboxes/{id}/csat", "PUT /api/v1/mailboxes/{id}/csat",
		"GET /api/v1/mailboxes/oauth/providers", "GET /api/v1/mailboxes/oauth/{provider}/start",
	},
	policy.AuditView: {"GET /api/v1/audit", "GET /api/v1/audit/export"},
	policy.SettingsManage: {
		"GET /api/v1/settings/security", "PUT /api/v1/settings/security", "GET /api/v1/settings/email",
		"PUT /api/v1/settings/email", "GET /api/v1/settings/reports",
		"PUT /api/v1/settings/reports", "GET /api/v1/jobs", "POST /api/v1/jobs/{id}/retry",
		"GET /api/v1/raw-messages", "POST /api/v1/raw-messages/{id}/retry",
		"POST /api/v1/custom-attributes", "PATCH /api/v1/custom-attributes/{id}", "DELETE /api/v1/custom-attributes/{id}",
		"GET /api/v1/settings/retention", "PUT /api/v1/settings/retention", "POST /api/v1/settings/retention/preview",
	},
	policy.APITokensManageAll: {"GET /api/v1/tokens", "DELETE /api/v1/tokens/{id}"},
	policy.WebhooksManage: {
		"GET /api/v1/webhooks", "POST /api/v1/webhooks", "PATCH /api/v1/webhooks/{id}", "DELETE /api/v1/webhooks/{id}",
		"POST /api/v1/webhooks/{id}/test", "GET /api/v1/webhooks/{id}/deliveries",
		"POST /api/v1/webhooks/{id}/deliveries/{deliveryId}/resend",
	},
	policy.ContactsErase: {
		"POST /api/v1/contacts/{id}/data-export", "POST /api/v1/contacts/{id}/erase",
		"GET /api/v1/data-exports/{id}", "GET /api/v1/data-exports/{id}/download",
	},
	policy.ContactsImport: {
		"POST /api/v1/contact-imports", "GET /api/v1/contact-imports/{id}", "GET /api/v1/contact-imports/{id}/errors",
	},
	policy.AutomationManage: {
		"GET /api/v1/rules", "POST /api/v1/rules", "PUT /api/v1/rules/order", "PATCH /api/v1/rules/{id}",
		"DELETE /api/v1/rules/{id}", "GET /api/v1/rules/{id}/runs",
		"GET /api/v1/assignment", "PUT /api/v1/mailboxes/{id}/automation", "PUT /api/v1/users/{id}/capacity",
		"GET /api/v1/settings/automation", "PUT /api/v1/settings/automation",
	},
	policy.SLAManage: {
		"POST /api/v1/sla-policies", "PATCH /api/v1/sla-policies/{id}", "DELETE /api/v1/sla-policies/{id}",
		"POST /api/v1/business-hours", "PATCH /api/v1/business-hours/{id}", "DELETE /api/v1/business-hours/{id}",
	},
	policy.KBManage: {
		"PUT /api/v1/kb/portal", "POST /api/v1/kb/categories", "PATCH /api/v1/kb/categories/{id}",
		"DELETE /api/v1/kb/categories/{id}",
	},
	policy.CampaignsManage: {
		"GET /api/v1/campaigns", "POST /api/v1/campaigns", "POST /api/v1/campaigns/preview",
		"GET /api/v1/campaigns/{id}", "PATCH /api/v1/campaigns/{id}", "DELETE /api/v1/campaigns/{id}",
		"POST /api/v1/campaigns/{id}/test", "POST /api/v1/campaigns/{id}/start", "POST /api/v1/campaigns/{id}/pause",
		"POST /api/v1/campaigns/{id}/resume", "POST /api/v1/campaigns/{id}/cancel",
		"GET /api/v1/campaigns/{id}/recipients", "GET /api/v1/campaigns/{id}/report",
		"POST /api/v1/contacts/{id}/resubscribe", "POST /api/v1/contacts/{id}/reactivate-address",
	},
}

// directoryRoutes are the lists of users, teams and mailboxes that pickers in several settings
// pages need. Any one of the listed permissions is enough.
var directoryRoutes = map[string][]policy.Permission{
	"GET /api/v1/users": {
		policy.UsersManage, policy.TeamsManage, policy.AutomationManage, policy.AuditView, policy.ReportsViewAll,
	},
	"GET /api/v1/teams": {
		policy.TeamsManage, policy.UsersManage, policy.MailboxesManage, policy.AutomationManage,
		policy.TemplatesManage, policy.ReportsViewAll,
	},
	"GET /api/v1/mailboxes": {
		policy.MailboxesManage, policy.AutomationManage, policy.SLAManage, policy.TemplatesManage,
	},
	// Inviting users depends on whether system mail works.
	"GET /api/v1/settings/system-mail": {policy.SettingsManage, policy.UsersManage},
	// The assignment page picks a default SLA policy and business hours per mailbox.
	"GET /api/v1/sla-policies":   {policy.SLAManage, policy.AutomationManage},
	"GET /api/v1/business-hours": {policy.SLAManage, policy.AutomationManage},
}

// routeNeeds maps "METHOD /path" to the permissions of which one must be held.
var routeNeeds = func() map[string][]policy.Permission {
	out := map[string][]policy.Permission{}
	for p, keys := range adminRoutes {
		for _, key := range keys {
			if _, dup := out[key]; dup {
				panic("route listed twice: " + key)
			}
			out[key] = []policy.Permission{p}
		}
	}
	for key, needs := range directoryRoutes {
		if _, dup := out[key]; dup {
			panic("route listed twice: " + key)
		}
		out[key] = needs
	}
	return out
}()

// requirePermission guards the management routes: the request needs one of the permissions
// routeNeeds lists for the matched route. Unknown routes are refused.
func requirePermission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		needs := routeNeeds[r.Method+" "+chi.RouteContext(r.Context()).RoutePattern()]
		user := sessionFrom(r.Context()).User
		if !slices.ContainsFunc(needs, func(p policy.Permission) bool { return policy.Has(user, p) }) {
			writeError(w, r, errForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireOwner is for the few settings that only the owner may touch.
func requireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r.Context()).User.Role != policy.RoleOwner {
			writeError(w, r, errForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// keepScope runs change and refuses it with errForbidden when it gives actor access to a mailbox,
// or write access to one, that they did not have. Only actors who do not see everything are
// checked. Run it inside the transaction that makes the change so refusing rolls it back; this
// backs up the privileged-permission rule for roles that predate it.
func keepScope(ctx context.Context, q *dbq.Queries, actor dbq.User, change func() error) error {
	if policy.SeesAll(actor) {
		return change()
	}
	// The callers lock the team or mailbox row, which does not stop two requests of the same
	// actor on different rows from passing the check separately and widening together.
	if _, err := q.GetUserForUpdate(ctx, actor.ID); err != nil {
		return err
	}
	before, err := policy.MailboxScope(ctx, q, actor)
	if err != nil {
		return err
	}
	if err := change(); err != nil {
		return err
	}
	after, err := policy.MailboxScope(ctx, q, actor)
	if err != nil {
		return err
	}
	if after.Widens(before) {
		return errForbidden
	}
	return nil
}

// requireSeesAll is for actions that reach data of every mailbox regardless of the team
// grants: GDPR exports and erasure, and anything else that is not filtered by scope.
func requireSeesAll(u dbq.User) error {
	if !policy.SeesAll(u) {
		return errForbidden
	}
	return nil
}

// requireWritableMailbox guards workspace configuration that acts on a mailbox: whoever sees
// everything may configure any mailbox, others only mailboxes they can write to. An invalid
// mailbox means workspace-wide and needs SeesAll.
func requireWritableMailbox(ctx context.Context, q *dbq.Queries, actor dbq.User, mailbox pgtype.UUID) error {
	if policy.SeesAll(actor) {
		return nil
	}
	if !mailbox.Valid {
		return errForbidden
	}
	scope, err := policy.MailboxScope(ctx, q, actor)
	if err != nil {
		return err
	}
	if !slices.Contains(scope.Write, mailbox) {
		return errForbidden
	}
	return nil
}
