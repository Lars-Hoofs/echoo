package api

import (
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"echoo/internal/audit"
	"echoo/internal/db/dbq"
)

const (
	auditPageSize   = 50
	auditExportPage = 500
	dateLayout      = "2006-01-02"
)

var actionPrefixPattern = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

// auditFilter is what both the list and the export accept.
type auditFilter struct {
	prefix string
	actor  pgtype.UUID
	from   pgtype.Timestamptz
	to     pgtype.Timestamptz
}

// parseAuditFilter reads action (prefix), actor (user id) and from/to (inclusive dates, UTC).
func parseAuditFilter(r *http.Request) (auditFilter, *apiError) {
	var f auditFilter
	q := r.URL.Query()
	if v := q.Get("action"); v != "" {
		if !actionPrefixPattern.MatchString(v) {
			return f, errBadRequest("action must be a lowercase action name or prefix")
		}
		// LIKE treats _ as a wildcard; the pattern check above excludes the other special characters.
		f.prefix = strings.ReplaceAll(v, "_", `\_`)
	}
	if v := q.Get("actor"); v != "" {
		id, ok := parseUUID(v)
		if !ok {
			return f, errBadRequest("actor must be a user id")
		}
		f.actor = id
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return f, errBadRequest("from must be a date (YYYY-MM-DD)")
		}
		f.from = pgtype.Timestamptz{Time: t, Valid: true}
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			return f, errBadRequest("to must be a date (YYYY-MM-DD)")
		}
		f.to = pgtype.Timestamptz{Time: t.AddDate(0, 0, 1), Valid: true}
	}
	return f, nil
}

func (s *Server) auditPage(r *http.Request, f auditFilter, before int64, size int32) ([]dbq.ListAuditRow, error) {
	return s.q.ListAudit(r.Context(), dbq.ListAuditParams{
		BeforeID: before, ActionPrefix: f.prefix, ActorID: f.actor, FromAt: f.from, ToAt: f.to, PageSize: size,
	})
}

type auditEntryJSON struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorID    string          `json:"actor_id"`
	ActorName  string          `json:"actor_name"`
	ActorEmail string          `json:"actor_email"`
	IP         string          `json:"ip"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Metadata   json.RawMessage `json:"metadata"`
}

func toAuditEntryJSON(row dbq.ListAuditRow) auditEntryJSON {
	e := row.AuditLog
	ip := ""
	if e.ActorIp != nil {
		ip = e.ActorIp.String()
	}
	return auditEntryJSON{
		ID: e.ID, At: e.At.Time.UTC(), ActorID: uuidStr(e.ActorUserID), ActorName: row.ActorName.String,
		ActorEmail: row.ActorEmail.String, IP: ip, Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Metadata: e.Metadata,
	}
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	var before int64
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeError(w, r, errBadRequest("before must be a positive integer"))
			return
		}
		before = n
	}
	f, apiErr := parseAuditFilter(r)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	rows, err := s.auditPage(r, f, before, auditPageSize)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]auditEntryJSON, len(rows))
	for i, row := range rows {
		out[i] = toAuditEntryJSON(row)
	}
	var next *int64
	if len(rows) == auditPageSize {
		next = &rows[len(rows)-1].AuditLog.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out, "next_before": next})
}

// exportAudit streams the filtered log as CSV, newest first, one keyset page at a time so the
// whole log never sits in memory. Exporting is itself audited.
func (s *Server) exportAudit(w http.ResponseWriter, r *http.Request) {
	f, apiErr := parseAuditFilter(r)
	if apiErr != nil {
		writeError(w, r, apiErr)
		return
	}
	actor := sessionFrom(r.Context()).User
	q := r.URL.Query()
	err := audit.Write(r.Context(), s.q, audit.Entry{
		Actor: actor.ID, IP: clientFrom(r).IP, Action: audit.AuditExported, TargetType: "audit_log",
		Metadata: map[string]any{"action": q.Get("action"), "actor": q.Get("actor"), "from": q.Get("from"), "to": q.Get("to")},
	})
	if err != nil {
		writeError(w, r, err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="auditlog.csv"`)
	rc := http.NewResponseController(w)
	out := csv.NewWriter(w)
	_ = out.Write([]string{"id", "at", "actor_id", "actor_name", "actor_email", "ip", "action", "target_type", "target_id", "metadata"})
	var before int64
	for {
		rows, err := s.auditPage(r, f, before, auditExportPage)
		if err != nil {
			// Headers are gone; aborting the response is the only honest way to signal a broken export.
			slog.ErrorContext(r.Context(), "audit export failed", "err", err, "request_id", requestIDFrom(r.Context()))
			panic(http.ErrAbortHandler)
		}
		for _, row := range rows {
			e := toAuditEntryJSON(row)
			_ = out.Write([]string{
				strconv.FormatInt(e.ID, 10), e.At.Format(time.RFC3339), e.ActorID, csvSafe(e.ActorName), csvSafe(e.ActorEmail),
				e.IP, e.Action, e.TargetType, e.TargetID, csvSafe(string(e.Metadata)),
			})
		}
		out.Flush()
		if out.Error() != nil {
			return
		}
		if len(rows) < auditExportPage {
			return
		}
		before = rows[len(rows)-1].AuditLog.ID
		// The server's write timeout is sized for normal requests, not for a long export.
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
}

// csvSafe stops spreadsheet programs from evaluating a cell as a formula.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
