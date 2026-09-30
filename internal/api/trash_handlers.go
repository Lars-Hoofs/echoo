package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/policy"
	"echoo/internal/retention"
)

func (s *Server) trashRoutes(r chi.Router) {
	r.Get("/trash", s.listTrash)
	r.Get("/trash/{id}", s.getTrashedConversation)
	r.Post("/trash", s.moveToTrash)
	r.Post("/trash/restore", s.restoreFromTrash)
	r.Post("/trash/purge", s.purgeTrash)
	r.Post("/trash/empty", s.emptyTrash)
}

// trashActor is inboxActor for the trash, which also needs conversations.delete. Everything in
// the trash is limited to the mailboxes the user may write.
func (s *Server) trashActor(r *http.Request) (inbox.Actor, error) {
	actor, err := s.inboxActor(r)
	if err != nil {
		return actor, err
	}
	if !policy.Has(sessionFrom(r.Context()).User, policy.ConversationsDelete) {
		return inbox.Actor{}, errForbidden
	}
	return actor, nil
}

// trashScope is the mailboxes whose trash u may see, for routes that also serve the content of
// trashed conversations: the writable ones, given conversations.delete.
func trashScope(u dbq.User, scope policy.Scope) []pgtype.UUID {
	if !policy.Has(u, policy.ConversationsWrite) || !policy.Has(u, policy.ConversationsDelete) {
		return []pgtype.UUID{}
	}
	return scope.Write
}

// getTrashedConversation shows a conversation in the trash, read-only: the same detail as
// GET /conversations/{id} with can_write false, plus its timeline and who deleted it.
func (s *Server) getTrashedConversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	info, err := s.q.TrashGetInfo(ctx, dbq.TrashGetInfoParams{ID: id, MailboxIds: actor.Write})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load trashed conversation: %w", err))
		return
	}
	rows, err := s.q.ListConversationsByID(ctx, dbq.ListConversationsByIDParams{Ids: []pgtype.UUID{id}, MailboxIds: actor.Write, Trashed: true})
	if err != nil {
		writeError(w, r, fmt.Errorf("load conversation: %w", err))
		return
	}
	if len(rows) == 0 {
		// Restored or purged in between.
		writeError(w, r, errNotFound)
		return
	}
	detail, err := s.conversationDetail(ctx, rows[0], policy.Scope{Read: actor.Read, Write: actor.Write})
	if err != nil {
		writeError(w, r, err)
		return
	}
	detail.Conversation.CanWrite = false
	events, err := s.conversationEvents(ctx, id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := struct {
		conversationDetailResponse
		Events    []conversationEventJSON `json:"events"`
		DeletedAt time.Time               `json:"deleted_at"`
		DeletedBy *refJSON                `json:"deleted_by"`
	}{conversationDetailResponse: detail, Events: events, DeletedAt: info.DeletedAt.Time.UTC()}
	if info.DeletedByID.Valid {
		out.DeletedBy = &refJSON{ID: uuidStr(info.DeletedByID), Name: info.DeletedByName.String}
	}
	writeJSON(w, http.StatusOK, out)
}

type trashedConversationJSON struct {
	ID            string          `json:"id"`
	Number        int64           `json:"number"`
	Subject       string          `json:"subject"`
	Status        string          `json:"status"`
	Preview       string          `json:"preview"`
	LastMessageAt time.Time       `json:"last_message_at"`
	MessageCount  int32           `json:"message_count"`
	Mailbox       refJSON         `json:"mailbox"`
	Contact       *contactRefJSON `json:"contact"`
	DeletedAt     time.Time       `json:"deleted_at"`
	DeletedBy     *refJSON        `json:"deleted_by"`
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	mailboxes := actor.Write
	if v := q.Get("mailbox_id"); v != "" {
		id, ok := parseUUID(v)
		if !ok {
			writeError(w, r, errBadRequest("mailbox_id must be a UUID"))
			return
		}
		mailboxes = slices.DeleteFunc(slices.Clone(actor.Write), func(m pgtype.UUID) bool { return m != id })
	}
	limit := int32(defaultPageSize)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > maxPageSize {
			writeError(w, r, errBadRequest("limit must be between 1 and "+strconv.Itoa(maxPageSize)))
			return
		}
		limit = int32(n)
	}
	arg := dbq.TrashListPageParams{MailboxIds: mailboxes, PageSize: limit + 1}
	if v := q.Get("cursor"); v != "" {
		c, err := decodeCursor(v)
		if err != nil {
			writeError(w, r, err)
			return
		}
		arg.CursorAt, arg.CursorID = pgtype.Timestamptz{Time: c.at, Valid: true}, c.id
	}

	out := struct {
		Conversations []trashedConversationJSON `json:"conversations"`
		NextCursor    *string                   `json:"next_cursor"`
	}{Conversations: []trashedConversationJSON{}}
	if len(mailboxes) == 0 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	rows, err := s.q.TrashListPage(r.Context(), arg)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := pageCursor{at: last.DeletedAt.Time, id: last.ID}.encode()
		out.NextCursor = &next
	}
	for _, row := range rows {
		c := trashedConversationJSON{
			ID: uuidStr(row.ID), Number: row.Number, Subject: row.Subject, Status: row.Status, Preview: row.Preview,
			LastMessageAt: row.LastMessageAt.Time.UTC(), MessageCount: row.MessageCount,
			Mailbox: refJSON{ID: uuidStr(row.MailboxID), Name: row.MailboxName}, DeletedAt: row.DeletedAt.Time.UTC(),
		}
		if row.ContactID.Valid {
			c.Contact = &contactRefJSON{ID: uuidStr(row.ContactID), Name: row.ContactName.String, Email: row.ContactEmail}
		}
		if row.DeletedByID.Valid {
			c.DeletedBy = &refJSON{ID: uuidStr(row.DeletedByID), Name: row.DeletedByName.String}
		}
		out.Conversations = append(out.Conversations, c)
	}
	writeJSON(w, http.StatusOK, out)
}

type trashIDsRequest struct {
	IDs []string `json:"ids"`
}

// decodeTrashIDs reads the ids of a trash call. Unparsable ids come back as results of their
// own, like in a bulk change.
func decodeTrashIDs(w http.ResponseWriter, r *http.Request) ([]pgtype.UUID, []bulkResultJSON, bool) {
	var req trashIDsRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return nil, nil, false
	}
	if len(req.IDs) == 0 || len(req.IDs) > inbox.MaxBulk {
		writeError(w, r, errValidation(map[string]string{"ids": "count"}))
		return nil, nil, false
	}
	valid, results := parseBulkIDs(req.IDs)
	return valid, results, true
}

func (s *Server) moveToTrash(w http.ResponseWriter, r *http.Request) {
	s.trashChange(w, r, s.inbox.Trash)
}

func (s *Server) restoreFromTrash(w http.ResponseWriter, r *http.Request) {
	s.trashChange(w, r, s.inbox.Restore)
}

func (s *Server) trashChange(w http.ResponseWriter, r *http.Request, change func(ctx context.Context, actor inbox.Actor, ids []pgtype.UUID) ([]inbox.BulkResult, error)) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	ids, results, ok := decodeTrashIDs(w, r)
	if !ok {
		return
	}
	applied, err := change(r.Context(), actor, ids)
	if err != nil {
		writeError(w, r, inboxError(err))
		return
	}
	for _, res := range applied {
		out := bulkResultJSON{ID: uuidStr(res.ID), OK: res.Err == nil, Version: res.Version}
		if res.Err != nil {
			out.Code = bulkCode(r, res.Err)
		}
		results = append(results, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) retentionService() (*retention.Service, error) {
	blobs, err := s.blobs()
	if err != nil {
		return nil, err
	}
	return retention.NewService(s.pool, blobs), nil
}

// drainAfterPurge deletes the files of what was just purged. The rows are gone already, so a
// storage failure is logged and left to the hourly job instead of failing the request.
func drainAfterPurge(r *http.Request, svc *retention.Service) {
	if err := svc.Drain(r.Context()); err != nil {
		slog.WarnContext(r.Context(), "files of purged conversations not deleted yet", "err", err, "request_id", requestIDFrom(r.Context()))
	}
}

func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	svc, err := s.retentionService()
	if err != nil {
		writeError(w, r, err)
		return
	}
	ids, results, ok := decodeTrashIDs(w, r)
	if !ok {
		return
	}
	by := retention.By{UserID: actor.UserID, IP: clientFrom(r).IP}
	outcomes, err := svc.PurgeTrashed(r.Context(), ids, actor.Write, by)
	if err != nil {
		writeError(w, r, err)
		return
	}
	purged := false
	seen := make(map[pgtype.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out := bulkResultJSON{ID: uuidStr(id)}
		switch outcomes[id] {
		case retention.Purged:
			out.OK, purged = true, true
		case retention.StillSending:
			out.Code = "sending"
		case retention.NotInTrash:
			out.Code = "not_found"
		}
		results = append(results, out)
	}
	if purged {
		drainAfterPurge(r, svc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) emptyTrash(w http.ResponseWriter, r *http.Request) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req struct {
		MailboxID *string `json:"mailbox_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	mailboxes := actor.Write
	if req.MailboxID != nil {
		id, ok := parseUUID(*req.MailboxID)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"mailbox_id": "invalid"}))
			return
		}
		if !slices.Contains(actor.Write, id) {
			writeError(w, r, errNotFound)
			return
		}
		mailboxes = []pgtype.UUID{id}
	}
	svc, err := s.retentionService()
	if err != nil {
		writeError(w, r, err)
		return
	}
	n, err := svc.EmptyTrash(r.Context(), mailboxes, retention.By{UserID: actor.UserID, IP: clientFrom(r).IP})
	if n > 0 {
		drainAfterPurge(r, svc)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}
