// Package policy is the single place that decides whether a user may perform an action.
package policy

import (
	"errors"

	"echoo/internal/db/dbq"
)

const (
	RoleOwner    = "owner"
	RoleAdmin    = "admin"
	RoleAgent    = "agent"
	RoleReadonly = "readonly"
	// RoleCustom marks a user whose rights come from a custom role.
	RoleCustom = "custom"
)

var ErrForbidden = errors.New("forbidden")

// ValidRole reports whether role is a built-in role. Custom roles are referenced by id.
func ValidRole(role string) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleAgent, RoleReadonly:
		return true
	}
	return false
}

func isAdmin(u dbq.User) bool { return u.Role == RoleOwner || u.Role == RoleAdmin }

// SeesAll is the role-level rule for seeing every mailbox, team view and contact instead of
// only what the user's teams were granted. Owner and admin only: custom roles never widen
// the visible data, only what may be done with it.
func SeesAll(u dbq.User) bool { return isAdmin(u) }

// CanGrant reports whether actor may put perms into a role or hand such a role to someone.
// Nobody grants what they do not hold, and roles that control access (users.manage,
// settings.manage) are the owner's to hand out.
func CanGrant(actor dbq.User, perms []string) bool {
	if !Has(actor, UsersManage) {
		return false
	}
	if actor.Role == RoleOwner {
		return true
	}
	return !Privileged(perms) && subset(perms, Effective(actor))
}

// CanAssignRole reports whether actor may give someone the given built-in role, on creation
// or change. Ownership is never assigned this way; it needs an explicit transfer, and only
// the owner hands out admin.
func CanAssignRole(actor dbq.User, role string) bool {
	if !ValidRole(role) || role == RoleOwner || !Has(actor, UsersManage) {
		return false
	}
	if actor.Role == RoleOwner {
		return true
	}
	return role != RoleAdmin && subset(RolePermissions(role), Effective(actor))
}

// CanManageUser covers role changes, deactivation, password and 2FA resets of another user.
// Nobody manages the owner or themselves through these actions (self-service uses the account
// endpoints). The owner manages everyone else; others manage only users whose rights they
// hold in full and who cannot change access themselves, so a reset never hands over an
// account that can do more than the actor.
func CanManageUser(actor, target dbq.User) bool {
	if actor.ID == target.ID || target.Role == RoleOwner || !Has(actor, UsersManage) {
		return false
	}
	if actor.Role == RoleOwner {
		return true
	}
	rights := Effective(target)
	return target.Role != RoleAdmin && !Privileged(rights) && subset(rights, Effective(actor))
}
