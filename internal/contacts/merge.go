package contacts

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

var (
	ErrNotFound  = errors.New("contact not found")
	ErrMergeSelf = errors.New("a contact cannot be merged into itself")
)

// Actor is who performs an audited action.
type Actor struct {
	UserID pgtype.UUID
	IP     *netip.Addr
}

type MergeResult struct {
	Addresses     int64
	Conversations int
	Notes         int64
}

// lockAddresses takes the same advisory locks as mail ingest and the importer, in a stable
// order, so no message can attach to a contact while it is being merged or erased. Contact
// locks come before row locks everywhere, which keeps the lock order the same for all users.
func lockAddresses(ctx context.Context, q *dbq.Queries, contactIDs ...pgtype.UUID) error {
	var emails []string
	for _, id := range contactIDs {
		list, err := q.ContactAddressEmails(ctx, id)
		if err != nil {
			return fmt.Errorf("list addresses: %w", err)
		}
		emails = append(emails, list...)
	}
	slices.Sort(emails)
	for _, e := range slices.Compact(emails) {
		if err := q.IngestAdvisoryLock(ctx, "contact:"+e); err != nil {
			return fmt.Errorf("lock contact address: %w", err)
		}
	}
	return nil
}

// Merge moves everything of secondary onto primary and deletes secondary. The primary wins
// where both have a value. The viewer must be able to see both contacts.
func Merge(ctx context.Context, pool *pgxpool.Pool, v Viewer, actor Actor, primary, secondary pgtype.UUID) (MergeResult, error) {
	if primary == secondary {
		return MergeResult{}, ErrMergeSelf
	}
	var res MergeResult
	err := db.InTx(ctx, pool, func(q *dbq.Queries) error {
		if err := lockAddresses(ctx, q, primary, secondary); err != nil {
			return err
		}
		ids := []pgtype.UUID{primary, secondary}
		slices.SortFunc(ids, func(a, b pgtype.UUID) int { return slices.Compare(a.Bytes[:], b.Bytes[:]) })
		for _, id := range ids {
			if _, err := q.LockContact(ctx, id); errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock contact: %w", err)
			}
			visible, err := v.ContactVisible(ctx, q, id)
			if err != nil {
				return fmt.Errorf("check visibility: %w", err)
			}
			if !visible {
				return ErrNotFound
			}
		}
		var err error
		if res.Addresses, err = q.MoveContactAddresses(ctx, dbq.MoveContactAddressesParams{PrimaryID: primary, SecondaryID: secondary}); err != nil {
			return fmt.Errorf("move addresses: %w", err)
		}
		moved, err := q.MoveContactConversations(ctx, dbq.MoveContactConversationsParams{PrimaryID: primary, SecondaryID: secondary})
		if err != nil {
			return fmt.Errorf("move conversations: %w", err)
		}
		res.Conversations = len(moved)
		for _, c := range moved {
			err := q.InsertContactMergedEvent(ctx, dbq.InsertContactMergedEventParams{
				ConversationID: c.ID, MailboxID: c.MailboxID, ActorUserID: actor.UserID, PrimaryID: uuidText(primary), SecondaryID: uuidText(secondary),
			})
			if err != nil {
				return fmt.Errorf("record merge event: %w", err)
			}
		}
		if res.Notes, err = q.MoveContactNotes(ctx, dbq.MoveContactNotesParams{PrimaryID: primary, SecondaryID: secondary}); err != nil {
			return fmt.Errorf("move notes: %w", err)
		}
		if err := q.MergeContactFields(ctx, dbq.MergeContactFieldsParams{PrimaryID: primary, SecondaryID: secondary}); err != nil {
			return fmt.Errorf("merge fields: %w", err)
		}
		if err := q.DeleteContact(ctx, secondary); err != nil {
			return fmt.Errorf("delete merged contact: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: actor.UserID, IP: actor.IP, Action: audit.ContactMerged, TargetType: "contact", TargetID: uuidText(primary),
			Metadata: map[string]any{
				"merged_contact_id": uuidText(secondary), "addresses": res.Addresses,
				"conversations": res.Conversations, "notes": res.Notes,
			},
		})
	})
	if err != nil {
		return MergeResult{}, err
	}
	return res, nil
}

func uuidText(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}
