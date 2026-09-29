package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/auth"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/realtime"
)

// Realtime returns the hub, which the process has to run and close.
func (s *Server) Realtime() *realtime.Hub { return s.hub }

func mailboxStrings(ids []pgtype.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = uuidStr(id)
	}
	return out
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess := sessionFrom(ctx)
	scope, err := policy.MailboxScope(ctx, s.q, sess.User)
	if err != nil {
		writeError(w, r, err)
		return
	}
	cookie, err := r.Cookie(s.cookieName())
	if err != nil {
		writeError(w, r, errUnauthenticated)
		return
	}
	// Authenticate would extend the idle timeout, and an open stream must not keep an
	// abandoned session alive, so this reads the session without touching it.
	refresh := func(ctx context.Context) ([]string, error) {
		row, err := s.q.GetActiveSession(ctx, auth.HashToken(cookie.Value))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.Session.MfaPending) {
			return nil, auth.ErrNoSession
		}
		if err != nil {
			return nil, err
		}
		scope, err := policy.MailboxScope(ctx, s.q, row.User)
		if err != nil {
			return nil, err
		}
		return mailboxStrings(scope.Read), nil
	}
	id := realtime.Identity{UserID: uuidStr(sess.User.ID), Name: sess.User.Name, Mailboxes: mailboxStrings(scope.Read)}
	switch err := s.hub.Serve(w, r, id, refresh); {
	case errors.Is(err, realtime.ErrTooManyStreams):
		writeError(w, r, &apiError{Status: http.StatusTooManyRequests, Code: "too_many_streams", Message: "too many open event streams"})
	case errors.Is(err, realtime.ErrClosed):
		writeError(w, r, &apiError{Status: http.StatusServiceUnavailable, Code: "shutting_down", Message: "server is shutting down"})
	case errors.Is(err, auth.ErrNoSession):
	case err != nil:
		slog.WarnContext(ctx, "event stream ended", "err", err, "request_id", requestIDFrom(ctx))
	}
}

// visibleConversation resolves a conversation's mailbox within the caller's scope. Anything
// outside it answers like a missing conversation.
func (s *Server) visibleConversation(r *http.Request) (conversationID, mailboxID string, err error) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return "", "", errNotFound
	}
	scope, err := policy.MailboxScope(ctx, s.q, sessionFrom(ctx).User)
	if err != nil {
		return "", "", err
	}
	mailbox, err := s.q.RealtimeConversationMailbox(ctx, dbq.RealtimeConversationMailboxParams{ID: id, MailboxIds: scope.Read})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errNotFound
	}
	if err != nil {
		return "", "", err
	}
	return uuidStr(id), uuidStr(mailbox), nil
}

var presenceStates = map[string]bool{realtime.StateViewing: true, realtime.StateTyping: true, realtime.StateLeft: true}

func (s *Server) postPresence(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State string `json:"state"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if !presenceStates[req.State] {
		writeError(w, r, errValidation(map[string]string{"state": "invalid"}))
		return
	}
	user := sessionFrom(r.Context()).User
	conv, mailbox, err := s.visibleConversation(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = realtime.Notify(r.Context(), s.q, realtime.Event{
		Type: realtime.TypePresence, ConversationID: conv, MailboxID: mailbox,
		UserID: uuidStr(user.ID), Name: user.Name, State: req.State,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getPresence(w http.ResponseWriter, r *http.Request) {
	conv, _, err := s.visibleConversation(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewers": s.hub.Viewers(conv)})
}

func (s *Server) onlineAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.hub.Online()})
}

// limitByUser bounds a chatty endpoint per signed-in user rather than per address.
func limitByUser(l *auth.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(uuidStr(sessionFrom(r.Context()).User.ID)) {
				writeError(w, r, errRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// notifyScopeChanged makes open streams reload their mailbox scope. It runs inside the
// admin transaction so the notification only goes out when the change commits.
func notifyScopeChanged(ctx context.Context, q realtime.Notifier) error {
	return realtime.Notify(ctx, q, realtime.Event{Type: realtime.TypeScopeChanged})
}
