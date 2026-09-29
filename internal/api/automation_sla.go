package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/automation"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/sla"
)

const maxHolidays = 200

type businessHoursJSON struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Timezone  string                 `json:"timezone"`
	Weekly    map[string][]sla.Range `json:"weekly"`
	Holidays  []string               `json:"holidays"`
	IsDefault bool                   `json:"is_default"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

func toBusinessHoursJSON(row dbq.BusinessHour) (businessHoursJSON, error) {
	def, err := automation.DefinitionFromRow(row)
	if err != nil {
		return businessHoursJSON{}, err
	}
	out := businessHoursJSON{
		ID: uuidStr(row.ID), Name: row.Name, Timezone: row.Timezone, Weekly: def.Weekly, Holidays: []string{},
		IsDefault: row.IsDefault, CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	for _, h := range def.Holidays {
		out.Holidays = append(out.Holidays, h.String())
	}
	return out, nil
}

func (s *Server) listBusinessHours(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.AutoListBusinessHours(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]businessHoursJSON, len(rows))
	for i, row := range rows {
		if out[i], err = toBusinessHoursJSON(row); err != nil {
			writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"business_hours": out})
}

type businessHoursRequest struct {
	Name      *string                `json:"name"`
	Timezone  *string                `json:"timezone"`
	Weekly    map[string][]sla.Range `json:"weekly"`
	Holidays  *[]string              `json:"holidays"`
	IsDefault *bool                  `json:"is_default"`
}

// build merges the request over the stored schedule and validates it with the calculator, so
// a schedule that was accepted can always be used for SLA deadlines.
func (req businessHoursRequest) build(base dbq.BusinessHour) (dbq.AutoInsertBusinessHoursParams, error) {
	fields := map[string]string{}
	p := dbq.AutoInsertBusinessHoursParams{Name: base.Name, Timezone: base.Timezone, IsDefault: base.IsDefault}
	def := sla.Definition{Timezone: base.Timezone}
	if base.ID.Valid {
		var err error
		if def, err = automation.DefinitionFromRow(base); err != nil {
			return p, err
		}
	}
	if req.Name != nil {
		name, ok := cleanText(*req.Name, 100)
		if !ok {
			fields["name"] = "invalid"
		}
		p.Name = name
	}
	if req.Timezone != nil {
		def.Timezone = *req.Timezone
	}
	if req.Weekly != nil {
		def.Weekly = req.Weekly
	}
	if req.Holidays != nil {
		def.Holidays = nil
		if len(*req.Holidays) > maxHolidays {
			fields["holidays"] = "too_many"
		}
		for _, h := range *req.Holidays {
			d, err := sla.ParseDate(h)
			if err != nil {
				fields["holidays"] = "invalid"
				break
			}
			def.Holidays = append(def.Holidays, d)
		}
	}
	if req.IsDefault != nil {
		p.IsDefault = *req.IsDefault
	}
	if _, err := sla.New(def); err != nil {
		switch {
		case strings.Contains(err.Error(), "timezone"):
			fields["timezone"] = "invalid"
		case errors.Is(err, sla.ErrNoBusinessTime):
			fields["weekly"] = "empty"
		default:
			fields["weekly"] = "invalid"
		}
	}
	if len(fields) > 0 {
		return p, errValidation(fields)
	}
	weekly := map[string][]sla.Range{}
	for _, day := range sla.Weekdays {
		if len(def.Weekly[day]) > 0 {
			weekly[day] = def.Weekly[day]
		}
	}
	// Marshaling plain strings cannot fail.
	p.Weekly, _ = json.Marshal(weekly)
	p.Timezone = def.Timezone
	slices.SortFunc(def.Holidays, func(a, b sla.Date) int { return strings.Compare(a.String(), b.String()) })
	p.Holidays = make([]pgtype.Date, 0, len(def.Holidays))
	for _, h := range def.Holidays {
		p.Holidays = append(p.Holidays, pgtype.Date{Time: time.Date(h.Year, h.Month, h.Day, 0, 0, 0, 0, time.UTC), Valid: true})
	}
	return p, nil
}

func (s *Server) createBusinessHours(w http.ResponseWriter, r *http.Request) {
	var req businessHoursRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Name == nil || req.Timezone == nil || req.Weekly == nil {
		fields := map[string]string{}
		for name, missing := range map[string]bool{"name": req.Name == nil, "timezone": req.Timezone == nil, "weekly": req.Weekly == nil} {
			if missing {
				fields[name] = "required"
			}
		}
		writeError(w, r, errValidation(fields))
		return
	}
	p, err := req.build(dbq.BusinessHour{})
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var row dbq.BusinessHour
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		existing, err := q.AutoListBusinessHours(r.Context())
		if err != nil {
			return err
		}
		// The first schedule becomes the default; later ones only when asked.
		p.IsDefault = p.IsDefault || len(existing) == 0
		if p.IsDefault {
			if err := q.AutoClearDefaultBusinessHours(r.Context(), pgtype.UUID{}); err != nil {
				return err
			}
		}
		row, err = q.AutoInsertBusinessHours(r.Context(), p)
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationBusinessHoursCreated, TargetType: "business_hours", TargetID: uuidStr(row.ID), Metadata: map[string]any{"name": row.Name}})
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	s.respondBusinessHours(w, r, http.StatusCreated, row, err)
}

func (s *Server) updateBusinessHours(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req businessHoursRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var row dbq.BusinessHour
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		base, err := q.AutoGetBusinessHours(r.Context(), id)
		if err != nil {
			return err
		}
		p, err := req.build(base)
		if err != nil {
			return err
		}
		if base.IsDefault && !p.IsDefault {
			return errValidation(map[string]string{"is_default": "one_default_required"})
		}
		if p.IsDefault {
			if err := q.AutoClearDefaultBusinessHours(r.Context(), id); err != nil {
				return err
			}
		}
		row, err = q.AutoUpdateBusinessHours(r.Context(), dbq.AutoUpdateBusinessHoursParams{
			ID: id, Name: p.Name, Timezone: p.Timezone, Weekly: p.Weekly, Holidays: p.Holidays, IsDefault: p.IsDefault,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationBusinessHoursUpdated, TargetType: "business_hours", TargetID: uuidStr(id), Metadata: map[string]any{"name": row.Name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
	default:
		s.respondBusinessHours(w, r, http.StatusOK, row, err)
	}
}

func (s *Server) respondBusinessHours(w http.ResponseWriter, r *http.Request, status int, row dbq.BusinessHour, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := toBusinessHoursJSON(row)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, status, map[string]any{"business_hours": out})
}

func (s *Server) deleteBusinessHours(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		row, err := q.AutoGetBusinessHours(r.Context(), id)
		if err != nil {
			return err
		}
		if row.IsDefault {
			return errValidation(map[string]string{"is_default": "default_cannot_be_deleted"})
		}
		if _, err := q.AutoDeleteBusinessHours(r.Context(), id); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationBusinessHoursDeleted, TargetType: "business_hours", TargetID: uuidStr(id), Metadata: map[string]any{"name": row.Name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isForeignKeyViolation(err):
		writeError(w, r, &apiError{Status: http.StatusConflict, Code: "in_use", Message: "an SLA policy still uses these business hours"})
	case err != nil:
		writeError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type slaPolicyJSON struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	FirstResponseMinutes *int32    `json:"first_response_minutes"`
	ResolutionMinutes    *int32    `json:"resolution_minutes"`
	AtRiskPercent        int32     `json:"at_risk_percent"`
	BusinessHoursID      *string   `json:"business_hours_id"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func toSLAPolicyJSON(p dbq.SlaPolicy) slaPolicyJSON {
	out := slaPolicyJSON{
		ID: uuidStr(p.ID), Name: p.Name, AtRiskPercent: p.AtRiskPercent,
		CreatedAt: p.CreatedAt.Time.UTC(), UpdatedAt: p.UpdatedAt.Time.UTC(),
	}
	if p.FirstResponseMinutes.Valid {
		out.FirstResponseMinutes = &p.FirstResponseMinutes.Int32
	}
	if p.ResolutionMinutes.Valid {
		out.ResolutionMinutes = &p.ResolutionMinutes.Int32
	}
	if p.BusinessHoursID.Valid {
		id := uuidStr(p.BusinessHoursID)
		out.BusinessHoursID = &id
	}
	return out
}

func (s *Server) listSLAPolicies(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.AutoListSLAPolicies(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]slaPolicyJSON, len(rows))
	for i, row := range rows {
		out[i] = toSLAPolicyJSON(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sla_policies": out})
}

const maxSLAMinutes = 525600

type slaPolicyRequest struct {
	Name                 *string          `json:"name"`
	FirstResponseMinutes nullable[int32]  `json:"first_response_minutes"`
	ResolutionMinutes    nullable[int32]  `json:"resolution_minutes"`
	AtRiskPercent        *int32           `json:"at_risk_percent"`
	BusinessHoursID      nullable[string] `json:"business_hours_id"`
}

func (req slaPolicyRequest) build(base dbq.SlaPolicy) (dbq.AutoInsertSLAPolicyParams, error) {
	fields := map[string]string{}
	p := dbq.AutoInsertSLAPolicyParams{
		Name: base.Name, FirstResponseMinutes: base.FirstResponseMinutes, ResolutionMinutes: base.ResolutionMinutes,
		AtRiskPercent: base.AtRiskPercent, BusinessHoursID: base.BusinessHoursID,
	}
	if !base.ID.Valid {
		p.AtRiskPercent = 80
	}
	if req.Name != nil {
		name, ok := cleanText(*req.Name, 100)
		if !ok {
			fields["name"] = "invalid"
		}
		p.Name = name
	}
	for _, m := range []struct {
		field string
		in    nullable[int32]
		out   *pgtype.Int4
	}{{"first_response_minutes", req.FirstResponseMinutes, &p.FirstResponseMinutes}, {"resolution_minutes", req.ResolutionMinutes, &p.ResolutionMinutes}} {
		if !m.in.set {
			continue
		}
		*m.out = pgtype.Int4{}
		if m.in.value != nil {
			if *m.in.value < 1 || *m.in.value > maxSLAMinutes {
				fields[m.field] = "invalid"
			}
			*m.out = pgtype.Int4{Int32: *m.in.value, Valid: true}
		}
	}
	if !p.FirstResponseMinutes.Valid && !p.ResolutionMinutes.Valid {
		fields["first_response_minutes"] = "target_required"
	}
	if req.AtRiskPercent != nil {
		if *req.AtRiskPercent < 1 || *req.AtRiskPercent > 99 {
			fields["at_risk_percent"] = "invalid"
		}
		p.AtRiskPercent = *req.AtRiskPercent
	}
	if req.BusinessHoursID.set {
		p.BusinessHoursID = pgtype.UUID{}
		if req.BusinessHoursID.value != nil {
			id, ok := parseUUID(*req.BusinessHoursID.value)
			if !ok {
				fields["business_hours_id"] = "invalid"
			}
			p.BusinessHoursID = id
		}
	}
	if p.Name == "" && req.Name == nil {
		fields["name"] = "required"
	}
	if len(fields) > 0 {
		return p, errValidation(fields)
	}
	return p, nil
}

func (s *Server) createSLAPolicy(w http.ResponseWriter, r *http.Request) {
	var req slaPolicyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := req.build(dbq.SlaPolicy{})
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var row dbq.SlaPolicy
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if row, err = q.AutoInsertSLAPolicy(r.Context(), p); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationSLAPolicyCreated, TargetType: "sla_policy", TargetID: uuidStr(row.ID), Metadata: map[string]any{"name": row.Name}})
	})
	switch {
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"business_hours_id": "unknown"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, map[string]any{"sla_policy": toSLAPolicyJSON(row)})
	}
}

func (s *Server) updateSLAPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req slaPolicyRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var row dbq.SlaPolicy
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		base, err := q.AutoGetSLAPolicy(r.Context(), id)
		if err != nil {
			return err
		}
		p, err := req.build(base)
		if err != nil {
			return err
		}
		row, err = q.AutoUpdateSLAPolicy(r.Context(), dbq.AutoUpdateSLAPolicyParams{
			ID: id, Name: p.Name, FirstResponseMinutes: p.FirstResponseMinutes, ResolutionMinutes: p.ResolutionMinutes,
			AtRiskPercent: p.AtRiskPercent, BusinessHoursID: p.BusinessHoursID,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationSLAPolicyUpdated, TargetType: "sla_policy", TargetID: uuidStr(id), Metadata: map[string]any{"name": row.Name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"business_hours_id": "unknown"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"sla_policy": toSLAPolicyJSON(row)})
	}
}

func (s *Server) deleteSLAPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		name, err := q.AutoDeleteSLAPolicy(r.Context(), id)
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationSLAPolicyDeleted, TargetType: "sla_policy", TargetID: uuidStr(id), Metadata: map[string]any{"name": name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case err != nil:
		writeError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
