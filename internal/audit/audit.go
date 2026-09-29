// Package audit writes append-only records of security-relevant actions.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

const (
	LoginSucceeded           = "auth.login"
	LoginFailed              = "auth.login_failed"
	AccountLocked            = "auth.account_locked"
	MFAFailed                = "auth.mfa_failed"
	Logout                   = "auth.logout"
	LogoutOthers             = "auth.logout_others"
	SessionRevoked           = "auth.session_revoked"
	PasswordChanged          = "auth.password_changed"
	MFAEnabled               = "auth.mfa_enabled"
	MFADisabled              = "auth.mfa_disabled"
	RecoveryCodesRegenerated = "auth.recovery_codes_regenerated"
	UserCreated              = "user.created"
	UserRenamed              = "user.renamed"
	UserRoleChanged          = "user.role_changed"
	UserDeactivated          = "user.deactivated"
	UserReactivated          = "user.reactivated"
	UserPasswordReset        = "user.password_reset"
	UserMFAReset             = "user.mfa_reset"
	TeamCreated              = "team.created"
	TeamRenamed              = "team.renamed"
	TeamDeleted              = "team.deleted"
	TeamMembersChanged       = "team.members_changed"
	SecuritySettingsChanged  = "settings.security_changed"
	MailboxCreated           = "mailbox.created"
	MailboxUpdated           = "mailbox.updated"
	MailboxDisabled          = "mailbox.disabled"
	MailboxEnabled           = "mailbox.enabled"
	MailboxSecretChanged     = "mailbox.secret_changed"
	MailboxAccessChanged     = "mailbox.access_changed"
	MailboxStoredSecretTest  = "mailbox.stored_secret_tested"
	MailboxInternalHost      = "mailbox.internal_host_changed"
	TemplateCreated          = "template.created"
	TemplateUpdated          = "template.updated"
	TemplateDeleted          = "template.deleted"
	MailboxSignatureChanged  = "mailbox.signature_changed"
	EmailSettingsChanged     = "settings.email_changed"
	LabelCreated             = "label.created"
	LabelUpdated             = "label.updated"
	LabelDeleted             = "label.deleted"
	KBPortalUpdated          = "kb.portal_updated"
	KBCategoryCreated        = "kb.category_created"
	KBCategoryUpdated        = "kb.category_updated"
	KBCategoryDeleted        = "kb.category_deleted"
	KBArticlePublished       = "kb.article_published"
	KBArticleUnpublished     = "kb.article_unpublished"
	KBArticleArchived        = "kb.article_archived"
	KBArticleDeleted         = "kb.article_deleted"
	RoleCreated              = "role.created"
	RoleUpdated              = "role.updated"
	RoleDeleted              = "role.deleted"
	SSOSettingsChanged       = "settings.sso_changed"
)

type Entry struct {
	Actor      pgtype.UUID
	IP         *netip.Addr
	Action     string
	TargetType string
	TargetID   string
	// Metadata must never contain secrets or message content.
	Metadata map[string]any
}

func Write(ctx context.Context, q *dbq.Queries, e Entry) error {
	meta := []byte("{}")
	if len(e.Metadata) > 0 {
		var err error
		if meta, err = json.Marshal(e.Metadata); err != nil {
			return fmt.Errorf("audit metadata: %w", err)
		}
	}
	err := q.InsertAudit(ctx, dbq.InsertAuditParams{
		ActorUserID: e.Actor,
		ActorIp:     e.IP,
		Action:      e.Action,
		TargetType:  e.TargetType,
		TargetID:    e.TargetID,
		Metadata:    meta,
	})
	if err != nil {
		return fmt.Errorf("write audit %s: %w", e.Action, err)
	}
	return nil
}
