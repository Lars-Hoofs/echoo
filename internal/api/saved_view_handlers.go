package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/db/dbq"
	"echoo/internal/policy"
	"echoo/internal/search"
)

// A count above this is shown as "999+", so the count query never reads more rows than this.
const savedViewCountCap = 1000

func (s *Server) savedViewRoutes(r chi.Router) {
	r.Get("/saved-views", s.listSavedViews)
	r.Post("/saved-views", s.createSavedView)
	r.Patch("/saved-views/{id}", s.updateSavedView)
	r.Delete("/saved-views/{id}", s.deleteSavedView)
}

type savedViewJSON struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Scope    string         `json:"scope"`
	Team     *refJSON       `json:"team"`
	Filters  search.Filters `json:"filters"`
	Sort     string         `json:"sort"`
	Position int32          `json:"position"`
	// Editable tells the client whether the caller may change or delete the view.
	Editable bool `json:"editable"`
	// OpenCount is capped at 1000; null when the view does not list open conversations.
	OpenCount *int `json:"open_count"`
}

func viewScope(v dbq.SavedView) string {
	switch {
	case v.OwnerUserID.Valid:
		return "personal"
	case v.TeamID.Valid:
		return "team"
	default:
		return "everyone"
	}
}

// canSeeView reports whether user may see v at all.
func (s *Server) canSeeView(ctx context.Context, user dbq.User, v dbq.SavedView) (bool, error) {
	switch {
	case v.OwnerUserID.Valid:
		return v.OwnerUserID == user.ID, nil
	case !v.TeamID.Valid || policy.SeesAll(user):
		return true, nil
	}
	return s.q.IsTeamMember(ctx, dbq.IsTeamMemberParams{TeamID: v.TeamID, UserID: user.ID})
}

func canEditView(user dbq.User, v dbq.SavedView) bool {
	if v.OwnerUserID.Valid {
		return v.OwnerUserID == user.ID
	}
	return policy.Has(user, policy.TemplatesManage)
}

func (s *Server) toSavedViewJSON(user dbq.User, v dbq.SavedView, teams map[pgtype.UUID]string) (savedViewJSON, error) {
	filters, err := search.DecodeFilters(v.Filters)
	if err != nil {
		return savedViewJSON{}, fmt.Errorf("saved view %s: %w", uuidStr(v.ID), err)
	}
	out := savedViewJSON{
		ID: uuidStr(v.ID), Name: v.Name, Scope: viewScope(v), Filters: filters, Sort: v.Sort,
		Position: v.Position, Editable: canEditView(user, v),
	}
	if v.TeamID.Valid {
		out.Team = &refJSON{ID: uuidStr(v.TeamID), Name: teams[v.TeamID]}
	}
	return out, nil
}

func (s *Server) teamNames(ctx context.Context) (map[pgtype.UUID]string, error) {
	teams, err := s.q.ListTeams(ctx)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	names := make(map[pgtype.UUID]string, len(teams))
	for _, t := range teams {
		names[t.ID] = t.Name
	}
	return names, nil
}

func (s *Server) listSavedViews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.ListSavedViewsFor(ctx, dbq.ListSavedViewsForParams{UserID: user.ID, AllTeams: policy.SeesAll(user)})
	if err != nil {
		writeError(w, r, fmt.Errorf("list saved views: %w", err))
		return
	}
	teams, err := s.teamNames(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]savedViewJSON, 0, len(rows))
	for _, row := range rows {
		view, err := s.toSavedViewJSON(user, row, teams)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if view.Filters.Status == "" || view.Filters.Status == "open" {
			n, err := s.countView(ctx, user, scope, view.Filters)
			if err != nil {
				writeError(w, r, err)
				return
			}
			view.OpenCount = &n
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"views": out})
}

func (s *Server) countView(ctx context.Context, user dbq.User, scope policy.Scope, f search.Filters) (int, error) {
	crit, err := s.buildCriteria(ctx, user, scope, f, search.Query{}, true)
	if err != nil {
		return 0, err
	}
	if crit.none || len(crit.mailboxIDs) == 0 {
		return 0, nil
	}
	rows, err := s.q.ListConversationPageFiltered(ctx, crit.filteredParams(nil, savedViewCountCap))
	if err != nil {
		return 0, fmt.Errorf("count saved view: %w", err)
	}
	return len(rows), nil
}

type savedViewRequest struct {
	Name     *string         `json:"name"`
	Filters  *search.Filters `json:"filters"`
	Position *int32          `json:"position"`
	Scope    *string         `json:"scope"`
	TeamID   *string         `json:"team_id"`
}

// validate checks the fields in place and returns the invalid ones. Creating needs a name and
// filters; a partial update needs neither.
func (req *savedViewRequest) validate(creating bool) map[string]string {
	fields := map[string]string{}
	if req.Name != nil {
		if name, ok := cleanText(*req.Name, 60); ok {
			req.Name = &name
		} else {
			fields["name"] = "invalid"
		}
	} else if creating {
		fields["name"] = "required"
	}
	if req.Filters != nil {
		for name, code := range req.Filters.Validate() {
			fields["filters."+name] = code
		}
	} else if creating {
		fields["filters"] = "required"
	}
	if req.Position != nil && (*req.Position < 0 || *req.Position > 10000) {
		fields["position"] = "invalid"
	}
	if req.Scope != nil {
		switch *req.Scope {
		case "personal", "everyone":
			if req.TeamID != nil {
				fields["team_id"] = "invalid"
			}
		case "team":
			if req.TeamID == nil {
				fields["team_id"] = "required"
			} else if _, ok := parseUUID(*req.TeamID); !ok {
				fields["team_id"] = "invalid"
			}
		default:
			fields["scope"] = "invalid"
		}
	} else if creating {
		fields["scope"] = "required"
	}
	return fields
}

// ownership maps a scope to the owner and team columns. Sharing is an admin action.
func (req *savedViewRequest) ownership(user dbq.User) (owner, team pgtype.UUID, err error) {
	if *req.Scope == "personal" {
		return user.ID, pgtype.UUID{}, nil
	}
	if !policy.Has(user, policy.TemplatesManage) {
		return owner, team, errForbidden
	}
	if *req.Scope == "team" {
		team, _ = parseUUID(*req.TeamID)
	}
	return owner, team, nil
}

func (s *Server) createSavedView(w http.ResponseWriter, r *http.Request) {
	var req savedViewRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if fields := req.validate(true); len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	ctx := r.Context()
	user := sessionFrom(ctx).User
	owner, team, err := req.ownership(user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	raw, err := json.Marshal(req.Filters)
	if err != nil {
		writeError(w, r, fmt.Errorf("encode filters: %w", err))
		return
	}
	view, err := s.q.InsertSavedView(ctx, dbq.InsertSavedViewParams{
		Name: *req.Name, OwnerUserID: owner, TeamID: team, CreatedBy: user.ID, Filters: raw,
	})
	s.writeSavedView(w, r, user, view, err, http.StatusCreated)
}

func (s *Server) writeSavedView(w http.ResponseWriter, r *http.Request, user dbq.User, view dbq.SavedView, err error, status int) {
	switch {
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"team_id": "invalid"}))
		return
	case err != nil:
		writeError(w, r, err)
		return
	}
	teams, err := s.teamNames(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.toSavedViewJSON(user, view, teams)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"view": out})
}

// loadEditableView returns the view when the caller may see it, and errForbidden when they may
// see it but not change it. A view the caller may not see is reported as missing.
func (s *Server) loadEditableView(r *http.Request, user dbq.User) (dbq.SavedView, error) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return dbq.SavedView{}, errNotFound
	}
	view, err := s.q.GetSavedView(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbq.SavedView{}, errNotFound
	}
	if err != nil {
		return dbq.SavedView{}, fmt.Errorf("load saved view: %w", err)
	}
	visible, err := s.canSeeView(r.Context(), user, view)
	if err != nil {
		return dbq.SavedView{}, fmt.Errorf("check saved view access: %w", err)
	}
	if !visible {
		return dbq.SavedView{}, errNotFound
	}
	if !canEditView(user, view) {
		return dbq.SavedView{}, errForbidden
	}
	return view, nil
}

func (s *Server) updateSavedView(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	view, err := s.loadEditableView(r, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req savedViewRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if fields := req.validate(false); len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	params := dbq.UpdateSavedViewParams{ID: view.ID}
	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Filters != nil {
		raw, err := json.Marshal(req.Filters)
		if err != nil {
			writeError(w, r, fmt.Errorf("encode filters: %w", err))
			return
		}
		params.Filters = raw
	}
	if req.Position != nil {
		params.Position = pgtype.Int4{Int32: *req.Position, Valid: true}
	}
	if req.Scope != nil {
		owner, team, err := req.ownership(user)
		if err != nil {
			writeError(w, r, err)
			return
		}
		params.SetScope, params.OwnerUserID, params.TeamID = true, owner, team
	}
	updated, err := s.q.UpdateSavedView(r.Context(), params)
	s.writeSavedView(w, r, user, updated, err, http.StatusOK)
}

func (s *Server) deleteSavedView(w http.ResponseWriter, r *http.Request) {
	view, err := s.loadEditableView(r, sessionFrom(r.Context()).User)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if _, err := s.q.RemoveSavedView(r.Context(), view.ID); err != nil {
		writeError(w, r, fmt.Errorf("delete saved view: %w", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
