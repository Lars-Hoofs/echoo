package campaigns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/compose"
	"echoo/internal/contacts"
	"echoo/internal/db/dbq"
	"echoo/internal/mail"
)

const resolveBatch = 500

// Skip reasons, as stored on campaign_recipients.
const (
	SkipUnsubscribed = "unsubscribed"
	SkipNoAddress    = "no_address"
	SkipDuplicate    = "duplicate"
	SkipBounced      = "bounced"
	SkipCancelled    = "cancelled"
)

var (
	errSegmentMissing     = errors.New("segment is missing or not visible to whoever started the campaign")
	errCreatorUnavailable = errors.New("the user who started the campaign is missing or deactivated")
)

type candidate struct {
	contactID pgtype.UUID
	email     string
	name      string
	skip      string // empty when the recipient will be mailed
}

// classify decides whether a contact is mailed and to which address. Unsubscribing wins over
// every other reason. seen holds the addresses already taken in this campaign.
func classify(r dbq.CampaignCandidatesRow, seen map[string]bool) candidate {
	c := candidate{contactID: r.ID, email: r.Email, name: r.Name}
	switch {
	case r.UnsubscribedAt.Valid:
		c.skip = SkipUnsubscribed
	case r.ErasedAt.Valid || !r.HasAddress || !plainAddress(r.Email):
		c.skip = SkipNoAddress
	case r.Bounced:
		c.skip = SkipBounced
	case seen[r.Email]:
		c.skip = SkipDuplicate
	default:
		seen[r.Email] = true
	}
	return c
}

func plainAddress(email string) bool {
	_, err := compose.NormalizeAddresses([]mail.Address{{Address: email}})
	return err == nil
}

// walk resolves the segment with v's visibility and calls fn per batch of classified contacts.
func (s *Service) walk(ctx context.Context, db dbq.DBTX, segment pgtype.UUID, v contacts.Viewer, fn func([]candidate) error) error {
	if !segment.Valid {
		return errSegmentMissing
	}
	q := dbq.New(db)
	seen := map[string]bool{}
	err := contacts.ResolveSegment(ctx, q, db, segment, v, resolveBatch, func(ids []pgtype.UUID) error {
		rows, err := q.CampaignCandidates(ctx, ids)
		if err != nil {
			return fmt.Errorf("load recipients: %w", err)
		}
		batch := make([]candidate, len(rows))
		for i, r := range rows {
			batch[i] = classify(r, seen)
		}
		return fn(batch)
	})
	if errors.Is(err, contacts.ErrSegmentNotFound) {
		return errSegmentMissing
	}
	return err
}

// Preview is what a campaign to a segment would do, before it starts.
type Preview struct {
	Total        int `json:"total"`
	Sendable     int `json:"sendable"`
	Unsubscribed int `json:"unsubscribed"`
	NoAddress    int `json:"no_address"`
	Duplicate    int `json:"duplicate"`
	Bounced      int `json:"bounced"`
	// WithoutName counts sendable recipients with no name, for which {{contact.first_name}}
	// comes out empty.
	WithoutName int `json:"without_name"`
}

// Preview counts the recipients of a campaign to the segment, as user would send it.
func (s *Service) Preview(ctx context.Context, user dbq.User, segment pgtype.UUID) (Preview, error) {
	v, err := contacts.ViewerFor(ctx, s.q, user)
	if err != nil {
		return Preview{}, err
	}
	var p Preview
	err = s.walk(ctx, s.d.Pool, segment, v, func(batch []candidate) error {
		for _, c := range batch {
			p.Total++
			switch c.skip {
			case "":
				p.Sendable++
				if strings.TrimSpace(c.name) == "" {
					p.WithoutName++
				}
			case SkipUnsubscribed:
				p.Unsubscribed++
			case SkipNoAddress:
				p.NoAddress++
			case SkipDuplicate:
				p.Duplicate++
			case SkipBounced:
				p.Bounced++
			}
		}
		return nil
	})
	return p, err
}

// ErrSegmentUnavailable means the segment does not exist or the user cannot see it.
var ErrSegmentUnavailable = errSegmentMissing

// materialize stores one row per contact in the segment, as the user who started the campaign
// sees it (not whoever wrote the draft, so nobody reaches contacts through someone else's
// start). It runs inside the dispatcher's transaction, so a campaign has all its recipients or
// none.
func (s *Service) materialize(ctx context.Context, tx pgx.Tx, c dbq.Campaign) error {
	q := dbq.New(tx)
	if !c.StartedBy.Valid {
		return errCreatorUnavailable
	}
	starter, err := q.GetUser(ctx, c.StartedBy)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && starter.DeactivatedAt.Valid) {
		return errCreatorUnavailable
	}
	if err != nil {
		return fmt.Errorf("load starter: %w", err)
	}
	v, err := contacts.ViewerFor(ctx, q, starter)
	if err != nil {
		return err
	}
	err = s.walk(ctx, tx, c.SegmentID, v, func(batch []candidate) error {
		p := dbq.CampaignInsertRecipientsParams{CampaignID: c.ID}
		for _, r := range batch {
			p.ContactIds = append(p.ContactIds, r.contactID)
			p.Emails = append(p.Emails, r.email)
			p.Names = append(p.Names, r.name)
			if r.skip == "" {
				p.States, p.Reasons = append(p.States, "pending"), append(p.Reasons, "")
			} else {
				p.States, p.Reasons = append(p.States, "skipped"), append(p.Reasons, r.skip)
			}
		}
		if err := q.CampaignInsertRecipients(ctx, p); err != nil {
			return fmt.Errorf("insert recipients: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return q.CampaignSetMaterialized(ctx, dbq.CampaignSetMaterializedParams{ID: c.ID, Now: ts(s.now())})
}
