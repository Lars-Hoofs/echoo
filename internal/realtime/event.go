// Package realtime fans database change notifications out to browsers over server-sent events.
//
// Events carry identifiers and types only, never content: clients refetch through the normal
// API, so authorization stays in one place. The hub additionally filters every event by the
// receiving user's mailbox scope.
package realtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// Channel is the Postgres NOTIFY channel every instance listens on.
const Channel = "echoo_events"

// maxPayload keeps well below Postgres' 8000 byte NOTIFY limit.
const maxPayload = 7000

const (
	TypeMessageCreated      = "message.created"
	TypeMessageUpdated      = "message.updated"
	TypeConversationUpdated = "conversation.updated"
	TypeNotification        = "notification"
	TypePresence            = "presence"
	TypeScopeChanged        = "scope.changed"
	TypeResync              = "resync"
	StateViewing            = "viewing"
	StateTyping             = "typing"
	StateLeft               = "left"
)

// Event is the wire format of both the NOTIFY payload and the SSE data line.
// Name is only set on presence events, so clients can label a viewer without a lookup.
type Event struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id,omitempty"`
	MailboxID      string `json:"mailbox_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	State          string `json:"state,omitempty"`
	Name           string `json:"name,omitempty"`
	Version        int64  `json:"version,omitempty"`
}

// Notifier is implemented by *dbq.Queries, on a pool or inside a transaction. Inside a
// transaction the notification is only delivered when it commits.
type Notifier interface {
	RealtimeNotify(ctx context.Context, payload string) error
}

// Notify publishes ev to every instance.
func Notify(ctx context.Context, q Notifier, ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode %s event: %w", ev.Type, err)
	}
	if len(payload) > maxPayload {
		return fmt.Errorf("%s event is %d bytes, over the notify limit", ev.Type, len(payload))
	}
	if err := q.RealtimeNotify(ctx, string(payload)); err != nil {
		return fmt.Errorf("notify %s: %w", ev.Type, err)
	}
	return nil
}

// decodeEvent parses a NOTIFY payload and canonicalizes its identifiers, so they compare
// equal to the scope's UUIDs whatever casing the emitter used.
func decodeEvent(payload string) (Event, error) {
	var ev Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	if ev.Type == "" {
		return Event{}, fmt.Errorf("event without type")
	}
	for _, id := range []*string{&ev.ConversationID, &ev.MailboxID, &ev.UserID} {
		if *id == "" {
			continue
		}
		var u pgtype.UUID
		if err := u.Scan(*id); err != nil {
			return Event{}, fmt.Errorf("event %s has an invalid identifier", ev.Type)
		}
		*id = u.String()
	}
	return ev, nil
}
