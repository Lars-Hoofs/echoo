package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

const previewRunes = 200

type payload struct {
	ID        string      `json:"id"`
	Event     string      `json:"event"`
	CreatedAt time.Time   `json:"created_at"`
	Data      payloadData `json:"data"`
}

type payloadData struct {
	Conversation *conversationData `json:"conversation,omitempty"`
	Message      *messageData      `json:"message,omitempty"`
	Contact      *contactData      `json:"contact,omitempty"`
}

type conversationData struct {
	ID             string  `json:"id"`
	Number         int64   `json:"number,omitempty"`
	MailboxID      string  `json:"mailbox_id,omitempty"`
	Status         string  `json:"status,omitempty"`
	AssigneeUserID *string `json:"assignee_user_id,omitempty"`
	Subject        *string `json:"subject,omitempty"`
}

type messageData struct {
	ID             string  `json:"id"`
	ConversationID string  `json:"conversation_id,omitempty"`
	MailboxID      string  `json:"mailbox_id,omitempty"`
	Direction      string  `json:"direction,omitempty"`
	Subject        *string `json:"subject,omitempty"`
	Preview        *string `json:"preview,omitempty"`
}

type contactData struct {
	ID    string  `json:"id"`
	Name  *string `json:"name,omitempty"`
	Email *string `json:"email,omitempty"`
}

// buildPayload reads the current state of what the event refers to. Subject, preview, names
// and addresses are only included when the webhook asked for content; an entity that no longer
// exists is reduced to its ID.
func buildPayload(ctx context.Context, q *dbq.Queries, ev dbq.WebhookEvent, includeContent bool) ([]byte, error) {
	p := payload{ID: "evt_" + strconv.FormatInt(ev.ID, 10), Event: ev.Type, CreatedAt: ev.OccurredAt.Time.UTC()}
	if strings.HasPrefix(ev.Type, "conversation.") {
		c, err := conversationPayload(ctx, q, ev.ConversationID, includeContent)
		if err != nil {
			return nil, err
		}
		p.Data.Conversation = c
	}
	if ev.MessageID.Valid {
		m, err := messagePayload(ctx, q, ev.MessageID, includeContent)
		if err != nil {
			return nil, err
		}
		p.Data.Message = m
	}
	if ev.ContactID.Valid {
		c, err := contactPayload(ctx, q, ev.ContactID, includeContent)
		if err != nil {
			return nil, err
		}
		p.Data.Contact = c
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode webhook payload: %w", err)
	}
	return b, nil
}

func conversationPayload(ctx context.Context, q *dbq.Queries, id pgtype.UUID, includeContent bool) (*conversationData, error) {
	if !id.Valid {
		return nil, nil
	}
	row, err := q.WebhookConversation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return &conversationData{ID: id.String()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load conversation for webhook: %w", err)
	}
	c := &conversationData{ID: row.ID.String(), Number: row.Number, MailboxID: row.MailboxID.String(), Status: row.Status}
	if row.AssigneeUserID.Valid {
		a := row.AssigneeUserID.String()
		c.AssigneeUserID = &a
	}
	if includeContent {
		c.Subject = &row.Subject
	}
	return c, nil
}

func messagePayload(ctx context.Context, q *dbq.Queries, id pgtype.UUID, includeContent bool) (*messageData, error) {
	row, err := q.WebhookMessage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return &messageData{ID: id.String()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load message for webhook: %w", err)
	}
	m := &messageData{ID: row.ID.String(), ConversationID: row.ConversationID.String(), MailboxID: row.MailboxID.String()}
	switch row.Direction.String {
	case "in":
		m.Direction = "inbound"
	case "out":
		m.Direction = "outbound"
	}
	if includeContent {
		pv := preview(row.BodyText)
		m.Subject, m.Preview = &row.Subject, &pv
	}
	return m, nil
}

func contactPayload(ctx context.Context, q *dbq.Queries, id pgtype.UUID, includeContent bool) (*contactData, error) {
	row, err := q.WebhookContact(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return &contactData{ID: id.String()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load contact for webhook: %w", err)
	}
	c := &contactData{ID: row.ID.String()}
	if includeContent {
		c.Name, c.Email = &row.Name, &row.Email
	}
	return c, nil
}

func preview(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(s) > previewRunes {
		s = string([]rune(s)[:previewRunes])
	}
	return s
}
