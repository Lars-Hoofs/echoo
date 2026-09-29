package sysmail

import (
	"context"
	"fmt"
	"html"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"echoo/internal/db/dbq"
	"echoo/internal/jobs"
)

const (
	notificationBatch = 100
	// maxBatches bounds one run, so a large backlog cannot hold a worker for long.
	maxBatches = 10
)

// NotificationWorker turns notification rows into emails for users who opted in. The mail
// says who did what and links to the conversation; it never contains message content.
type NotificationWorker struct {
	river.WorkerDefaults[jobs.NotificationMail]
	pool    *pgxpool.Pool
	sender  *Sender
	baseURL string
}

// NewNotificationWorker creates the worker; baseURL is ECHOO_BASE_URL without a trailing slash.
func NewNotificationWorker(pool *pgxpool.Pool, sender *Sender, baseURL string) *NotificationWorker {
	return &NotificationWorker{pool: pool, sender: sender, baseURL: baseURL}
}

func (w *NotificationWorker) Work(ctx context.Context, _ *river.Job[jobs.NotificationMail]) error {
	return w.run(ctx, river.ClientFromContext[pgx.Tx](ctx))
}

func (w *NotificationWorker) run(ctx context.Context, client *river.Client[pgx.Tx]) error {
	if !w.sender.Configured() {
		return nil
	}
	for range maxBatches {
		n, err := w.batch(ctx, client)
		if err != nil {
			return err
		}
		if n < notificationBatch {
			return nil
		}
	}
	return nil
}

// batch handles up to notificationBatch notifications in one transaction: the rows are
// marked handled and the emails queued together, so a row is never mailed twice or lost.
func (w *NotificationWorker) batch(ctx context.Context, client *river.Client[pgx.Tx]) (int, error) {
	st, err := w.sender.Status(ctx)
	if err != nil {
		return 0, err
	}
	var claimed int
	err = pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		if _, err := q.ExpireStaleNotificationMail(ctx); err != nil {
			return fmt.Errorf("expire stale notifications: %w", err)
		}
		if !st.Available {
			return nil
		}
		rows, err := q.ClaimNotificationsForMail(ctx)
		if err != nil {
			return fmt.Errorf("claim notifications: %w", err)
		}
		ids := make([]pgtype.UUID, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
			msg, ok := w.compose(row)
			if !ok {
				continue
			}
			if err := w.sender.Enqueue(ctx, tx, client, msg); err != nil {
				return err
			}
		}
		claimed = len(rows)
		if len(ids) == 0 {
			return nil
		}
		return q.MarkNotificationsMailHandled(ctx, ids)
	})
	return claimed, err
}

func (w *NotificationWorker) compose(row dbq.ClaimNotificationsForMailRow) (Message, bool) {
	if row.ReadAt.Valid || row.RecipientDeactivatedAt.Valid {
		return Message{}, false
	}
	actor := row.ActorName.String
	if !row.ActorName.Valid || actor == "" {
		actor = "Iemand"
	}
	var line string
	switch {
	case row.Kind == "mention" && row.EmailNotifyMentions:
		line = fmt.Sprintf("%s noemde je in gesprek #%d", actor, row.ConversationNumber)
	case row.Kind == "assigned" && row.EmailNotifyAssignments:
		line = fmt.Sprintf("%s heeft gesprek #%d aan je toegewezen", actor, row.ConversationNumber)
	case row.Kind == "reply" && row.EmailNotifyReplies:
		line = fmt.Sprintf("Nieuw antwoord van de klant in gesprek #%d", row.ConversationNumber)
	default:
		return Message{}, false
	}
	link := w.baseURL + "/inbox/alle/" + row.ConversationID.String()
	return Message{
		To:      row.RecipientEmail,
		Subject: line,
		Text: line + ".\n\nOpen het gesprek: " + link +
			"\n\nJe krijgt deze e-mail omdat je meldingen per e-mail hebt ingeschakeld. Je past dat aan onder Instellingen, Profiel.\n",
		HTML: `<p>` + html.EscapeString(line) + `.</p><p><a href="` + html.EscapeString(link) + `">Gesprek openen</a></p>` +
			`<p>Je krijgt deze e-mail omdat je meldingen per e-mail hebt ingeschakeld. Je past dat aan onder Instellingen, Profiel.</p>`,
	}, true
}
