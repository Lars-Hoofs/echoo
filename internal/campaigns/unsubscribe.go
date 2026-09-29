package campaigns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

// Unsubscription is what the public unsubscribe page shows about a link.
type Unsubscription struct {
	Brand string
	// Email is masked: the link is a bearer credential, and a leaked link must not reveal the
	// address.
	Email string
	// Done is set when the contact is unsubscribed, by this call or an earlier one.
	Done bool
}

// LookupUnsubscribe resolves a link without changing anything. Mail scanners and link
// previews open links, so opening one must never unsubscribe.
func (s *Service) LookupUnsubscribe(ctx context.Context, token string) (Unsubscription, error) {
	rec, err := s.recipientOf(ctx, s.q, token)
	if err != nil {
		return Unsubscription{}, err
	}
	done, err := s.unsubscribed(ctx, rec)
	return Unsubscription{Brand: rec.Brand, Email: maskEmail(rec.Email), Done: done}, err
}

// Unsubscribe marks the contact behind the link as unsubscribed from all campaigns. It is
// idempotent: repeating it changes nothing and writes no second audit entry.
func (s *Service) Unsubscribe(ctx context.Context, token string) (Unsubscription, error) {
	rec, err := s.recipientOf(ctx, s.q, token)
	if err != nil {
		return Unsubscription{}, err
	}
	if rec.ContactID.Valid {
		err = db.InTx(ctx, s.d.Pool, func(q *dbq.Queries) error {
			n, err := q.ContactUnsubscribe(ctx, dbq.ContactUnsubscribeParams{ID: rec.ContactID, Now: ts(s.now())})
			if err != nil || n == 0 {
				return err
			}
			return audit.Write(ctx, q, audit.Entry{
				Action: audit.Unsubscribed, TargetType: "contact", TargetID: rec.ContactID.String(),
				Metadata: map[string]any{"campaign_recipient_id": rec.ID.String()},
			})
		})
		if err != nil {
			return Unsubscription{}, fmt.Errorf("unsubscribe contact: %w", err)
		}
	}
	return Unsubscription{Brand: rec.Brand, Email: maskEmail(rec.Email), Done: true}, nil
}

func (s *Service) recipientOf(ctx context.Context, q *dbq.Queries, token string) (dbq.CampaignRecipientForTokenRow, error) {
	t, err := parseToken(token)
	if err != nil {
		return dbq.CampaignRecipientForTokenRow{}, err
	}
	rec, err := q.CampaignRecipientForToken(ctx, t.recipient)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, ErrInvalidToken
	}
	if err != nil {
		return rec, fmt.Errorf("load recipient: %w", err)
	}
	return rec, t.verify(rec.UnsubscribeSecret, s.now())
}

func (s *Service) unsubscribed(ctx context.Context, rec dbq.CampaignRecipientForTokenRow) (bool, error) {
	if !rec.ContactID.Valid {
		return true, nil
	}
	done, err := s.q.ContactIsUnsubscribed(ctx, rec.ContactID)
	if err != nil {
		return false, fmt.Errorf("load contact: %w", err)
	}
	return done, nil
}

// maskEmail keeps the first character and the domain: j***@example.com.
func maskEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" {
		return ""
	}
	return local[:1] + "***@" + domain
}
