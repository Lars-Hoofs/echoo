package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

type segmentJSON struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Shared    bool            `json:"shared"`
	Owner     *refJSON        `json:"owner"`
	Filter    json.RawMessage `json:"filter"`
	CanEdit   bool            `json:"can_edit"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func canEditSegment(u dbq.User, owner pgtype.UUID) bool {
	return policy.Has(u, policy.ContactsWrite) && (owner == u.ID || policy.Has(u, policy.ContactsModerate))
}

func (s *Server) listSegments(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	rows, err := s.q.ListSegments(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list segments: %w", err))
		return
	}
	out := make([]segmentJSON, len(rows))
	for i, row := range rows {
		out[i] = segmentJSON{
			ID: uuidStr(row.ID), Name: row.Name, Shared: row.Shared, Owner: &refJSON{ID: uuidStr(row.OwnerUserID), Name: row.OwnerName},
			Filter: row.Filter, CanEdit: canEditSegment(user, row.OwnerUserID), CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"segments": out})
}

type segmentRequest struct {
	Name   *string          `json:"name"`
	Shared *bool            `json:"shared"`
	Filter *json.RawMessage `json:"filter"`
}

// cleanSegmentFilter validates the filter against the current attribute definitions and
// returns it re-encoded in canonical form.
func (s *Server) cleanSegmentFilter(r *http.Request, raw json.RawMessage) ([]byte, error) {
	defs, err := s.attributeDefs(r.Context(), "")
	if err != nil {
		return nil, err
	}
	if len(raw) > maxFilterBytes {
		return nil, errValidation(map[string]string{"filter": "too_large"})
	}
	f, err := contacts.ParseFilter(raw, defs)
	if err != nil {
		return nil, errValidation(map[string]string{"filter": "invalid"})
	}
	if f.Conditions == nil {
		f.Conditions = []contacts.Condition{}
	}
	out, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode filter: %w", err)
	}
	return out, nil
}

func cleanSegmentName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= 80 && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func (s *Server) createSegment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	if err := requireModifier(user); err != nil {
		writeError(w, r, err)
		return
	}
	var req segmentRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	name := ""
	if req.Name != nil {
		var ok bool
		if name, ok = cleanSegmentName(*req.Name); !ok {
			fields["name"] = "invalid"
		}
	} else {
		fields["name"] = "required"
	}
	if req.Filter == nil {
		fields["filter"] = "required"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	filter, err := s.cleanSegmentFilter(r, *req.Filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	shared := req.Shared != nil && *req.Shared
	var id pgtype.UUID
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		var err error
		id, err = q.InsertSegment(ctx, dbq.InsertSegmentParams{Name: name, OwnerUserID: user.ID, Shared: shared, Filter: filter})
		if err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.SegmentCreated, TargetType: "contact_segment", TargetID: uuidStr(id), Metadata: map[string]any{"shared": shared}})
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondSegment(w, r, id, http.StatusCreated)
}

func (s *Server) respondSegment(w http.ResponseWriter, r *http.Request, id pgtype.UUID, status int) {
	user := sessionFrom(r.Context()).User
	rows, err := s.q.ListSegments(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, fmt.Errorf("list segments: %w", err))
		return
	}
	for _, row := range rows {
		if row.ID == id {
			writeJSON(w, status, map[string]any{"segment": segmentJSON{
				ID: uuidStr(row.ID), Name: row.Name, Shared: row.Shared, Owner: &refJSON{ID: uuidStr(row.OwnerUserID), Name: row.OwnerName},
				Filter: row.Filter, CanEdit: canEditSegment(user, row.OwnerUserID), CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
			}})
			return
		}
	}
	writeError(w, r, errNotFound)
}

// editableSegment loads a segment the caller may see and change. A personal segment of
// someone else is invisible (404), a shared one of someone else is visible but not editable.
func (s *Server) editableSegment(r *http.Request) (dbq.ContactSegment, error) {
	user := sessionFrom(r.Context()).User
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return dbq.ContactSegment{}, errNotFound
	}
	seg, err := s.q.GetSegment(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && seg.OwnerUserID != user.ID && !seg.Shared) {
		return dbq.ContactSegment{}, errNotFound
	}
	if err != nil {
		return dbq.ContactSegment{}, fmt.Errorf("load segment: %w", err)
	}
	if !canEditSegment(user, seg.OwnerUserID) {
		return dbq.ContactSegment{}, errForbidden
	}
	return seg, nil
}

func (s *Server) updateSegment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	seg, err := s.editableSegment(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req segmentRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	params := dbq.UpdateSegmentParams{ID: seg.ID, Name: seg.Name, Shared: seg.Shared, Filter: seg.Filter}
	if req.Name != nil {
		name, ok := cleanSegmentName(*req.Name)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"name": "invalid"}))
			return
		}
		params.Name = name
	}
	if req.Shared != nil {
		params.Shared = *req.Shared
	}
	if req.Filter != nil {
		if params.Filter, err = s.cleanSegmentFilter(r, *req.Filter); err != nil {
			writeError(w, r, err)
			return
		}
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := q.UpdateSegment(ctx, params); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.SegmentUpdated, TargetType: "contact_segment", TargetID: uuidStr(seg.ID), Metadata: map[string]any{"shared": params.Shared}})
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.respondSegment(w, r, seg.ID, http.StatusOK)
}

func (s *Server) deleteSegment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := sessionFrom(ctx).User
	seg, err := s.editableSegment(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		if err := q.DeleteSegment(ctx, seg.ID); err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.SegmentDeleted, TargetType: "contact_segment", TargetID: uuidStr(seg.ID)})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
