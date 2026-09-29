package api

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

var labelColorTokens = []string{"slate", "blue", "teal", "green", "amber", "orange", "red", "violet"}

type labelJSON struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ColorToken  string    `json:"color_token"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func toLabelJSON(id, name, color, description string, createdAt time.Time) labelJSON {
	return labelJSON{ID: id, Name: name, ColorToken: color, Description: description, CreatedAt: createdAt.UTC()}
}

func (s *Server) listLabels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListLabels(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]labelJSON, len(rows))
	for i, l := range rows {
		out[i] = toLabelJSON(uuidStr(l.ID), l.Name, l.ColorToken, l.Description, l.CreatedAt.Time)
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": out})
}

type labelRequest struct {
	Name        *string `json:"name"`
	ColorToken  *string `json:"color_token"`
	Description *string `json:"description"`
}

// validate cleans the fields in place and returns the invalid ones. When required is set,
// name and color_token must be present.
func (req *labelRequest) validate(required bool) map[string]string {
	fields := map[string]string{}
	if req.Name != nil {
		if name, ok := cleanText(*req.Name, 50); ok {
			req.Name = &name
		} else {
			fields["name"] = "invalid"
		}
	} else if required {
		fields["name"] = "required"
	}
	if req.ColorToken != nil {
		if !slices.Contains(labelColorTokens, *req.ColorToken) {
			fields["color_token"] = "invalid"
		}
	} else if required {
		fields["color_token"] = "required"
	}
	if req.Description != nil {
		if desc, ok := cleanDescription(*req.Description, 200); ok {
			req.Description = &desc
		} else {
			fields["description"] = "invalid"
		}
	}
	return fields
}

// cleanDescription is like cleanText for an optional field, so empty is allowed.
func cleanDescription(s string, maxRunes int) (string, bool) {
	if len([]rune(s)) == 0 {
		return "", true
	}
	return cleanText(s, maxRunes)
}

func (s *Server) createLabel(w http.ResponseWriter, r *http.Request) {
	var req labelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if fields := req.validate(true); len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	}
	actor := sessionFrom(r.Context()).User
	var label dbq.Label
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		label, err = q.InsertLabel(r.Context(), dbq.InsertLabelParams{Name: *req.Name, ColorToken: *req.ColorToken, Description: description})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.LabelCreated, TargetType: "label", TargetID: uuidStr(label.ID), Metadata: map[string]any{"name": label.Name}})
	})
	if isUniqueViolation(err) {
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"label": toLabelJSON(uuidStr(label.ID), label.Name, label.ColorToken, label.Description, label.CreatedAt.Time)})
}

func (s *Server) updateLabel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req labelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if fields := req.validate(false); len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	actor := sessionFrom(r.Context()).User
	var label dbq.Label
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		label, err = q.UpdateLabel(r.Context(), dbq.UpdateLabelParams{ID: id, Name: optionalText(req.Name), ColorToken: optionalText(req.ColorToken), Description: optionalText(req.Description)})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.LabelUpdated, TargetType: "label", TargetID: uuidStr(id), Metadata: map[string]any{"name": label.Name}})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, errNotFound)
	case isUniqueViolation(err):
		writeError(w, r, errValidation(map[string]string{"name": "taken"}))
	case err != nil:
		writeError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"label": toLabelJSON(uuidStr(label.ID), label.Name, label.ColorToken, label.Description, label.CreatedAt.Time)})
	}
}

func (s *Server) deleteLabel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		name, err := q.DeleteLabel(r.Context(), id)
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.LabelDeleted, TargetType: "label", TargetID: uuidStr(id), Metadata: map[string]any{"name": name}})
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

// optionalText maps an absent JSON field to NULL, which UpdateLabel reads as "keep".
func optionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}
