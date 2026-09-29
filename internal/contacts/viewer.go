// Package contacts holds the CRM domain logic: visibility, custom attributes, segments,
// CSV import and export, merging and GDPR export and erasure.
package contacts

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

// Viewer is whoever looks at contacts: the user, whether they administer, and the mailboxes
// they may read. Visibility rules are evaluated in SQL (contact_visible) with these values.
type Viewer struct {
	UserID     pgtype.UUID
	Admin      bool
	MailboxIDs []pgtype.UUID
}

func ViewerFor(ctx context.Context, q *dbq.Queries, u dbq.User) (Viewer, error) {
	scope, err := policy.MailboxScope(ctx, q, u)
	if err != nil {
		return Viewer{}, fmt.Errorf("resolve mailbox scope: %w", err)
	}
	return Viewer{UserID: u.ID, Admin: policy.SeesAll(u), MailboxIDs: scope.Read}, nil
}

func (v Viewer) mailboxes() []pgtype.UUID {
	if v.MailboxIDs == nil {
		return []pgtype.UUID{}
	}
	return v.MailboxIDs
}

// ContactVisible reports whether v may see the contact.
func (v Viewer) ContactVisible(ctx context.Context, q *dbq.Queries, id pgtype.UUID) (bool, error) {
	return q.ContactIsVisible(ctx, dbq.ContactIsVisibleParams{ID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.mailboxes()})
}

// OrganizationVisible reports whether v may see the organization.
func (v Viewer) OrganizationVisible(ctx context.Context, q *dbq.Queries, id pgtype.UUID) (bool, error) {
	return q.OrganizationIsVisible(ctx, dbq.OrganizationIsVisibleParams{ID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.mailboxes()})
}
