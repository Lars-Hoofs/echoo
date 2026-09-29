package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/automation"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

type ruleJSON struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	MailboxID      *string         `json:"mailbox_id"`
	Trigger        string          `json:"trigger"`
	IdleHours      *int32          `json:"idle_hours"`
	Conditions     json.RawMessage `json:"conditions"`
	Actions        json.RawMessage `json:"actions"`
	StopProcessing bool            `json:"stop_processing"`
	Position       int32           `json:"position"`
	Enabled        bool            `json:"enabled"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

func toRuleJSON(r dbq.Rule) ruleJSON {
	out := ruleJSON{
		ID: uuidStr(r.ID), Name: r.Name, Trigger: r.Trigger, Conditions: r.Conditions, Actions: r.Actions,
		StopProcessing: r.StopProcessing, Position: r.Position, Enabled: r.Enabled,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if r.MailboxID.Valid {
		id := uuidStr(r.MailboxID)
		out.MailboxID = &id
	}
	if r.IdleHours.Valid {
		out.IdleHours = &r.IdleHours.Int32
	}
	return out
}

// validationError turns a schema error of the automation package into a 422.
func validationError(err error) error {
	var ve automation.ValidationError
	if errors.As(err, &ve) {
		return errValidation(ve)
	}
	return err
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.AutoListRules(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	out := make([]ruleJSON, 0, len(rows))
	for _, row := range rows {
		switch err := requireWritableMailbox(r.Context(), s.q, actor, row.MailboxID); {
		case errors.Is(err, errForbidden):
			continue
		case err != nil:
			writeError(w, r, err)
			return
		}
		out = append(out, toRuleJSON(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

type ruleRequest struct {
	Name           *string          `json:"name"`
	MailboxID      nullable[string] `json:"mailbox_id"`
	Trigger        *string          `json:"trigger"`
	IdleHours      nullable[int]    `json:"idle_hours"`
	Conditions     json.RawMessage  `json:"conditions"`
	Actions        json.RawMessage  `json:"actions"`
	StopProcessing *bool            `json:"stop_processing"`
	Enabled        *bool            `json:"enabled"`
}

// merge overlays the request on the stored rule (or an empty one on create) and validates the
// result as a whole, so a PATCH cannot leave a rule half valid.
func (req ruleRequest) merge(base dbq.Rule) (automation.RuleDef, error) {
	def := automation.RuleDef{
		Name: base.Name, MailboxID: base.MailboxID, Trigger: base.Trigger, IdleHours: int(base.IdleHours.Int32),
		Conditions: base.Conditions, Actions: base.Actions, StopProcessing: base.StopProcessing, Enabled: true,
	}
	if base.ID.Valid {
		def.Enabled = base.Enabled
	}
	fields := map[string]string{}
	if req.Name != nil {
		def.Name = *req.Name
	}
	if req.Trigger != nil {
		def.Trigger = *req.Trigger
	}
	if req.MailboxID.set {
		def.MailboxID = pgtype.UUID{}
		if req.MailboxID.value != nil {
			id, ok := parseUUID(*req.MailboxID.value)
			if !ok {
				fields["mailbox_id"] = "invalid"
			}
			def.MailboxID = id
		}
	}
	if req.IdleHours.set {
		def.IdleHours = 0
		if req.IdleHours.value != nil {
			def.IdleHours = *req.IdleHours.value
		}
	}
	if req.Conditions != nil {
		def.Conditions = req.Conditions
	}
	if req.Actions != nil {
		def.Actions = req.Actions
	}
	if req.StopProcessing != nil {
		def.StopProcessing = *req.StopProcessing
	}
	if req.Enabled != nil {
		def.Enabled = *req.Enabled
	}
	// Moving away from the idle trigger drops its hours unless the request says otherwise.
	if req.Trigger != nil && *req.Trigger != automation.TriggerCustomerIdle && !req.IdleHours.set {
		def.IdleHours = 0
	}
	valid, err := automation.ValidateRule(def)
	if err != nil {
		var ve automation.ValidationError
		if errors.As(err, &ve) {
			for k, v := range ve {
				fields[k] = v
			}
		} else {
			return def, err
		}
	}
	if len(fields) > 0 {
		return def, errValidation(fields)
	}
	return valid, nil
}

// checkRuleTemplates refuses an auto-reply that would send a canned response the actor may not
// use, such as a personal template of someone else or one of a mailbox they cannot read.
func checkRuleTemplates(ctx context.Context, q *dbq.Queries, actor dbq.User, def automation.RuleDef) error {
	actions, err := automation.ParseActions(def.Actions, true)
	if err != nil {
		return validationError(err)
	}
	var scope *policy.Scope
	for _, a := range actions {
		if a.Type != automation.ActionAutoReply || a.TemplateID == "" {
			continue
		}
		if scope == nil {
			sc, err := policy.MailboxScope(ctx, q, actor)
			if err != nil {
				return err
			}
			scope = &sc
		}
		id, ok := parseUUID(a.TemplateID)
		visible := false
		if ok {
			if visible, err = q.ComposeTemplateVisible(ctx, dbq.ComposeTemplateVisibleParams{ID: id, UserID: actor.ID, MailboxIds: scope.Read}); err != nil {
				return err
			}
		}
		if !visible {
			return errValidation(map[string]string{"actions": "unknown_template"})
		}
	}
	return nil
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var req ruleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	def, err := req.merge(dbq.Rule{})
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var rule dbq.Rule
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := requireWritableMailbox(r.Context(), q, actor, def.MailboxID); err != nil {
			return err
		}
		if err := checkRuleTemplates(r.Context(), q, actor, def); err != nil {
			return err
		}
		var err error
		rule, err = q.AutoInsertRule(r.Context(), dbq.AutoInsertRuleParams{
			Name: def.Name, MailboxID: def.MailboxID, Trigger: def.Trigger, IdleHours: idleHours(def.IdleHours),
			Conditions: def.Conditions, Actions: def.Actions, StopProcessing: def.StopProcessing, Enabled: def.Enabled,
			CreatedBy: actor.ID,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationRuleCreated, TargetType: "rule", TargetID: uuidStr(rule.ID), Metadata: map[string]any{"name": rule.Name, "trigger": rule.Trigger}})
	})
	if isForeignKeyViolation(err) {
		writeError(w, r, errValidation(map[string]string{"mailbox_id": "unknown"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": toRuleJSON(rule)})
}

func idleHours(h int) pgtype.Int4 {
	if h == 0 {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(h), Valid: true} //nolint:gosec // validated to at most 720
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req ruleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var rule dbq.Rule
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		base, err := q.AutoGetRule(r.Context(), id)
		if err != nil {
			return err
		}
		if err := requireWritableMailbox(r.Context(), q, actor, base.MailboxID); err != nil {
			return err
		}
		def, err := req.merge(base)
		if err != nil {
			return err
		}
		if err := requireWritableMailbox(r.Context(), q, actor, def.MailboxID); err != nil {
			return err
		}
		if req.Actions != nil {
			if err := checkRuleTemplates(r.Context(), q, actor, def); err != nil {
				return err
			}
		}
		rule, err = q.AutoUpdateRule(r.Context(), dbq.AutoUpdateRuleParams{
			ID: id, Name: def.Name, MailboxID: def.MailboxID, Trigger: def.Trigger, IdleHours: idleHours(def.IdleHours),
			Conditions: def.Conditions, Actions: def.Actions, StopProcessing: def.StopProcessing, Enabled: def.Enabled,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationRuleUpdated, TargetType: "rule", TargetID: uuidStr(id), Metadata: map[string]any{"name": rule.Name, "enabled": rule.Enabled}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isForeignKeyViolation(err):
		writeError(w, r, errValidation(map[string]string{"mailbox_id": "unknown"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"rule": toRuleJSON(rule)})
	}
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		base, err := q.AutoGetRule(r.Context(), id)
		if err != nil {
			return err
		}
		if err := requireWritableMailbox(r.Context(), q, actor, base.MailboxID); err != nil {
			return err
		}
		name, err := q.AutoDeleteRule(r.Context(), id)
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationRuleDeleted, TargetType: "rule", TargetID: uuidStr(id), Metadata: map[string]any{"name": name}})
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

// reorderRules sets the evaluation order. The list must contain every rule exactly once; when
// another admin added or removed one in the meantime the client is told to reload.
func (s *Server) reorderRules(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.IDs))
	for _, raw := range req.IDs {
		id, ok := parseUUID(raw)
		if !ok || slices.Contains(ids, id) {
			writeError(w, r, errValidation(map[string]string{"ids": "invalid"}))
			return
		}
		ids = append(ids, id)
	}
	actor := sessionFrom(r.Context()).User
	if err := requireSeesAll(actor); err != nil {
		writeError(w, r, err)
		return
	}
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		existing, err := q.AutoListRules(r.Context())
		if err != nil {
			return err
		}
		if len(existing) != len(ids) {
			return errRulesChanged
		}
		for _, rule := range existing {
			if !slices.Contains(ids, rule.ID) {
				return errRulesChanged
			}
		}
		if err := q.AutoReorderRules(r.Context(), ids); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AutomationRulesReordered, TargetType: "rule", TargetID: "", Metadata: map[string]any{"count": len(ids)}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errRulesChanged = &apiError{Status: http.StatusConflict, Code: "rules_changed", Message: "the set of rules changed; reload and try again"}

type ruleRunJSON struct {
	ID                 string          `json:"id"`
	ConversationID     string          `json:"conversation_id"`
	ConversationNumber int64           `json:"conversation_number"`
	Trigger            string          `json:"trigger"`
	Matched            bool            `json:"matched"`
	Actions            json.RawMessage `json:"actions"`
	Error              string          `json:"error"`
	CreatedAt          time.Time       `json:"created_at"`
}

const maxRuleRuns = 200

func (s *Server) listRuleRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	limit := int32(50)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 1 || n > maxRuleRuns {
			writeError(w, r, errBadRequest("limit must be between 1 and "+strconv.Itoa(maxRuleRuns)))
			return
		}
		limit = int32(n)
	}
	rule, err := s.q.AutoGetRule(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err == nil {
		err = requireWritableMailbox(r.Context(), s.q, sessionFrom(r.Context()).User, rule.MailboxID)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.AutoListRuleRuns(r.Context(), dbq.AutoListRuleRunsParams{RuleID: id, PageSize: limit})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]ruleRunJSON, len(rows))
	for i, row := range rows {
		out[i] = ruleRunJSON{
			ID: uuidStr(row.ID), ConversationID: uuidStr(row.ConversationID), ConversationNumber: row.ConversationNumber,
			Trigger: row.Trigger, Matched: row.Matched, Actions: row.ActionsApplied, Error: row.Error, CreatedAt: row.CreatedAt.Time.UTC(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}
