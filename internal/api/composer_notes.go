package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/compose"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

const (
	maxMentions       = 20
	defaultNotifLimit = 50
)

type noteRequest struct {
	HTML     string   `json:"html"`
	Mentions []string `json:"mentions"`
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	conv, err := s.writableConversation(ctx, user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req noteRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.HTML) > maxBodyHTMLBytes {
		writeError(w, r, errValidation(map[string]string{"html": "too_large"}))
		return
	}
	body := compose.Sanitize(req.HTML, compose.SanitizeOptions{Mentions: true})
	if compose.IsEmpty(body) {
		writeError(w, r, errValidation(map[string]string{"html": "empty"}))
		return
	}
	mentions, err := s.checkMentions(r, conv, req.Mentions)
	if err != nil {
		writeError(w, r, err)
		return
	}

	var noteID pgtype.UUID
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := dbq.New(tx)
		id, err := q.ComposeInsertNote(ctx, dbq.ComposeInsertNoteParams{
			ConversationID: conv.ID, MailboxID: conv.MailboxID, FromAddr: user.Email, FromName: user.Name,
			BodyText: compose.TextFromHTML(body), BodyHtml: body, AuthorUserID: user.ID,
		})
		if err != nil {
			return fmt.Errorf("insert note: %w", err)
		}
		noteID = id
		if len(mentions) > 0 {
			if err := q.ComposeInsertMentions(ctx, dbq.ComposeInsertMentionsParams{MessageID: id, UserIds: mentions}); err != nil {
				return fmt.Errorf("insert mentions: %w", err)
			}
		}
		for _, uid := range mentions {
			if err := compose.Notify(ctx, q, compose.Notification{
				UserID: uid, Kind: compose.KindMention, ConversationID: conv.ID, MessageID: id, ActorID: user.ID,
			}); err != nil {
				return err
			}
		}
		version, err := q.ComposeTouchConversation(ctx, dbq.ComposeTouchConversationParams{ID: conv.ID})
		if err != nil {
			return fmt.Errorf("update conversation: %w", err)
		}
		if err := q.MarkConversationRead(ctx, dbq.MarkConversationReadParams{UserID: user.ID, ConversationID: conv.ID}); err != nil {
			return fmt.Errorf("mark read: %w", err)
		}
		return notifyMessage(ctx, q, conv.ID, conv.MailboxID, int64(version))
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.sentMessage(ctx, noteID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message": out.Message})
}

// checkMentions returns the mentioned users, refusing anyone without read access to the
// conversation's mailbox: a mention must never expose a conversation to someone who cannot open it.
func (s *Server) checkMentions(r *http.Request, conv dbq.ComposeGetConversationRow, raw []string) ([]pgtype.UUID, error) {
	if len(raw) > maxMentions {
		return nil, errValidation(map[string]string{"mentions": "too_many"})
	}
	ids := make([]pgtype.UUID, 0, len(raw))
	for _, v := range raw {
		id, ok := parseUUID(v)
		if !ok {
			return nil, errValidation(map[string]string{"mentions": "invalid"})
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ids, nil
	}
	allowed, err := s.q.ComposeListMentionable(r.Context(), conv.MailboxID)
	if err != nil {
		return nil, fmt.Errorf("list mentionable users: %w", err)
	}
	for _, id := range ids {
		if !slices.ContainsFunc(allowed, func(u dbq.ComposeListMentionableRow) bool { return u.ID == id }) {
			return nil, &apiError{Status: http.StatusUnprocessableEntity, Code: "mention_no_access", Message: "a mentioned user cannot read this mailbox", Fields: map[string]string{"mentions": id.String()}}
		}
	}
	return ids, nil
}

type notificationJSON struct {
	ID                 string     `json:"id"`
	Kind               string     `json:"kind"`
	ConversationID     string     `json:"conversation_id"`
	ConversationNumber int64      `json:"conversation_number"`
	ConversationTitle  string     `json:"conversation_subject"`
	MessageID          *string    `json:"message_id"`
	Actor              *refJSON   `json:"actor"`
	CreatedAt          time.Time  `json:"created_at"`
	ReadAt             *time.Time `json:"read_at"`
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	unreadOnly := false
	switch v := r.URL.Query().Get("unread"); v {
	case "", "false":
	case "true":
		unreadOnly = true
	default:
		writeError(w, r, errBadRequest("unread must be true or false"))
		return
	}
	rows, err := s.q.ComposeListNotifications(ctx, dbq.ComposeListNotificationsParams{
		UserID: user.ID, MailboxIds: scope.Read, UnreadOnly: unreadOnly, PageSize: defaultNotifLimit,
	})
	if err != nil {
		writeError(w, r, fmt.Errorf("list notifications: %w", err))
		return
	}
	unread, err := s.q.ComposeCountUnreadNotifications(ctx, dbq.ComposeCountUnreadNotificationsParams{UserID: user.ID, MailboxIds: scope.Read})
	if err != nil {
		writeError(w, r, fmt.Errorf("count notifications: %w", err))
		return
	}
	list := make([]notificationJSON, len(rows))
	for i, n := range rows {
		list[i] = notificationJSON{
			ID: uuidStr(n.ID), Kind: n.Kind, ConversationID: uuidStr(n.ConversationID), ConversationNumber: n.ConversationNumber,
			ConversationTitle: n.ConversationSubject, CreatedAt: n.CreatedAt.Time.UTC(), ReadAt: timeOrNil(n.ReadAt),
		}
		if n.MessageID.Valid {
			id := uuidStr(n.MessageID)
			list[i].MessageID = &id
		}
		if n.ActorID.Valid {
			list[i].Actor = &refJSON{ID: uuidStr(n.ActorID), Name: n.ActorName.String}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": list, "unread_count": unread})
}

type markReadRequest struct {
	IDs []string `json:"ids"`
	All bool     `json:"all"`
}

func (s *Server) markNotificationsRead(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	var req markReadRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if !req.All && len(req.IDs) == 0 {
		writeError(w, r, errValidation(map[string]string{"ids": "required"}))
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.IDs))
	for _, v := range req.IDs {
		id, ok := parseUUID(v)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"ids": "invalid"}))
			return
		}
		ids = append(ids, id)
	}
	if err := s.q.ComposeMarkNotificationsRead(ctx, dbq.ComposeMarkNotificationsReadParams{UserID: user.ID, MarkAll: req.All, Ids: ids}); err != nil {
		writeError(w, r, fmt.Errorf("mark notifications read: %w", err))
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	unread, err := s.q.ComposeCountUnreadNotifications(ctx, dbq.ComposeCountUnreadNotificationsParams{UserID: user.ID, MailboxIds: scope.Read})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, fmt.Errorf("count notifications: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unread_count": unread})
}
