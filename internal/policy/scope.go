package policy

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

// Scope is the set of mailboxes a user may touch. Write is always a subset of Read.
type Scope struct {
	Read  []pgtype.UUID
	Write []pgtype.UUID
}

// MailboxScope resolves which mailboxes u may read and write. Owners and admins get every
// mailbox with write access; other users get the mailboxes their teams were granted, at the
// highest level among their teams. Without conversations.read there is nothing to see, and
// without conversations.write (readonly users) the grant never gives write access.
func MailboxScope(ctx context.Context, q *dbq.Queries, u dbq.User) (Scope, error) {
	scope := Scope{Read: []pgtype.UUID{}, Write: []pgtype.UUID{}}
	if !Has(u, ConversationsRead) {
		return scope, nil
	}
	if SeesAll(u) {
		ids, err := q.ListAllMailboxIDs(ctx)
		if err != nil {
			return Scope{}, fmt.Errorf("list mailboxes: %w", err)
		}
		scope.Read = append(scope.Read, ids...)
		scope.Write = append(scope.Write, ids...)
		return scope, nil
	}
	rows, err := q.ListUserMailboxAccess(ctx, u.ID)
	if err != nil {
		return Scope{}, fmt.Errorf("list mailbox access: %w", err)
	}
	for _, row := range rows {
		scope.Read = append(scope.Read, row.MailboxID)
		if row.CanWrite && Has(u, ConversationsWrite) {
			scope.Write = append(scope.Write, row.MailboxID)
		}
	}
	return scope, nil
}

// Widens reports whether s reaches a mailbox, or writes to one, that before did not.
func (s Scope) Widens(before Scope) bool {
	return !containsAll(before.Read, s.Read) || !containsAll(before.Write, s.Write)
}

func containsAll(have, want []pgtype.UUID) bool {
	return !slices.ContainsFunc(want, func(id pgtype.UUID) bool { return !slices.Contains(have, id) })
}
