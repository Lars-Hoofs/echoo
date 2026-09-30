package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"echoo/internal/audit"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
)

// The blocklist is per mailbox and open to whoever may mark its conversations as spam: the
// trash rights, conversations.delete on a mailbox the user may write.
func (s *Server) blocklistRoutes(r chi.Router) {
	r.Get("/blocked-senders", s.listBlockedSenders)
	r.Post("/blocked-senders", s.createBlockedSender)
	r.Delete("/blocked-senders/{id}", s.deleteBlockedSender)
}

type blockedSenderJSON struct {
	ID        string    `json:"id"`
	Mailbox   refJSON   `json:"mailbox"`
	Pattern   string    `json:"pattern"`
	CreatedBy *refJSON  `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

func toBlockedSenderJSON(r dbq.GetBlockedSenderRow) blockedSenderJSON {
	out := blockedSenderJSON{
		ID: uuidStr(r.ID), Mailbox: refJSON{ID: uuidStr(r.MailboxID), Name: r.MailboxName},
		Pattern: r.Pattern, CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if r.CreatedByID.Valid {
		out.CreatedBy = &refJSON{ID: uuidStr(r.CreatedByID), Name: r.CreatedByName.String}
	}
	return out
}

// blockPattern normalizes an address or a domain; "@example.com" is the domain.
func blockPattern(raw string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(raw))
	if domain, ok := strings.CutPrefix(p, "@"); ok {
		p = domain
	}
	if strings.Contains(p, "@") {
		return p, validEmail(p)
	}
	return p, len(p) <= 253 && ssoDomainPattern.MatchString(p)
}

func (s *Server) listBlockedSenders(w http.ResponseWriter, r *http.Request) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rows, err := s.q.ListBlockedSenders(r.Context(), actor.Write)
	if err != nil {
		writeError(w, r, err)
		return
	}
	// The mailboxes a new entry may go to, for the form.
	mailboxes, err := s.q.ListMailboxRefs(r.Context(), actor.Write)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]blockedSenderJSON, len(rows))
	for i, row := range rows {
		out[i] = toBlockedSenderJSON(dbq.GetBlockedSenderRow(row))
	}
	refs := make([]refJSON, len(mailboxes))
	for i, m := range mailboxes {
		refs[i] = refJSON{ID: uuidStr(m.ID), Name: m.Name}
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked_senders": out, "mailboxes": refs})
}

func (s *Server) createBlockedSender(w http.ResponseWriter, r *http.Request) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req struct {
		MailboxID string `json:"mailbox_id"`
		Pattern   string `json:"pattern"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	fields := map[string]string{}
	mailbox, ok := parseUUID(req.MailboxID)
	switch {
	case req.MailboxID == "":
		fields["mailbox_id"] = "required"
	case !ok:
		fields["mailbox_id"] = "invalid"
	}
	pattern, valid := blockPattern(req.Pattern)
	switch {
	case pattern == "":
		fields["pattern"] = "required"
	case !valid:
		fields["pattern"] = "invalid"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	if !slices.Contains(actor.Write, mailbox) {
		writeError(w, r, errNotFound)
		return
	}
	var entry dbq.GetBlockedSenderRow
	var created bool
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		res, err := q.InsertBlockedSender(r.Context(), dbq.InsertBlockedSenderParams{MailboxID: mailbox, Pattern: pattern, CreatedBy: actor.UserID})
		if err != nil {
			return err
		}
		created = res.Created
		if entry, err = q.GetBlockedSender(r.Context(), res.ID); err != nil {
			return err
		}
		if !created {
			return nil
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.UserID, IP: clientFrom(r).IP, Action: audit.BlocklistAdded,
			TargetType: "blocked_sender", TargetID: uuidStr(res.ID), Metadata: map[string]any{"pattern": pattern, "mailbox_id": uuidStr(mailbox)}})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toBlockedSenderJSON(entry))
}

func (s *Server) deleteBlockedSender(w http.ResponseWriter, r *http.Request) {
	actor, err := s.trashActor(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	id, ok := parseUUID(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, r, errNotFound)
		return
	}
	err = db.InTx(r.Context(), s.pool, func(q *dbq.Queries) error {
		row, err := q.DeleteBlockedSender(r.Context(), dbq.DeleteBlockedSenderParams{ID: id, MailboxIds: actor.Write})
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), q, audit.Entry{Actor: actor.UserID, IP: clientFrom(r).IP, Action: audit.BlocklistRemoved,
			TargetType: "blocked_sender", TargetID: uuidStr(id), Metadata: map[string]any{"pattern": row.Pattern, "mailbox_id": uuidStr(row.MailboxID)}})
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
