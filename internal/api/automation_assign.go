package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/automation"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

var (
	assignModes    = []string{"off", "round_robin", "balanced"}
	availabilities = []string{"online", "busy", "offline"}
)

const maxOpenLimit = 10000

type mailboxAutomationJSON struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	EmailAddress       string  `json:"email_address"`
	AutoAssignMode     string  `json:"auto_assign_mode"`
	DefaultSLAPolicyID *string `json:"default_sla_policy_id"`
	BusinessHoursID    *string `json:"business_hours_id"`
}

func optionalID(id pgtype.UUID) *string {
	if !id.Valid {
		return nil
	}
	s := uuidStr(id)
	return &s
}

type agentJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	MaxOpen      *int32 `json:"max_open"`
	Availability string `json:"availability"`
	OpenCount    int32  `json:"open_count"`
}

// getAssignment lists every mailbox and agent for owners and admins. Anyone else sees only the
// mailboxes they can write to and no agents, so the page does not become a directory.
func (s *Server) getAssignment(w http.ResponseWriter, r *http.Request) {
	actor := sessionFrom(r.Context()).User
	mailboxes, err := s.q.AutoListMailboxAutomation(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	agents, err := s.q.AutoListAgents(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !policy.SeesAll(actor) {
		scope, err := policy.MailboxScope(r.Context(), s.q, actor)
		if err != nil {
			writeError(w, r, err)
			return
		}
		mailboxes = slices.DeleteFunc(mailboxes, func(m dbq.AutoListMailboxAutomationRow) bool { return !slices.Contains(scope.Write, m.ID) })
		agents = nil
	}
	out := struct {
		Mailboxes []mailboxAutomationJSON `json:"mailboxes"`
		Agents    []agentJSON             `json:"agents"`
	}{Mailboxes: make([]mailboxAutomationJSON, len(mailboxes)), Agents: make([]agentJSON, len(agents))}
	for i, m := range mailboxes {
		out.Mailboxes[i] = mailboxAutomationJSON{
			ID: uuidStr(m.ID), Name: m.Name, EmailAddress: m.EmailAddress, AutoAssignMode: m.AutoAssignMode,
			DefaultSLAPolicyID: optionalID(m.DefaultSlaPolicyID), BusinessHoursID: optionalID(m.BusinessHoursID),
		}
	}
	for i, a := range agents {
		out.Agents[i] = agentJSON{ID: uuidStr(a.ID), Name: a.Name, Email: a.Email, Role: a.Role, Availability: a.Availability, OpenCount: a.OpenCount}
		if a.MaxOpen.Valid {
			out.Agents[i].MaxOpen = &a.MaxOpen.Int32
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) putMailboxAutomation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		AutoAssignMode     *string `json:"auto_assign_mode"`
		DefaultSLAPolicyID *string `json:"default_sla_policy_id"`
		BusinessHoursID    *string `json:"business_hours_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.AutoAssignMode == nil || !slices.Contains(assignModes, *req.AutoAssignMode) {
		fields["auto_assign_mode"] = "invalid"
	}
	params := dbq.AutoUpdateMailboxAutomationParams{ID: id}
	if req.AutoAssignMode != nil {
		params.AutoAssignMode = *req.AutoAssignMode
	}
	for _, f := range []struct {
		name string
		in   *string
		out  *pgtype.UUID
	}{{"default_sla_policy_id", req.DefaultSLAPolicyID, &params.DefaultSlaPolicyID}, {"business_hours_id", req.BusinessHoursID, &params.BusinessHoursID}} {
		if f.in == nil {
			continue
		}
		v, ok := parseUUID(*f.in)
		if !ok {
			fields[f.name] = "invalid"
		}
		*f.out = v
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	actor := sessionFrom(r.Context()).User
	var row dbq.AutoUpdateMailboxAutomationRow
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := requireWritableMailbox(r.Context(), q, actor, id); err != nil {
			return err
		}
		var err error
		if row, err = q.AutoUpdateMailboxAutomation(r.Context(), params); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationMailboxChanged, TargetType: "mailbox", TargetID: uuidStr(id), Metadata: map[string]any{"auto_assign_mode": row.AutoAssignMode}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"default_sla_policy_id": "unknown"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"mailbox": mailboxAutomationJSON{
			ID: uuidStr(row.ID), Name: row.Name, EmailAddress: row.EmailAddress, AutoAssignMode: row.AutoAssignMode,
			DefaultSLAPolicyID: optionalID(row.DefaultSlaPolicyID), BusinessHoursID: optionalID(row.BusinessHoursID),
		}})
	}
}

func (s *Server) putUserCapacity(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req struct {
		MaxOpen nullable[int32] `json:"max_open"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if !req.MaxOpen.set {
		writeError(w, r, errValidation(map[string]string{"max_open": "required"}))
		return
	}
	var maxOpen pgtype.Int4
	if v := req.MaxOpen.value; v != nil {
		if *v < 1 || *v > maxOpenLimit {
			writeError(w, r, errValidation(map[string]string{"max_open": "invalid"}))
			return
		}
		maxOpen = pgtype.Int4{Int32: *v, Valid: true}
	}
	actor := sessionFrom(r.Context()).User
	if err := requireSeesAll(actor); err != nil {
		writeError(w, r, err)
		return
	}
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if _, err := q.AutoSetUserCapacity(r.Context(), dbq.AutoSetUserCapacityParams{ID: id, MaxOpen: maxOpen}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationCapacityChanged, TargetType: "user", TargetID: uuidStr(id), Metadata: map[string]any{"max_open": req.MaxOpen.value}})
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

// putAvailability is the one setting an agent changes on their own account: whether new
// conversations may be assigned to them automatically.
func (s *Server) putAvailability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Availability string `json:"availability"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if !slices.Contains(availabilities, req.Availability) {
		writeError(w, r, errValidation(map[string]string{"availability": "invalid"}))
		return
	}
	user := sessionFrom(r.Context()).User
	if err := s.q.AutoSetUserAvailability(r.Context(), dbq.AutoSetUserAvailabilityParams{ID: user.ID, Availability: req.Availability}); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"availability": req.Availability})
}

const maxAutoResolveDays = 365

func (s *Server) getAutomationSettings(w http.ResponseWriter, r *http.Request) {
	set, err := automation.LoadSettings(r.Context(), s.q)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) putAutomationSettings(w http.ResponseWriter, r *http.Request) {
	var req automation.Settings
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.AutoResolveDays < 0 || req.AutoResolveDays > maxAutoResolveDays {
		writeError(w, r, errValidation(map[string]string{"auto_resolve_days": "invalid"}))
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	if err := requireSeesAll(actor); err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := q.UpsertSetting(r.Context(), dbq.UpsertSettingParams{Key: "automation", Value: raw, UpdatedBy: actor.ID}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationSettingsChanged, TargetType: "settings", TargetID: "automation", Metadata: map[string]any{"auto_resolve_days": req.AutoResolveDays}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}
