package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

// A contact that unsubscribed or whose address bounced is skipped by every campaign. These two
// routes let someone with campaigns.manage undo that, for example when the contact asks to
// receive mail again or the mailbox problem is fixed.

func (s *Server) resubscribeContact(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	user, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := requireVisibleContact(r.Context(), q, v, id); err != nil {
			return err
		}
		n, err := q.ContactResubscribe(r.Context(), id)
		if err != nil || n == 0 {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: user.ID, IP: clientFrom(r).IP, Action: audit.Resubscribed, TargetType: "contact", TargetID: uuidStr(id)})
	})
	s.finishConsent(w, r, err)
}

func (s *Server) reactivateAddress(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	user, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	email, err := contacts.NormalizeEmail(req.Email)
	if err != nil {
		writeError(w, r, errValidation(map[string]string{"email": "invalid"}))
		return
	}
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		if err := requireVisibleContact(r.Context(), q, v, id); err != nil {
			return err
		}
		n, err := q.ContactClearBounce(r.Context(), dbq.ContactClearBounceParams{ContactID: id, Email: email})
		if err != nil || n == 0 {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.AddressReactivated, TargetType: "contact", TargetID: uuidStr(id),
			Metadata: map[string]any{"email": email},
		})
	})
	s.finishConsent(w, r, err)
}

func requireVisibleContact(ctx context.Context, q *dbq.Queries, v contacts.Viewer, id pgtype.UUID) error {
	visible, err := q.ContactIsVisible(ctx, dbq.ContactIsVisibleParams{ID: id, UserID: v.UserID, Admin: v.Admin, MailboxIds: v.MailboxIDs})
	if err != nil {
		return err
	}
	// The visibility rule says yes to admins for any id, so a missing contact is checked apart.
	if _, err := q.LockContact(ctx, id); !visible || errors.Is(err, pgx.ErrNoRows) {
		return contacts.ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Server) finishConsent(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, contacts.ErrNotFound):
		writeError(w, r, errNotFound)
	case err != nil:
		writeError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
