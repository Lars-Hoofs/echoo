package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/automation"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/inbox"
	"echoo/internal/policy"
)

type macroJSON struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Scope     string          `json:"scope"`
	Owner     bool            `json:"owner"`
	Actions   json.RawMessage `json:"actions"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func toMacroJSON(m dbq.Macro, viewer pgtype.UUID) macroJSON {
	return macroJSON{
		ID: uuidStr(m.ID), Name: m.Name, Scope: m.Scope, Owner: m.OwnerUserID == viewer, Actions: m.Actions,
		CreatedAt: m.CreatedAt.Time.UTC(), UpdatedAt: m.UpdatedAt.Time.UTC(),
	}
}

func (s *Server) listMacros(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	rows, err := s.q.AutoListMacros(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]macroJSON, len(rows))
	for i, m := range rows {
		out[i] = toMacroJSON(m, user.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"macros": out})
}

// canEditMacro: personal macros belong to their owner, workspace macros to the admins.
func canEditMacro(user dbq.User, m dbq.Macro) bool {
	if m.Scope == "global" {
		return policy.Has(user, policy.TemplatesManage)
	}
	return m.OwnerUserID == user.ID && policy.Has(user, policy.ConversationsWrite)
}

type macroRequest struct {
	Name    *string         `json:"name"`
	Scope   *string         `json:"scope"`
	Actions json.RawMessage `json:"actions"`
}

func (req macroRequest) validate(base dbq.Macro, user dbq.User) (dbq.AutoInsertMacroParams, error) {
	fields := map[string]string{}
	p := dbq.AutoInsertMacroParams{Name: base.Name, Scope: base.Scope, Actions: base.Actions}
	if req.Name != nil {
		name, ok := cleanText(*req.Name, 100)
		if !ok {
			fields["name"] = "invalid"
		}
		p.Name = name
	} else if !base.ID.Valid {
		fields["name"] = "required"
	}
	switch {
	case base.ID.Valid && req.Scope != nil && *req.Scope != base.Scope:
		fields["scope"] = "immutable"
	case !base.ID.Valid && (req.Scope == nil || (*req.Scope != "personal" && *req.Scope != "global")):
		fields["scope"] = "invalid"
	case !base.ID.Valid:
		p.Scope = *req.Scope
	}
	if req.Actions != nil {
		actions, err := automation.ParseActions(req.Actions, false)
		if err != nil {
			var ve automation.ValidationError
			if errors.As(err, &ve) {
				for k, v := range ve {
					fields[k] = v
				}
			} else {
				return p, err
			}
		} else {
			// Marshaling values this package built cannot fail.
			p.Actions, _ = json.Marshal(actions)
		}
	} else if !base.ID.Valid {
		fields["actions"] = "required"
	}
	if len(fields) > 0 {
		return p, errValidation(fields)
	}
	if p.Scope == "personal" {
		p.OwnerUserID = user.ID
	}
	return p, nil
}

func (s *Server) createMacro(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	if !policy.Has(user, policy.ConversationsWrite) {
		writeError(w, r, errForbidden)
		return
	}
	var req macroRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := req.validate(dbq.Macro{}, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if p.Scope == "global" && !policy.Has(user, policy.TemplatesManage) {
		writeError(w, r, errForbidden)
		return
	}
	var row dbq.Macro
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		if row, err = q.AutoInsertMacro(r.Context(), p); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AutomationMacroCreated, TargetType: "macro", TargetID: uuidStr(row.ID), Metadata: map[string]any{"name": row.Name, "scope": row.Scope}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"macro": toMacroJSON(row, user.ID)})
}

// visibleMacro loads a macro the user may see: their own personal ones and workspace macros.
// Anything else is reported as missing.
func (s *Server) visibleMacro(r *http.Request, q *dbq.Queries, user dbq.User) (dbq.Macro, error) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		return dbq.Macro{}, errNotFound
	}
	m, err := q.AutoGetMacro(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && m.Scope == "personal" && m.OwnerUserID != user.ID) {
		return dbq.Macro{}, errNotFound
	}
	return m, err
}

func (s *Server) updateMacro(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	var req macroRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	var row dbq.Macro
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		base, err := s.visibleMacro(r, q, user)
		if err != nil {
			return err
		}
		if !canEditMacro(user, base) {
			return errForbidden
		}
		p, err := req.validate(base, user)
		if err != nil {
			return err
		}
		row, err = q.AutoUpdateMacro(r.Context(), dbq.AutoUpdateMacroParams{ID: base.ID, Name: p.Name, Actions: p.Actions})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AutomationMacroUpdated, TargetType: "macro", TargetID: uuidStr(base.ID), Metadata: map[string]any{"name": row.Name, "scope": row.Scope}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"macro": toMacroJSON(row, user.ID)})
}

func (s *Server) deleteMacro(w http.ResponseWriter, r *http.Request) {
	user := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		m, err := s.visibleMacro(r, q, user)
		if err != nil {
			return err
		}
		if !canEditMacro(user, m) {
			return errForbidden
		}
		if err := q.AutoDeleteMacro(r.Context(), m.ID); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AutomationMacroDeleted, TargetType: "macro", TargetID: uuidStr(m.ID), Metadata: map[string]any{"name": m.Name, "scope": m.Scope}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type macroActionJSON struct {
	Type   string `json:"type"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

type macroResultJSON struct {
	ID      string            `json:"id"`
	OK      bool              `json:"ok"`
	Code    string            `json:"code,omitempty"`
	Actions []macroActionJSON `json:"actions"`
}

func (s *Server) runMacro(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConversationIDs []string `json:"conversation_ids"`
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
	if len(req.ConversationIDs) == 0 || len(req.ConversationIDs) > inbox.MaxBulk {
		writeError(w, r, errValidation(map[string]string{"conversation_ids": "count"}))
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.ConversationIDs))
	for _, raw := range req.ConversationIDs {
		id, ok := parseUUID(raw)
		if !ok {
			writeError(w, r, errValidation(map[string]string{"conversation_ids": "invalid"}))
			return
		}
		ids = append(ids, id)
	}
	macro, err := s.visibleMacro(r, s.q, sessionFrom(r.Context()).User)
	if err != nil {
		writeError(w, r, err)
		return
	}
	actions, err := automation.ParseActions(macro.Actions, false)
	if err != nil {
		writeError(w, r, validationError(err))
		return
	}
	if err := macroRights(sessionFrom(r.Context()).User, actions); err != nil {
		writeError(w, r, err)
		return
	}
	results, err := s.engine.RunMacro(r.Context(), actor, macro.Name, actions, ids)
	if err != nil {
		writeError(w, r, inboxError(err))
		return
	}
	out := make([]macroResultJSON, len(results))
	for i, res := range results {
		out[i] = macroResultJSON{ID: uuidStr(res.ConversationID), OK: !res.Failed(), Actions: []macroActionJSON{}}
		for _, a := range res.Actions {
			out[i].Actions = append(out[i].Actions, macroActionJSON(a))
		}
		switch {
		case res.Err != nil:
			out[i].Code = bulkCode(r, res.Err)
		case res.Failed():
			out[i].Code = "action_failed"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// macroRights holds a macro to the same finer permissions as doing its actions by hand.
func macroRights(u dbq.User, actions []automation.Action) error {
	for _, a := range actions {
		assigns := a.Type == automation.ActionAssignAgent || a.Type == automation.ActionAssignTeam || a.Type == automation.ActionAssignRoundRobin
		spam := a.Type == automation.ActionMarkSpam || (a.Type == automation.ActionSetStatus && a.Status == inbox.StatusSpam)
		if (assigns && !policy.Has(u, policy.ConversationsAssign)) || (spam && !policy.Has(u, policy.ConversationsDelete)) {
			return errForbidden
		}
	}
	return nil
}

func (s *Server) automationRoutes(r chi.Router) {
	r.Put("/me/availability", s.putAvailability)
	r.Get("/macros", s.listMacros)
	r.Post("/macros", s.createMacro)
	r.Patch("/macros/{id}", s.updateMacro)
	r.Delete("/macros/{id}", s.deleteMacro)
	r.Post("/macros/{id}/run", s.runMacro)
}

func (s *Server) automationAdminRoutes(r chi.Router) {
	r.Get("/rules", s.listRules)
	r.Post("/rules", s.createRule)
	r.Put("/rules/order", s.reorderRules)
	r.Patch("/rules/{id}", s.updateRule)
	r.Delete("/rules/{id}", s.deleteRule)
	r.Get("/rules/{id}/runs", s.listRuleRuns)
	r.Get("/business-hours", s.listBusinessHours)
	r.Post("/business-hours", s.createBusinessHours)
	r.Patch("/business-hours/{id}", s.updateBusinessHours)
	r.Delete("/business-hours/{id}", s.deleteBusinessHours)
	r.Get("/sla-policies", s.listSLAPolicies)
	r.Post("/sla-policies", s.createSLAPolicy)
	r.Patch("/sla-policies/{id}", s.updateSLAPolicy)
	r.Delete("/sla-policies/{id}", s.deleteSLAPolicy)
	r.Get("/assignment", s.getAssignment)
	r.Put("/mailboxes/{id}/automation", s.putMailboxAutomation)
	r.Put("/users/{id}/capacity", s.putUserCapacity)
	r.Get("/settings/automation", s.getAutomationSettings)
	r.Put("/settings/automation", s.putAutomationSettings)
}
