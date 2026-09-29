package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/contacts"
	"echoo/internal/db"
	"echoo/internal/db/dbq"
	"echoo/internal/policy"
)

// exportContacts streams the contacts the caller may see, filtered like the list, as CSV.
// Nothing is buffered: rows are read in keyset pages and written as they come.
func (s *Server) exportContacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, v, err := s.contactViewer(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !policy.Has(user, policy.ContactsExport) {
		writeError(w, r, errForbidden)
		return
	}
	p, err := s.parseContactList(r, user, false)
	if err != nil {
		writeError(w, r, err)
		return
	}
	contactDefs := make([]dbq.CustomAttributeDef, 0, len(p.defs))
	for _, d := range p.defs {
		if d.Entity == contacts.EntityContact {
			contactDefs = append(contactDefs, d)
		}
	}
	total, err := contacts.Count(ctx, s.pool, v, p.ListParams, p.defs)
	if err != nil {
		writeError(w, r, err)
		return
	}
	err = db.InTx(ctx, s.pool, func(q *dbq.Queries) error {
		return audit.Write(ctx, q, audit.Entry{
			Actor: user.ID, IP: clientFrom(r).IP, Action: audit.ContactExported, TargetType: "contact",
			Metadata: map[string]any{"rows": total, "search": p.Search != "", "filtered": p.Filter != nil, "segment_id": r.URL.Query().Get("segment_id")},
		})
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacten-`+time.Now().UTC().Format("2006-01-02")+`.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	cw, err := contacts.NewCSVWriter(w)
	if err != nil {
		return
	}
	header := []string{"Naam", "E-mail", "Overige e-mailadressen", "Telefoon", "Organisatie", "Aangemaakt", "Laatste activiteit", "Gesprekken"}
	for _, d := range contactDefs {
		header = append(header, d.Label)
	}
	if err := cw.Write(header...); err != nil {
		return
	}
	// Once the header is out the status cannot change, so a failure ends the stream and is
	// logged; the client sees a truncated file.
	rc := http.NewResponseController(w)
	err = contacts.Each(ctx, s.pool, v, p.ListParams, exportBatch, p.defs, func(rows []contacts.Row) error {
		// The server's write timeout covers the whole response; a long export renews it per batch.
		if err := rc.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		ids := make([]pgtype.UUID, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		addrs, err := s.q.ListAddressesOfContacts(ctx, ids)
		if err != nil {
			return fmt.Errorf("load addresses: %w", err)
		}
		others := map[pgtype.UUID][]string{}
		for _, a := range addrs {
			if !a.IsPrimary {
				others[a.ContactID] = append(others[a.ContactID], a.Email)
			}
		}
		for _, row := range rows {
			attrs, err := attributesOut(row.CustomAttributes)
			if err != nil {
				return err
			}
			cells := []string{
				row.Name, row.PrimaryEmail, joinList(others[row.ID]), row.Phone, row.OrganizationName,
				row.CreatedAt.UTC().Format(time.RFC3339), row.LastActivityAt.UTC().Format(time.RFC3339),
				strconv.Itoa(int(row.ConversationCount)),
			}
			for _, d := range contactDefs {
				cells = append(cells, attributeCell(attrs[d.Key]))
			}
			if err := cw.Write(cells...); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		err = cw.Flush()
	}
	if err != nil {
		writeStreamError(r, "contact export", err)
	}
}

func joinList(items []string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += ", "
		}
		out += it
	}
	return out
}

func attributeCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "ja"
		}
		return "nee"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// writeStreamError logs a failure after the response headers were sent; the client can only
// notice a truncated download.
func writeStreamError(r *http.Request, what string, err error) {
	slog.ErrorContext(r.Context(), what+" stream failed", "err", err, "request_id", requestIDFrom(r.Context()))
}
