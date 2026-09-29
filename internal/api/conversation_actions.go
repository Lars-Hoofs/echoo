package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/policy"
)

const maxLabelsPerRequest = 100

type labelRefJSON struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ColorToken string `json:"color_token"`
}

// conversationLabels returns the labels of the given conversations, keyed by conversation id.
func (s *Server) conversationLabels(ctx context.Context, ids []pgtype.UUID) (map[pgtype.UUID][]labelRefJSON, error) {
	rows, err := s.q.ListLabelsForConversations(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load labels: %w", err)
	}
	out := make(map[pgtype.UUID][]labelRefJSON, len(ids))
	for _, r := range rows {
		out[r.ConversationID] = append(out[r.ConversationID], labelRefJSON{ID: uuidStr(r.ID), Name: r.Name, ColorToken: r.ColorToken})
	}
	return out, nil
}

func (s *Server) attachLabels(ctx context.Context, list []conversationJSON) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, len(list))
	for i, c := range list {
		ids[i], _ = parseUUID(c.ID)
	}
	labels, err := s.conversationLabels(ctx, ids)
	if err != nil {
		return err
	}
	for i := range list {
		if l := labels[ids[i]]; l != nil {
			list[i].Labels = l
		}
	}
	return nil
}

// nullable distinguishes a JSON field that is absent from one that is null.
type nullable[T any] struct {
	set   bool
	value *T
}

func (n *nullable[T]) UnmarshalJSON(b []byte) error {
	n.set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		n.value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	n.value = &v
	return nil
}

// changeFields is the shared shape of a single-conversation PATCH and a bulk action.
type changeFields struct {
	status, priority *string
	assigneeUser     nullable[string]
	assigneeTeam     nullable[string]
	snooze           nullable[time.Time]
	addLabels        []string
	removeLabels     []string
}

func parseUUIDs(field string, raw []string, fields map[string]string) []pgtype.UUID {
	if len(raw) > maxLabelsPerRequest {
		fields[field] = "too_many"
		return nil
	}
	out := make([]pgtype.UUID, 0, len(raw))
	for _, v := range raw {
		id, ok := parseUUID(v)
		if !ok {
			fields[field] = "invalid"
			return nil
		}
		out = append(out, id)
	}
	return out
}

func nullableUUID(field string, n nullable[string], fields map[string]string) *pgtype.UUID {
	if !n.set {
		return nil
	}
	var id pgtype.UUID
	if n.value != nil {
		var ok bool
		if id, ok = parseUUID(*n.value); !ok {
			fields[field] = "invalid"
			return nil
		}
	}
	return &id
}

func (f changeFields) toChange() (inbox.Change, error) {
	fields := map[string]string{}
	ch := inbox.Change{Status: f.status, Priority: f.priority}
	if f.status != nil && !inbox.ValidStatus(*f.status) {
		fields["status"] = "invalid"
	}
	if f.priority != nil && !inbox.ValidPriority(*f.priority) {
		fields["priority"] = "invalid"
	}
	ch.Assignee = nullableUUID("assignee_user_id", f.assigneeUser, fields)
	ch.Team = nullableUUID("assignee_team_id", f.assigneeTeam, fields)
	if f.snooze.set {
		until := pgtype.Timestamptz{}
		if f.snooze.value != nil {
			until = pgtype.Timestamptz{Time: *f.snooze.value, Valid: true}
		}
		ch.SnoozedUntil = &until
	}
	ch.AddLabels = parseUUIDs("add_label_ids", f.addLabels, fields)
	ch.RemoveLabels = parseUUIDs("remove_label_ids", f.removeLabels, fields)
	if len(fields) > 0 {
		return ch, errValidation(fields)
	}
	return ch, nil
}

// inboxActor resolves what the session user may read and write. Readonly users are refused
// before any lookup, so a mutation never even reveals whether a conversation exists.
func (s *Server) inboxActor(r *http.Request) (inbox.Actor, error) {
	user := sessionFrom(r.Context()).User
	if !policy.Has(user, policy.ConversationsWrite) {
		return inbox.Actor{}, errForbidden
	}
	scope, err := policy.MailboxScope(r.Context(), s.q, user)
	if err != nil {
		return inbox.Actor{}, err
	}
	return inbox.Actor{UserID: user.ID, Read: scope.Read, Write: scope.Write}, nil
}

// changeRights checks the finer permissions behind a change: handing a conversation to
// someone and marking it as spam are separate rights on top of conversations.write.
func changeRights(u dbq.User, ch inbox.Change) error {
	if (ch.Assignee != nil || ch.Team != nil) && !policy.Has(u, policy.ConversationsAssign) {
		return errForbidden
	}
	if ch.Status != nil && *ch.Status == inbox.StatusSpam && !policy.Has(u, policy.ConversationsDelete) {
		return errForbidden
	}
	return nil
}

// inboxError maps domain errors to API errors; anything else is an internal error.
func inboxError(err error) error {
	var conflict *inbox.VersionConflictError
	switch {
	case errors.Is(err, inbox.ErrNotFound):
		return errNotFound
	case errors.Is(err, inbox.ErrForbidden):
		return errForbidden
	case errors.As(err, &conflict):
		return &apiError{Status: http.StatusConflict, Code: "version_conflict", Message: "the conversation changed since it was loaded", CurrentVersion: &conflict.Current}
	case errors.Is(err, inbox.ErrAssigneeNoAccess):
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "assignee_no_access", Message: err.Error()}
	case errors.Is(err, inbox.ErrTeamNoAccess):
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "team_no_access", Message: err.Error()}
	case errors.Is(err, inbox.ErrNotSnoozable):
		return &apiError{Status: http.StatusUnprocessableEntity, Code: "not_snoozable", Message: err.Error()}
	case errors.Is(err, inbox.ErrUnknownLabel):
		return errValidation(map[string]string{"label_ids": "unknown"})
	case errors.Is(err, inbox.ErrInvalidSnooze):
		return errValidation(map[string]string{"snoozed_until": "invalid"})
	case errors.Is(err, inbox.ErrInvalidStatus):
		return errValidation(map[string]string{"status": "invalid"})
	case errors.Is(err, inbox.ErrInvalidPriority):
		return errValidation(map[string]string{"priority": "invalid"})
	}
	return err
}

func (s *Server) patchConversation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status          *string             `json:"status"`
		Priority        *string             `json:"priority"`
		AssigneeUserID  nullable[string]    `json:"assignee_user_id"`
		AssigneeTeamID  nullable[string]    `json:"assignee_team_id"`
		SnoozedUntil    nullable[time.Time] `json:"snoozed_until"`
		ExpectedVersion *int32              `json:"expected_version"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor, err := s.inboxActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	ch, err := changeFields{
		status: req.Status, priority: req.Priority, assigneeUser: req.AssigneeUserID,
		assigneeTeam: req.AssigneeTeamID, snooze: req.SnoozedUntil,
	}.toChange()
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := changeRights(sessionFrom(r.Context()).User, ch); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := s.inbox.Apply(r.Context(), actor, id, req.ExpectedVersion, ch)
	if err != nil {
		writeError(w, r, inboxError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version})
}

func (s *Server) putConversationLabels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LabelIDs        []string `json:"label_ids"`
		ExpectedVersion *int32   `json:"expected_version"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor, err := s.inboxActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	fields := map[string]string{}
	labels := parseUUIDs("label_ids", req.LabelIDs, fields)
	if req.LabelIDs == nil {
		fields["label_ids"] = "required"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	version, err := s.inbox.Apply(r.Context(), actor, id, req.ExpectedVersion, inbox.Change{SetLabels: &labels})
	if err != nil {
		writeError(w, r, inboxError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version})
}

type bulkResultJSON struct {
	ID      string `json:"id"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Version int32  `json:"version,omitempty"`
}

func (s *Server) bulkConversations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		Action struct {
			Status         *string             `json:"status"`
			Priority       *string             `json:"priority"`
			AssigneeUserID nullable[string]    `json:"assignee_user_id"`
			AssigneeTeamID nullable[string]    `json:"assignee_team_id"`
			SnoozeUntil    nullable[time.Time] `json:"snooze_until"`
			AddLabelIDs    []string            `json:"add_label_ids"`
			RemoveLabelIDs []string            `json:"remove_label_ids"`
		} `json:"action"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor, err := s.inboxActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > inbox.MaxBulk {
		writeError(w, r, errValidation(map[string]string{"ids": "count"}))
		return
	}
	a := req.Action
	ch, err := changeFields{
		status: a.Status, priority: a.Priority, assigneeUser: a.AssigneeUserID, assigneeTeam: a.AssigneeTeamID,
		snooze: a.SnoozeUntil, addLabels: a.AddLabelIDs, removeLabels: a.RemoveLabelIDs,
	}.toChange()
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := changeRights(sessionFrom(r.Context()).User, ch); err != nil {
		writeError(w, r, err)
		return
	}
	if ch.Status == nil && ch.Priority == nil && ch.Assignee == nil && ch.Team == nil && ch.SnoozedUntil == nil &&
		len(ch.AddLabels) == 0 && len(ch.RemoveLabels) == 0 {
		writeError(w, r, errValidation(map[string]string{"action": "empty"}))
		return
	}

	results := make([]bulkResultJSON, 0, len(req.IDs))
	valid := make([]pgtype.UUID, 0, len(req.IDs))
	invalid := map[string]bool{}
	for _, raw := range req.IDs {
		id, ok := parseUUID(raw)
		if !ok {
			if !invalid[raw] {
				invalid[raw] = true
				results = append(results, bulkResultJSON{ID: raw, Code: "invalid_id"})
			}
			continue
		}
		valid = append(valid, id)
	}
	applied, err := s.inbox.Bulk(r.Context(), actor, valid, ch)
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

func bulkCode(r *http.Request, err error) string {
	var ae *apiError
	if errors.As(inboxError(err), &ae) {
		return ae.Code
	}
	slog.ErrorContext(r.Context(), "bulk conversation change failed", "err", err, "request_id", requestIDFrom(r.Context()))
	return "internal"
}

type conversationEventJSON struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Actor     *refJSON        `json:"actor"`
	User      *refJSON        `json:"user"`
	Data      json.RawMessage `json:"data"`
}

func (s *Server) listConversationEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, sessionFrom(ctx).User)
	if err != nil {
		writeError(w, r, err)
		return
	}
	readable, err := s.q.ConversationInScope(ctx, dbq.ConversationInScopeParams{ID: id, MailboxIds: scope.Read})
	if err != nil {
		writeError(w, r, fmt.Errorf("check conversation scope: %w", err))
		return
	}
	if !readable {
		writeError(w, r, errNotFound)
		return
	}
	rows, err := s.q.ListConversationEvents(ctx, id)
	if err != nil {
		writeError(w, r, fmt.Errorf("load events: %w", err))
		return
	}
	out := make([]conversationEventJSON, len(rows))
	for i, e := range rows {
		out[i] = conversationEventJSON{ID: uuidStr(e.ID), Type: e.Type, CreatedAt: e.CreatedAt.Time.UTC(), Data: e.Data}
		if e.ActorID.Valid {
			out[i].Actor = &refJSON{ID: uuidStr(e.ActorID), Name: e.ActorName.String}
		}
		if e.UserID.Valid {
			out[i].User = &refJSON{ID: uuidStr(e.UserID), Name: e.UserName.String}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

func (s *Server) listAssignees(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mailboxID, ok := parseUUID(r.URL.Query().Get("mailbox_id"))
	if !ok {
		writeError(w, r, errBadRequest("mailbox_id must be a UUID"))
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, sessionFrom(ctx).User)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !slices.Contains(scope.Read, mailboxID) {
		writeError(w, r, errNotFound)
		return
	}
	users, err := s.q.ListMailboxAssignees(ctx, mailboxID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list assignees: %w", err))
		return
	}
	teams, err := s.q.ListMailboxTeams(ctx, mailboxID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list mailbox teams: %w", err))
		return
	}
	out := struct {
		Users []refJSON `json:"users"`
		Teams []refJSON `json:"teams"`
	}{Users: make([]refJSON, len(users)), Teams: make([]refJSON, len(teams))}
	for i, u := range users {
		out.Users[i] = refJSON{ID: uuidStr(u.ID), Name: u.Name}
	}
	for i, t := range teams {
		out.Teams[i] = refJSON{ID: uuidStr(t.ID), Name: t.Name}
	}
	writeJSON(w, http.StatusOK, out)
}
