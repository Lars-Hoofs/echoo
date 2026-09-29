package campaigns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/mail"
	"echoo/internal/mail/send"
	"echoo/internal/threading"
)

const (
	// TickInterval is how often the dispatcher runs. It hands out a slice of the minute's
	// rate each time, which keeps sending smooth instead of a burst at the start of a minute.
	TickInterval = 5 * time.Second
	rateWindow   = time.Minute

	errSegmentCode  = "segment_missing"
	errCreatorCode  = "creator_unavailable"
	errMailboxCode  = "mailbox_unavailable"
	statusSending   = "sending"
	statusCancelled = "cancelled"
	statusPaused    = "paused"
	statusScheduled = "scheduled"
	campaignEvent   = "campaign"
	createdEvent    = "created"
	outboundReason  = "outbound"
)

// Tick advances every campaign that is due, running, or still waiting for deliveries. One
// campaign failing does not hold back the others; the errors are returned together.
func (s *Service) Tick(ctx context.Context) error {
	ids, err := s.q.CampaignListActive(ctx, ts(s.now()))
	if err != nil {
		return fmt.Errorf("list active campaigns: %w", err)
	}
	var errs []error
	for _, id := range ids {
		err := pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error { return s.advance(ctx, tx, id) })
		if err != nil {
			slog.ErrorContext(ctx, "campaign dispatch failed", "campaign_id", id.String(), "error", err)
			errs = append(errs, fmt.Errorf("campaign %s: %w", id.String(), err))
		}
	}
	return errors.Join(errs...)
}

// advance does one dispatcher step for one campaign, in one transaction: the row lock keeps
// pause, cancel and a concurrent run from interleaving with it, and a crash leaves nothing
// half done.
func (s *Service) advance(ctx context.Context, tx pgx.Tx, id pgtype.UUID) error {
	q := dbq.New(tx)
	now := s.now()
	c, err := q.CampaignLock(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock campaign: %w", err)
	}
	if c.Status == statusScheduled {
		if c.ScheduledAt.Time.After(now) {
			return nil
		}
		if err := q.CampaignBegin(ctx, dbq.CampaignBeginParams{ID: id, Now: ts(now)}); err != nil {
			return fmt.Errorf("begin campaign: %w", err)
		}
		c.Status = statusSending
	}
	if c.Status == statusSending && !c.MaterializedAt.Valid {
		code, err := s.materializeOrStop(ctx, tx, c)
		if err != nil {
			return err
		}
		if code != "" {
			return q.CampaignStop(ctx, dbq.CampaignStopParams{ID: id, Status: statusCancelled, Error: code, Now: ts(now)})
		}
	}
	if c.Status == statusCancelled {
		// A message that was being delivered when the campaign was cancelled may since have gone
		// back to waiting for a retry; it must not be tried again.
		if _, err := q.CampaignCancelOutbound(ctx, dbq.CampaignCancelOutboundParams{CampaignID: id, Now: ts(now)}); err != nil {
			return fmt.Errorf("stop queued messages: %w", err)
		}
	}
	if _, err := q.CampaignSyncResults(ctx, dbq.CampaignSyncResultsParams{CampaignID: id, Now: ts(now)}); err != nil {
		return fmt.Errorf("record delivery results: %w", err)
	}
	if _, err := q.CampaignFailOrphans(ctx, dbq.CampaignFailOrphansParams{CampaignID: id, Now: ts(now)}); err != nil {
		return fmt.Errorf("fail orphaned recipients: %w", err)
	}
	if c.Status != statusSending {
		return nil
	}

	mb, err := q.GetMailbox(ctx, c.MailboxID)
	if err != nil {
		return fmt.Errorf("load mailbox: %w", err)
	}
	if mb.DisabledAt.Valid {
		return q.CampaignStop(ctx, dbq.CampaignStopParams{ID: id, Status: statusPaused, Error: errMailboxCode, Now: ts(now)})
	}
	if err := s.dispatch(ctx, tx, c, mb, now); err != nil {
		return err
	}
	open, err := q.CampaignCountOpen(ctx, id)
	if err != nil {
		return fmt.Errorf("count open recipients: %w", err)
	}
	if open.Pending == 0 && open.Queued == 0 {
		return q.CampaignFinish(ctx, dbq.CampaignFinishParams{ID: id, Now: ts(now)})
	}
	return nil
}

// materializeOrStop resolves the recipients in a savepoint. When the campaign can never
// run (its segment is gone, its creator left), it returns the reason and leaves no rows.
func (s *Service) materializeOrStop(ctx context.Context, tx pgx.Tx, c dbq.Campaign) (string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin savepoint: %w", err)
	}
	err = s.materialize(ctx, sp, c)
	if err == nil {
		return "", sp.Commit(ctx)
	}
	rbErr := sp.Rollback(ctx)
	switch {
	case errors.Is(err, errSegmentMissing):
		return errSegmentCode, rbErr
	case errors.Is(err, errCreatorUnavailable):
		return errCreatorCode, rbErr
	}
	return "", errors.Join(err, rbErr)
}

// dispatch queues the next slice of recipients. The budget is what is left of the mailbox's
// rate over the last minute, across all campaigns on it, capped to one tick's share.
func (s *Service) dispatch(ctx context.Context, tx pgx.Tx, c dbq.Campaign, mb dbq.Mailbox, now time.Time) error {
	q := dbq.New(tx)
	rate := min(int(c.RatePerMinute), s.d.MaxRate)
	recent, err := q.CampaignCountQueuedSince(ctx, dbq.CampaignCountQueuedSinceParams{MailboxID: c.MailboxID, Since: ts(now.Add(-rateWindow))})
	if err != nil {
		return fmt.Errorf("count recent messages: %w", err)
	}
	perTick := (rate*int(TickInterval/time.Second) + 59) / 60
	budget := min(rate-int(recent), perTick)
	if budget <= 0 {
		return nil
	}
	rows, err := q.CampaignClaimPending(ctx, dbq.CampaignClaimPendingParams{CampaignID: c.ID, Batch: int32(budget)}) //nolint:gosec // budget is at most one tick's share of the rate, below 1000
	if err != nil {
		return fmt.Errorf("claim recipients: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	from := Sender{Brand: brandOf(mb)}
	if c.CreatedBy.Valid {
		if u, err := q.GetUser(ctx, c.CreatedBy); err == nil {
			from.Agent = u.Name
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load creator: %w", err)
		}
	}
	for _, r := range rows {
		if err := s.enqueue(ctx, tx, client, c, mb, from, r, now); err != nil {
			return fmt.Errorf("recipient %s: %w", r.ID.String(), err)
		}
	}
	return nil
}

// enqueue turns one recipient into a message in the send queue, in its own conversation so
// that a reply threads into the inbox like any other. Everything happens in the caller's
// transaction under the recipient's row lock, and the send key is derived from campaign and
// recipient, so no path can queue the same recipient twice.
func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, client *river.Client[pgx.Tx], c dbq.Campaign, mb dbq.Mailbox, from Sender, r dbq.CampaignClaimPendingRow, now time.Time) error {
	q := dbq.New(tx)
	switch {
	case r.Unsubscribed:
		return skip(ctx, q, r.ID, SkipUnsubscribed, now)
	case r.Bounced:
		return skip(ctx, q, r.ID, SkipBounced, now)
	}
	key := idempotencyKey(c.ID, r.ID)
	prev, err := q.SendGetEnqueuedByKey(ctx, key)
	if err == nil {
		return link(ctx, q, r.ID, prev.MessageID, now)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("look up earlier message: %w", err)
	}

	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin savepoint: %w", err)
	}
	messageID, err := s.queueMessage(ctx, sp, client, c, mb, from, r, key, now)
	var invalid *send.ValidationError
	switch {
	case err == nil:
		if err := sp.Commit(ctx); err != nil {
			return fmt.Errorf("release savepoint: %w", err)
		}
		return link(ctx, q, r.ID, messageID, now)
	case errors.As(err, &invalid):
		if err := sp.Rollback(ctx); err != nil {
			return fmt.Errorf("roll back savepoint: %w", err)
		}
		return q.CampaignFailRecipient(ctx, dbq.CampaignFailRecipientParams{ID: r.ID, Error: invalid.Error(), Now: ts(now)})
	}
	return errors.Join(err, sp.Rollback(ctx))
}

func skip(ctx context.Context, q *dbq.Queries, id pgtype.UUID, reason string, now time.Time) error {
	return q.CampaignSkipRecipient(ctx, dbq.CampaignSkipRecipientParams{ID: id, Reason: reason, Now: ts(now)})
}

// link records the message of a recipient. The conversation comes from the message, which is
// what makes the path for an earlier-queued message the same as for a new one.
func link(ctx context.Context, q *dbq.Queries, recipient, message pgtype.UUID, now time.Time) error {
	msg, err := q.SendGetMessage(ctx, message)
	if err != nil {
		return fmt.Errorf("load queued message: %w", err)
	}
	return q.CampaignQueueRecipient(ctx, dbq.CampaignQueueRecipientParams{
		ID: recipient, MessageID: message, ConversationID: msg.ConversationID, Now: ts(now),
	})
}

func (s *Service) queueMessage(ctx context.Context, tx pgx.Tx, client *river.Client[pgx.Tx], c dbq.Campaign, mb dbq.Mailbox, from Sender, r dbq.CampaignClaimPendingRow, key pgtype.UUID, now time.Time) (pgtype.UUID, error) {
	q := dbq.New(tx)
	to := mail.Address{Name: r.Name, Address: r.Email}
	content := Render(c.Subject, c.BodyHtml, from, to, s.unsubscribeURL(r.ID, r.UnsubscribeSecret, now))

	// Closed, so a campaign does not fill the open inbox; a reply reopens the conversation.
	conv, err := q.CampaignCreateConversation(ctx, dbq.CampaignCreateConversationParams{
		MailboxID: c.MailboxID, Subject: content.Subject, SubjectNormalized: threading.NormalizeSubject(content.Subject), ContactID: r.ContactID,
	})
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("create conversation: %w", err)
	}
	for _, e := range []struct {
		typ  string
		data map[string]string
	}{
		{createdEvent, map[string]string{"reason": outboundReason}},
		{campaignEvent, map[string]string{"campaign_id": c.ID.String(), "name": c.Name}},
	} {
		data, err := json.Marshal(e.data)
		if err != nil {
			return pgtype.UUID{}, fmt.Errorf("encode %s event: %w", e.typ, err)
		}
		if err := q.ComposeInsertEvent(ctx, dbq.ComposeInsertEventParams{
			ConversationID: conv, MailboxID: c.MailboxID, ActorUserID: c.CreatedBy, Type: e.typ, Data: data,
		}); err != nil {
			return pgtype.UUID{}, fmt.Errorf("insert %s event: %w", e.typ, err)
		}
	}
	res, err := send.Enqueue(ctx, tx, client, send.EnqueueParams{
		ConversationID: conv, MailboxID: mb.ID, IdempotencyKey: key, AuthorUserID: c.CreatedBy,
		To: []mail.Address{to}, Subject: content.Subject, Text: content.Text, HTML: content.HTML, Headers: content.Headers,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	if res.Duplicate {
		return pgtype.UUID{}, errors.New("send key was taken while the recipient was locked")
	}
	return res.MessageID, nil
}
