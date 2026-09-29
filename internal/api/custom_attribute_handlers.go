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

const maxDefsPerEntity = 50

type attributeDefJSON struct {
	ID        string    `json:"id"`
	Entity    string    `json:"entity"`
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	Type      string    `json:"type"`
	Options   []string  `json:"options"`
	CreatedAt time.Time `json:"created_at"`
}

func toAttributeDefJSON(d dbq.CustomAttributeDef) attributeDefJSON {
	opts := d.Options
	if opts == nil {
		opts = []string{}
	}
	return attributeDefJSON{ID: uuidStr(d.ID), Entity: d.Entity, Key: d.Key, Label: d.Label, Type: d.Type, Options: opts, CreatedAt: d.CreatedAt.Time.UTC()}
}

func (s *Server) listAttributeDefs(w http.ResponseWriter, r *http.Request) {
	entity := r.URL.Query().Get("entity")
	if entity != "" && !contacts.ValidEntity(entity) {
		writeError(w, r, errBadRequest("entity must be contact, organization or conversation"))
		return
	}
	defs, err := s.attributeDefs(r.Context(), entity)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]attributeDefJSON, len(defs))
	for i, d := range defs {
		out[i] = toAttributeDefJSON(d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"attributes": out})
}

type attributeDefRequest struct {
	Entity  *string  `json:"entity"`
	Key     *string  `json:"key"`
	Label   *string  `json:"label"`
	Type    *string  `json:"type"`
	Options []string `json:"options"`
}

func cleanLabel(s string) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= 60 && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func (s *Server) createAttributeDef(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req attributeDefRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	entity, key, typ := str(req.Entity), str(req.Key), str(req.Type)
	if !contacts.ValidEntity(entity) {
		fields["entity"] = "invalid"
	}
	if !contacts.ValidKey(key) {
		fields["key"] = "invalid"
	}
	label, ok := cleanLabel(str(req.Label))
	if !ok {
		fields["label"] = "invalid"
	}
	if !contacts.ValidType(typ) {
		fields["type"] = "invalid"
	}
	var options []string
	if fields["type"] == "" {
		var err error
		if options, err = contacts.CleanOptions(typ, req.Options); err != nil {
			fields["options"] = "invalid"
		}
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	user := sessionFrom(ctx).User
	var def dbq.CustomAttributeDef
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		existing, err := q.ListAttributeDefs(ctx, pgtype.Text{String: entity, Valid: true})
		if err != nil {
			return fmt.Errorf("list definitions: %w", err)
		}
		if len(existing) >= maxDefsPerEntity {
			return errValidation(map[string]string{"entity": "limit_reached"})
		}
		def, err = q.InsertAttributeDef(ctx, dbq.InsertAttributeDefParams{Entity: entity, Key: key, Label: label, Type: typ, Options: options})
		if err != nil {
			return err
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AttributeCreated, TargetType: "custom_attribute", TargetID: uuidStr(def.ID),
			Metadata: map[string]any{"entity": entity, "key": key, "type": typ},
		})
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"key": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"attribute": toAttributeDefJSON(def)})
}

// updateAttributeDef changes the label and the options. Key, entity and type stay fixed
// because stored values depend on them.
func (s *Server) updateAttributeDef(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req attributeDefRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Entity != nil || req.Key != nil || req.Type != nil {
		writeError(w, r, errValidation(map[string]string{"type": "immutable"}))
		return
	}
	user := sessionFrom(ctx).User
	var def dbq.CustomAttributeDef
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		cur, err := q.GetAttributeDef(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("load definition: %w", err)
		}
		label, options := cur.Label, cur.Options
		fields := map[string]string{}
		if req.Label != nil {
			var ok bool
			if label, ok = cleanLabel(*req.Label); !ok {
				fields["label"] = "invalid"
			}
		}
		if req.Options != nil {
			var err error
			if options, err = contacts.CleanOptions(cur.Type, req.Options); err != nil {
				fields["options"] = "invalid"
			}
		}
		if len(fields) > 0 {
			return errValidation(fields)
		}
		def, err = q.UpdateAttributeDef(ctx, dbq.UpdateAttributeDefParams{ID: id, Label: label, Options: options})
		if err != nil {
			return fmt.Errorf("update definition: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AttributeUpdated, TargetType: "custom_attribute", TargetID: uuidStr(id),
			Metadata: map[string]any{"entity": cur.Entity, "key": cur.Key},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attribute": toAttributeDefJSON(def)})
}

// deleteAttributeDef removes a definition together with every stored value for its key.
func (s *Server) deleteAttributeDef(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	user := sessionFrom(ctx).User
	err := db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		gone, err := q.DeleteAttributeDef(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("delete definition: %w", err)
		}
		var stripped int64
		switch gone.Entity {
		case contacts.EntityContact:
			stripped, err = q.StripContactAttribute(ctx, gone.Key)
		case contacts.EntityOrganization:
			stripped, err = q.StripOrganizationAttribute(ctx, gone.Key)
		default:
			stripped, err = q.StripConversationAttribute(ctx, gone.Key)
		}
		if err != nil {
			return fmt.Errorf("remove stored values: %w", err)
		}
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AttributeDeleted, TargetType: "custom_attribute", TargetID: uuidStr(id),
			Metadata: map[string]any{"entity": gone.Entity, "key": gone.Key, "values_removed": stripped},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Conversation attributes

func (s *Server) getConversationAttributes(w http.ResponseWriter, r *http.Request) {
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
	row, err := s.q.GetConversationAttributes(ctx, dbq.GetConversationAttributesParams{ID: id, MailboxIds: scope.Read})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, fmt.Errorf("load conversation attributes: %w", err))
		return
	}
	attrs, err := attributesOut(row.CustomAttributes)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attributes": attrs})
}

type conversationAttributesRequest struct {
	Attributes map[string]json.RawMessage `json:"attributes"`
}

func (s *Server) patchConversationAttributes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	user := sessionFrom(ctx).User
	if !policy.Has(user, policy.ConversationsWrite) {
		writeError(w, r, errForbidden)
		return
	}
	var req conversationAttributesRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	patch, err := contacts.ParsePatch(req.Attributes)
	if err != nil {
		writeError(w, r, errBadRequest(err.Error()))
		return
	}
	defs, err := s.attributeDefs(ctx, contacts.EntityConversation)
	if err != nil {
		writeError(w, r, err)
		return
	}
	scope, err := policy.MailboxScope(ctx, s.q, user)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var result contacts.Attributes
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		row, err := q.LockConversationAttributes(ctx, dbq.LockConversationAttributesParams{ID: id, MailboxIds: scope.Write})
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return fmt.Errorf("lock conversation: %w", err)
		}
		current, err := contacts.DecodeAttributes(row.CustomAttributes)
		if err != nil {
			return err
		}
		merged, problems := contacts.ApplyPatch(defs, current, patch, false)
		if len(problems) > 0 {
			return attributeProblems(problems)
		}
		encoded, err := merged.Encode()
		if err != nil {
			return err
		}
		result = merged
		return q.UpdateConversationAttributes(ctx, dbq.UpdateConversationAttributesParams{ID: id, CustomAttributes: encoded})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attributes": map[string]any(result)})
}
