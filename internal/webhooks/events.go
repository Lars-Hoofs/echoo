// Package webhooks implements outgoing webhooks: an outbox that domain code writes to inside
// its own transaction, a fan-out job, and the delivery worker.
package webhooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

const (
	ConversationCreated       = "conversation.created"
	ConversationUpdated       = "conversation.updated"
	ConversationStatusChanged = "conversation.status_changed"
	ConversationAssigned      = "conversation.assigned"
	ConversationAutomation    = "conversation.automation"
	MessageCreated            = "message.created"
	MessageSent               = "message.sent"
	MessageFailed             = "message.failed"
	ContactCreated            = "contact.created"

	// Test is only used for "Testbericht versturen"; it cannot be subscribed to.
	Test = "webhook.test"
)

// Events are the event types a webhook can subscribe to.
var Events = []string{
	ConversationCreated, ConversationUpdated, ConversationStatusChanged, ConversationAssigned,
	ConversationAutomation, MessageCreated, MessageSent, MessageFailed, ContactCreated,
}

func ValidEvent(name string) bool { return slices.Contains(Events, name) }

// ErrUnknownEvent is returned by Record for a type that is not in Events.
var ErrUnknownEvent = errors.New("unknown webhook event type")

// Event identifies what happened. Only the IDs are stored: the payload is read from the
// database when it is delivered, so it reflects the current state and holds no message content
// at rest in the outbox.
type Event struct {
	Type           string
	MailboxID      pgtype.UUID
	ConversationID pgtype.UUID
	MessageID      pgtype.UUID
	ContactID      pgtype.UUID
}

// Record writes an event to the outbox. Call it with the Queries of the transaction that makes
// the change, so the event exists if and only if the change was committed. It stores nothing
// when no enabled webhook subscribes to the type.
func Record(ctx context.Context, q *dbq.Queries, e Event) error {
	if !ValidEvent(e.Type) {
		return fmt.Errorf("%w: %q", ErrUnknownEvent, e.Type)
	}
	err := q.RecordWebhookEvent(ctx, dbq.RecordWebhookEventParams{
		Type: e.Type, MailboxID: e.MailboxID, ConversationID: e.ConversationID,
		MessageID: e.MessageID, ContactID: e.ContactID,
	})
	if err != nil {
		return fmt.Errorf("record webhook event %s: %w", e.Type, err)
	}
	return nil
}

func pgtypeTime(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
