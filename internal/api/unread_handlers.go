package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
)

func (s *Server) unreadRoutes(r chi.Router) {
	r.Post("/conversations/{id}/read", s.markConversationRead)
	r.Post("/conversations/{id}/unread", s.markConversationUnread)
}

// The read state is personal, so marking emits no realtime event: nobody else's view changes.
func (s *Server) markConversationRead(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	conv, err := s.readableConversation(r.Context(), user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.q.MarkConversationRead(r.Context(), dbq.MarkConversationReadParams{UserID: user.ID, ConversationID: conv.ID}); err != nil {
		writeError(w, r, fmt.Errorf("mark read: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) markConversationUnread(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	conv, err := s.readableConversation(r.Context(), user, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.q.MarkConversationUnread(r.Context(), dbq.MarkConversationUnreadParams{UserID: user.ID, ConversationID: conv.ID}); err != nil {
		writeError(w, r, fmt.Errorf("mark unread: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// attachUnread sets the unread flag of each conversation for the user.
func (s *Server) attachUnread(ctx context.Context, userID pgtype.UUID, list []conversationJSON) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(list))
	for i, c := range list {
		ids[i], _ = parseUUID(c.ID)
	}
	unread, err := s.q.ListUnreadConversationIDs(ctx, dbq.ListUnreadConversationIDsParams{UserID: userID, Ids: ids})
	if err != nil {
		return fmt.Errorf("load unread state: %w", err)
	}
	set := make(map[pgtype.UUID]bool, len(unread))
	for _, id := range unread {
		set[id] = true
	}
	for i := range list {
		list[i].Unread = set[ids[i]]
	}
	return nil
}
