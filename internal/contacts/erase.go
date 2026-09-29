package contacts

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

const (
	// TombstoneName and TombstoneAddress replace the identity of an erased contact on messages.
	TombstoneName    = "Gewist contact"
	TombstoneAddress = "erased@erased.invalid"
)

type EraseResult struct {
	Addresses            int
	ConversationsErased  int
	ConversationsKept    int
	MessagesBlanked      int
	MessagesTombstoned   int
	AttachmentsDeleted   int
	RawCopiesDropped     int
	Notes                int
	ExportsDeleted       int
	BlobDeletionsPending int
}

// Erase implements the GDPR erasure of docs/data-model.md: the sender name and address on
// messages become a tombstone; bodies, subjects, attachments and raw copies of conversations
// in which the contact is the only external party are deleted; the contact rows go. It is
// audited with counts only. Backups are out of reach (SECURITY.md, section G).
func Erase(ctx context.Context, pool *pgxpool.Pool, blobs Blobs, actor Actor, id pgtype.UUID) (EraseResult, error) {
	var res EraseResult
	var keys []string
	err := db.InTx(ctx, pool, func(q *dbq.Queries) error {
		if err := lockAddresses(ctx, q, id); err != nil {
			return err
		}
		if _, err := q.LockContact(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock contact: %w", err)
		}
		addrs, err := q.ContactAddressEmails(ctx, id)
		if err != nil {
			return fmt.Errorf("list addresses: %w", err)
		}
		res.Addresses = len(addrs)
		convIDs, err := q.ConversationsOfContact(ctx, id)
		if err != nil {
			return fmt.Errorf("list conversations: %w", err)
		}
		shared, err := q.ConversationsWithOtherParties(ctx, dbq.ConversationsWithOtherPartiesParams{Ids: convIDs, Own: addrs})
		if err != nil {
			return fmt.Errorf("find other parties: %w", err)
		}
		var sole []pgtype.UUID
		for _, c := range convIDs {
			if !slices.Contains(shared, c) {
				sole = append(sole, c)
			}
		}
		res.ConversationsErased, res.ConversationsKept = len(sole), len(convIDs)-len(sole)

		var rawIDs []pgtype.UUID
		tombstoned, err := q.TombstoneMessagesOfAddresses(ctx, dbq.TombstoneMessagesOfAddressesParams{
			Addrs: addrs, ConversationIds: convIDs, TombstoneName: TombstoneName, TombstoneAddr: TombstoneAddress,
		})
		if err != nil {
			return fmt.Errorf("tombstone messages: %w", err)
		}
		res.MessagesTombstoned = len(tombstoned)
		for _, m := range tombstoned {
			rawIDs = append(rawIDs, m.RawMessageID)
		}
		if len(sole) > 0 {
			blanked, err := q.BlankMessagesOfConversations(ctx, dbq.BlankMessagesOfConversationsParams{
				ConversationIds: sole, TombstoneName: TombstoneName, TombstoneAddr: TombstoneAddress,
			})
			if err != nil {
				return fmt.Errorf("blank messages: %w", err)
			}
			res.MessagesBlanked = len(blanked)
			for _, m := range blanked {
				rawIDs = append(rawIDs, m.RawMessageID)
			}
			if _, err := q.BlankConversations(ctx, sole); err != nil {
				return fmt.Errorf("blank conversations: %w", err)
			}
			if err := q.DeleteThreadRefsOfConversations(ctx, sole); err != nil {
				return fmt.Errorf("delete thread refs: %w", err)
			}
			if err := q.DeleteDraftsOfConversations(ctx, sole); err != nil {
				return fmt.Errorf("delete drafts: %w", err)
			}
			if err := q.ClearOutboundResponses(ctx, sole); err != nil {
				return fmt.Errorf("clear delivery responses: %w", err)
			}
			attachmentKeys, err := q.DeleteAttachmentsOfConversations(ctx, sole)
			if err != nil {
				return fmt.Errorf("delete attachments: %w", err)
			}
			res.AttachmentsDeleted = len(attachmentKeys)
			keys = append(keys, attachmentKeys...)
		}
		// The survey comment is the customer's own words about the conversation.
		if _, err := q.BlankCSATComments(ctx, convIDs); err != nil {
			return fmt.Errorf("blank survey comments: %w", err)
		}
		rawIDs = slices.DeleteFunc(rawIDs, func(u pgtype.UUID) bool { return !u.Valid })
		if len(rawIDs) > 0 {
			rawKeys, err := q.RawBlobKeys(ctx, rawIDs)
			if err != nil {
				return fmt.Errorf("list raw copies: %w", err)
			}
			if err := q.DropRawMessages(ctx, rawIDs); err != nil {
				return fmt.Errorf("drop raw copies: %w", err)
			}
			res.RawCopiesDropped = len(rawKeys)
			keys = append(keys, rawKeys...)
		}
		notes, err := q.CountContactNotes(ctx, id)
		if err != nil {
			return fmt.Errorf("count notes: %w", err)
		}
		res.Notes = int(notes)
		exportKeys, err := q.DeleteDataExportsOfContact(ctx, id)
		if err != nil {
			return fmt.Errorf("delete exports: %w", err)
		}
		res.ExportsDeleted = len(exportKeys)
		keys = append(keys, exportKeys...)
		if _, err := q.DeleteAutomationRepliesOfAddresses(ctx, addrs); err != nil {
			return fmt.Errorf("delete automation replies: %w", err)
		}
		if _, err := q.DeleteImageAllowlistOfAddresses(ctx, addrs); err != nil {
			return fmt.Errorf("delete image allowlist entries: %w", err)
		}
		if err := QueueBlobDeletions(ctx, q, keys); err != nil {
			return err
		}
		if err := q.DeleteContactWebhookEvents(ctx, id); err != nil {
			return fmt.Errorf("delete webhook events: %w", err)
		}
		if err := q.CampaignScrubRecipients(ctx, id); err != nil {
			return fmt.Errorf("scrub campaign recipients: %w", err)
		}
		if err := q.DeleteContact(ctx, id); err != nil {
			return fmt.Errorf("delete contact: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: actor.UserID, IP: actor.IP, Action: audit.ContactErased, TargetType: "contact", TargetID: uuidText(id),
			Metadata: map[string]any{
				"addresses": res.Addresses, "conversations_erased": res.ConversationsErased,
				"conversations_kept": res.ConversationsKept, "messages_blanked": res.MessagesBlanked,
				"messages_tombstoned": res.MessagesTombstoned, "attachments_deleted": res.AttachmentsDeleted,
				"raw_copies_dropped": res.RawCopiesDropped, "notes_deleted": res.Notes, "exports_deleted": res.ExportsDeleted,
				"blobs_queued": len(keys),
			},
		})
	})
	if err != nil {
		return EraseResult{}, err
	}
	// The blob keys are already queued; draining now makes the erasure real straight away.
	// A failure leaves them queued for the purge job, which the caller can report.
	pending, err := DrainBlobDeletions(ctx, dbq.New(pool), blobs)
	if err != nil {
		return res, err
	}
	res.BlobDeletionsPending = pending
	return res, nil
}
