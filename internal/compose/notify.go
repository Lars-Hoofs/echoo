package compose

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/realtime"
)

const (
	KindMention  = "mention"
	KindAssigned = "assigned"
	KindReply    = "reply"
	KindSLA      = "sla"
	KindCSAT     = "csat"
)

// Notification is one in-app notification for one user.
type Notification struct {
	UserID         pgtype.UUID
	Kind           string
	ConversationID pgtype.UUID
	MessageID      pgtype.UUID // invalid when the notification is not about one message
	ActorID        pgtype.UUID // invalid for system events such as SLA breaches
}

// Notify stores the notification and tells the user's open tabs to refetch, in the caller's
// transaction when q is bound to one. Nothing is stored for a user's own actions.
func Notify(ctx context.Context, q *dbq.Queries, n Notification) error {
	if n.ActorID.Valid && n.ActorID == n.UserID {
		return nil
	}
	if _, err := q.ComposeInsertNotification(ctx, dbq.ComposeInsertNotificationParams{
		UserID: n.UserID, Kind: n.Kind, ConversationID: n.ConversationID, MessageID: n.MessageID, ActorID: n.ActorID,
	}); err != nil {
		return fmt.Errorf("insert %s notification: %w", n.Kind, err)
	}
	return realtime.Notify(ctx, q, realtime.Event{Type: realtime.TypeNotification, UserID: n.UserID.String()})
}

// NotifyAssigned tells a user that a conversation was assigned to them. The inbox actions call
// it in the transaction that changes the assignee.
func NotifyAssigned(ctx context.Context, q *dbq.Queries, conversationID, assignee, actor pgtype.UUID) error {
	return Notify(ctx, q, Notification{UserID: assignee, Kind: KindAssigned, ConversationID: conversationID, ActorID: actor})
}
