package policy

import (
	"slices"

	"echoo/internal/db/dbq"
)

// Permission is one right a role can grant. The list is closed: custom roles are validated
// against it, and the frontend mirrors it in web/src/lib/permissions.ts.
type Permission string

const (
	ConversationsRead   Permission = "conversations.read"
	ConversationsWrite  Permission = "conversations.write"
	ConversationsAssign Permission = "conversations.assign"
	ConversationsDelete Permission = "conversations.delete"
	ContactsRead        Permission = "contacts.read"
	ContactsWrite       Permission = "contacts.write"
	ContactsExport      Permission = "contacts.export"
	ContactsImport      Permission = "contacts.import"
	ContactsErase       Permission = "contacts.erase"
	ContactsModerate    Permission = "contacts.moderate"
	ReportsView         Permission = "reports.view"
	ReportsViewAll      Permission = "reports.view_all_agents"
	KBWrite             Permission = "kb.write"
	KBManage            Permission = "kb.manage"
	LabelsManage        Permission = "labels.manage"
	TemplatesManage     Permission = "templates.manage_shared"
	AutomationManage    Permission = "automation.manage"
	SLAManage           Permission = "sla.manage"
	MailboxesManage     Permission = "mailboxes.manage"
	UsersManage         Permission = "users.manage"
	TeamsManage         Permission = "teams.manage"
	SettingsManage      Permission = "settings.manage"
	AuditView           Permission = "audit.view"
	APITokensManageAll  Permission = "api_tokens.manage_all" //nolint:gosec // a permission name, not a credential
	WebhooksManage      Permission = "webhooks.manage"
	CampaignsManage     Permission = "campaigns.manage"
)

// all is the canonical order, also used to sort effective permission lists.
var all = []Permission{
	ConversationsRead, ConversationsWrite, ConversationsAssign, ConversationsDelete,
	ContactsRead, ContactsWrite, ContactsExport, ContactsImport, ContactsErase, ContactsModerate,
	ReportsView, ReportsViewAll,
	KBWrite, KBManage,
	LabelsManage, TemplatesManage, AutomationManage, SLAManage,
	MailboxesManage, UsersManage, TeamsManage, SettingsManage, AuditView, APITokensManageAll, WebhooksManage,
	CampaignsManage,
}

// requires lists the permission each one is useless without. A role that breaks this is
// refused, so what the editor shows is what the role can do.
var requires = map[Permission]Permission{
	ConversationsWrite:  ConversationsRead,
	ConversationsAssign: ConversationsWrite,
	ConversationsDelete: ConversationsWrite,
	ContactsWrite:       ContactsRead,
	ContactsExport:      ContactsRead,
	ContactsImport:      ContactsRead,
	ContactsErase:       ContactsRead,
	ContactsModerate:    ContactsWrite,
	ReportsViewAll:      ReportsView,
	KBManage:            KBWrite,
	CampaignsManage:     ContactsRead,
}

// privileged permissions are workspace-wide and can be turned into wider access: changing who
// may do what (users.manage, settings.manage), placing oneself in a team or mailbox
// (teams.manage, mailboxes.manage), or reaching data outside one's own mailboxes
// (contacts.erase, contacts.import, webhooks.manage, automation.manage). Roles that grant them,
// and the users holding such roles, are managed by the owner only.
var privileged = []Permission{
	UsersManage, SettingsManage, TeamsManage, MailboxesManage,
	ContactsErase, ContactsImport, WebhooksManage, AutomationManage,
}

// Permissions returns every permission in canonical order.
func Permissions() []Permission { return slices.Clone(all) }

func ValidPermission(p string) bool { return slices.Contains(all, Permission(p)) }

// MissingRequirement returns the first permission in set that lacks what it requires, or "".
func MissingRequirement(set []string) (permission, needs Permission) {
	for _, p := range all {
		if req, ok := requires[p]; ok && slices.Contains(set, string(p)) && !slices.Contains(set, string(req)) {
			return p, req
		}
	}
	return "", ""
}

// Privileged reports whether set grants any permission that controls other people's access.
func Privileged(set []string) bool {
	return slices.ContainsFunc(privileged, func(p Permission) bool { return slices.Contains(set, string(p)) })
}

// Presets are the effective rights of the built-in roles. Owner and admin hold everything;
// the two others hold what they could always do.
func rolePermissions(role string) []Permission {
	switch role {
	case RoleOwner, RoleAdmin:
		return all
	case RoleAgent:
		return []Permission{
			ConversationsRead, ConversationsWrite, ConversationsAssign, ConversationsDelete,
			ContactsRead, ContactsWrite, ContactsExport, ReportsView, KBWrite,
		}
	case RoleReadonly:
		return []Permission{ConversationsRead, ContactsRead, ContactsExport, ReportsView}
	}
	return nil
}

// RolePermissions lists the permissions of a built-in role in canonical order.
func RolePermissions(role string) []string { return toStrings(rolePermissions(role)) }

// Has reports whether u holds p. Which mailboxes a permission applies to is decided
// separately, by MailboxScope.
func Has(u dbq.User, p Permission) bool {
	switch u.Role {
	case RoleOwner:
		return true
	case RoleCustom:
		return slices.Contains(u.Permissions, string(p))
	}
	return slices.Contains(rolePermissions(u.Role), p)
}

// Effective lists what u may do, in canonical order.
func Effective(u dbq.User) []string {
	if u.Role == RoleCustom {
		return slices.DeleteFunc(toStrings(all), func(p string) bool { return !slices.Contains(u.Permissions, p) })
	}
	return RolePermissions(u.Role)
}

func toStrings(ps []Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

func subset(set, of []string) bool {
	return !slices.ContainsFunc(set, func(p string) bool { return !slices.Contains(of, p) })
}
