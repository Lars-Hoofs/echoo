package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

func (s *Server) reportAdminRoutes(r chi.Router) {
	r.Get("/settings/reports", s.getReportSettings)
	r.Put("/settings/reports", s.putReportSettings)
	r.Get("/mailboxes/{id}/csat", s.getCSATSettings)
	r.Put("/mailboxes/{id}/csat", s.putCSATSettings)
}

type csatSettingsJSON struct {
	Enabled    bool `json:"enabled"`
	DelayHours int  `json:"delay_hours"`
}

func (s *Server) getCSATSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	if _, err := s.q.GetMailbox(r.Context(), id); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, errNotFound)
		return
	} else if err != nil {
		writeError(w, r, err)
		return
	}
	row, err := s.q.CsatGetSettings(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, csatSettingsJSON{})
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, csatSettingsJSON{Enabled: row.Enabled, DelayHours: int(row.DelayHours)})
}

func (s *Server) putCSATSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	var req csatSettingsJSON
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.DelayHours < 0 || req.DelayHours > 24 {
		writeError(w, r, errValidation(map[string]string{"delay_hours": "invalid"}))
		return
	}
	actor := sessionFrom(r.Context()).User
	err := db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if _, err := q.GetMailbox(r.Context(), id); err != nil {
			return err
		}
		delay := int32(req.DelayHours) //nolint:gosec // 0 to 24, validated above
		if _, err := q.CsatUpsertSettings(r.Context(), dbq.CsatUpsertSettingsParams{MailboxID: id, Enabled: req.Enabled, DelayHours: delay}); err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.CSATSettingsChanged, TargetType: "mailbox", TargetID: id.String(),
			Metadata: map[string]any{"enabled": req.Enabled, "delay_hours": req.DelayHours},
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
	writeJSON(w, http.StatusOK, req)
}

type csatRatingJSON struct {
	Rating    int       `json:"rating"`
	Comment   string    `json:"comment"`
	UpdatedAt time.Time `json:"updated_at"`
}

// conversationRating is the customer's rating of a conversation the caller can read, or nil.
func (s *Server) conversationRating(ctx context.Context, id pgtype.UUID, mailboxes []pgtype.UUID) (*csatRatingJSON, error) {
	row, err := s.q.CsatConversationRating(ctx, dbq.CsatConversationRatingParams{ID: id, MailboxIds: mailboxes})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &csatRatingJSON{Rating: int(row.Rating), Comment: row.Comment, UpdatedAt: row.UpdatedAt.Time.UTC()}, nil
}
