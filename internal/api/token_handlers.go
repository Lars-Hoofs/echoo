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
	"echoo/internal/auth"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

// maxTokenLifetime keeps "optional expiry" from meaning "forever by accident".
const maxTokenLifetime = 5 * 365 * 24 * time.Hour

type apiTokenJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UserID     string     `json:"user_id"`
	UserName   string     `json:"user_name,omitempty"`
	UserEmail  string     `json:"user_email,omitempty"`
}

func toAPITokenJSON(t dbq.ApiToken) apiTokenJSON {
	scope := auth.ScopeRead
	if slices.Contains(t.Scopes, auth.ScopeWrite) {
		scope = auth.ScopeWrite
	}
	return apiTokenJSON{
		ID: uuidStr(t.ID), Name: t.Name, Prefix: t.Prefix, Scope: scope,
		ExpiresAt: timeOrNil(t.ExpiresAt), LastUsedAt: timeOrNil(t.LastUsedAt),
		CreatedAt: t.CreatedAt.Time.UTC(), UserID: uuidStr(t.UserID),
	}
}

func (s *Server) listMyTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListUserAPITokens(r.Context(), sessionFrom(r.Context()).User.ID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]apiTokenJSON, len(rows))
	for i, t := range rows {
		out[i] = toAPITokenJSON(t)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) listAllTokens(w http.ResponseWriter, r *http.Request) {
	rows, err := s.q.ListAllAPITokens(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]apiTokenJSON, len(rows))
	for i, row := range rows {
		out[i] = toAPITokenJSON(row.ApiToken)
		out[i].UserName, out[i].UserEmail = row.UserName, row.UserEmail
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) createMyToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string     `json:"name"`
		Scope     string     `json:"scope"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	name, ok := cleanText(req.Name, 100)
	if !ok {
		fields["name"] = "invalid"
	}
	var scopes []string
	switch req.Scope {
	case auth.ScopeRead:
		scopes = []string{auth.ScopeRead}
	case auth.ScopeWrite:
		scopes = []string{auth.ScopeRead, auth.ScopeWrite}
	default:
		fields["scope"] = "invalid"
	}
	var expires pgtype.Timestamptz
	if req.ExpiresAt != nil {
		if !req.ExpiresAt.After(time.Now()) || req.ExpiresAt.After(time.Now().Add(maxTokenLifetime)) {
			fields["expires_at"] = "invalid"
		}
		expires = pgtype.Timestamptz{Time: *req.ExpiresAt, Valid: true}
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	token, prefix, hash, err := auth.NewAPIToken()
	if err != nil {
		writeError(w, r, err)
		return
	}
	actor := sessionFrom(r.Context()).User
	var row dbq.ApiToken
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		var err error
		row, err = q.CreateAPIToken(r.Context(), dbq.CreateAPITokenParams{
			UserID: actor.ID, Name: name, Prefix: prefix, TokenHash: hash, Scopes: scopes, ExpiresAt: expires,
		})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.APITokenCreated, TargetType: "api_token", TargetID: uuidStr(row.ID),
			Metadata: map[string]any{"name": name, "scope": req.Scope, "expires": req.ExpiresAt != nil},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "api_token": toAPITokenJSON(row)})
}

func (s *Server) revokeMyToken(w http.ResponseWriter, r *http.Request) {
	s.revokeToken(w, r, sessionFrom(r.Context()).User.ID)
}

func (s *Server) revokeAnyToken(w http.ResponseWriter, r *http.Request) {
	s.revokeToken(w, r, pgtype.UUID{})
}

// revokeToken restricts revocation to owner when it is valid; admins pass an invalid UUID.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, owner pgtype.UUID) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		tok, err := q.RevokeAPIToken(r.Context(), dbq.RevokeAPITokenParams{ID: id, UserID: owner})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.APITokenRevoked, TargetType: "api_token", TargetID: uuidStr(tok.ID),
			Metadata: map[string]any{"name": tok.Name, "owner_id": uuidStr(tok.UserID)},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
